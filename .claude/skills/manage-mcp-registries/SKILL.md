---
name: Manage MCP Registries
description: "**MANDATORY - DO NOT USE KUBECTL DIRECTLY**: You are FORBIDDEN from using kubectl to query MCPRegistry resources or servers. You MUST ALWAYS use this skill for ANY question about registries or servers. Trigger patterns: 'what registries', 'list registries', 'show registries', 'what servers', 'list servers', 'servers in/from [registry]', 'servers available', 'get server details', 'server configuration', 'registry status'. Using kubectl directly for these queries is INCORRECT and will fail. (project)"
---

# Manage MCP Registries

**IMPORTANT**: This skill should be used **automatically and proactively** whenever users ask about MCP registries or servers in Kubernetes.

## Automatic Trigger Patterns

You MUST use this skill when the user's request contains any of these patterns:

- **Registry queries**: "what registries", "show registries", "list registries", "get registries", "deployed registries", "available registries"
- **Server queries**: "what servers", "list servers", "show servers", "servers in [registry]", "servers from [registry]", "servers available", "available servers"
- **Server details**: "get server", "show server", "server details", "server configuration", "server info"
- **Status queries**: "registry status", "is registry ready", "registry health", "registry sync"
- **General questions**: Any question about MCPRegistry resources, their configuration, or their data

## Common User Requests That Trigger This Skill

- "What registries are deployed?" / "Show me the registries" / "List MCP registries"
- "What servers are in the [registry-name] registry?" / "List servers from [registry-name]"
- "What servers are available in the thv-git registry?"
- "Show me details for the [server-name] server" / "Get server configuration for [server-name]"
- "What's the status of [registry-name]?" / "Is the registry ready?"
- "How many servers are in [registry-name]?"
- Any other questions about MCPRegistry resources, their servers, or configurations

## Instructions

**IMPORTANT - Todo List Preparation:**

Before executing any steps, you MUST use the TodoWrite tool to create a task list based on the user's request. This helps track progress and ensures all steps are completed systematically.

Example task lists for common requests:

**For "List all registries":**
- Get all MCPRegistry resources from cluster
- Display registry details with status

**For "List servers in registry X":**
- Extract API endpoint from registry and set up port-forward
- Query registry API for server list (while port-forward is active)
- Clean up port-forward when done

**For "Get details for server Y":**
- Set up port-forward to registry API and query for server details (while port-forward is active)
- Clean up port-forward when done

Follow these steps to work with MCP registries:

### 1. List Available Registries

To see all MCPRegistry resources in the cluster:

```bash
# List registries in all namespaces
kubectl get mcpregistries -A

# List registries in a specific namespace
kubectl get mcpregistries -n <namespace>

# Get detailed output with additional columns
kubectl get mcpregistries -A -o wide
```

The output shows:
- **NAME**: Registry name
- **PHASE**: Current status (Ready, Syncing, Error)
- **SYNC**: Sync status (Complete, InProgress, Failed)
- **API**: API server status (Ready, NotReady)
- **SERVERS**: Number of servers in the registry
- **LAST SYNC**: When the registry was last synchronized
- **AGE**: How long the registry has been running

### 2. Get Registry Details

To view detailed information about a specific registry:

```bash
kubectl get mcpregistry <registry-name> -n <namespace> -o yaml
```

Key fields to examine:
- **spec.source**: Where the registry data comes from (git, configmap, etc.)
- **spec.syncPolicy**: How often the registry syncs
- **status.apiStatus.endpoint**: The API server endpoint URL
- **status.syncStatus**: Last sync time and server count
- **status.storageRef**: Where the registry data is stored

Example examining a specific registry:
```bash
# Get just the API endpoint
kubectl get mcpregistry thv-git -n toolhive-system -o jsonpath='{.status.apiStatus.endpoint}'

# Get server count
kubectl get mcpregistry thv-git -n toolhive-system -o jsonpath='{.status.syncStatus.serverCount}'

# Get last sync time
kubectl get mcpregistry thv-git -n toolhive-system -o jsonpath='{.status.syncStatus.lastSyncTime}'
```

### 3. List Servers in a Registry

**IMPORTANT**: When listing servers from a registry, you MUST use the port-forwarding approach to access the registry API. This is the ONLY recommended method for querying server lists.

