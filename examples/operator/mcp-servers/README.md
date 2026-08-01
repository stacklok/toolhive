# MCP Server Examples

This directory contains example configurations for deploying MCP servers with ToolHive.

## Available Examples

### Core MCP Servers

- **GitHub** (`mcpserver_github.yaml`): GitHub repository integration with authentication
- **MKP** (`mcpserver_mkp.yaml`): Kubernetes Management Protocol server  
- **You.com** (`mcpserver_youcom_web.yaml`): Web search and research capabilities

### Configuration Variants

- **OIDC Authentication** (`mcpserver_with_oidcconfig_ref.yaml`): Shared OIDC provider configuration
- **OpenTelemetry** (`mcpserver_fetch_otel.yaml`): With observability and tracing
- **Tool Filtering** (`mcpserver_fetch_tools_filter.yaml`): Custom tool exposure and metadata
- **Pod Customization** (`mcpserver_with_pod_template.yaml`): Resource limits and node affinity

## Quick Start

```bash
# Deploy a basic MCP server
kubectl apply -f mcpserver_github.yaml

# Deploy with secrets
kubectl create secret generic github-token --from-literal=token=your-token
kubectl apply -f mcpserver_github.yaml

# Connect via ToolHive CLI
thv run github
```

## Usage Patterns

### Web Search with You.com
```bash
# Deploy You.com MCP server (keyless operation)
kubectl apply -f mcpserver_youcom_web.yaml

# Search the web
thv mcp call youcom-web you-search '{"query": "ToolHive MCP deployment", "count": 5}'
```

### GitHub Integration
```bash
# Set up GitHub authentication
kubectl create secret generic github-token \
  --from-literal=token=ghp_your_token_here

# Deploy GitHub MCP server
kubectl apply -f mcpserver_github.yaml

# List repositories
thv mcp call github github-list-repos '{"owner": "stacklok"}'
```

### Kubernetes Management
```bash
# Deploy MKP server with service account
kubectl apply -f mcpserver_mkp.yaml

# Query cluster resources
thv run mkp
```

## Authentication Options

### API Keys and Tokens
Store sensitive credentials in Kubernetes Secrets:

```yaml
secrets:
  - name: api-token
    key: token  
    targetEnvName: API_TOKEN
    optional: false
```

### OIDC Integration
Use shared OIDC configuration for multiple servers:

```yaml
spec:
  oidcConfigRef:
    name: shared-oidc
    namespace: toolhive-system
```

## Resource Management

### Basic Resource Limits
```yaml
resources:
  limits:
    cpu: "200m"
    memory: "256Mi"
  requests:
    cpu: "100m" 
    memory: "128Mi"
```

### Production Sizing
```yaml
resources:
  limits:
    cpu: "500m"
    memory: "1Gi"
  requests:
    cpu: "200m"
    memory: "512Mi"
```

## Observability

### OpenTelemetry Integration
```yaml
spec:
  telemetryConfigRef:
    name: otel-config
    namespace: toolhive-system
```

### Logging Configuration
```yaml
env:
  - name: LOG_LEVEL
    value: info  # debug, info, warn, error
```

## Security Best Practices

1. **Store secrets securely**: Use Kubernetes Secrets, never embed in YAML
2. **Principle of least privilege**: Grant minimal required permissions  
3. **Network policies**: Restrict egress where possible
4. **Regular updates**: Keep MCP server images updated

## Troubleshooting

### Check Server Status
```bash
kubectl get mcpserver -n toolhive-system
kubectl describe mcpserver your-server -n toolhive-system
```

### View Logs
```bash
kubectl logs -n toolhive-system -l app=your-server
```

### Test Connectivity
```bash
thv mcp call your-server list-tools
```

## See Also

- [ToolHive Operator Documentation](../../../docs/operator/)
- [MCP Server API Reference](../../../docs/operator/mcpserver-api.md)
- [Virtual MCP Examples](../virtual-mcps/)
- [Registry Configuration](../mcp-registries/)