#!/usr/bin/env bash
#
# Test the optimizer package with sqlite-vec integration
# This script downloads sqlite-vec if needed and runs the full integration tests
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "🔍 ToolHive Optimizer Integration Tests"
echo "=========================================="
echo ""

# Determine OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Map architecture names
case "$ARCH" in
    x86_64)
        ARCH="x86_64"
        ;;
    aarch64|arm64)
        ARCH="aarch64"
        ;;
    *)
        echo -e "${RED}❌ Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

# Map OS names for sqlite-vec download
case "$OS" in
    darwin)
        OS_NAME="macos"
        EXT="dylib"
        ;;
    linux)
        OS_NAME="linux"
        EXT="so"
        ;;
    *)
        echo -e "${RED}❌ Unsupported OS: $OS${NC}"
        exit 1
        ;;
esac

# sqlite-vec configuration
SQLITE_VEC_VERSION="v0.1.1"
SQLITE_VEC_DOWNLOAD_DIR="/tmp/sqlite-vec"
SQLITE_VEC_FILE="$SQLITE_VEC_DOWNLOAD_DIR/vec0.$EXT"

# Check if sqlite-vec is already downloaded
if [ -f "$SQLITE_VEC_FILE" ]; then
    echo -e "${GREEN}✓${NC} sqlite-vec already available at $SQLITE_VEC_FILE"
else
    echo -e "${YELLOW}⬇${NC}  Downloading sqlite-vec ($SQLITE_VEC_VERSION for $OS_NAME-$ARCH)..."
    
    # Create download directory
    mkdir -p "$SQLITE_VEC_DOWNLOAD_DIR"
    
    # Download URL
    DOWNLOAD_URL="https://github.com/asg017/sqlite-vec/releases/download/${SQLITE_VEC_VERSION}/sqlite-vec-0.1.1-loadable-${OS_NAME}-${ARCH}.tar.gz"
    
    # Download and extract
    cd "$SQLITE_VEC_DOWNLOAD_DIR"
    if curl -L -f "$DOWNLOAD_URL" -o sqlite-vec.tar.gz; then
        tar xzf sqlite-vec.tar.gz
        rm sqlite-vec.tar.gz
        echo -e "${GREEN}✓${NC} Downloaded and extracted sqlite-vec"
    else
        echo -e "${RED}❌ Failed to download sqlite-vec from $DOWNLOAD_URL${NC}"
        echo ""
        echo "You can manually download it from:"
        echo "  https://github.com/asg017/sqlite-vec/releases"
        exit 1
    fi
fi

# Verify the file exists
if [ ! -f "$SQLITE_VEC_FILE" ]; then
    echo -e "${RED}❌ sqlite-vec extension not found at $SQLITE_VEC_FILE${NC}"
    exit 1
fi

echo -e "${GREEN}✓${NC} sqlite-vec available: $SQLITE_VEC_FILE"
echo ""

# Run the tests
echo "🧪 Running optimizer tests with sqlite-vec..."
echo ""

cd "$PROJECT_ROOT"

# Set environment and run tests
export SQLITE_VEC_PATH="$SQLITE_VEC_FILE"
export CGO_ENABLED=1

# Run tests with FTS5 support
if go test -tags="fts5" ./pkg/optimizer/ingestion/... -v "$@"; then
    echo ""
    echo -e "${GREEN}✅ All tests passed!${NC}"
    exit 0
else
    echo ""
    echo -e "${RED}❌ Tests failed${NC}"
    exit 1
fi

