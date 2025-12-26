# Multi-stage build for Go application
FROM golang:1.23-alpine AS builder

# Install git for go mod download
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application (main.go is at root level)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -ldflags="-w -s" -o orchestrator .

# Final stage - use Amazon Linux 2023
FROM amazonlinux:2023

# Install ca-certificates, shadow-utils, and AWS CLI
RUN dnf update -y && \
    dnf install -y ca-certificates shadow-utils awscli && \
    dnf clean all

# Create non-root user
RUN groupadd -r orchestrator && \
    useradd -r -g orchestrator -s /sbin/nologin orchestrator

# Create app directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/orchestrator /app/orchestrator

# Change ownership to non-root user
RUN chown -R orchestrator:orchestrator /app

# Switch to non-root user
USER orchestrator

# Expose port for API server
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD /app/orchestrator --health-check || exit 1

# Run the binary
ENTRYPOINT ["/app/orchestrator"]