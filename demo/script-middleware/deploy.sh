#!/usr/bin/env bash
# Deploy the script middleware demo to a local Kind cluster.
# Prerequisites: kind, kubectl, docker, task (Taskfile)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CLUSTER_NAME="script-demo"

echo "=== Script Middleware Demo ==="
echo ""

# 1. Create Kind cluster (if not exists)
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Kind cluster '$CLUSTER_NAME' already exists, reusing."
else
    echo "Creating Kind cluster '$CLUSTER_NAME'..."
    cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 30080
        hostPort: 4483
        protocol: TCP
EOF
fi

export KUBECONFIG="$(kind get kubeconfig-path --name="$CLUSTER_NAME" 2>/dev/null || echo "$HOME/.kube/config")"
kubectl config use-context "kind-${CLUSTER_NAME}"

# 2. Build and load images
echo ""
echo "Building operator and vmcp images..."
cd "$REPO_ROOT"
task build-all-images 2>&1 | tail -5

echo "Loading images into Kind cluster..."
kind load docker-image ghcr.io/stacklok/toolhive/operator:latest --name "$CLUSTER_NAME"
kind load docker-image ghcr.io/stacklok/toolhive/vmcp:latest --name "$CLUSTER_NAME"
kind load docker-image ghcr.io/stacklok/toolhive/proxyrunner:latest --name "$CLUSTER_NAME"

# 3. Install CRDs and operator
echo ""
echo "Installing CRDs..."
kubectl apply -f deploy/charts/operator-crds/files/crds/ 2>&1 | head -5

echo "Deploying operator..."
helm upgrade --install thv-operator deploy/charts/operator \
    --namespace toolhive-system --create-namespace \
    --set image.tag=latest \
    --set vmcpImage.tag=latest \
    --set proxyRunnerImage.tag=latest \
    --wait --timeout 120s 2>&1 | tail -3

# 4. Deploy demo manifests
echo ""
echo "Deploying demo MCP servers..."
kubectl apply -f "$SCRIPT_DIR/manifests.yaml"

# 5. Wait for VirtualMCPServer
echo ""
echo "Waiting for VirtualMCPServer to be ready..."
kubectl wait --for=condition=Ready virtualmcpserver/demo-vmcp \
    -n script-demo --timeout=180s 2>&1 || true

# 6. Patch the NodePort to use 30080 (mapped to host 4483)
echo ""
echo "Configuring NodePort..."
VMCP_SVC=$(kubectl get svc -n script-demo -l app.kubernetes.io/instance=demo-vmcp -o name | head -1)
if [ -n "$VMCP_SVC" ]; then
    kubectl patch "$VMCP_SVC" -n script-demo --type='json' \
        -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30080}]' 2>/dev/null || true
fi

echo ""
echo "=== Demo Ready ==="
echo ""
echo "VirtualMCPServer: http://localhost:4483/mcp"
echo ""
echo "Tools available: slack (4), jira (4), confluence (2), github (4),"
echo "                 pagerduty (3), datadog (3), google-drive (2), linear (2)"
echo "                 + execute_tool_script"
echo ""
echo "Connect with an MCP client or add to Claude Code settings:"
echo '  { "mcpServers": { "demo": { "url": "http://localhost:4483/mcp" } } }'
echo ""
echo "Teardown: kind delete cluster --name $CLUSTER_NAME"
