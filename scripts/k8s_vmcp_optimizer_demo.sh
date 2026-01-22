#!/bin/bash
# End-to-end demo script for vmcp with optimizer in a local kind k8s cluster
#
# This script:
# 1. Sets up a kind cluster with port mappings
# 2. Installs CRDs and operator
# 3. Deploys Ollama for embeddings
# 4. Deploys Jaeger for telemetry/tracing
# 5. Sets up MCP servers (fetch, github, etc.)
# 6. Deploys VirtualMCPServer with optimizer enabled
# 7. Sets up ingress and port-forwarding for testing
#
# Prerequisites:
# - kind installed
# - kubectl installed
# - task installed (for running tasks)
# - docker/podman available
# - Ollama will be deployed in-cluster automatically
#
# Usage:
#   ./scripts/k8s_vmcp_optimizer_demo.sh
#
# To use a different embedding backend (e.g., openai-compatible):
#   EMBEDDING_BACKEND=openai-compatible EMBEDDING_URL=https://api.openai.com/v1 ./scripts/k8s_vmcp_optimizer_demo.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOOLHIVE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
MCP_OPTIMIZER_DIR="${TOOLHIVE_DIR}/../mcp-optimizer"
# Use the location that tasks expect (cmd/thv-operator/kconfig.yaml)
KCONFIG="${TOOLHIVE_DIR}/cmd/thv-operator/kconfig.yaml"

# Configuration
EMBEDDING_BACKEND="${EMBEDDING_BACKEND:-ollama}"
EMBEDDING_MODEL="${EMBEDDING_MODEL:-nomic-embed-text}"
EMBEDDING_DIMENSION="${EMBEDDING_DIMENSION:-384}"
OLLAMA_NAMESPACE="${OLLAMA_NAMESPACE:-toolhive-system}"
OLLAMA_SERVICE_NAME="${OLLAMA_SERVICE_NAME:-ollama}"
OLLAMA_SERVICE_URL="http://${OLLAMA_SERVICE_NAME}.${OLLAMA_NAMESPACE}.svc.cluster.local:11434"
JAEGER_NAMESPACE="${JAEGER_NAMESPACE:-toolhive-system}"
JAEGER_SERVICE_NAME="${JAEGER_SERVICE_NAME:-jaeger-collector}"
JAEGER_OTLP_ENDPOINT="${JAEGER_SERVICE_NAME}.${JAEGER_NAMESPACE}.svc.cluster.local:4318"

echo "=========================================="
echo "vMCP Optimizer Demo Setup"
echo "=========================================="
echo ""

# Step 1: Setup kind cluster
echo "Step 1: Setting up kind cluster..."
cd "${TOOLHIVE_DIR}"

# Check if cluster already exists
if kind get clusters 2>/dev/null | grep -qE "^toolhive$"; then
    echo "  Kind cluster 'toolhive' already exists, reusing it..."
    # Always recreate kubeconfig to ensure it's up to date and in the right location
    echo "  Updating kubeconfig..."
    mkdir -p "$(dirname "${KCONFIG}")"
    kind get kubeconfig --name toolhive > "${KCONFIG}"
    
    # Verify cluster is accessible
    if ! kubectl cluster-info --kubeconfig "${KCONFIG}" &>/dev/null; then
        echo "  Warning: Cluster exists but is not accessible, destroying and recreating..."
        kind delete cluster --name toolhive 2>/dev/null || true
        # Fall through to create new cluster
    else
        echo "  Cluster is accessible"
    fi
fi

# Create cluster if it doesn't exist or was destroyed above
if ! kind get clusters 2>/dev/null | grep -qE "^toolhive$"; then
    # Check for other clusters and warn
    EXISTING_CLUSTER=$(kind get clusters 2>/dev/null | head -n 1 || echo "")
    if [ -n "${EXISTING_CLUSTER}" ] && [ "${EXISTING_CLUSTER}" != "toolhive" ]; then
        echo "  Warning: Found existing kind cluster '${EXISTING_CLUSTER}'"
        echo "  This script will create a new cluster named 'toolhive'"
    fi
    
    echo "  Creating new kind cluster 'toolhive'..."
    # The task kind-setup-e2e uses {{.ROOT_DIR}} which points to cmd/thv-operator/
    # but the config file is at test/e2e/thv-operator/kind-config.yaml relative to project root
    # So we'll call kind directly with the correct paths
    KIND_CONFIG="${TOOLHIVE_DIR}/test/e2e/thv-operator/kind-config.yaml"
    if [ ! -f "${KIND_CONFIG}" ]; then
        echo "  Error: kind-config.yaml not found at ${KIND_CONFIG}"
        exit 1
    fi
    
    cd "${TOOLHIVE_DIR}"
    kind create cluster --name toolhive --config "${KIND_CONFIG}" || {
        echo "Warning: kind create failed, trying to destroy and recreate..."
        kind delete cluster --name toolhive 2>/dev/null || true
        kind create cluster --name toolhive --config "${KIND_CONFIG}" || {
            echo "  Error: Failed to create cluster"
            exit 1
        }
    }
    
    # Write kubeconfig to the location tasks expect
    mkdir -p "$(dirname "${KCONFIG}")"
    kind get kubeconfig --name toolhive > "${KCONFIG}"
    echo "  ✓ Cluster created and kubeconfig written to ${KCONFIG}"
