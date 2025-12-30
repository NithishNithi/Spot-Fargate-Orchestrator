#!/bin/bash

# Spot Fargate Orchestrator Build Script
# This script builds the Go application with various options

set -e  # Exit on any error

# Configuration
APP_NAME="spot-fargate-orchestrator"
BINARY_NAME="orchestrator"
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')
GO_VERSION=$(go version | awk '{print $3}')

# Build flags
LDFLAGS="-w -s -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GoVersion=${GO_VERSION}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

show_help() {
    cat << EOF
Usage: $0 [OPTIONS]

Build script for ${APP_NAME}

OPTIONS:
    -h, --help          Show this help message
    -c, --clean         Clean build artifacts before building
    -t, --test          Run tests before building
    -l, --lint          Run linter before building
    -m, --tidy          Run go mod tidy before building
    -d, --docker        Build Docker image after binary
    -p, --push          Push Docker image (requires -d)
    -r, --race          Enable race detection
    -v, --verbose       Enable verbose output
    --cross-compile     Build for multiple platforms
    --no-cgo            Disable CGO (default: disabled)
    --output DIR        Output directory (default: ./bin)

EXAMPLES:
    $0                  # Basic build
    $0 -c -t -l -m      # Clean, test, lint, tidy, then build
    $0 -d               # Build binary and Docker image
    $0 --cross-compile  # Build for multiple platforms
    $0 -t -d -p         # Test, build, and push Docker image

ENVIRONMENT VARIABLES:
    VERSION             Version string (default: git describe)
    DOCKER_REGISTRY     Docker registry for image push
    DOCKER_TAG          Docker image tag (default: VERSION)
EOF
}

clean_build() {
    log_info "Cleaning build artifacts..."
    rm -rf ./bin
    rm -rf ./dist
    go clean -cache
    log_success "Build artifacts cleaned"
}

run_tests() {
    log_info "Running tests..."
    if ! go test -v ./...; then
        log_error "Tests failed"
        exit 1
    fi
    log_success "All tests passed"
}

run_linter() {
    log_info "Running linter..."
    
    # Try to find golangci-lint in PATH or GOPATH/bin
    local linter_cmd=""
    if command -v golangci-lint >/dev/null 2>&1; then
        linter_cmd="golangci-lint"
    elif [ -f "$(go env GOPATH)/bin/golangci-lint" ]; then
        linter_cmd="$(go env GOPATH)/bin/golangci-lint"
        log_info "Found golangci-lint in GOPATH/bin"
    fi
    
    if [ "$linter_cmd" != "" ]; then
        if ! $linter_cmd run; then
            log_error "Linting failed"
            exit 1
        fi
        log_success "Linting passed"
    else
        log_warn "golangci-lint not found, skipping linting"
        log_info "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
        log_info "Or add \$(go env GOPATH)/bin to your PATH"
    fi
}

tidy_dependencies() {
    log_info "Tidying Go module dependencies..."
    if ! go mod tidy; then
        log_error "go mod tidy failed"
        exit 1
    fi
    
    # Verify dependencies
    if ! go mod verify; then
        log_error "go mod verify failed"
        exit 1
    fi
    
    log_success "Dependencies tidied and verified"
}

build_binary() {
    local output_dir="$1"
    local goos="$2"
    local goarch="$3"
    local race_flag="$4"
    
    local output_file="${output_dir}/${BINARY_NAME}"
    if [ "$goos" != "" ] && [ "$goos" != "$(go env GOOS)" ]; then
        output_file="${output_dir}/${BINARY_NAME}-${goos}-${goarch}"
    fi
    
    if [ "$goos" = "windows" ]; then
        output_file="${output_file}.exe"
    fi
    
    log_info "Building ${output_file}..."
    
    local build_env=""
    if [ "$goos" != "" ]; then
        build_env="GOOS=${goos} GOARCH=${goarch}"
    fi
    
    local cgo_flag="CGO_ENABLED=0"
    local build_flags="-a -installsuffix cgo -ldflags=\"${LDFLAGS}\""
    
    if [ "$race_flag" = "true" ] && [ "$goos" = "" ]; then
        cgo_flag="CGO_ENABLED=1"
        build_flags="-race ${build_flags}"
        log_warn "Race detection enabled (CGO required)"
    fi
    
    if [ "$VERBOSE" = "true" ]; then
        build_flags="-v ${build_flags}"
    fi
    
    eval "${build_env} ${cgo_flag} go build ${build_flags} -o ${output_file} ."
    
    if [ -f "$output_file" ]; then
        log_success "Built ${output_file}"
        ls -lh "$output_file"
    else
        log_error "Failed to build ${output_file}"
        exit 1
    fi
}

cross_compile() {
    local output_dir="$1"
    
    log_info "Cross-compiling for multiple platforms..."
    
    # Define target platforms
    local platforms=(
        "linux/amd64"
        "linux/arm64"
        "darwin/amd64"
        "darwin/arm64"
        "windows/amd64"
    )
    
    for platform in "${platforms[@]}"; do
        local goos=$(echo "$platform" | cut -d'/' -f1)
        local goarch=$(echo "$platform" | cut -d'/' -f2)
        build_binary "$output_dir" "$goos" "$goarch" "false"
    done
    
    log_success "Cross-compilation completed"
}

