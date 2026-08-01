# You.com MCP Server Integration

This directory contains ToolHive configuration files for integrating You.com's web search capabilities as an MCP server.

## Overview

The You.com MCP server provides AI agents with access to:
- **Web search** with real-time results and citations
- **Content extraction** from specific URLs
- **Research capabilities** with multi-step reasoning

## Quick Start

### 1. Deploy the MCP Server

```bash
# Deploy You.com MCP server (keyless operation - 100 free searches/day)
kubectl apply -f mcpserver_youcom_web.yaml
```

### 2. Optional: Configure API Key for Enhanced Features

```bash
# Create secret with your You.com API key (optional)
# Get your key at https://api.you.com/
kubectl create secret generic youcom-api-key \
  --from-literal=api-key=your-ydc-api-key-here \
  --namespace=toolhive-system

# Or use the provided template
cp youcom_api_key_secret.yaml youcom_api_key_secret_local.yaml
# Edit youcom_api_key_secret_local.yaml with your API key
kubectl apply -f youcom_api_key_secret_local.yaml
```

### 3. Connect Client

```bash
# Connect Claude Code or other MCP client to ToolHive
thv run youcom-web

# Or use with ToolHive CLI directly
thv mcp call youcom-web you-search '{"query": "TypeScript MCP frameworks", "count": 10}'
```

## Features

### Keyless Operation (Default)
- **No setup required** - works immediately after deployment
- **100 free searches per day** per IP address
- **All core features** available (search, contents, research)

### API Key Authentication (Optional)
- **Higher quotas** based on your You.com plan
- **Enhanced features** and priority processing
- **Rate limit transparency** with clear error messages

### Available Tools
- `you-search`: Real-time web search with citations
- `you-contents`: Extract content from specific URLs
- `you-research`: Multi-step research with synthesized answers

## Configuration Options

### Basic Configuration
```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPServer
metadata:
  name: youcom-web
spec:
  remoteURL: https://api.you.com/mcp
  transport: sse
  # No secrets needed for keyless operation
```

### With API Key (Recommended for Production)
```yaml
spec:
  secrets:
    - name: youcom-api-key
      key: api-key
      targetEnvName: YDC_API_KEY
      optional: true
```

### Resource Tuning
```yaml
spec:
  resources:
    limits:
      cpu: "200m"      # Increase for high-throughput
      memory: "256Mi"   # Increase for complex research tasks
    requests:
      cpu: "100m"
      memory: "128Mi"
```

## Usage Examples

### Web Search
```bash
thv mcp call youcom-web you-search '{
  "query": "latest TypeScript features 2024",
  "count": 10,
  "freshness": "recent"
}'
```

### Content Extraction
```bash
thv mcp call youcom-web you-contents '{
  "url": "https://docs.example.com/api"
}'
```

### Research
```bash
thv mcp call youcom-web you-research '{
  "query": "What are the security implications of MCP in enterprise environments?"
}'
```

## Error Handling

### Missing API Key (when using authenticated features)
```
Error: 401 Unauthorized
Solution: Configure YDC_API_KEY secret or use keyless operation
```

### Rate Limits
```
Error: 429 Too Many Requests  
Solution: 
- Keyless: Wait for quota reset (24 hours)
- With API key: Upgrade your You.com plan
```

### Network Issues
```
Error: Connection failed
Solution: Check network connectivity to api.you.com
```

## Integration with Other MCP Servers

You.com complements other MCP servers in your ToolHive deployment:

```bash
# Combine local knowledge with web search
thv group run knowledge-stack  # Includes youcom-web + local vector DB + code search

# Use in Virtual MCP configurations
thv vmcp create research-agent --servers youcom-web,postgres-mcp,github-mcp
```

## Security Considerations

- **API keys**: Store in Kubernetes Secrets, never in ConfigMaps or code
- **Network policies**: You.com MCP server communicates with api.you.com (HTTPS)
- **Data privacy**: Search queries and results are processed by You.com's API
- **Logging**: Set `LOG_LEVEL=error` in production to minimize log verbosity

## Troubleshooting

### Server Not Starting
```bash
# Check pod status
kubectl get pods -n toolhive-system -l app=youcom-web

# View logs
kubectl logs -n toolhive-system -l app=youcom-web
```

### Connection Issues
```bash
# Test connectivity
thv mcp call youcom-web list-tools

# Check proxy status  
kubectl get mcpserver youcom-web -n toolhive-system -o yaml
```

### Performance Optimization
```bash
# Monitor resource usage
kubectl top pods -n toolhive-system -l app=youcom-web

# Scale horizontally (if needed)
kubectl scale mcpserver youcom-web --replicas=3 -n toolhive-system
```

## Files

- `mcpserver_youcom_web.yaml`: Main MCPServer configuration
- `youcom_api_key_secret.yaml`: Template for API key Secret
- `README.md`: This documentation

## Support

- **You.com API Documentation**: https://docs.you.com/api
- **ToolHive Documentation**: https://docs.stacklok.com/toolhive
- **Issues**: https://github.com/stacklok/toolhive/issues