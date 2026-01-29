---
name: deploying-vmcp-locally
description: Deploys a VirtualMCPServer configuration locally for manual testing and verification
---

# Deploying vMCP Locally

This skill helps you deploy and test VirtualMCPServer configurations in a local Kind cluster for manual verification.

## Prerequisites

Before using this skill, ensure you have:
- [Kind](https://kind.sigs.k8s.io/) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Task](https://taskfile.dev/installation/) installed
- [Helm](https://helm.sh/) installed
- A cloned copy of the toolhive repository

## Instructions

### 1. Set up the local cluster

If no Kind cluster exists, create one with the ToolHive operator:

```bash
# From the toolhive repository root
task kind-with-toolhive-operator
```

This creates a Kind cluster named `toolhive` with:
- Nginx ingress controller
- ToolHive CRDs installed
- ToolHive operator deployed

### 2. For development/testing with local changes

If you need to test local code changes:

```bash
# Set up cluster with e2e port mappings
task kind-setup-e2e

# Install CRDs
task operator-install-crds

# Build and deploy local operator image
task operator-deploy-local
```

### 3. Apply the VirtualMCPServer configuration

Apply the YAML configuration you want to test:

```bash
kubectl apply -f <path-to-vmcp-yaml> --kubeconfig kconfig.yaml
```

### 4. Verify deployment

Check the VirtualMCPServer status:

```bash
# List all VirtualMCPServers
kubectl get virtualmcpserver --kubeconfig kconfig.yaml

# Get detailed status
kubectl get virtualmcpserver <name> -o yaml --kubeconfig kconfig.yaml

# Check operator logs for issues
kubectl logs -n toolhive-system -l app.kubernetes.io/name=thv-operator --kubeconfig kconfig.yaml
```

### 5. Test the vMCP endpoint

For NodePort service type (useful for local testing):

```bash
# Get the NodePort
kubectl get svc vmcp-<name> -o jsonpath='{.spec.ports[0].nodePort}' --kubeconfig kconfig.yaml

# Test the endpoint (port will be on localhost when using kind-setup-e2e)
curl http://localhost:<nodeport>/mcp
```

For ClusterIP (default), use port-forward:

```bash
kubectl port-forward svc/vmcp-<name> 4483:4483 --kubeconfig kconfig.yaml
curl http://localhost:4483/mcp
```

### 6. Test MCP protocol

Use an MCP client to verify tool discovery and execution:

```bash
# Initialize MCP session
curl -X POST http://localhost:<port>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "test", "version": "1.0"}}, "id": 1}'

# List tools
curl -X POST http://localhost:<port>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "method": "tools/list", "id": 2}'
```

### 7. Clean up

When done testing:

```bash
# Remove specific resources
kubectl delete -f <path-to-vmcp-yaml> --kubeconfig kconfig.yaml

# Or destroy the entire cluster
task kind-destroy
```

## Example YAML files

Reference example configurations are in `examples/operator/virtual-mcps/`:

| File | Description |
|------|-------------|
| `vmcp_simple_discovered.yaml` | Basic discovered mode configuration |
| `vmcp_conflict_resolution.yaml` | Tool conflict handling strategies |
| `vmcp_inline_incoming_auth.yaml` | Inline authentication configuration |
| `vmcp_production_full.yaml` | Full production configuration |
| `composite_tool_simple.yaml` | Simple composite tool workflow |
| `composite_tool_complex.yaml` | Complex multi-step workflows |
| `composite_tool_with_elicitations.yaml` | Workflows with user prompts |

## Troubleshooting

### VirtualMCPServer stuck in Pending phase

Check that:
1. The MCPGroup exists and is Ready
2. All backend MCPServers in the group are Running
3. The operator has permissions to create the vMCP deployment

```bash
kubectl describe virtualmcpserver <name> --kubeconfig kconfig.yaml
kubectl get mcpgroup --kubeconfig kconfig.yaml
kubectl get mcpserver --kubeconfig kconfig.yaml
```

### Backend servers not discovered

Verify backend servers have the correct `groupRef`:

```bash
kubectl get mcpserver -o custom-columns=NAME:.metadata.name,GROUP:.spec.groupRef --kubeconfig kconfig.yaml
```

### Authentication issues

For testing, use anonymous auth:

```yaml
incomingAuth:
  type: anonymous
  authzConfig:
    type: inline
    inline:
      policies:
        - 'permit(principal, action, resource);'
```
