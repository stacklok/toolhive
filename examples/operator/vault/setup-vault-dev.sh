#!/bin/bash
set -euo pipefail

# ToolHive Vault Agent Injector Development Setup
#
# Prerequisites: Run 'task kind-with-toolhive-operator-local' first
# This script assumes kconfig.yaml exists in the current directory

KUBECONFIG_FILE="kconfig.yaml"

echo "Installing Vault with Agent Injector..."

# Add Hashicorp helm repository
helm repo add hashicorp https://helm.releases.hashicorp.com || true
helm repo update

# Create vault namespace
kubectl create namespace vault --kubeconfig="$KUBECONFIG_FILE" || true

# Install Vault with development configuration
helm install vault hashicorp/vault \
    --namespace vault \
    --kubeconfig="$KUBECONFIG_FILE" \
    --set "server.dev.enabled=true" \
    --set "server.dev.devRootToken=dev-only-token" \
    --set "injector.enabled=true"

echo "Waiting for Vault pod to be ready..."
kubectl wait --for=condition=ready pod vault-0 \
    --namespace vault \
    --timeout=300s \
    --kubeconfig="$KUBECONFIG_FILE"

echo "Configuring Vault..."

# Get vault pod name
VAULT_POD=$(kubectl get pods --namespace vault \
    -l app.kubernetes.io/name=vault \
    -o jsonpath="{.items[0].metadata.name}" \
    --kubeconfig="$KUBECONFIG_FILE")

# Enable Kubernetes auth
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    vault auth enable kubernetes || true

# Configure Kubernetes auth
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    vault write auth/kubernetes/config \
        kubernetes_host="https://kubernetes.default.svc:443" \
        kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
        token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token

# Enable KV secrets engine
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    vault secrets enable -path=workload-secrets kv-v2 || true

# Create Vault policy
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    sh -c 'vault policy write toolhive-workload-secrets - << EOF
path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self" { capabilities = ["update"] }
path "workload-secrets/data/github-mcp/*" { capabilities = ["read"] }
EOF'

# Create Kubernetes auth role
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    vault write auth/kubernetes/role/toolhive-mcp-workloads \
        bound_service_account_names="*-proxy-runner,mcp-*" \
        bound_service_account_namespaces="toolhive-system" \
        policies="toolhive-workload-secrets" \
        audience="https://kubernetes.default.svc.cluster.local" \
        ttl="1h" \
        max_ttl="4h"

# Create test secrets
kubectl exec --namespace vault "$VAULT_POD" --kubeconfig="$KUBECONFIG_FILE" -- \
    vault kv put workload-secrets/github-mcp/config \
        token="ghp_test_token_12345" \
        organization="test-org"

echo "Vault setup complete!"
echo "Login token: dev-only-token"

# Test Vault Agent Injector
echo "Testing Vault Agent Injector..."

# Create service account if it doesn't exist
kubectl create serviceaccount mcp-test \
    --namespace toolhive-system \
    --kubeconfig="$KUBECONFIG_FILE" || true

# Apply test pod
kubectl apply -f test/vault/simple-test-pod.yaml --kubeconfig="$KUBECONFIG_FILE"

# Wait for pod to be ready
kubectl wait --for=condition=ready pod vault-simple-test-pod \
    --namespace toolhive-system \
    --timeout=300s \
    --kubeconfig="$KUBECONFIG_FILE"

# Test secret injection
echo "Testing secret injection:"
kubectl exec vault-simple-test-pod \
    --namespace toolhive-system \
    --kubeconfig="$KUBECONFIG_FILE" \
    -c test-app -- cat /vault/secrets/github-config

# Cleanup test pod
kubectl delete pod vault-simple-test-pod \
    --namespace toolhive-system \
    --kubeconfig="$KUBECONFIG_FILE"

echo "Vault Agent Injector test successful!"