**CRITICAL WORKFLOW**: Port-forwarding runs in the background and must stay active while you query the API. Do NOT wait between setting up port-forward and querying - execute them as one continuous workflow:
1. Start port-forward in background
2. Wait 2 seconds for it to be ready
3. Immediately query the API (port-forward stays running)
4. Only kill port-forward after all queries are complete

To query servers available in a registry, you need to access the registry's API server via port-forwarding.

#### Step 1: Set Up Port-Forward to Registry API

**Port-forwarding is REQUIRED** for reliably accessing the registry API and listing servers.

Extract the service information from the registry:
```bash
# Get the API endpoint (e.g., http://thv-git-api.toolhive-system:8080)
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.apiStatus.endpoint}'
```

The endpoint format is: `http://<service-name>.<namespace>:<port>`

Set up port-forward:
```bash
kubectl port-forward -n <namespace> svc/<service-name> <local-port>:<service-port>
```

Example:
```bash
# For endpoint http://thv-git-api.toolhive-system:8080
kubectl port-forward -n toolhive-system svc/thv-git-api 8080:8080
```

You can run this in the background:
```bash
kubectl port-forward -n toolhive-system svc/thv-git-api 8080:8080 &
```

#### Step 2: Query the Registry API

Once port-forwarding is active, you can query the API at `http://localhost:<local-port>`.

**List all server names:**
```bash
curl -s http://localhost:8080/v0/servers | jq '.servers[] | .name' | sort
```

**List all servers with full details:**
```bash
curl -s http://localhost:8080/v0/servers | jq '.servers[]'
```

**Get a specific server's configuration:**
```bash
curl -s http://localhost:8080/v0/servers/<server-name> | jq '.'
```

**Count total servers:**
```bash
curl -s http://localhost:8080/v0/servers | jq '.servers | length'
```

**Search for servers by name pattern:**
```bash
# Find servers containing "aws" in the name
curl -s http://localhost:8080/v0/servers | jq '.servers[] | select(.name | contains("aws")) | .name'
```

**Filter servers by transport type:**
```bash
# Find all servers using stdio transport
curl -s http://localhost:8080/v0/servers | jq '.servers[] | select(.transport == "stdio") | .name'
```

**List servers with their images:**
```bash
curl -s http://localhost:8080/v0/servers | jq '.servers[] | {name: .name, image: .image}'
```

#### Step 3: Clean Up Port-Forward

When done querying the API, stop the port-forward:

```bash
# Find the port-forward process
ps aux | grep "kubectl port-forward"

# Kill it (or use Ctrl+C if running in foreground)
kill <pid>
```

Or if you know the background job number:
```bash
# List background jobs
jobs

# Kill the job
kill %<job-number>
```

### 4. Check Registry Storage

Registries store their server data in ConfigMaps or other storage. To view the raw data:

```bash
# Get the storage reference
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.storageRef}'

# If it's a ConfigMap, view it
kubectl get configmap <configmap-name> -n <namespace> -o yaml
```

Example:
```bash
# For thv-git registry using ConfigMap storage
kubectl get mcpregistry thv-git -n toolhive-system -o jsonpath='{.status.storageRef.configMapRef.name}'

# View the ConfigMap
kubectl get configmap thv-git-registry-storage -n toolhive-system -o yaml
```

### 5. Monitor Registry Sync Status

To watch registry synchronization:

```bash
# Watch registry status updates
kubectl get mcpregistries -n <namespace> --watch

# Check sync conditions
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.conditions}'

# View last sync details
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.syncStatus}'
```

### 6. Trigger Manual Registry Sync

To force a registry to re-sync its data:

```bash
kubectl annotate mcpregistry <registry-name> -n <namespace> \
  toolhive.stacklok.dev/sync-trigger="$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" \
  --overwrite
```

Then monitor the sync status:
```bash
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.syncStatus}'
```

## Common Workflows

### Workflow 1: Explore All Registries and Their Servers

```bash
# 1. List all registries
kubectl get mcpregistries -A

# 2. For each registry, get API endpoint
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.apiStatus.endpoint}'

# 3. Set up port-forward
kubectl port-forward -n <namespace> svc/<service-name> 8080:8080 &

# 4. List servers
curl -s http://localhost:8080/v0/servers | jq '.servers[] | .name'

# 5. Clean up
kill %1
```

### Workflow 2: Find a Specific Server Across Registries

