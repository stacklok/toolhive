#!/bin/bash

# E2E Test Runner for ToolHive
# This script sets up the environment and runs the e2e tests

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}ToolHive E2E Test Runner${NC}"
echo "================================"

# Check if thv binary exists
THV_BINARY="${THV_BINARY:-thv}"
if ! command -v "$THV_BINARY" &> /dev/null; then
    echo -e "${RED}Error: thv binary not found in PATH${NC}"
    echo "Please build the binary first with: task build"
    echo "Or set THV_BINARY environment variable to the binary path"
    exit 1
fi

echo -e "${GREEN}✓${NC} Found thv binary: $(which $THV_BINARY)"

# Check if container runtime is available
if ! command -v docker &> /dev/null && ! command -v podman &> /dev/null; then
    echo -e "${RED}Error: Neither docker nor podman found${NC}"
    echo "Please install docker or podman to run MCP servers"
    exit 1
fi

if command -v docker &> /dev/null; then
    echo -e "${GREEN}✓${NC} Found container runtime: docker"
else
    echo -e "${GREEN}✓${NC} Found container runtime: podman"
fi

# Set test timeout
TEST_TIMEOUT="${TEST_TIMEOUT:-10m}"
echo -e "${GREEN}✓${NC} Test timeout: $TEST_TIMEOUT"

# Export environment variables for tests
export THV_BINARY
export TEST_TIMEOUT

echo ""
echo -e "${YELLOW}Running E2E Tests...${NC}"
echo ""

# Run the tests
cd "$(dirname "$0")"
if ginkgo run --timeout="$TEST_TIMEOUT" --v --progress --trace .; then
    echo ""
    echo -e "${GREEN}✓ All E2E tests passed!${NC}"
    exit 0
else
    echo ""
    echo -e "${RED}✗ Some E2E tests failed${NC}"
    exit 1
fi