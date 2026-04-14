# Plan: Remove Deprecated CRD Fields Before v1beta1

## Context

The operator has accumulated deprecated fields across MCPServer, MCPRemoteProxy, and VirtualMCPServer CRDs. Each deprecated field has a replacement, backward-compatibility shim code, CEL mutual-exclusivity rules, and tests. These must be removed before promoting the API to v1beta1.

## Inventory of Deprecated Items

### D1: Inline `telemetry` field (MCPServer, MCPRemoteProxy)

| Aspect | Details |
|--------|---------|
| **Fields** | `MCPServerSpec.Telemetry *TelemetryConfig`, `MCPRemoteProxySpec.Telemetry *TelemetryConfig` |
| **Replacement** | `TelemetryConfigRef *MCPTelemetryConfigReference` (references shared `MCPTelemetryConfig` CR) |
| **CEL rules** | Mutual-exclusivity `telemetry`/`telemetryConfigRef` on both specs |

**Controller fallbacks:**
- `mcpserver_runconfig.go:199` — `else { AddTelemetryConfigOptions(...) }`
- `mcpremoteproxy_runconfig.go:358` — `else { AddTelemetryConfigOptions(...) }`
- `mcpserver_controller.go:1037-1040` — deprecated env var generation
- `mcpserver_controller.go:1648-1650` — deployment drift check
- `mcpremoteproxy_deployment.go:177-178` — deprecated env var generation

**Deprecated utility functions:**
- `pkg/runconfig/AddTelemetryConfigOptions()` + tests
- `pkg/spectoconfig/ConvertTelemetryConfig()` (only caller is above)
- `pkg/controllerutil/GenerateOpenTelemetryEnvVars()` (only callers are deprecated paths)

**Type to remove:** `TelemetryConfig` struct and its sub-types (`OpenTelemetryConfig`, etc.) — only used by deprecated fields

---

### D2: Inline `oidcConfig` field (MCPServer, MCPRemoteProxy)

| Aspect | Details |
|--------|---------|
| **Fields** | `MCPServerSpec.OIDCConfig *OIDCConfigRef`, `MCPRemoteProxySpec.OIDCConfig *OIDCConfigRef` |
| **Replacement** | `OIDCConfigRef *MCPOIDCConfigReference` (references shared `MCPOIDCConfig` CR) |
| **CEL rules** | Mutual-exclusivity `oidcConfig`/`oidcConfigRef` on both specs; rate-limiting CEL rules reference `has(self.oidcConfig)` |
| **Interface** | `OIDCConfigurable` interface (`GetOIDCConfig() *OIDCConfigRef`) + `oidc.Resolver.Resolve()` method |

**Controller legacy paths:**
- `mcpserver_runconfig.go:241-252` — legacy OIDCConfig resolution
- `mcpremoteproxy_runconfig.go:128-129` — `resolveAndAddOIDCConfig` legacy branch
- `mcpremoteproxy_deployment.go:251-255` — client secret resolution

**Deprecated utilities:**
- `controllerutil.AddOIDCConfigOptions()` — uses `OIDCConfigurable` interface

**Methods to remove:** `MCPServer.GetOIDCConfig()`, `MCPRemoteProxy.GetOIDCConfig()`

---

### D3: Inline `incomingAuth.oidcConfig` field (VirtualMCPServer)

| Aspect | Details |
|--------|---------|
| **Field** | `IncomingAuthConfig.OIDCConfig *OIDCConfigRef` |
| **Replacement** | `IncomingAuthConfig.OIDCConfigRef *MCPOIDCConfigReference` |
| **CEL rules** | Mutual-exclusivity on `IncomingAuthConfig`; oidc-required validation references `has(self.oidcConfig)` |

**Controller legacy paths:**
- `vmcpconfig/converter.go:232-242` — legacy `resolveOIDCConfig`
- `virtualmcpserver_deployment.go:300-301,382-385,849-853` — CA bundle volumes and client secret handling

**Method to update:** `VirtualMCPServer.GetOIDCConfig()` (returns inline config; may be removable)

---

### D4: VirtualMCPServer `config.telemetry` inline fallback

| Aspect | Details |
|--------|---------|
| **Field** | `config.Config.Telemetry` in VirtualMCPServer's embedded `config` field |
| **Replacement** | `VirtualMCPServerSpec.TelemetryConfigRef *MCPTelemetryConfigReference` |
| **CEL rule** | `!(has(self.config.telemetry) && has(self.telemetryConfigRef))` |
| **Controller fallback** | `vmcpconfig/converter.go:343-351` — deprecated `normalizeTelemetry` path |

