# Usage Examples for Kubernetes Registry

This document provides comprehensive examples of using the Kubernetes Registry system.

## Registry Configuration Examples

### Creating a Registry from URL Source

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: upstream-community
  namespace: toolhive-system
spec:
  displayName: "MCP Community Registry"
  description: "Official community registry for MCP servers"
  format: upstream
  source:
    type: url
    url:
      url: "https://registry.modelcontextprotocol.io/servers.json"
  syncPolicy:
    enabled: true
    interval: "1h"
    onUpdate: update
  filter:
    include:
      tiers: ["Official", "Community"]
    exclude:
      names: ["*-experimental-*"]
```

### Creating a Registry from Git Source

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: company-internal
  namespace: production
spec:
  displayName: "Company Internal MCP Registry"
  description: "Internal registry for company-specific MCP servers"
  format: toolhive
  source:
    type: git
    git:
      repository: "https://github.com/company/mcp-registry.git"
      branch: "main"
      path: "registry/toolhive-format.json"
      authSecret:
        name: git-credentials
        usernameKey: username
        passwordKey: token
  syncPolicy:
    interval: "30m"
    onUpdate: update
```

### Creating a Registry from Another Registry API

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: curated-upstream
  namespace: production
spec:
  displayName: "Curated Upstream Registry"
  description: "Filtered view of upstream community registry"
  format: upstream
  source:
    type: registry
    registry:
      url: "http://registry-api.upstream-cluster.svc.cluster.local:8080/api/v1/servers"
      authSecret:
        name: upstream-api-credentials
        usernameKey: username
        passwordKey: token
  filter:
    include:
      tiers: ["Official"]
      categories: ["filesystem", "database"]
  syncPolicy:
    interval: "1h"
    onUpdate: update
```

### Creating an MCPServer from Registry Data

```bash
# Direct creation using registry information
kubectl create -f - <<EOF
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: my-filesystem
  namespace: production
  labels:
    app: my-filesystem
    category: filesystem
    toolhive.stacklok.io/registry-name: upstream-community
    toolhive.stacklok.io/registry-namespace: toolhive-system
    toolhive.stacklok.io/server-name: filesystem-server
  annotations:
    registry.toolhive.io/source: upstream-community
spec:
  image: "mcpproject/filesystem-server:latest"
  transport: stdio
  env:
    - name: FILESYSTEM_ROOT
      value: "/data/shared"
EOF

# Using CLI with registry data (current capability)
thv run filesystem-server --name my-filesystem \
  --env FILESYSTEM_ROOT=/data/shared \
  --namespace production
```

## CLI Workflow Examples

### Discovering and Installing a Server
```bash
# List available servers from all registries
thv search --category database

# Get details about a specific server
thv show postgres-mcp-server --registry upstream-community

# Install the server with registry metadata
thv run postgres-mcp-server my-postgres \
  --registry upstream-community \
  --env DB_HOST=prod-db.company.com \
  --env DB_NAME=production \
  --env READONLY=true \
  --namespace production

# Check registry source information
thv show my-postgres --include-registry-info
```

### Working with External Registries
```bash
# Add the official MCP community registry
thv registry add community \
  --url https://registry.modelcontextprotocol.io/servers.json \
  --format upstream \
  --interval 1h

# Add a private Git-based registry
thv registry add company-internal \
  --git-repo https://github.com/company/mcp-registry.git \
  --git-path registry.json \
  --auth-secret git-credentials

# Manually sync all registries
thv registry sync --all

# List servers from specific registry
thv list --registry company-internal

# Export registry data for backup
thv registry export community --format toolhive --output community-backup.json
```

## Label-Based Server Management

### Filtering Servers by Registry
```bash
# List all servers from a specific registry
kubectl get mcpserver -l toolhive.stacklok.io/registry-name=upstream-community

# List servers by category
kubectl get mcpserver -l toolhive.stacklok.io/category=filesystem

# List servers by tier
kubectl get mcpserver -l toolhive.stacklok.io/tier=Official

# Combine multiple label selectors
kubectl get mcpserver -l toolhive.stacklok.io/registry-name=upstream-community,toolhive.stacklok.io/category=database
```

### Pre-deployed Server Association
```bash
# Associate existing server with registry
kubectl label mcpserver my-existing-server \
  toolhive.stacklok.io/registry-name=company-internal \
  toolhive.stacklok.io/registry-namespace=production \
  toolhive.stacklok.io/server-name=custom-server
```

## REST API Usage Examples

### Application Integration
```bash
# List all registries (via Kubernetes API)
kubectl get mcpregistry --all-namespaces -o json

# Get servers from specific registry's API endpoint
curl -H "Authorization: Bearer $TOKEN" \
  "http://upstream-community-api.toolhive-system.svc.cluster.local:8080/api/v1/servers"

# Get server details from specific registry in upstream format
curl -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/json; format=upstream" \
  "http://upstream-community-api.toolhive-system.svc.cluster.local:8080/api/v1/servers/filesystem-server"

# Query registry status via Kubernetes API
kubectl get mcpregistry upstream-community -o jsonpath='{.status.servers}' | jq '.'

# Get all servers with registry labels via Kubernetes API
kubectl get mcpserver --all-namespaces -l toolhive.stacklok.io/registry-name -o json
```

## Migration Examples

### Converting File Registry to Kubernetes
```bash
# Convert existing registry files to Kubernetes resources
thv registry migrate registry.json --output k8s-registry.yaml

# Apply the converted registry
kubectl apply -f k8s-registry.yaml

# Verify synchronization
kubectl get mcpregistry -o wide
```

### Backup and Restore
```bash
# Export all registries for backup
for registry in $(kubectl get mcpregistry -o name); do
  name=$(echo $registry | cut -d'/' -f2)
  thv registry export $name --format toolhive --output backup-$name.json
done

# Restore from backup
thv registry import --file backup-upstream-community.json --registry restored-upstream
```