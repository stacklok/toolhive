# MCPServer workloads

An operator-managed `MCPServer` is two Kubernetes workloads, not one.

| Object | Kind | What it runs |
| --- | --- | --- |
| `<name>` | Deployment | Proxy-runner (`thv` / HTTP listener). This is what the Service targets. |
| `<name>` | StatefulSet | The MCP process itself (image, `npx`, `uvx`, …). |

`kubectl get pods` therefore shows two pods per stdio server. The StatefulSet
pod is not leftover. Do not delete it.

## Recreate if the StatefulSet is deleted

The operator owns that StatefulSet. If it is missing, the next reconcile
bounces the proxy Deployment so the runner re-applies it, and the
`MCPServer` stays `Pending` until the StatefulSet is back.

Do **not** delete the `MCPServer` custom resource just to recover a missing
StatefulSet.

## Supported bounce

See [restart-annotation.md](./restart-annotation.md). Prefer:

```bash
kubectl annotate mcpserver <name> \
  mcpserver.toolhive.stacklok.dev/restarted-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

Do not delete the StatefulSet to bounce the server; the operator will
treat that as a missing workload and recreate it.
