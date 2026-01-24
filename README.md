# Metroserver

A high performance Go server for Metrolist, for the listen together feature.

# Quickstart

## Locally

```bash
# Install dependencies
go mod download

# Build the server
go build -o main .

# Run on default port 8080
./main

# Run on custom port
PORT=9000 ./main
```

## Docker

```bash
# Build locally
docker build -t metroserver:latest .

# Run on port 8080
docker run -d \
  -p 8080:8080 \
  -e PORT=8080 \
  --name metroserver \
  metroserver:latest

# Run on custom port
docker run -d \
  -p 9000:9000 \
  -e PORT=9000 \
  --name metroserver \
  metroserver:latest
```

## Docker Compose

```yaml
---
services:
  metroserver:
    image: ghcr.io/nyxiereal/metrolist/metroserver:latest
    ports:
      - "8080:8080"
    environment:
      - PORT=8080
    healthcheck:
      test:
        [
          "CMD",
          "wget",
          "--no-verbose",
          "--tries=1",
          "--spider",
          "http://localhost:8080/health",
        ]
      interval: 30s
      timeout: 3s
      retries: 3
      start_period: 5s
    restart: unless-stopped
```
