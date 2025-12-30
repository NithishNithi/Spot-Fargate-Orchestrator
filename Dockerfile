# Multi-stage build for Go application
FROM golang:1.23-alpine AS builder

# Install git for go mod download and build info
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build arguments for version information
ARG VERSION
ARG BUILD_TIME
ARG GO_VERSION

# Set default values if not provided
ENV VERSION=${VERSION:-dev}
ENV BUILD_TIME=${BUILD_TIME:-unknown}
ENV GO_VERSION=${GO_VERSION:-unknown}

# Build the application with embedded version info
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -a -installsuffix cgo \
    -ldflags="-w -s -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GoVersion=${GO_VERSION}" \
    -o orchestrator .

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

# Health check using the built-in version flag
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD /app/orchestrator --version > /dev/null || exit 1

# Run the binary
ENTRYPOINT ["/app/orchestrator"]