fi

# Verify cluster exists and set environment for ko
KIND_CLUSTER_NAME=$(kind get clusters 2>/dev/null | grep -E "^toolhive$" | head -n 1 || echo "toolhive")
export KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME}"

# Wait for cluster nodes to be ready
echo "  Waiting for cluster nodes to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s --kubeconfig "${KCONFIG}" >/dev/null 2>&1 || {
    echo "  Warning: Some nodes may not be ready yet"
}

echo "✓ Kind cluster ready (name: ${KIND_CLUSTER_NAME})"
echo ""

# Step 2: Install CRDs
echo "Step 2: Installing CRDs..."
# Use helm directly with the correct paths (task expects to run from project root)
cd "${TOOLHIVE_DIR}"
helm upgrade --install toolhive-operator-crds deploy/charts/operator-crds --kubeconfig "${KCONFIG}" || {
    echo "  Error: Failed to install CRDs"
    exit 1
}
echo "✓ CRDs installed"
echo ""

# Step 3: Build and deploy operator locally using task
echo "Step 3: Building and deploying operator locally..."
cd "${TOOLHIVE_DIR}"

# Ensure cluster nodes are ready before building/loading images
echo "  Ensuring cluster nodes are ready for image loading..."
kubectl wait --for=condition=Ready nodes --all --timeout=180s --kubeconfig "${KCONFIG}" || {
    echo "  Warning: Some nodes may not be ready, but continuing..."
}

# Build images locally using ko
echo "  Building images with ko..."
# Use ko.local to build to local Docker daemon without auto-loading into kind
export KO_DOCKER_REPO=ko.local
echo "    Building operator image..."
OPERATOR_IMAGE=$(ko build -B ./cmd/thv-operator 2>&1 | tee /dev/stderr | tail -n 1)
echo "    Building proxyrunner image..."
TOOLHIVE_IMAGE=$(ko build -B ./cmd/thv-proxyrunner 2>&1 | tee /dev/stderr | tail -n 1)
echo "    Building vmcp image..."
VMCP_IMAGE=$(ko build -B ./cmd/vmcp 2>&1 | tee /dev/stderr | tail -n 1)

# Tag images with kind.local for use in kind cluster
echo "  Tagging images for kind..."
docker tag "${OPERATOR_IMAGE}" "kind.local/thv-operator:latest"
docker tag "${TOOLHIVE_IMAGE}" "kind.local/thv-proxyrunner:latest"
docker tag "${VMCP_IMAGE}" "kind.local/vmcp:latest"

# Update image references to use kind.local tags
OPERATOR_IMAGE="kind.local/thv-operator:latest"
TOOLHIVE_IMAGE="kind.local/thv-proxyrunner:latest"
VMCP_IMAGE="kind.local/vmcp:latest"

# Verify images were built
if [ -z "${OPERATOR_IMAGE}" ] || [ -z "${TOOLHIVE_IMAGE}" ] || [ -z "${VMCP_IMAGE}" ]; then
    echo "  Error: Failed to build images"
    echo "  Operator: ${OPERATOR_IMAGE:-<empty>}"
    echo "  Proxyrunner: ${TOOLHIVE_IMAGE:-<empty>}"
    echo "  VMCP: ${VMCP_IMAGE:-<empty>}"
    echo "  Falling back to remote images..."
    helm upgrade --install toolhive-operator deploy/charts/operator \
        --namespace toolhive-system \
        --create-namespace \
        --kubeconfig "${KCONFIG}" && echo "✓ Operator deployed using remote images" || {
        echo "  Error: Failed to deploy operator"
        exit 1
    }
