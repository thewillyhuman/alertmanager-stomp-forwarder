# Build the binary from the sources files
FROM golang:1.20.4 AS build-stage
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /alertmanager-stomp-forwarder

# Compress the binary to get the smaller possible image
FROM alpine:latest as compress-stage
WORKDIR /
COPY --from=build-stage /alertmanager-stomp-forwarder /alertmanager-stomp-forwarder
RUN apk add upx
RUN upx --best --lzma -o /alertmanager-stomp-forwarder-release /alertmanager-stomp-forwarder

# Deploy the application binary into a lean image
FROM scratch AS release-stage
WORKDIR /
COPY --from=compress-stage /alertmanager-stomp-forwarder-release /alertmanager-stomp-forwarder-release
ENTRYPOINT ["/alertmanager-amq-forwarder-release"]