build_docker() {
    log_info "Building Docker image..."
    
    local image_name="${APP_NAME}"
    local tag="${DOCKER_TAG:-$VERSION}"
    local full_image="${image_name}:${tag}"
    
    if [ "$DOCKER_REGISTRY" != "" ]; then
        full_image="${DOCKER_REGISTRY}/${full_image}"
    fi
    
    # Build with version information as build args
    if ! docker build \
        --build-arg VERSION="$VERSION" \
        --build-arg BUILD_TIME="$BUILD_TIME" \
        --build-arg GO_VERSION="$GO_VERSION" \
        -t "$full_image" .; then
        log_error "Docker build failed"
        exit 1
    fi
    
    log_success "Docker image built: ${full_image}"
    
    # Also tag as latest if not a dev build
    if [ "$VERSION" != "dev" ] && [[ ! "$VERSION" =~ dirty ]]; then
        local latest_image="${image_name}:latest"
        if [ "$DOCKER_REGISTRY" != "" ]; then
            latest_image="${DOCKER_REGISTRY}/${latest_image}"
        fi
        docker tag "$full_image" "$latest_image"
        log_success "Tagged as latest: ${latest_image}"
    fi
    
    echo "DOCKER_IMAGE=${full_image}" >> build.env
}

push_docker() {
    log_info "Pushing Docker image..."
    
    local tag="${DOCKER_TAG:-$VERSION}"
    local image_name="${APP_NAME}"
    local full_image="${image_name}:${tag}"
    
    if [ "$DOCKER_REGISTRY" != "" ]; then
        full_image="${DOCKER_REGISTRY}/${full_image}"
    fi
    
    if ! docker push "$full_image"; then
        log_error "Docker push failed"
        exit 1
    fi
    
    log_success "Docker image pushed: ${full_image}"
    
    # Push latest tag if available
    if [ "$VERSION" != "dev" ] && [[ ! "$VERSION" =~ dirty ]]; then
        local latest_image="${image_name}:latest"
        if [ "$DOCKER_REGISTRY" != "" ]; then
            latest_image="${DOCKER_REGISTRY}/${latest_image}"
        fi
        if docker image inspect "$latest_image" >/dev/null 2>&1; then
            docker push "$latest_image"
            log_success "Latest image pushed: ${latest_image}"
        fi
    fi
}

# Parse command line arguments
CLEAN=false
TEST=false
LINT=false
TIDY=false
DOCKER=false
PUSH=false
RACE=false
VERBOSE=false
CROSS_COMPILE=false
OUTPUT_DIR="./bin"

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            show_help
            exit 0
            ;;
        -c|--clean)
            CLEAN=true
            shift
            ;;
        -t|--test)
            TEST=true
            shift
            ;;
        -l|--lint)
            LINT=true
            shift
            ;;
        -m|--tidy)
            TIDY=true
            shift
            ;;
        -d|--docker)
            DOCKER=true
            shift
            ;;
        -p|--push)
            PUSH=true
            shift
            ;;
        -r|--race)
            RACE=true
            shift
            ;;
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        --cross-compile)
            CROSS_COMPILE=true
            shift
            ;;
        --no-cgo)
            # Already default, but kept for compatibility
            shift
            ;;
        --output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Validate dependencies
log_info "Validating build environment..."

if ! command -v go >/dev/null 2>&1; then
    log_error "Go is not installed or not in PATH"
    exit 1
fi

if [ "$DOCKER" = "true" ] && ! command -v docker >/dev/null 2>&1; then
    log_error "Docker is not installed or not in PATH"
    exit 1
fi

if [ "$PUSH" = "true" ] && [ "$DOCKER" = "false" ]; then
    log_error "--push requires --docker"
    exit 1
fi

# Show build information
log_info "Build Information:"
echo "  App Name: ${APP_NAME}"
echo "  Binary Name: ${BINARY_NAME}"
echo "  Version: ${VERSION}"
echo "  Build Time: ${BUILD_TIME}"
echo "  Go Version: ${GO_VERSION}"
echo "  Output Directory: ${OUTPUT_DIR}"

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Execute build steps
if [ "$CLEAN" = "true" ]; then
    clean_build
fi

if [ "$TIDY" = "true" ]; then
    tidy_dependencies
fi

if [ "$TEST" = "true" ]; then
    run_tests
fi

if [ "$LINT" = "true" ]; then
    run_linter
fi

# Build binary
if [ "$CROSS_COMPILE" = "true" ]; then
    cross_compile "$OUTPUT_DIR"
else
    build_binary "$OUTPUT_DIR" "" "" "$RACE"
fi

# Build Docker image
if [ "$DOCKER" = "true" ]; then
    build_docker
fi

# Push Docker image
if [ "$PUSH" = "true" ]; then
    push_docker
fi

# Create build info file
cat > "${OUTPUT_DIR}/build-info.txt" << EOF
Build Information
=================
App Name: ${APP_NAME}
Binary Name: ${BINARY_NAME}
Version: ${VERSION}
Build Time: ${BUILD_TIME}
Go Version: ${GO_VERSION}
Git Commit: $(git rev-parse HEAD 2>/dev/null || echo "unknown")
Built By: $(whoami)@$(hostname)
Build Flags: ${LDFLAGS}
EOF

log_success "Build completed successfully!"
log_info "Build artifacts available in: ${OUTPUT_DIR}"

if [ -f "build.env" ]; then
    log_info "Build environment variables saved to: build.env"
fi