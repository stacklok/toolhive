---
title: thv mcp check
---

<!-- Generated CLI documentation. Run `task docs` to regenerate. -->

# thv mcp check

Check MCP initialize readiness without enumerating or invoking capabilities.

```text
thv mcp check --server <url-or-workload> [flags]
```

The command exits successfully only when the transport starts and the MCP
`initialize` handshake completes. Use `--format json` for CI and Agent
harnesses.
