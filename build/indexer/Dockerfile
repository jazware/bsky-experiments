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
COPY telemetry ./atproto/telemetry
COPY version ./atproto/version

# Set working directory to atproto package
RUN mkdir -p /app/atproto
WORKDIR /app/atproto

# Copy in go.mod and go.sum first to leverage Docker cache
COPY go.mod .
COPY go.sum .

# Fetch dependencies
RUN go mod download

# Copy relevant source files
COPY pkg ./pkg
COPY cmd/indexer ./cmd/indexer

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -installsuffix cgo \
    -ldflags "-X github.com/jazware/bsky-experiments/version.GitCommit=${GIT_COMMIT} -X github.com/jazware/bsky-experiments/version.BuildTime=${BUILD_TIME}" \
    -o indexer ./cmd/indexer

# Stage 2: Import SSL certificates
FROM --platform=$BUILDPLATFORM alpine:latest AS certs

RUN apk --update add ca-certificates

# runtime-base is everything but the binary: the base `just docker-push` puts
# ko's layer on (pinned by digest in ../../.ko.yaml), so the image config lives
# here for both builds. ko sets the entrypoint to /ko-app/indexer.
FROM debian:stable-slim AS runtime-base

# Import the SSL certificates from the first stage.
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN groupadd --system --gid 10001 atproto \
    && useradd --system --uid 10001 --gid atproto --no-create-home atproto
USER atproto:atproto
CMD ["./indexer"]

FROM runtime-base AS runtime

# Copy the binary from the first stage.
COPY --from=builder /app/atproto/indexer .
