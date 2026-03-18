# Remove Deprecated Fields

**As a** CRD API consumer,
**I want** deprecated fields removed in the v1alpha2 API,
**so that** the API surface is clean and there is no ambiguity about which field to use.

**Size**: M
**Dependencies**: None
**Labels**: `operator`, `api`, `breaking-change`

## Context

MCPServer and MCPRemoteProxy include deprecated fields with fallback helper methods. These were retained during v1alpha1 for backward compatibility, but v1alpha2 is a breaking change and the right time to remove them.

| Deprecated Field | Replacement | Location |
|---|---|---|
| `MCPServer.spec.port` | `proxyPort` | `mcpserver_types.go:96` |
| `MCPServer.spec.targetPort` | `mcpPort` | `mcpserver_types.go:103` |
| `MCPServer.spec.toolsFilter` | `toolConfigRef` | `mcpserver_types.go:175` |
| `InlineOIDCConfig.clientSecret` | `clientSecretRef` | `mcpserver_types.go:541` |
| `InlineOIDCConfig.thvCABundlePath` | `caBundleRef` | `mcpserver_types.go:553` |
| `MCPRemoteProxy.spec.port` | `proxyPort` | `mcpremoteproxy_types.go:48` |

## Acceptance Criteria

- [ ] `MCPServer.spec.port` removed (use `proxyPort`)
- [ ] `MCPServer.spec.targetPort` removed (use `mcpPort`)
- [ ] `MCPServer.spec.toolsFilter` removed (use `toolConfigRef`)
- [ ] `InlineOIDCConfig.clientSecret` removed (use `clientSecretRef`)
- [ ] `InlineOIDCConfig.thvCABundlePath` removed (use `caBundleRef`)
- [ ] `MCPRemoteProxy.spec.port` removed (use `proxyPort`)
- [ ] All fallback helper methods (`GetProxyPort()`, `GetMcpPort()`, etc.) simplified to use only the canonical field
- [ ] All controller code referencing deprecated fields is updated
- [ ] Unit tests updated to use only canonical fields
- [ ] Migration notes document the exact field mappings

## Sub-Issues

| ID | Title |
|---|---|
| [04-A](04-A.md) | Remove deprecated fields from MCPServer types |
| [04-B](04-B.md) | Remove deprecated `Port` from MCPRemoteProxy types |
| [04-C](04-C.md) | Update controllers and tests for deprecated field removal |
