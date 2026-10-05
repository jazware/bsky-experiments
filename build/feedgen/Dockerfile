# Stage 1: Build the dashboard
FROM node:26-alpine AS dashboard-builder

WORKDIR /app/dashboard

# Install pnpm
RUN npm install -g pnpm@10.29.1

# Copy package files first for better caching
COPY atproto/dashboard/package.json atproto/dashboard/pnpm-lock.yaml* ./

# Install dependencies
RUN pnpm install --frozen-lockfile || pnpm install

# Copy dashboard source
COPY atproto/dashboard/ ./

# Build the dashboard
RUN pnpm build

# Stage 2: Build the Go binary
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
COPY atproto/cmd/feedgen ./cmd/feedgen

# Copy the built dashboard from the previous stage
COPY --from=dashboard-builder /app/dashboard/dist ./dashboard/dist
COPY atproto/dashboard/embed.go ./dashboard/embed.go

# Build the application with version info
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags "-X github.com/jazware/mono/packages/version.GitCommit=${GIT_COMMIT} -X github.com/jazware/mono/packages/version.BuildTime=${BUILD_TIME}" \
    -o feedgen ./cmd/feedgen

# Stage 2: Import SSL certificates
FROM alpine:latest as certs

RUN apk --update add ca-certificates

# Stage 3: Build a minimal Docker image
FROM debian:stable-slim

# Import the SSL certificates from the first stage.
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Copy the binary from the first stage.
COPY --from=builder /app/atproto/feedgen .

# Set the startup command to run the binary
CMD ["./feedgen"]