> **Note**: The `Config.Telemetry` field in `pkg/vmcp/config/config.go` is STILL VALID for standalone CLI deployments — do NOT remove the field from the shared struct. Only remove the operator-side fallback handling.

---

### D5: VirtualMCPServer `config.groupRef` fallback

| Aspect | Details |
|--------|---------|
| **Field** | `config.Config.Group` in VirtualMCPServer's embedded `config` field |
| **Replacement** | `VirtualMCPServerSpec.GroupRef *MCPGroupRef` |
| **Fallback code** | `VirtualMCPServer.ResolveGroupName()` falls back to `Spec.Config.Group`; `Validate()` accepts either |

> **Note**: Same as D4 — `Config.Group` is still valid for standalone CLI. Only remove the operator-side fallback. The converter must explicitly set `config.Group = vmcp.ResolveGroupName()` (which should now just return `spec.groupRef.name`).

---

### D6: Deprecated `external_auth_config_ref` enum value

| Aspect | Details |
|--------|---------|
| **Constant** | `DeprecatedBackendAuthTypeExternalAuthConfigRef = "external_auth_config_ref"` |
| **Replacement** | `BackendAuthTypeExternalAuthConfigRef = "externalAuthConfigRef"` |
| **Enum marker** | `+kubebuilder:validation:Enum=discovered;externalAuthConfigRef;external_auth_config_ref` |
| **Compat code** | `vmcpconfig/converter.go:501-506` — deprecation log warning |
| **Validation code** | `virtualmcpserver_types.go:548` — accepts deprecated value in switch |

---

### D7: Orphaned types (removable after D1-D3)

| Type | File | Used By |
|------|------|---------|
| `OIDCConfigRef` struct + sub-types | `mcpserver_types.go` | Only deprecated `oidcConfig` fields |
| `OIDCConfigurable` interface | `oidc/resolver.go` | Only deprecated inline path |
| `Resolve()` method on resolver | `oidc/resolver.go` | Only deprecated inline path |
| `MockOIDCConfigurable` | `oidc/mocks/mock_resolver.go` | Only deprecated test path |
| `TelemetryConfig` struct + sub-types | `mcpserver_types.go` | Only deprecated `telemetry` fields |

---

## Proposed PR Split

Given the 400-line limit per PR, this should be split into 4-5 PRs, each independently mergeable:

### PR 1: Remove deprecated inline `telemetry` field (D1 + D4)

**Files changed** (~15 files):
- `api/v1alpha1/mcpserver_types.go` — remove `Telemetry` field, CEL rule, update `TelemetryConfigRef` comment
- `api/v1alpha1/mcpremoteproxy_types.go` — remove `Telemetry` field, CEL rule, update comment
- `api/v1alpha1/virtualmcpserver_types.go` — remove CEL rule for `config.telemetry`/`telemetryConfigRef`
- `controllers/mcpserver_runconfig.go` — remove `else` branch calling `AddTelemetryConfigOptions`
- `controllers/mcpserver_controller.go` — remove deprecated env var paths (2 locations)
- `controllers/mcpremoteproxy_runconfig.go` — simplify `addTelemetryOptions`
- `controllers/mcpremoteproxy_deployment.go` — remove deprecated telemetry env var path
- `pkg/vmcpconfig/converter.go` — remove deprecated `normalizeTelemetry` inline path
- `pkg/runconfig/telemetry.go` — remove `AddTelemetryConfigOptions` function
- `pkg/runconfig/telemetry_test.go` — remove tests for deprecated function
- `pkg/spectoconfig/telemetry.go` — remove `ConvertTelemetryConfig` (keep `NormalizeMCPTelemetryConfig`)
- `pkg/controllerutil/tokenexchange.go` — remove `GenerateOpenTelemetryEnvVars`
- Remove `TelemetryConfig`, `OpenTelemetryConfig` types from `mcpserver_types.go`
- Run `task operator-generate && task operator-manifests`

### PR 2: Remove deprecated inline `oidcConfig` from MCPServer + MCPRemoteProxy (D2)

