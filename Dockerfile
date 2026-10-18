# Multi-stage build for minimal image size
FROM golang:1.27.2-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git protobuf-dev bash

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download Go protobuf plugins
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 && \
    go mod download

# Ensure protoc-gen-go is on PATH
ENV PATH="/root/go/bin:${PATH}"

# Copy source code
COPY . .

# Generate protobuf files from the vendored Echo schema
RUN bash scripts/generate_proto.sh

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o echo-sync -ldflags="-w -s" ./cmd/metroserver

# Final stage - minimal runtime image
FROM alpine:3.24

# Install runtime dependencies (curl for health checks)
RUN apk add --no-cache ca-certificates

# Create non-root user for security
RUN addgroup -g 1000 echo && \
    adduser -D -u 1000 -G echo echo

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/echo-sync .

# Change ownership to non-root user
RUN mkdir /app/data && chown -R echo:echo /app

# Switch to non-root user
USER echo

# Expose port (default 8080, can be overridden)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider "http://localhost:${PORT:-8080}/health" || exit 1

# Run the application
# PORT can be overridden at runtime using -e PORT=<port>
CMD ["./echo-sync"]