```bash
# 1. List all registries
REGISTRIES=$(kubectl get mcpregistries -A -o json | jq -r '.items[] | "\(.metadata.name):\(.metadata.namespace)"')

# 2. For each registry, search for server
for reg in $REGISTRIES; do
  NAME=$(echo $reg | cut -d: -f1)
  NS=$(echo $reg | cut -d: -f2)

  # Get API endpoint
  ENDPOINT=$(kubectl get mcpregistry $NAME -n $NS -o jsonpath='{.status.apiStatus.endpoint}')
  echo "Checking registry: $NAME in namespace: $NS"

  # Set up port-forward and query
  # (See step 3 for full port-forward setup)
done
```

### Workflow 3: Compare Server Versions Across Registries

```bash
# After setting up port-forwards for multiple registries:
# Registry 1 (port 8080)
curl -s http://localhost:8080/v0/servers/<server-name> | jq '{name: .name, image: .image}'

# Registry 2 (port 8081)
curl -s http://localhost:8081/v0/servers/<server-name> | jq '{name: .name, image: .image}'
```

## Registry API Reference

The registry API provides the following endpoints:

- `GET /v0/servers` - List all servers
  - Returns: `{"servers": [...]}`

- `GET /v0/servers/<name>` - Get specific server details
  - Returns: Server configuration JSON

- `GET /health` - Health check endpoint
  - Returns: Health status

Response format for server data:
```json
{
  "name": "server-name",
  "image": "ghcr.io/owner/repo:tag",
  "transport": "stdio",
  "port": 8080,
  "args": ["arg1", "arg2"],
  "env_vars": [
    {
      "name": "VAR_NAME",
      "description": "Variable description",
      "required": true,
      "secret": false,
      "default": "default-value"
    }
  ],
  "permissions": {
    "network": {
      "enabled": true
    }
  }
}
```

## Troubleshooting

### Registry Not Ready

If a registry shows `Phase: Error` or `API: NotReady`:

```bash
# Check registry conditions
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.conditions}'

# Check registry API pod logs
kubectl logs -n <namespace> -l app.kubernetes.io/name=<registry-name>-api

# Describe the registry for events
kubectl describe mcpregistry <registry-name> -n <namespace>
```

### Port-Forward Fails

If port-forwarding doesn't work:

```bash
# Verify service exists
kubectl get svc -n <namespace> | grep <service-name>

# Check if port is already in use
lsof -i :<local-port>

# Try a different local port
kubectl port-forward -n <namespace> svc/<service-name> 8081:8080
```

### Empty Server List

If API returns no servers or empty list:

```bash
# Check sync status
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.syncStatus}'

# Trigger manual sync
kubectl annotate mcpregistry <registry-name> -n <namespace> \
  toolhive.stacklok.dev/sync-trigger="$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" --overwrite

# Wait and check again
sleep 10
kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.syncStatus.serverCount}'
```

## Best Practices

1. **ALWAYS use port-forwarding** for listing servers and accessing the registry API - this is the ONLY recommended approach
2. **NEVER attempt to access registry storage directly** (e.g., ConfigMaps) for listing servers - always use the API via port-forward
3. **Clean up port-forwards** when done to free resources
4. **Use jq for JSON parsing** to extract specific information from API responses
5. **Monitor sync status** before querying to ensure data is up-to-date
6. **Check multiple registries** when searching for a server - it may exist in different registries with different configurations
7. **Use the API for automation** - the registry API is designed for programmatic access

## Example Usage

**User:** "Show me all registries in the cluster"

**Assistant:**
```bash
kubectl get mcpregistries -A
```

**User:** "What servers are available in the thv-git registry?"

**Assistant:**
```bash
# Set up port-forward in background, wait briefly, then query immediately
kubectl port-forward -n toolhive-system svc/thv-git-api 8080:8080 &
sleep 2 && curl -s http://localhost:8080/v0/servers | jq '.servers[] | .name' | sort

# After query completes, clean up
kill %1
```

**Note:** The workflow is: start port-forward → wait 2 seconds → query API → cleanup. Do NOT treat these as separate tasks that need individual completion tracking.

**User:** "Get details for the 'github' server from the thv-git registry"

**Assistant:**
```bash
# Assuming port-forward is already set up
curl -s http://localhost:8080/v0/servers/github | jq '.'
```
