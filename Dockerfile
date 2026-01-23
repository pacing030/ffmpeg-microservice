# Stage 1: Build the Go application
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o ffmpeg-microservice .

# Stage 2: Create minimal runtime image
FROM alpine:3.19

# Install FFmpeg with all necessary codecs and CA certificates
RUN apk add --no-cache \
    ffmpeg \
    ca-certificates \
    && rm -rf /var/cache/apk/*

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/ffmpeg-microservice .

# Create temp directory for FFmpeg file processing
RUN mkdir -p /tmp && chmod 1777 /tmp

# Expose port (Railway provides PORT env variable)
EXPOSE 8080

# Create an entrypoint script to handle PORT variable
RUN echo '#!/bin/sh' > /app/entrypoint.sh && \
    echo 'exec ./ffmpeg-microservice -hport=:${PORT:-8080} -ao=*' >> /app/entrypoint.sh && \
    chmod +x /app/entrypoint.sh

CMD ["/app/entrypoint.sh"]
