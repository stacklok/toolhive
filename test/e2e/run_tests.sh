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

# Set TOOLHIVE_DEV environment variable to true
export TOOLHIVE_DEV=true

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
TEST_TIMEOUT="${TEST_TIMEOUT:-20m}"
echo -e "${GREEN}✓${NC} Test timeout: $TEST_TIMEOUT"

# Set number of parallel Ginkgo processes (default 1 = sequential)
PROCS="${PROCS:-1}"
echo -e "${GREEN}✓${NC} Ginkgo procs: $PROCS"

# Export environment variables for tests
export THV_BINARY
export TEST_TIMEOUT

echo ""
echo -e "${YELLOW}Running E2E Tests...${NC}"
echo ""

# Run the tests
cd "$(dirname "$0")"

# Build ginkgo command with conditional GitHub output flag
GINKGO_CMD="ginkgo run --timeout=\"$TEST_TIMEOUT\" --procs=$PROCS"
GINKGO_CMD="$GINKGO_CMD --junit-report=junit-report.xml --output-dir=."
GINKGO_CMD="$GINKGO_CMD --silence-skips"
if [ -n "$GITHUB_ACTIONS" ]; then
    echo -e "${GREEN}✓${NC} GitHub Actions detected, enabling GitHub output format"
    GINKGO_CMD="$GINKGO_CMD --github-output --vv"
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

# List workload names currently known to thv, one per line. Returns nothing
# (not an error) if thv isn't available or the call fails, so the sweep below
# degrades to a no-op instead of crashing the script.
list_workload_names() {
    "$THV_BINARY" list --all --format json 2>/dev/null | grep -o '"name": *"[^"]*"' | sed -E 's/.*"([^"]*)"$/\1/'
}

# Snapshot workloads that already existed before this run, so the post-run
# sweep only ever touches workloads this run created. A developer's own
# pre-existing workloads are never in this diff and are never touched.
PRE_RUN_WORKLOADS="$(list_workload_names || true)"

if eval "$GINKGO_CMD"; then
    GINKGO_EXIT=0
    echo ""
    echo -e "${GREEN}✓ All E2E tests passed!${NC}"
else
    GINKGO_EXIT=$?
    echo ""
    echo -e "${RED}✗ Some E2E tests failed${NC}"
fi

# Safety net: sweep workloads leaked by this run (e.g. Ginkgo's --timeout
# force-killing the suite mid-test, skipping AfterEach/AfterSuite cleanup).
# This runs unconditionally, regardless of the ginkgo exit code above, and
# only removes workloads that appeared during this run (diffed against the
# pre-run snapshot) -- never a blanket "remove everything thv knows about".
echo ""
echo -e "${YELLOW}Checking for leaked e2e workloads...${NC}"
LEAKED_WORKLOADS="$(comm -13 <(echo "$PRE_RUN_WORKLOADS" | sort) <(list_workload_names | sort) || true)"
if [ -n "$LEAKED_WORKLOADS" ]; then
    echo -e "${YELLOW}Sweeping leaked workloads not cleaned up by the test run:${NC}"
    while IFS= read -r workload; do
        [ -z "$workload" ] && continue
        echo "  - $workload"
        if ! RM_OUTPUT=$("$THV_BINARY" rm "$workload" 2>&1); then
            echo -e "    ${RED}(failed to remove $workload, leaving for manual cleanup)${NC}"
            echo "$RM_OUTPUT" | sed 's/^/    /'
        fi
    done <<< "$LEAKED_WORKLOADS"
else
    echo -e "${GREEN}✓${NC} No leaked workloads found"
fi

exit "$GINKGO_EXIT"
