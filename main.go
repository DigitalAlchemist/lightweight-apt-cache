package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func DownloadFile(url string, path string) (*http.Header, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return &resp.Header, nil
}

type Metadata struct {
	URL           string
	ContentLength string
	ContentType   string
	LastModified  string
}

func LoadMetadata(path string) *Metadata {
	var meta Metadata
	data, _ := ioutil.ReadFile(path)
	_ = json.Unmarshal(data, &meta)
	return &meta
}

var filesystemRWLock sync.RWMutex
var conditionMutex sync.Mutex
var downloadedFiles map[string]chan struct {}

func DownloadHandler(w http.ResponseWriter, r *http.Request) {

	uri := r.URL.RequestURI()

	// Ideally this'd be something like S3, where its just a string, but directories are a thing, and we don't care to clean those up.
	hash := sha256.Sum256([]byte(r.URL.Path))
	sum := hex.EncodeToString(hash[:])

	metadata := fmt.Sprintf("cache/%s.json", sum)
	filename := fmt.Sprintf("cache/%s", sum)
	fmt.Printf("%s %s -> %s\n", r.RemoteAddr, filename, r.URL.String())

	// Lock the filesystem for concurrent access. Since our application doesn't write to the same file multiple times,
	// we can have multiple files being written, just no one writing the same file twice. This prevents the cleanup routine
	// from running and wiping out files.
	filesystemRWLock.RLock()
	defer filesystemRWLock.RUnlock()

	conditionMutex.Lock()
	c, inProgress := downloadedFiles[sum]
  conditionMutex.Unlock()

	var meta *Metadata
	if inProgress {
    // If this operation fails, that's okay. The channel was closed.
		_, _ = <-c
		meta = LoadMetadata(metadata)
	} else {
		// File download isn't in progress. Maybe its already here?
		_, err := os.Stat(filename)

		// File exists, read the metadata about it
		if !os.IsNotExist(err) {
			meta = LoadMetadata(metadata)
		} else {
			c = make(chan struct{})
			conditionMutex.Lock()
			downloadedFiles[sum] = c
			conditionMutex.Unlock()

			// TODO: Make this configurable.
			headers, err := DownloadFile(fmt.Sprintf("http://archive.ubuntu.com/%s", r.URL.String()), filename)

      // XXX: Checking this IS NOT an error to simplify the logic.
			if err == nil {
        meta = &Metadata {
          URL: uri,
          ContentLength: headers.Get("Content-Length"),
          LastModified: headers.Get("Last-Modified"),
          ContentType: headers.Get("Content-Type"),
        }

        b, _ := json.Marshal(meta)
        _ = ioutil.WriteFile(metadata, b, 0644)
      }

      // Closing the channel signals to everything we are done (error or not).
			conditionMutex.Lock()
      close(c)
			conditionMutex.Unlock()
		}
	}

  // This check simplifies the above code in the error cases.
  if meta == nil {
    w.WriteHeader(http.StatusNotFound)
    return
  }

	// Finally send file.
	w.Header().Set("Last-Modified", meta.LastModified)
	w.Header().Set("Content-Length", meta.ContentLength)
	w.Header().Set("Content-Type", meta.ContentType)
	http.ServeFile(w, r, filename)
}

func main() {

  downloadedFiles = make(map[string]chan struct{})

	go func() {
		for {
			filesystemRWLock.Lock()
			fmt.Println("Checking cache")
			now := time.Now()
			filepath.Walk("./cache", func(path string, info os.FileInfo, err error) error {
				if info.IsDir() { return nil }
				if filepath.Ext(path) != ".json" { return nil }

				meta := LoadMetadata(path)
				fmt.Printf("Checking %s\n", path)
				date, err := time.Parse(time.RFC1123, meta.LastModified)
				if err != nil {
					fmt.Printf("%s -> %s has an invalid time", path, meta.URL)
					return nil
				}

				diff := now.Sub(date)
				fmt.Println(diff)
				if diff.Hours() > 24 {
					fmt.Printf("Invalidating %s\n", meta.URL)
					os.Remove(path)
					os.Remove(path[:len(path)-5])
				}
				return nil
			})
			filesystemRWLock.Unlock()
			time.Sleep(10 * time.Minute)
		}
	}()

	http.HandleFunc("/", DownloadHandler)
	log.Fatal(http.ListenAndServe(":80", nil))
}
