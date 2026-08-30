# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go.work and modules
COPY go.work go.work
COPY pkg pkg
COPY services/upload services/upload

# Build the upload service
WORKDIR /app/services/upload
RUN go build -o upload-service ./cmd

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /app/services/upload/upload-service .

# Create uploads directory
RUN mkdir -p /uploads

# Expose port
EXPOSE 50052

# Run the service
CMD ["./upload-service"]
