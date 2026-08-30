# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go.work and modules
COPY go.work go.work
COPY pkg pkg
COPY services/metadata services/metadata

# Build the metadata service
WORKDIR /app/services/metadata
RUN go build -o metadata-service ./cmd

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /app/services/metadata/metadata-service .

# Expose port
EXPOSE 50053

# Run the service
CMD ["./metadata-service"]
