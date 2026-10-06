# Stage 1: Build the Go binary
FROM --platform=$BUILDPLATFORM golang:1.26.8 AS builder

# Build args for version info
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TARGETOS=linux
ARG TARGETARCH

# Create a directory for the application
WORKDIR /app

# Copy packages to maintain the relative structure
COPY telemetry ./telemetry
COPY version ./version

# Set working directory to atproto package
RUN mkdir -p /app/atproto
WORKDIR /app/atproto

# Copy in go.mod and go.sum first to leverage Docker cache
COPY atproto/go.mod .
COPY atproto/go.sum .

# Fetch dependencies
RUN go mod download

# Copy relevant source files
COPY atproto/pkg ./pkg
COPY atproto/cmd/indexer ./cmd/indexer

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -installsuffix cgo \
    -ldflags "-X github.com/jazware/mono/packages/version.GitCommit=${GIT_COMMIT} -X github.com/jazware/mono/packages/version.BuildTime=${BUILD_TIME}" \
    -o indexer ./cmd/indexer

# Stage 2: Import SSL certificates
FROM --platform=$BUILDPLATFORM alpine:latest AS certs

RUN apk --update add ca-certificates

# Stage 3: Build a minimal Docker image
FROM debian:stable-slim

# Import the SSL certificates from the first stage.
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Copy the binary from the first stage.
COPY --from=builder /app/atproto/indexer .

# Set the startup command to run the binary
CMD ["./indexer"]
