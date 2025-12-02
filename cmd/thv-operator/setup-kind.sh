#!/bin/bash
# Setup a local Kind cluster with ToolHive Operator installed
#
# This script:
# 1. Creates a Kind cluster with DNS configured via extraMounts
# 2. Installs the ToolHive Operator CRDs
# 3. Deploys the latest ToolHive Operator from GitHub
#
# Usage:
#   ./cmd/thv-operator/setup-kind.sh
#   (can be run from anywhere in the repo)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Get the repo root directory (parent of cmd/)
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "Setting up Kind cluster with ToolHive Operator..."
echo ""

# Step 1: Create Kind cluster
# Run from repo root - the task will cd to cmd/thv-operator internally
echo "Step 1/3: Creating Kind cluster..."
cd "${REPO_ROOT}"

# Check if cluster already exists
if kind get clusters 2>/dev/null | grep -q "^toolhive$"; then
    echo "  Kind cluster 'toolhive' already exists. Destroying it first..."
    task kind-destroy || true
fi

task kind-setup

# Step 2: Install CRDs
# Run from repo root - the tasks expect deploy/charts to be relative to repo root
echo ""
echo "Step 2/3: Installing ToolHive Operator CRDs..."
cd "${REPO_ROOT}"

# Generate manifests if charts don't exist
if [ ! -d "deploy/charts/operator-crds" ]; then
    echo "  Generating operator manifests..."
    cd "${SCRIPT_DIR}"
    task operator-manifests
    cd "${REPO_ROOT}"
fi

task operator-install-crds

# Step 3: Deploy latest operator
# Run from repo root - the tasks expect deploy/charts to be relative to repo root
echo ""
echo "Step 3/3: Deploying latest ToolHive Operator from GitHub..."
task operator-deploy-latest

echo ""
echo "✓ Kind cluster setup complete!"
echo ""
echo "Verify the operator is running:"
echo "  kubectl get pods -n toolhive-system"
echo ""
echo "Check operator logs:"
echo "  kubectl logs -n toolhive-system -l app.kubernetes.io/name=toolhive-operator"

