# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go.work and modules
COPY go.work go.work
COPY pkg pkg
COPY services/auth services/auth

# Build the auth service
WORKDIR /app/services/auth
RUN go build -o auth-service ./cmd

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /app/services/auth/auth-service .

# Expose port
EXPOSE 50051

# Health check
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:50051/health || exit 1

# Run the service
CMD ["./auth-service"]
