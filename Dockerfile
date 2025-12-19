# Multi-stage build for Go application
FROM golang:1.21-alpine AS builder

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o orchestrator ./cmd/orchestrator

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1001 -S orchestrator && \
    adduser -u 1001 -S orchestrator -G orchestrator

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/orchestrator .

# Change ownership to non-root user
RUN chown orchestrator:orchestrator orchestrator

# Switch to non-root user
USER orchestrator

# Expose port (if needed for health checks)
EXPOSE 8080

# Run the binary
CMD ["./orchestrator"]