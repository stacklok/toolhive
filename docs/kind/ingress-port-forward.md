# Port-Forward to Access MCP Servers

This document walks through using kubectl port-forward to access MCP servers running in a local Kind cluster. Port-forwarding provides a simple way to access services without setting up ingress controllers, making it ideal for testing and development workflows.

## Prerequisites

- Kind cluster with the [ToolHive Operator installed](./deploying-toolhive-operator.md)
- At least one [MCP server deployed](./deploying-mcp-server-with-operator.md) in the cluster
- kubectl configured to communicate with your cluster

## Port-Forward to MCP Server

### List Available MCP Servers

First, check what MCP servers are running in your cluster:

```bash
kubectl get mcpservers -n toolhive-system
```

You should see output similar to:
```
NAME    STATUS    AGE
fetch   Running   2m30s
```

### List MCP Server Services

To port-forward to an MCP server, you need to identify the service that exposes it:

```bash
kubectl get services -n toolhive-system
```

You should see services with names like `mcp-{server-name}-proxy`:
```
NAME              TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)    AGE
mcp-fetch-proxy   ClusterIP   10.96.45.123   <none>        8080/TCP   2m45s
```

### Port-Forward to the MCP Server

To access the MCP server from your local machine, use kubectl port-forward:

```bash
kubectl port-forward -n toolhive-system service/mcp-fetch-proxy 8080:8080
```

This command:
- Forwards local port 8080 to the service's port 8080
- Keeps running in the foreground (use Ctrl+C to stop)
- Allows you to access the MCP server at `http://localhost:8080`

### Access the MCP Server

With the port-forward active, you can now access the MCP server:

```bash
# Test connectivity
curl http://localhost:8080/sse

# Or use your MCP client to connect to localhost:8080
```

In your MCP config for your client you simply add the URL.

The following is a Cursor MCP server entry:

```json
{
	"mcpServers": {
		"fetch":  {"url": "http://localhost:8080/sse"},
	}
}
```