**Files changed** (~12 files):
- `api/v1alpha1/mcpserver_types.go` — remove `OIDCConfig` field, CEL rules, update rate-limiting CELs
- `api/v1alpha1/mcpremoteproxy_types.go` — remove `OIDCConfig` field, CEL rule
- Remove `MCPServer.GetOIDCConfig()`, `MCPRemoteProxy.GetOIDCConfig()` methods
- `controllers/mcpserver_runconfig.go` — remove legacy OIDCConfig branch
- `controllers/mcpremoteproxy_runconfig.go` — remove legacy branch in `resolveAndAddOIDCConfig`
- `controllers/mcpremoteproxy_deployment.go` — remove inline client-secret resolution
- `pkg/controllerutil/oidc.go` — remove `AddOIDCConfigOptions`
- `pkg/oidc/resolver.go` — remove `OIDCConfigurable` interface and `Resolve()` method
- `pkg/oidc/mocks/mock_resolver.go` — remove `MockOIDCConfigurable`
- Update tests referencing deprecated fields
- Run `task operator-generate && task operator-manifests`

### PR 3: Remove deprecated `incomingAuth.oidcConfig` from VirtualMCPServer (D3)

**Files changed** (~8 files):
- `api/v1alpha1/virtualmcpserver_types.go` — remove `IncomingAuthConfig.OIDCConfig` field, update CEL rules
- Remove or update `VirtualMCPServer.GetOIDCConfig()` method
- `pkg/vmcpconfig/converter.go` — remove legacy `resolveOIDCConfig` path, remove `mapResolvedOIDCToVmcpConfig`
- `controllers/virtualmcpserver_deployment.go` — remove inline OIDCConfig CA bundle and client secret handling
- Update tests
- Run `task operator-generate && task operator-manifests`

### PR 4: Remove `config.groupRef` fallback + `external_auth_config_ref` enum (D5 + D6)

**Files changed** (~8 files):
- `api/v1alpha1/virtualmcpserver_types.go`:
  - Update `ResolveGroupName()` to only return `spec.groupRef.name`
  - Update `Validate()` to require `spec.groupRef.name`
  - Remove `external_auth_config_ref` from `BackendAuthConfig.Type` enum
  - Remove `DeprecatedBackendAuthTypeExternalAuthConfigRef` constant
- `pkg/vmcpconfig/converter.go`:
  - After DeepCopy, set `config.Group = vmcp.ResolveGroupName()` (so downstream code works)
  - Remove deprecated snake_case handling in `convertBackendAuth`
- `pkg/vmcpconfig/validator.go` — `config.Group` check remains (set by converter now)
- Update tests
- Run `task operator-generate && task operator-manifests`

### PR 5: Remove orphaned `OIDCConfigRef` type + cleanup (D7)

After PRs 2 and 3 merge:
- Remove `OIDCConfigRef` struct and all its inline-specific sub-types from `mcpserver_types.go`
- Remove `OIDCConfigurable` interface from `oidc/resolver.go` (if not already done in PR 2)
- Remove `controllerutil/oidc_volumes.go` helpers only used by inline OIDCConfig
- Remove test files for deprecated types
- Update docs (`docs/operator/crd-api.md`, `docs/operator/virtualmcpserver-api.md`)
- Run `task operator-generate && task operator-manifests && task crdref-gen`

---

## Verification

After each PR:
1. `task operator-generate` — regenerate deepcopy
2. `task operator-manifests` — regenerate CRD YAML
3. `task operator-test` — unit tests pass
4. `task lint-fix` — no lint errors
5. After all PRs: `task crdref-gen` from `cmd/thv-operator/` — regenerate API docs
6. After all PRs: `task operator-e2e-test` — end-to-end tests pass (requires kind cluster)

---

## Risks & Considerations

- **Breaking change for existing CRs**: Any deployed CRs using deprecated fields will fail validation after CRD update. This is intentional for a v1beta1 promotion but should be documented in release notes with a migration guide.
- **Config.Telemetry / Config.Group in shared struct**: These fields live in `pkg/vmcp/config/config.go` and are valid for standalone CLI deployments. The operator-side fallback is removed, but the fields remain in the struct.
- **OIDCConfigRef type reuse**: The `OIDCConfigRef` struct is ONLY used by deprecated `oidcConfig` fields. The new path uses `MCPOIDCConfigReference`. Safe to remove after D2+D3.
- **Chart CRDs**: The `deploy/charts/operator-crds/` CRD files are generated — they'll be updated by `task operator-manifests`.
