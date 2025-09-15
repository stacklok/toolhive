# MCPServer Restart Annotation Feature

This document describes how to use annotations to trigger a restart of an MCPServer instance without modifying its spec configuration.

## Overview

The MCPServer operator supports triggering pod restarts through specific annotations. This provides operational control and better GitOps workflow integration by allowing restarts through metadata changes rather than spec modifications.

## Annotations

### Restart Trigger
- **Key**: `mcpserver.toolhive.stacklok.dev/restarted-at`
- **Value**: RFC3339 timestamp (e.g., `2025-09-14T10:30:00Z`)
- **Purpose**: Triggers a restart when the timestamp value changes

### Restart Strategy (Optional)
- **Key**: `mcpserver.toolhive.stacklok.dev/restart-strategy`
- **Value**: `rolling` (default) or `immediate`
- **Purpose**: Controls the restart method

## Restart Strategies

### Rolling Restart (Default)
- **Strategy**: `rolling` or omitted
- **Behavior**: Updates the deployment pod template annotation to trigger a Kubernetes rolling update
- **Downtime**: Zero downtime - pods are replaced gradually
- **Use case**: Production environments where availability is critical

### Immediate Restart
- **Strategy**: `immediate`
- **Behavior**: Directly deletes all pods belonging to the MCPServer
- **Downtime**: Brief downtime while pods are recreated
- **Use case**: Development environments or when fast restart is needed

## Usage Examples

### Basic Rolling Restart
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-mcpserver
  annotations:
    mcpserver.toolhive.stacklok.dev/restarted-at: "2025-09-14T10:30:00Z"
spec:
  image: my-mcp-image:latest
  # ... other spec fields
```

### Immediate Restart
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-mcpserver
  annotations:
    mcpserver.toolhive.stacklok.dev/restarted-at: "2025-09-14T10:30:00Z"
    mcpserver.toolhive.stacklok.dev/restart-strategy: "immediate"
spec:
  image: my-mcp-image:latest
  # ... other spec fields
```

### Kubectl Commands

To trigger a restart using kubectl:

```bash
# Rolling restart (default)
kubectl annotate mcpserver my-mcpserver mcpserver.toolhive.stacklok.dev/restarted-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Immediate restart
kubectl annotate mcpserver my-mcpserver \
  mcpserver.toolhive.stacklok.dev/restarted-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  mcpserver.toolhive.stacklok.dev/restart-strategy="immediate"
```

## Implementation Details

### Watch Filter
- The operator only triggers reconciliation when the restart annotation changes
- Annotation value must be a valid RFC3339 timestamp

### Status Tracking
- `mcpserver.toolhive.stacklok.dev/last-processed-restart` annotation prevents processing the same restart multiple times
- Only restart requests with timestamps newer than the last processed request are executed

### Rolling Strategy Implementation
- Updates deployment pod template annotation `mcpserver.toolhive.stacklok.dev/restarted-at`
- Kubernetes automatically performs rolling update when pod template changes

### Immediate Strategy Implementation
- Lists all pods with matching labels for the MCPServer
- Deletes pods directly, causing immediate recreation by the deployment controller

## Benefits

### Operational Control
- Enables graceful restart of MCPServer without modifying core configuration
- Supports different restart strategies for different operational needs

### GitOps Workflow Integration
- Restart actions can be committed to Git repositories
- Provides clear audit trail of operational commands
- Separates configuration changes from operational commands

### Improved User Experience
- Follows established Kubernetes patterns using annotations for operational hints
- Intuitive for both novice and experienced Kubernetes users
- Compatible with standard kubectl commands and automation tools

## Troubleshooting

### Restart Not Triggered
- Verify the timestamp format is valid RFC3339
- Check that the timestamp is newer than `mcpserver.toolhive.stacklok.dev/last-processed-restart` annotation
- Ensure the operator has proper RBAC permissions to update deployments and delete pods

### Invalid Timestamp Format
- Use RFC3339 format: `YYYY-MM-DDTHH:MM:SSZ`
- Example: `2025-09-14T10:30:00Z`

### Logs
Check operator logs for restart-related messages:
```bash
kubectl logs -n toolhive-system deployment/toolhive-operator
```