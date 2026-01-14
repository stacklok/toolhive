#!/bin/bash

# API E2E Test Runner for ToolHive
# This script sets up the environment and runs the API e2e tests

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}ToolHive API E2E Test Runner${NC}"
echo "================================"

# Set TOOLHIVE_DEV environment variable to true
export TOOLHIVE_DEV=true

# Check if container runtime is available
if ! command -v docker &> /dev/null && ! command -v podman &> /dev/null; then
    echo -e "${RED}Error: Neither docker nor podman found${NC}"
    echo "Please install docker or podman to run the API server"
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
export TEST_TIMEOUT

echo ""
echo -e "${YELLOW}Running API E2E Tests...${NC}"
echo ""

# Run the tests
cd "$(dirname "$0")"

# Build ginkgo command with conditional GitHub output flag
GINKGO_CMD="ginkgo run --timeout=\"$TEST_TIMEOUT\""
if [ -n "$GITHUB_ACTIONS" ]; then
    echo -e "${GREEN}✓${NC} GitHub Actions detected, enabling GitHub output format"
    GINKGO_CMD="$GINKGO_CMD --github-output"
else
    GINKGO_CMD="$GINKGO_CMD --vv --show-node-events --trace"
fi

# Optional label filter (LABEL_FILTER or E2E_LABEL_FILTER)
LABEL_FILTER_EFFECTIVE="${LABEL_FILTER:-${E2E_LABEL_FILTER:-}}"
if [ -n "$LABEL_FILTER_EFFECTIVE" ]; then
    echo -e "${GREEN}✓${NC} Using label filter: $LABEL_FILTER_EFFECTIVE"
    GINKGO_CMD="$GINKGO_CMD --label-filter=\"$LABEL_FILTER_EFFECTIVE\""
fi

GINKGO_CMD="$GINKGO_CMD ."

if eval "$GINKGO_CMD"; then
    echo ""
    echo -e "${GREEN}✓ All API E2E tests passed!${NC}"
    exit 0
else
    echo ""
    echo -e "${RED}✗ Some API E2E tests failed${NC}"
    exit 1
fi
