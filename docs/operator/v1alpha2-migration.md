# v1alpha2 Migration Guide: Removed Deprecated Fields

This document covers the deprecated fields removed in the v1alpha2 API and the exact
migration steps for each.

## Field Mappings

| Removed Field | Replacement | Resource |
|---|---|---|
| `spec.port` | `spec.proxyPort` | MCPServer |
| `spec.targetPort` | `spec.mcpPort` | MCPServer |
| `spec.tools` (ToolsFilter) | `spec.toolConfigRef` + MCPToolConfig | MCPServer |
| `spec.oidcConfig.inline.clientSecret` | `spec.oidcConfig.inline.clientSecretRef` | MCPServer, MCPRemoteProxy, VirtualMCPServer |
| `spec.oidcConfig.inline.thvCABundlePath` | `spec.oidcConfig.inline.caBundleRef` | MCPServer, MCPRemoteProxy, VirtualMCPServer |
| `spec.port` | `spec.proxyPort` | MCPRemoteProxy |

## Migration Steps

### Port fields (`port` → `proxyPort`, `targetPort` → `mcpPort`)

Replace directly in your manifests:

```yaml
# Before
spec:
  port: 9090
  targetPort: 3000

# After
spec:
  proxyPort: 9090
  mcpPort: 3000
```

### Tools filter (`tools` → `toolConfigRef` + MCPToolConfig)

Create a separate MCPToolConfig resource and reference it:

```yaml
# Before
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
spec:
  tools:
    - fetch
    - search

# After
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPToolConfig
metadata:
  name: my-server-tools
spec:
  toolsFilter:
    - fetch
    - search
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
spec:
  toolConfigRef:
    name: my-server-tools
```

### Client secret (`clientSecret` → `clientSecretRef`)

Store the client secret in a Kubernetes Secret and reference it:

```yaml
# Before
spec:
  oidcConfig:
    type: inline
    inline:
      issuer: "https://auth.example.com"
      clientId: "my-client"
      clientSecret: "my-secret-value"

# After
apiVersion: v1
kind: Secret
metadata:
  name: oidc-client-secret
type: Opaque
stringData:
  client-secret: "my-secret-value"
---
spec:
  oidcConfig:
    type: inline
    inline:
      issuer: "https://auth.example.com"
      clientId: "my-client"
      clientSecretRef:
        name: oidc-client-secret
        key: client-secret
```

### CA bundle path (`thvCABundlePath` → `caBundleRef`)

Store the CA certificate in a ConfigMap and reference it:

```yaml
# Before
spec:
  oidcConfig:
    type: inline
    inline:
      thvCABundlePath: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

# After
apiVersion: v1
kind: ConfigMap
metadata:
  name: oidc-ca-bundle
data:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    ...
    -----END CERTIFICATE-----
---
spec:
  oidcConfig:
    type: inline
    inline:
      caBundleRef:
        configMapRef:
          name: oidc-ca-bundle
          key: ca.crt
```

ToolHive automatically mounts the ConfigMap and computes the CA bundle path.