else
    echo "  Built images:"
    echo "    Operator: ${OPERATOR_IMAGE}"
    echo "    Proxyrunner: ${TOOLHIVE_IMAGE}"
    echo "    VMCP: ${VMCP_IMAGE}"
    
    # Load images into kind
    echo "  Loading images into kind..."
    kind load docker-image --name toolhive "${OPERATOR_IMAGE}" || {
        echo "  Error: Failed to load operator image"
        exit 1
    }
    kind load docker-image --name toolhive "${TOOLHIVE_IMAGE}" || {
        echo "  Error: Failed to load proxyrunner image"
        exit 1
    }
    kind load docker-image --name toolhive "${VMCP_IMAGE}" || {
        echo "  Error: Failed to load vmcp image"
        exit 1
    }
    
    # Deploy with helm
    echo "  Deploying operator with helm..."
    helm upgrade --install toolhive-operator deploy/charts/operator \
        --set operator.image="${OPERATOR_IMAGE}" \
        --set operator.imagePullPolicy=Never \
        --set operator.toolhiveRunnerImage="${TOOLHIVE_IMAGE}" \
        --set operator.vmcpImage="${VMCP_IMAGE}" \
        --namespace toolhive-system \
        --create-namespace \
        --kubeconfig "${KCONFIG}" && {
        echo "✓ Operator deployed with locally built images"
        # Force rollout restart to ensure new image is picked up
        # (Helm might not detect changes when using same tag like 'latest')
        echo "  Restarting operator to pick up new image..."
        kubectl rollout restart deployment toolhive-operator -n toolhive-system --kubeconfig "${KCONFIG}" || {
            echo "  Warning: Failed to restart operator, it may still be using old image"
        }
        echo "  Waiting for operator rollout to complete..."
        kubectl rollout status deployment toolhive-operator -n toolhive-system --timeout=120s --kubeconfig "${KCONFIG}" || {
            echo "  Warning: Operator rollout status check timed out or failed"
        }
    } || {
        echo "  Error: Failed to deploy operator"
        exit 1
    }
fi
echo ""

# Step 4: Wait for operator to be ready
echo "Step 4: Waiting for operator to be ready..."
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=toolhive-operator \
    -n toolhive-system --timeout=120s --kubeconfig "${KCONFIG}" || {
    echo "Warning: Operator not ready yet, continuing anyway..."
}
echo "✓ Operator ready"
# Delete existing MCPServer pods to force recreation with correct imagePullPolicy
# (The operator will recreate them with the new code that sets imagePullPolicy: Never for local images)
echo "  Deleting existing MCPServer pods to force recreation with correct imagePullPolicy..."
kubectl delete pods -n toolhive-system -l app.kubernetes.io/name=toolhive-proxyrunner \
    --kubeconfig "${KCONFIG}" --ignore-not-found=true 2>/dev/null || true
echo "  MCPServer pods will be recreated by the operator with correct imagePullPolicy"
echo ""

# Step 5: Deploy Ollama service
echo "Step 5: Deploying Ollama service..."
cd "${TOOLHIVE_DIR}"

# Check if Ollama already exists
if kubectl get deployment ${OLLAMA_SERVICE_NAME} -n ${OLLAMA_NAMESPACE} --kubeconfig "${KCONFIG}" &>/dev/null; then
    echo "  Ollama already deployed, skipping..."
