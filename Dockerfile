# Stage 1: Build the Go application
FROM golang:1.21-alpine AS builder

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

# Expose port (Railway will override with PORT env variable)
EXPOSE 8080

# Run the microservice
# Railway provides PORT env variable, use it or default to 8080
CMD ["sh", "-c", "./ffmpeg-microservice -listen-addr=:${PORT:-8080} -allowed-origins=*"]
