FROM golang:1.16 as builder
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o apt-cacher *.go

FROM busybox
COPY --from=builder /build/apt-cacher /apt-cacher
RUN mkdir -p /cache
CMD ["/apt-cacher"]
