# Stage 1: Build the Go binary
FROM golang:1.26.0 AS builder

# Build args for version info
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

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
COPY atproto/cmd/search ./cmd/search

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags "-X github.com/jazware/mono/packages/version.GitCommit=${GIT_COMMIT} -X github.com/jazware/mono/packages/version.BuildTime=${BUILD_TIME}" \
    -o search ./cmd/search

# Stage 2: Build a minimal Docker image
FROM debian:stable-slim

# Chromium + fonts for rendering post embed cards (screenshots), plus certs
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    chromium \
    fonts-noto-core \
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# Copy the binary from the first stage.
COPY --from=builder /app/atproto/search .

# Set the startup command to run the binary
CMD ["./search"]