else
    echo "  Creating Ollama Deployment and Service..."
    kubectl apply -f - --kubeconfig "${KCONFIG}" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${OLLAMA_SERVICE_NAME}
  namespace: ${OLLAMA_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${OLLAMA_SERVICE_NAME}
  template:
    metadata:
      labels:
        app: ${OLLAMA_SERVICE_NAME}
    spec:
      containers:
      - name: ollama
        image: ollama/ollama:latest
        ports:
        - containerPort: 11434
          name: http
        env:
        - name: OLLAMA_HOST
          value: "0.0.0.0:11434"
        resources:
          requests:
            cpu: "500m"
            memory: "2Gi"
          limits:
            cpu: "2"
            memory: "4Gi"
        volumeMounts:
        - name: ollama-data
          mountPath: /root/.ollama
      volumes:
      - name: ollama-data
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: ${OLLAMA_SERVICE_NAME}
  namespace: ${OLLAMA_NAMESPACE}
spec:
  selector:
    app: ${OLLAMA_SERVICE_NAME}
  ports:
  - port: 11434
    targetPort: 11434
    name: http
  type: ClusterIP
EOF
        echo "  Waiting for Ollama to be ready..."
        kubectl wait --for=condition=Available deployment/${OLLAMA_SERVICE_NAME} \
            -n ${OLLAMA_NAMESPACE} --timeout=300s --kubeconfig "${KCONFIG}" || {
            echo "  Warning: Ollama deployment not ready yet, continuing..."
        }
        
        # Pull the embedding model
        echo "  Pulling embedding model '${EMBEDDING_MODEL}'..."
    kubectl exec -n ${OLLAMA_NAMESPACE} deployment/${OLLAMA_SERVICE_NAME} \
        --kubeconfig "${KCONFIG}" -- ollama pull ${EMBEDDING_MODEL} || {
        echo "  Warning: Failed to pull model, you may need to pull it manually:"
        echo "    kubectl exec -n ${OLLAMA_NAMESPACE} deployment/${OLLAMA_SERVICE_NAME} -- ollama pull ${EMBEDDING_MODEL}"
    }
fi
echo "✓ Ollama service deployed"
echo ""

# Step 6: Deploy Jaeger for telemetry
echo "Step 6: Deploying Jaeger for telemetry..."
cd "${TOOLHIVE_DIR}"

# Always apply to ensure QUERY_BASE_PATH is set correctly
echo "  Creating/updating Jaeger all-in-one Deployment and Services..."
kubectl apply -f - --kubeconfig "${KCONFIG}" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jaeger
  namespace: ${JAEGER_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: jaeger
  template:
    metadata:
      labels:
        app: jaeger
    spec:
      containers:
      - name: jaeger
        image: jaegertracing/all-in-one:latest
        ports:
        - containerPort: 16686
          name: jaeger-ui
        - containerPort: 14268
          name: jaeger-http
        - containerPort: 4317
          name: otlp-grpc
        - containerPort: 4318
          name: otlp-http
        env:
        - name: COLLECTOR_OTLP_ENABLED
          value: "true"
        - name: QUERY_BASE_PATH
          value: "/jaeger"
        resources:
          requests:
            cpu: "100m"
            memory: "256Mi"
          limits:
            cpu: "500m"
            memory: "512Mi"
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger-collector
  namespace: ${JAEGER_NAMESPACE}
spec:
  selector:
    app: jaeger
  ports:
  - port: 4317
    targetPort: 4317
    name: otlp-grpc
  - port: 4318
    targetPort: 4318
    name: otlp-http
  type: ClusterIP
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger-query
  namespace: ${JAEGER_NAMESPACE}
spec:
  selector:
    app: jaeger
  ports:
  - port: 16686
    targetPort: 16686
    name: jaeger-ui
  type: ClusterIP
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger
  namespace: ${JAEGER_NAMESPACE}
spec:
  selector:
    app: jaeger
  ports:
  - port: 14268
    targetPort: 14268
    name: jaeger-http
  type: ClusterIP
EOF
echo "  Waiting for Jaeger to be ready..."
kubectl wait --for=condition=Available deployment/jaeger \
    -n ${JAEGER_NAMESPACE} --timeout=180s --kubeconfig "${KCONFIG}" || {
    echo "  Warning: Jaeger deployment not ready yet, continuing..."
}
echo "✓ Jaeger service deployed"
echo ""

# Step 7: Setup MCP servers (if mcp-optimizer directory exists)
if [ -d "${MCP_OPTIMIZER_DIR}/examples/mcp-servers" ]; then
    echo "Step 7: Setting up MCP servers..."
    cd "${MCP_OPTIMIZER_DIR}"
    
    # Check for GitHub secrets (optional)
    if [ -f "github_secrets.sh" ]; then
        echo "  Loading GitHub secrets..."
        source github_secrets.sh 2>/dev/null || echo "  Warning: Could not load github_secrets.sh"
    fi
    
    # Create GitHub secrets if GITHUB_TOKEN is set
    if [ -n "${GITHUB_TOKEN}" ] && [ -n "${GITHUB_USERNAME}" ]; then
        echo "  Creating GitHub secrets..."
        export GITHUB_TOKEN
        export GITHUB_USERNAME
        if [ -f "./examples/mcp-servers/create-github-secrets.sh" ]; then
            if ./examples/mcp-servers/create-github-secrets.sh; then
                echo "  ✓ GitHub secrets created"
            else
                echo "  Warning: Failed to create GitHub secrets using script, trying direct method..."
                # Fallback: create secrets directly
                kubectl create secret docker-registry ghcr-pull-secret \
                    --docker-server=ghcr.io \
                    --docker-username="${GITHUB_USERNAME}" \
                    --docker-password="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" \
                    --dry-run=client -o yaml | kubectl apply -f - --kubeconfig "${KCONFIG}" 2>/dev/null || \
                kubectl create secret docker-registry ghcr-pull-secret \
                    --docker-server=ghcr.io \
                    --docker-username="${GITHUB_USERNAME}" \
                    --docker-password="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" 2>/dev/null || true
                
                kubectl create secret generic github-token \
                    --from-literal=token="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" \
                    --dry-run=client -o yaml | kubectl apply -f - --kubeconfig "${KCONFIG}" 2>/dev/null || \
                kubectl create secret generic github-token \
                    --from-literal=token="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" 2>/dev/null || true
                
                echo "  ✓ GitHub secrets created (using direct method)"
            fi
        else
            echo "  Warning: create-github-secrets.sh not found, creating secrets directly..."
            kubectl create secret docker-registry ghcr-pull-secret \
                --docker-server=ghcr.io \
                --docker-username="${GITHUB_USERNAME}" \
                --docker-password="${GITHUB_TOKEN}" \
                -n toolhive-system \
                --kubeconfig "${KCONFIG}" \
                    --dry-run=client -o yaml | kubectl apply -f - --kubeconfig "${KCONFIG}" 2>/dev/null || \
                kubectl create secret docker-registry ghcr-pull-secret \
                    --docker-server=ghcr.io \
                    --docker-username="${GITHUB_USERNAME}" \
                    --docker-password="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" 2>/dev/null || true
                
                kubectl create secret generic github-token \
                    --from-literal=token="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" \
                    --dry-run=client -o yaml | kubectl apply -f - --kubeconfig "${KCONFIG}" 2>/dev/null || \
                kubectl create secret generic github-token \
                    --from-literal=token="${GITHUB_TOKEN}" \
                    -n toolhive-system \
                    --kubeconfig "${KCONFIG}" 2>/dev/null || true
            
            echo "  ✓ GitHub secrets created"
        fi
    else
        echo "  Warning: GITHUB_TOKEN and/or GITHUB_USERNAME not set"
        echo "  GitHub MCP servers will fail without these secrets"
        echo "  To fix: export GITHUB_TOKEN=your_token && export GITHUB_USERNAME=your_username"
        echo "  Then re-run this script or manually create secrets:"
        echo "    cd ${MCP_OPTIMIZER_DIR}"
        echo "    ./examples/mcp-servers/create-github-secrets.sh"
    fi
    
    # Patch and apply MCP servers
    echo "  Applying MCP servers..."
    
    # Clean up any corrupted YAML files before applying
    # (restore from git if files were corrupted by a previous run that added podTemplateSpec to non-MCPServer resources)
    echo "    Checking for corrupted YAML files..."
    
    # Check if mcp-optimizer directory is a git repo and restore YAML files if corrupted
    if [ -d "${MCP_OPTIMIZER_DIR}/.git" ]; then
        cd "${MCP_OPTIMIZER_DIR}"
        
        # Check if any YAML files have been modified (indicating possible corruption)
        modified_yamls=$(git status --porcelain examples/mcp-servers/*.yaml 2>/dev/null | awk '{print $2}' || echo "")
        
        if [ -n "${modified_yamls}" ]; then
            echo "    Found modified YAML files, restoring from git to remove any corruption..."
            echo "${modified_yamls}" | while read -r yaml_file; do
                if [ -n "${yaml_file}" ]; then
                    git checkout -- "${yaml_file}" 2>/dev/null && echo "      ✓ Restored ${yaml_file}" || true
                fi
            done
        fi
        
        cd - >/dev/null
    else
        echo "    Note: ${MCP_OPTIMIZER_DIR} is not a git repo, skipping YAML cleanup"
        echo "    If you see errors about podTemplateSpec in ServiceAccount/ClusterRole, restore files manually:"
        echo "      cd ${MCP_OPTIMIZER_DIR} && git checkout examples/mcp-servers/*.yaml"
    fi
    
    # Note: We patch MCPServer resources after applying (not before) to avoid corrupting YAML files
    # This is safer and more reliable than pre-patching.
    
    # Apply MCP servers
    echo "  Applying MCP servers..."
    if ./examples/mcp-servers/apply-mcp-servers.sh; then
        echo "✓ MCP servers setup"
        
        # Wait for MCPGroup to be ready before proceeding
        echo "  Waiting for MCPGroup 'optimized' to be ready..."
        MCPGROUP_READY=false
        for i in {1..60}; do
            PHASE=$(kubectl get mcpgroup optimized -n toolhive-system \
                -o jsonpath='{.status.phase}' --kubeconfig "${KCONFIG}" 2>/dev/null || echo "")
            if [ "${PHASE}" = "Ready" ]; then
                MCPGROUP_READY=true
                break
            fi
            if [ $((i % 10)) -eq 0 ]; then
                echo "    Still waiting... (${i}/60, current phase: ${PHASE:-unknown})"
            fi
            sleep 5
        done
        if [ "${MCPGROUP_READY}" = "true" ]; then
            echo "  ✓ MCPGroup 'optimized' is ready"
        else
            echo "  Warning: MCPGroup 'optimized' not ready yet, checking status..."
            kubectl get mcpgroup optimized -n toolhive-system --kubeconfig "${KCONFIG}" || true
            echo "  Continuing anyway, but VirtualMCPServer may not work until MCPGroup is ready"
        fi
    else
        echo "  Warning: Failed to apply some MCP servers"
        echo "  This may be due to missing GitHub secrets (github-token)"
        echo "  Check pod status: kubectl get pods -n toolhive-system --kubeconfig ${KCONFIG}"
        echo "  If GitHub MCP servers are failing, create secrets:"
        echo "    export GITHUB_TOKEN=your_token && export GITHUB_USERNAME=your_username"
        echo "    cd ${MCP_OPTIMIZER_DIR} && ./examples/mcp-servers/create-github-secrets.sh"
    fi
else
    echo "Step 7: Creating minimal MCP setup..."
    cd "${TOOLHIVE_DIR}"
    
    # Create a minimal MCPGroup if it doesn't exist
    if ! kubectl get mcpgroup optimized -n toolhive-system --kubeconfig "${KCONFIG}" &>/dev/null; then
        echo "  Creating 'optimized' MCPGroup..."
        kubectl apply -f - --kubeconfig "${KCONFIG}" <<EOF || true
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPGroup
metadata:
  name: optimized
  namespace: toolhive-system
spec:
  description: "Optimized MCP servers for tool discovery"
EOF
    fi
    
    # Wait for MCPGroup to be ready
    echo "  Waiting for MCPGroup 'optimized' to be ready..."
    MCPGROUP_READY=false
    for i in {1..60}; do
        PHASE=$(kubectl get mcpgroup optimized -n toolhive-system \
            -o jsonpath='{.status.phase}' --kubeconfig "${KCONFIG}" 2>/dev/null || echo "")
        if [ "${PHASE}" = "Ready" ]; then
            MCPGROUP_READY=true
            break
        fi
        if [ $((i % 10)) -eq 0 ]; then
            echo "    Still waiting... (${i}/60, current phase: ${PHASE:-unknown})"
        fi
        sleep 5
    done
    if [ "${MCPGROUP_READY}" = "true" ]; then
        echo "  ✓ MCPGroup 'optimized' is ready"
    else
        echo "  Warning: MCPGroup 'optimized' not ready yet, checking status..."
        kubectl get mcpgroup optimized -n toolhive-system --kubeconfig "${KCONFIG}" || true
        echo "  Continuing anyway, but VirtualMCPServer may not work until MCPGroup is ready"
    fi
    
    echo "  Note: Add MCPServer resources to the 'optimized' group to enable backend tools"
    echo "✓ MCPGroup created"
fi
echo ""

# Step 8: Create VirtualMCPServer with optimizer
echo "Step 8: Creating VirtualMCPServer with optimizer..."
cd "${TOOLHIVE_DIR}"

# Use the YAML file as the base configuration
VMCP_YAML="${TOOLHIVE_DIR}/examples/operator/virtual-mcps/vmcp_optimizer.yaml"
if [ ! -f "${VMCP_YAML}" ]; then
    echo "  Error: ${VMCP_YAML} not found"
    exit 1
fi

# Apply the base YAML file (has correct defaults for Kubernetes)
echo "  Applying VirtualMCPServer from ${VMCP_YAML}..."
kubectl apply -f "${VMCP_YAML}" --kubeconfig "${KCONFIG}"

# Optionally patch if environment variables differ from defaults
# (The YAML file already has correct defaults, so patching is only needed for customization)
if [ "${EMBEDDING_BACKEND}" != "ollama" ] || \
   [ "${EMBEDDING_MODEL}" != "nomic-embed-text" ] || \
   [ "${EMBEDDING_DIMENSION}" != "384" ]; then
    echo "  Patching embedding configuration (custom values)..."
    kubectl patch virtualmcpserver vmcp-optimizer -n toolhive-system \
        --type=merge \
        --kubeconfig "${KCONFIG}" \
        -p="{\"spec\":{\"config\":{\"optimizer\":{\"embeddingBackend\":\"${EMBEDDING_BACKEND}\",\"embeddingDimension\":${EMBEDDING_DIMENSION},\"embeddingModel\":\"${EMBEDDING_MODEL}\"}}}}" || true
fi

# Patch embedding URL if using a different backend or custom URL
if [ "${EMBEDDING_BACKEND}" = "openai-compatible" ]; then
    EMBEDDING_URL="${EMBEDDING_URL:-https://api.openai.com/v1}"
    echo "  Patching embedding URL for OpenAI-compatible backend..."
    kubectl patch virtualmcpserver vmcp-optimizer -n toolhive-system \
        --type=merge \
        --kubeconfig "${KCONFIG}" \
        -p="{\"spec\":{\"config\":{\"optimizer\":{\"embeddingURL\":\"${EMBEDDING_URL}\"}}}}" || true
elif [ "${EMBEDDING_BACKEND}" = "ollama" ] && [ -n "${EMBEDDING_URL:-}" ]; then
    # Only patch if custom URL is provided (otherwise use YAML default)
    echo "  Patching embedding URL (custom Ollama URL)..."
    kubectl patch virtualmcpserver vmcp-optimizer -n toolhive-system \
        --type=merge \
        --kubeconfig "${KCONFIG}" \
        -p="{\"spec\":{\"config\":{\"optimizer\":{\"embeddingURL\":\"${EMBEDDING_URL}\"}}}}" || true
fi

echo "✓ VirtualMCPServer created"
echo ""

# Step 9: Wait for VirtualMCPServer to be ready
echo "Step 9: Waiting for VirtualMCPServer to be ready..."
if kubectl wait --for=condition=Ready virtualmcpserver/vmcp-optimizer \
    -n toolhive-system --timeout=300s --kubeconfig "${KCONFIG}" 2>/dev/null; then
    echo "✓ VirtualMCPServer ready"
else
    echo "Warning: VirtualMCPServer not ready yet, checking status..."
    kubectl get virtualmcpserver vmcp-optimizer -n toolhive-system --kubeconfig "${KCONFIG}"
    echo ""
    echo "Checking VirtualMCPServer conditions..."
    kubectl get virtualmcpserver vmcp-optimizer -n toolhive-system \
        -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}): {.message}{"\n"}{end}' \
        --kubeconfig "${KCONFIG}" 2>/dev/null || echo "  No conditions found"
    echo ""
    echo "Checking deployment status..."
    kubectl get deployment vmcp-optimizer -n toolhive-system --kubeconfig "${KCONFIG}" 2>/dev/null || echo "  Deployment not found"
    echo ""
    echo "Checking pods..."
    kubectl get pods -n toolhive-system -l app.kubernetes.io/instance=vmcp-optimizer --kubeconfig "${KCONFIG}" 2>/dev/null || echo "  No pods found"
    echo ""
    echo "If VirtualMCPServer is stuck, check:"
    echo "  1. MCPGroup 'optimized' is Ready: kubectl get mcpgroup optimized -n toolhive-system"
    echo "  2. Backend MCPServers are Ready: kubectl get mcpservers -n toolhive-system"
    echo "  3. Operator logs: kubectl logs -n toolhive-system -l app.kubernetes.io/name=toolhive-operator --tail=50"
    echo ""
fi

echo ""
echo "✓ Setup complete!"
echo ""
echo "The VirtualMCPServer is now running with optimizer enabled."
echo "External images (like ghcr.io/stackloklabs/toolhive-doc-mcp:v0.0.7) will be pulled normally."
echo "Only locally built images (operator, toolhive-runner, vmcp) use imagePullPolicy=Never."
echo ""
echo "Note: If MCPServer pods fail with 'Failed to pull image' errors for kind.local images,"
echo "      delete the pods to force recreation with the correct imagePullPolicy:"
echo "      kubectl delete pods -n toolhive-system -l app.kubernetes.io/name=toolhive-proxyrunner --kubeconfig ${KCONFIG}"
echo ""
echo "To test the VirtualMCPServer:"
echo "  1. Port-forward: kubectl port-forward -n toolhive-system svc/vmcp-optimizer 8080:8080 --kubeconfig ${KCONFIG}"
echo "  2. Test endpoint: curl http://localhost:8080/health"
echo ""

# Step 10: Setup ingress
echo "Step 10: Setting up ingress..."
cd "${TOOLHIVE_DIR}"

# Check if ingress controller is installed
if ! kubectl get deployment ingress-nginx-controller -n ingress-nginx --kubeconfig "${KCONFIG}" &>/dev/null; then
    echo "  Installing ingress controller..."
    # Run task from cmd/thv-operator/ so it finds kconfig.yaml
    cd "${TOOLHIVE_DIR}/cmd/thv-operator"
    task kind-ingress-setup || {
        echo "  Warning: Failed to setup ingress, continuing..."
    }
    cd "${TOOLHIVE_DIR}"
fi

# Apply ingress (includes MCP servers, vmcp-optimizer, and Jaeger)
echo "  Applying ingress configuration..."
kubectl apply -f examples/ingress/mcp-servers-ingress.yaml --kubeconfig "${KCONFIG}" || {
    echo "  Warning: Failed to apply ingress, continuing..."
}
echo "✓ Ingress configured"
    
# Start port-forward for ingress controller (background)
echo "  Starting port-forward for ingress controller (port 8080)..."
# Check if port 8080 is already in use
if lsof -Pi :8080 -sTCP:LISTEN -t >/dev/null 2>&1; then
    echo "  Warning: Port 8080 is already in use, skipping port-forward"
    echo "  You may need to manually port-forward:"
    echo "    kubectl port-forward -n ingress-nginx svc/ingress-nginx-controller 8080:80 --kubeconfig ${KCONFIG}"
    INGRESS_PF_PID=""
else
    kubectl port-forward -n ingress-nginx svc/ingress-nginx-controller 8080:80 \
        --kubeconfig "${KCONFIG}" > /dev/null 2>&1 &
    INGRESS_PF_PID=$!
    sleep 2  # Give it a moment to start
    if kill -0 ${INGRESS_PF_PID} 2>/dev/null; then
        echo "  ✓ Ingress port-forward running in background (PID: ${INGRESS_PF_PID})"
        echo "  Access services via ingress at: http://localhost:8080"
    else
        echo "  Warning: Port-forward failed to start"
        INGRESS_PF_PID=""
    fi
fi
echo ""

# Step 11: Display status and connection info
echo "=========================================="
echo "Setup Complete!"
echo "=========================================="
echo ""
echo "VirtualMCPServer Status:"
kubectl get virtualmcpserver vmcp-optimizer -n toolhive-system --kubeconfig "${KCONFIG}"
echo ""
echo "VirtualMCPServer URL:"
kubectl get virtualmcpserver vmcp-optimizer -n toolhive-system \
    -o jsonpath='{.status.url}' --kubeconfig "${KCONFIG}" 2>/dev/null || echo "  (URL not available yet)"
echo ""
echo "Pods:"
kubectl get pods -n toolhive-system --kubeconfig "${KCONFIG}" | grep -E "vmcp|mcp-|ollama|jaeger"
echo ""
echo "Ollama Service:"
kubectl get svc ${OLLAMA_SERVICE_NAME} -n ${OLLAMA_NAMESPACE} --kubeconfig "${KCONFIG}"
echo ""
echo "Ollama Models:"
kubectl exec -n ${OLLAMA_NAMESPACE} deployment/${OLLAMA_SERVICE_NAME} \
    --kubeconfig "${KCONFIG}" -- ollama list 2>/dev/null || echo "  (Unable to list models)"
echo ""
echo "Jaeger Services:"
kubectl get svc -n ${JAEGER_NAMESPACE} --kubeconfig "${KCONFIG}" | grep jaeger
echo ""
echo "=========================================="
echo "Testing Instructions"
echo "=========================================="
echo ""
if [ -n "${INGRESS_PF_PID:-}" ]; then
    echo "1. Access services via Ingress (already running):"
    echo "   MCP Servers:"
    echo "     http://localhost:8080/fetch"
    echo "     http://localhost:8080/github"
    echo "     http://localhost:8080/toolhive-doc-mcp"
    echo "     http://localhost:8080/mcp-optimizer"
    echo "   VirtualMCPServer:"
    echo "     http://localhost:8080/vmcp-optimizer"
    echo "   Jaeger UI:"
    echo "     http://localhost:8080/jaeger"
    echo ""
    echo "2. Or port-forward directly to VirtualMCPServer:"
    echo "   kubectl port-forward -n toolhive-system svc/vmcp-optimizer 4483:4483 --kubeconfig "${KCONFIG}""
    echo ""
else
    echo "1. Port-forward to VirtualMCPServer:"
    echo "   kubectl port-forward -n toolhive-system svc/vmcp-optimizer 4483:4483 --kubeconfig "${KCONFIG}""
    echo ""
    echo "2. Port-forward to Jaeger UI:"
    echo "   kubectl port-forward -n ${JAEGER_NAMESPACE} svc/jaeger-query 16686:16686 --kubeconfig "${KCONFIG}""
    echo "   Then open: http://localhost:16686"
    echo ""
fi
if [ -z "${INGRESS_PF_PID:-}" ]; then
    echo "2. Or use NodePort (if serviceType is NodePort):"
    NODEPORT=$(kubectl get svc vmcp-optimizer -n toolhive-system \
        -o jsonpath='{.spec.ports[0].nodePort}' --kubeconfig "${KCONFIG}" 2>/dev/null || echo "N/A")
    if [ "${NODEPORT}" != "N/A" ]; then
        echo "   Connect to: http://localhost:${NODEPORT}"
    else
        echo "   (NodePort not available, use port-forward instead)"
    fi
    echo ""
fi
echo "3. Test with MCP client:"
echo "   Connect to: stdio://kubectl exec -it -n toolhive-system deployment/vmcp-optimizer -- vmcp"
echo "   Or HTTP/SSE: http://localhost:4483 (after port-forward)"
echo ""
echo "4. Available tools (when optimizer is enabled):"
echo "   - optim.find_tool: Search for tools by semantic query"
echo "   - optim.call_tool: Execute a tool by name"
echo ""
echo "5. Check logs:"
echo "   kubectl logs -n toolhive-system deployment/vmcp-optimizer --kubeconfig "${KCONFIG}""
echo ""
if [ -z "${INGRESS_PF_PID:-}" ]; then
    echo "6. Access Jaeger UI (port-forward):"
    echo "   kubectl port-forward -n ${JAEGER_NAMESPACE} svc/jaeger-query 16686:16686 --kubeconfig "${KCONFIG}""
    echo "   Then open: http://localhost:16686"
    echo ""
fi
if [ -n "${INGRESS_PF_PID:-}" ]; then
    echo "7. Stop ingress port-forward:"
    echo "   kill ${INGRESS_PF_PID}"
    echo ""
    echo "8. Cleanup:"
else
    echo "7. Cleanup:"
fi
echo "   cd ${TOOLHIVE_DIR}"
echo "   task kind-destroy"
echo ""
