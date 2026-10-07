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
COPY atproto/cmd/search ./cmd/search

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -installsuffix cgo \
    -ldflags "-X github.com/jazware/mono/packages/version.GitCommit=${GIT_COMMIT} -X github.com/jazware/mono/packages/version.BuildTime=${BUILD_TIME}" \
    -o search ./cmd/search

# Stage 2: Build a minimal Docker image
# runtime-base is everything but the binary: the base `just docker-push` puts
# ko's layer on (pinned by digest in ../../.ko.yaml), so the image config lives
# here for both builds. ko sets the entrypoint to /ko-app/search.
FROM debian:stable-slim AS runtime-base

# Chromium + fonts for rendering post embed cards (screenshots), plus certs.
# Chromium wants a writable home for its profile and crash dirs.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    chromium \
    fonts-noto-core \
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 atproto \
    && useradd --system --uid 10001 --gid atproto --home-dir /home/atproto --create-home atproto
USER atproto:atproto
CMD ["./search"]

FROM runtime-base AS runtime

# Copy the binary from the first stage.
COPY --from=builder /app/atproto/search .
