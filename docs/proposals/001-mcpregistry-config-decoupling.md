# RFC: Decouple MCPRegistry CRD from Registry Server Config Format

**Status:** Draft
**Author:** Chris Burns
**Date:** 2026-04-08

## Problem Statement

The MCPRegistry CRD currently mirrors the registry server's `config.yaml` structure
field-by-field. Every time the registry server's configuration format evolves, the
operator must update its CRD types, config generation logic, and tests — often as a
breaking CRD change.

**Evidence:** PR #4653 ("Align MCPRegistry CRD with registry server v2 config format")
required 2,993 additions / 2,458 deletions to align the CRD with a v2 config format.
The change added 10+ new Go types, rewrote config generation, migrated all tests, and
introduced a breaking API change (spec.registries changed meaning). Most of the new
CRD fields were pure pass-through — the operator just relayed them into config.yaml
without acting on them.

This coupling means:
- Registry server config changes force operator CRD releases
- CRD breaking changes require user migration
- The operator carries ~1,800 lines of transformation code that adds no value beyond
  relaying typed fields into YAML

## Proposal

Add new decoupled fields to MCPRegistrySpec **alongside** the existing typed fields:

1. **`configYAML`** — a multiline YAML string containing the complete registry server
   config. The operator puts this into a ConfigMap and mounts it at `/config/config.yaml`.
2. **`volumes`** — standard `[]corev1.Volume` definitions that the operator appends to
   the pod spec.
3. **`volumeMounts`** — standard `[]corev1.VolumeMount` definitions that the operator
   appends to the registry-api container.
4. **`pgpassSecretRef`** — references a pre-created Secret containing a pgpass file.
   When set, the operator handles the init container + `chmod 0600` permission plumbing
   invisibly.

The existing typed fields (`registries`, `databaseConfig`, `authConfig`) are
**deprecated but retained** for backward compatibility. The operator supports both
paths:
- **New path**: when `configYAML` is set, the operator uses it directly (no config
  transformation). Volumes, mounts, and pgpass are user-managed via the new fields.
- **Legacy path**: when `configYAML` is NOT set, the operator uses the existing typed
  fields and config generation logic exactly as it does today.

The two paths are **mutually exclusive** — setting `configYAML` alongside `registries`,
`databaseConfig`, or `authConfig` is a validation error.

This strategy allows:
1. A release with both paths available
2. Users migrate at their own pace
3. A future release removes the deprecated fields and legacy code path

See [Phase 2: Deprecation Removal](#phase-2-deprecation-removal) at the bottom of this
document for the exact list of types, functions, and files to remove when the legacy
path is dropped.

## Current Architecture (Before)

### CRD Types (`mcpregistry_types.go`)

The current `MCPRegistrySpec` contains strongly-typed fields for every config.yaml
section:

```go
type MCPRegistrySpec struct {
    DisplayName     string                     `json:"displayName,omitempty"`
    Registries      []MCPRegistryConfig        `json:"registries"`
    EnforceServers  bool                       `json:"enforceServers,omitempty"`
    PodTemplateSpec *runtime.RawExtension      `json:"podTemplateSpec,omitempty"`
    DatabaseConfig  *MCPRegistryDatabaseConfig `json:"databaseConfig,omitempty"`
    AuthConfig      *MCPRegistryAuthConfig     `json:"authConfig,omitempty"`
}
```

This pulls in **15 additional types**: `MCPRegistryConfig`, `GitSource`, `GitAuthConfig`,
`APISource`, `PVCSource`, `SyncPolicy`, `RegistryFilter`, `NameFilter`, `TagFilter`,
`MCPRegistryDatabaseConfig`, `MCPRegistryAuthConfig`, `MCPRegistryOAuthConfig`,
`MCPRegistryOAuthProviderConfig`, `MCPRegistryAuthMode`, `MCPRegistryPhase`.

### Config Generation (`config/config.go`)

The `ConfigManager` contains ~470 lines of `build*` functions that transform CRD types
into config YAML structs:

- `BuildConfig()` — orchestrates the full transformation
- `buildRegistryConfig()` — converts MCPRegistryConfig → RegistryConfig
- `buildGitSourceConfig()` — converts GitSource → GitConfig
- `buildGitAuthConfig()` — converts GitAuthConfig → config GitAuthConfig, resolving
  `SecretKeySelector` → file path (`/secrets/{name}/{key}`)
- `buildAPISourceConfig()` — converts APISource → APIConfig
- `buildDatabaseConfig()` — converts MCPRegistryDatabaseConfig → DatabaseConfig with
  defaults
- `buildAuthConfig()` — converts MCPRegistryAuthConfig → AuthConfig
- `buildOAuthConfig()` — converts MCPRegistryOAuthConfig → OAuthConfig
- `buildOAuthProviderConfig()` — converts provider config, resolving `SecretKeySelector`
  and `ConfigMapKeySelector` refs → file paths
- `buildFilePath()` / `buildFilePathWithCustomName()` — computes mount paths for
  ConfigMap/PVC sources
- `buildSecretFilePath()` — computes `/secrets/{name}/{key}` from SecretKeySelector
- `buildCACertFilePath()` — computes `/config/certs/{name}/{key}` from
  ConfigMapKeySelector

The config module also defines ~220 lines of config YAML structs (`Config`,
`RegistryConfig`, `DatabaseConfig`, `AuthConfig`, `OAuthConfig`,
`OAuthProviderConfig`, `GitConfig`, `APIConfig`, `FileConfig`, `KubernetesConfig`,
`SyncPolicyConfig`, `FilterConfig`, etc.).

### Volume Mount Logic (`podtemplatespec.go`)

The operator generates volumes and mounts based on CRD field inspection:

- **`WithRegistrySourceMounts(containerName, registries)`** — iterates
  `spec.registries[]`, creates a ConfigMap volume for each `configMapRef` source and a
  PVC volume for each `pvcRef` source, mounted at `/config/registry/{name}/`
- **`WithGitAuthMount(containerName, secretRef)`** — creates a secret volume named
  `git-auth-{secretName}` mounted at `/secrets/{secretName}/`
- **`WithPGPassMount(containerName, secretName)`** — creates a secret volume, an
  emptyDir volume, an init container that copies the secret with `chmod 0600`, and mounts
  the result at `/home/appuser/.pgpass` with `PGPASSFILE` env var
- **`WithRegistryServerConfigMount(containerName, configMapName)`** — creates the config
  ConfigMap volume mounted at `/config/config.yaml`

### PGPass Generation (`pgpass.go`)

The operator reads two password secrets (app user + migration user), constructs a
pgpass-format string, and creates a derived Secret:

```go
// pgpass format: hostname:port:database:username:password
pgpassContent := fmt.Sprintf("%s:%d:%s:%s:%s\n%s:%d:%s:%s:%s\n",
    dbConfig.Host, port, dbConfig.Database, dbConfig.User, appUserPassword,
    dbConfig.Host, port, dbConfig.Database, dbConfig.MigrationUser, migrationPassword,
)
```

The pgpass secret is then mounted via an init container to ensure `0600` permissions.

### Deployment Construction (`deployment.go`)

`buildRegistryAPIDeployment()` inspects the MCPRegistry spec to decide which
`PodTemplateSpecOption`s to apply:

```go
opts := []PodTemplateSpecOption{
    WithLabels(labels),
    WithAnnotations(map[string]string{configHashAnnotation: configHash}),
    WithServiceAccountName(GetServiceAccountName(mcpRegistry)),
    WithContainer(BuildRegistryAPIContainer(getRegistryAPIImage())),
    WithRegistryServerConfigMount(registryAPIContainerName, configManager.GetRegistryServerConfigMapName()),
    WithRegistrySourceMounts(registryAPIContainerName, mcpRegistry.Spec.Registries),
    WithRegistryStorageMount(registryAPIContainerName),
}

if mcpRegistry.HasDatabaseConfig() {
    opts = append(opts, WithPGPassMount(registryAPIContainerName, secretName))
}

for _, registry := range mcpRegistry.Spec.Registries {
    if registry.Git != nil && registry.Git.Auth != nil {
        opts = append(opts, WithGitAuthMount(registryAPIContainerName, registry.Git.Auth.PasswordSecretRef))
    }
}
```

### Reconciliation Flow (`manager.go`)

`ReconcileAPIService()` calls:
1. `ensureRegistryServerConfigConfigMap()` — builds config via ConfigManager, creates
   ConfigMap
2. `ensureRBACResources()` — creates ServiceAccount, Role, RoleBinding
3. `ensurePGPassSecret()` — if database configured, reads secrets and builds pgpass
4. `ensureDeployment()` — builds and creates/updates Deployment
5. `ensureService()` — creates/updates ClusterIP Service

### Current Example: ConfigMap Source

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: my-registry
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: prod-registry
        key: registry.json
      syncPolicy:
        interval: "1h"
      filter:
        tags:
          include: ["production"]
          exclude: ["experimental"]
  databaseConfig:
    host: postgres
    port: 5432
    user: db_app
    migrationUser: db_migrator
    database: registry
    sslMode: require
    dbAppUserPasswordSecretRef:
      name: db-credentials
      key: app_password
    dbMigrationUserPasswordSecretRef:
      name: db-credentials
      key: migration_password
  authConfig:
    mode: anonymous
```

### Current Example: Git with Auth

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: git-registry
spec:
  registries:
    - name: private-repo
      format: toolhive
      git:
        repository: https://github.com/org/private-registry
        branch: main
        path: registry.json
        auth:
          username: git
          passwordSecretRef:
            name: git-credentials
            key: token
      syncPolicy:
        interval: "1h"
  authConfig:
    mode: anonymous
```

### Current Example: API Source

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: api-registry
spec:
  registries:
    - name: upstream
      format: toolhive
      api:
        endpoint: http://upstream-registry.default.svc:8080
      syncPolicy:
        interval: "30m"
  authConfig:
    mode: anonymous
```

### Current Example: OAuth Authentication

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: secure-registry
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: prod-registry
        key: registry.json
  authConfig:
    mode: oauth
    oauth:
      resourceUrl: https://registry.example.com
      realm: mcp-registry
      scopesSupported:
        - mcp-registry:read
        - mcp-registry:write
      providers:
        - name: keycloak
          issuerUrl: https://keycloak.example.com/realms/mcp
          audience: mcp-registry
          clientId: mcp-registry
          clientSecretRef:
            name: oauth-client-secret
            key: secret
          caCertRef:
            name: keycloak-ca
            key: ca.crt
```

## Proposed Architecture (Phase 1: Add New Path)

### Updated CRD Types

The new fields are added to `MCPRegistrySpec` alongside the existing ones. The existing
typed fields are marked as deprecated in documentation and comments but remain fully
functional.

```go
type MCPRegistrySpec struct {
    // ============================================================
    // NEW FIELDS — decoupled config path
    // ============================================================

    // ConfigYAML is the complete registry server config.yaml content.
    // The operator creates a ConfigMap from this string and mounts it
    // at /config/config.yaml in the registry-api container.
    // The operator does NOT parse, validate, or transform this content.
    //
    // Mutually exclusive with the legacy typed fields (Registries,
    // DatabaseConfig, AuthConfig). When set, the operator uses the
    // decoupled code path — volumes and mounts must be provided via
    // the Volumes and VolumeMounts fields below.
    //
    // +optional
    ConfigYAML string `json:"configYAML,omitempty"`

    // Volumes defines additional volumes to add to the registry API pod.
    // These are standard Kubernetes volume definitions. The operator appends
    // them to the pod spec alongside its own config volume.
    // Only used when configYAML is set.
    //
    // Use these to mount:
    //   - Secrets (git auth tokens, OAuth client secrets, CA certs)
    //   - ConfigMaps (registry data files)
    //   - PersistentVolumeClaims (registry data on persistent storage)
    //   - Any other volume type the registry server needs
    //
    // +optional
    // +listType=map
    // +listMapKey=name
    Volumes []corev1.Volume `json:"volumes,omitempty"`

    // VolumeMounts defines additional volume mounts for the registry-api container.
    // These are standard Kubernetes volume mount definitions. The operator appends
    // them to the container's volume mounts alongside the config mount.
    // Only used when configYAML is set.
    //
    // Mount paths must match the file paths referenced in configYAML.
    // For example, if configYAML references passwordFile: /secrets/git-creds/token,
    // a corresponding volume mount must exist with mountPath: /secrets/git-creds.
    //
    // +optional
    // +listType=map
    // +listMapKey=mountPath
    VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

    // PGPassSecretRef references a Secret containing a pre-created pgpass file.
    // Only used when configYAML is set. Mutually exclusive with DatabaseConfig.
    //
    // Why this is a dedicated field instead of a regular volume/volumeMount:
    // PostgreSQL's libpq rejects pgpass files that aren't mode 0600. Kubernetes
    // secret volumes mount files as root-owned, and the registry-api container
    // runs as non-root (UID 65532). A root-owned 0600 file is unreadable by
    // UID 65532, and using fsGroup changes permissions to 0640 which libpq also
    // rejects. The only solution is an init container that copies the file to an
    // emptyDir as the app user and runs chmod 0600. This cannot be expressed
    // through volumes/volumeMounts alone — it requires an init container, two
    // extra volumes (secret + emptyDir), a subPath mount, and an environment
    // variable, all wired together correctly.
    //
    // When specified, the operator generates all of that plumbing invisibly.
    // The user creates the Secret with pgpass-formatted content; the operator
    // handles only the Kubernetes permission mechanics.
    //
    // Example Secret:
    //   apiVersion: v1
    //   kind: Secret
    //   metadata:
    //     name: my-pgpass
    //   stringData:
    //     .pgpass: |
    //       postgres:5432:registry:db_app:mypassword
    //       postgres:5432:registry:db_migrator:otherpassword
    //
    // Then reference it:
    //   pgpassSecretRef:
    //     name: my-pgpass
    //     key: .pgpass
    //
    // +optional
    PGPassSecretRef *corev1.SecretKeySelector `json:"pgpassSecretRef,omitempty"`

    // ============================================================
    // EXISTING FIELDS — retained, shared between both paths
    // ============================================================

    // EnforceServers indicates whether MCPServers in this namespace must have
    // their images present in at least one registry in the namespace.
    // +kubebuilder:default=false
    // +optional
    EnforceServers bool `json:"enforceServers,omitempty"`

    // PodTemplateSpec defines the pod template for infrastructure customization.
    // Use this for concerns like affinity, tolerations, nodeSelector, resource
    // overrides, and imagePullSecrets. Merged with operator defaults.
    // Works with both the new configYAML path and the legacy typed path.
    // +optional
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Type=object
    PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

    // DisplayName is a human-readable name for the registry.
    // Works with both the new configYAML path and the legacy typed path.
    // +optional
    DisplayName string `json:"displayName,omitempty"`

    // ============================================================
    // DEPRECATED FIELDS — legacy typed config path
    // Deprecated: Use configYAML, volumes, volumeMounts, and
    // pgpassSecretRef instead. These fields will be removed in a
    // future release.
    // ============================================================

    // Registries defines the configuration for the registry data sources.
    // Deprecated: Use configYAML with volumes/volumeMounts instead.
    // +optional
    Registries []MCPRegistryConfig `json:"registries,omitempty"`

    // DatabaseConfig defines the PostgreSQL database configuration.
    // Deprecated: Put database config in configYAML and use pgpassSecretRef.
    // +optional
    DatabaseConfig *MCPRegistryDatabaseConfig `json:"databaseConfig,omitempty"`

    // AuthConfig defines the authentication configuration.
    // Deprecated: Put auth config in configYAML instead.
    // +optional
    AuthConfig *MCPRegistryAuthConfig `json:"authConfig,omitempty"`
}
```

### Mutual Exclusivity Validation

The reconciler validates at the start of each reconciliation:

```go
func (r *MCPRegistryReconciler) validateSpec(spec *MCPRegistrySpec) error {
    hasNewPath := spec.ConfigYAML != ""
    hasLegacyPath := len(spec.Registries) > 0 || spec.DatabaseConfig != nil || spec.AuthConfig != nil

    if hasNewPath && hasLegacyPath {
        return fmt.Errorf(
            "configYAML is mutually exclusive with registries, databaseConfig, and authConfig; " +
            "use configYAML with volumes/volumeMounts for the decoupled path, " +
            "or use registries/databaseConfig/authConfig for the legacy path")
    }

    if !hasNewPath && !hasLegacyPath {
        return fmt.Errorf("either configYAML or registries must be specified")
    }

    // New path: volumes/volumeMounts/pgpassSecretRef only valid with configYAML
    if !hasNewPath {
        if len(spec.Volumes) > 0 || len(spec.VolumeMounts) > 0 {
            return fmt.Errorf("volumes and volumeMounts require configYAML to be set")
        }
        if spec.PGPassSecretRef != nil {
            return fmt.Errorf("pgpassSecretRef requires configYAML to be set; use databaseConfig for the legacy path")
        }
    }

    // Legacy path: databaseConfig/authConfig only valid without configYAML
    // (already covered by the mutual exclusivity check above)

    return nil
}
```

### Code Path Switch

The operator's `ReconcileAPIService` and `buildRegistryAPIDeployment` branch on
`spec.ConfigYAML`:

```go
func (m *manager) ReconcileAPIService(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) *Error {
    if mcpRegistry.Spec.ConfigYAML != "" {
        return m.reconcileNewPath(ctx, mcpRegistry)
    }
    return m.reconcileLegacyPath(ctx, mcpRegistry)
}
```

- **`reconcileNewPath`**: creates ConfigMap from raw YAML string, skips pgpass Secret
  generation, builds deployment with user volumes/mounts appended, adds pgpass init
  container if `pgpassSecretRef` is set
- **`reconcileLegacyPath`**: existing logic unchanged — uses `ConfigManager.BuildConfig()`,
  generates pgpass Secret, builds deployment with `WithRegistrySourceMounts`,
  `WithGitAuthMount`, `WithPGPassMount`

Both paths share: RBAC, Service, status updates, readiness checking, PodTemplateSpec
merge logic.

**No existing types, functions, or tests are modified or deleted in Phase 1.** The
legacy path continues to work exactly as before. New code is additive only.

### New Config Generation

The entire `config/config.go` file is replaced with a minimal function:

```go
package config

const (
    RegistryServerConfigFilePath = "/config"
    RegistryServerConfigFileName = "config.yaml"
)

// ToConfigMap creates a ConfigMap from the raw config YAML string.
// The operator does not parse or transform the content.
func ToConfigMap(name, namespace, configYAML string) (*corev1.ConfigMap, error) {
    configMap := &corev1.ConfigMap{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-registry-server-config", name),
            Namespace: namespace,
            Annotations: map[string]string{
                checksum.ContentChecksumAnnotation: ctrlutil.CalculateConfigHash(
                    []byte(configYAML)),
            },
        },
        Data: map[string]string{
            RegistryServerConfigFileName: configYAML,
        },
    }
    return configMap, nil
}
```

**Files/functions deleted:**
- `config/config.go` — all `build*` functions (~470 lines), all config struct types
  (~220 lines)
- `config/config_test.go` — all config generation tests (~976 lines)
- `ConfigManager` interface and `configManager` struct

### New Volume Mount Logic

`WithRegistrySourceMounts()` and `WithGitAuthMount()` are deleted — users provide
these volumes directly.

`WithPGPassMount()` is **retained but simplified** — it no longer takes a generated
secret name. Instead it takes a `SecretKeySelector` pointing to the user-created
pgpass Secret and handles the init container + emptyDir + `chmod 0600` + env var
plumbing. The operator no longer generates the pgpass Secret content; it only mounts
the user-provided one with correct permissions.

The deployment builder becomes:

```go
opts := []PodTemplateSpecOption{
    WithLabels(labels),
    WithAnnotations(map[string]string{configHashAnnotation: configHash}),
    WithServiceAccountName(GetServiceAccountName(mcpRegistry)),
    WithContainer(BuildRegistryAPIContainer(getRegistryAPIImage())),
    WithRegistryServerConfigMount(registryAPIContainerName, configMapName),
}

// Append user-provided volumes
for _, vol := range mcpRegistry.Spec.Volumes {
    opts = append(opts, WithVolume(vol))
}

// Append user-provided volume mounts
for _, mount := range mcpRegistry.Spec.VolumeMounts {
    opts = append(opts, WithVolumeMount(registryAPIContainerName, mount))
}

// Mount pgpass with correct permissions if secret ref is provided
if mcpRegistry.Spec.PGPassSecretRef != nil {
    opts = append(opts, WithPGPassMount(registryAPIContainerName, *mcpRegistry.Spec.PGPassSecretRef))
}
```

Note: `WithRegistryStorageMount()` is removed. The registry server uses in-memory
git clones and PostgreSQL for all storage — it does not require a writable local
directory. Users who need writable scratch space can add an emptyDir via
`spec.volumes[]` and `spec.volumeMounts[]`.

### New Reconciliation Flow

`ReconcileAPIService()` simplifies to:

1. `ensureRegistryServerConfigConfigMap()` — puts `spec.configYAML` string into ConfigMap
2. `ensureRBACResources()` — unchanged
3. `ensureDeployment()` — builds deployment with config mount, user volumes/mounts
   appended, plus pgpass init container if `pgpassSecretRef` is set
4. `ensureService()` — unchanged

The old `ensurePGPassSecret()` step is removed — the operator no longer generates
the pgpass Secret. It only mounts a user-provided one with correct permissions.

### Config Hash

Currently: `ctrlutil.CalculateConfigHash(mcpRegistry.Spec)` hashes the full typed spec.

After: `ctrlutil.CalculateConfigHash([]byte(mcpRegistry.Spec.ConfigYAML))` hashes the
raw YAML string. Changes to `spec.volumes` or `spec.volumeMounts` are captured by the
existing `podTemplateSpecHashAnnotation` mechanism (extend it to include volumes/mounts
in the hash).

## Migration: Before → After for Every Scenario

### Scenario 1: ConfigMap Source

**Before:**
```yaml
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: prod-registry
        key: registry.json
      syncPolicy:
        interval: "1h"
      filter:
        tags:
          include: ["production"]
  authConfig:
    mode: anonymous
```

**After:**
```yaml
spec:
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
        syncPolicy:
          interval: 1h
        filter:
          tags:
            include: ["production"]
    registries:
      - name: default
        sources: ["production"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: registry-data-production
      configMap:
        name: prod-registry
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
```

### Scenario 2: Git Source with Auth

**Before:**
```yaml
spec:
  registries:
    - name: private-repo
      format: toolhive
      git:
        repository: https://github.com/org/private-registry
        branch: main
        path: registry.json
        auth:
          username: git
          passwordSecretRef:
            name: git-credentials
            key: token
      syncPolicy:
        interval: "1h"
  authConfig:
    mode: anonymous
```

**After:**
```yaml
spec:
  configYAML: |
    sources:
      - name: private-repo
        format: toolhive
        git:
          repository: https://github.com/org/private-registry
          branch: main
          path: registry.json
          auth:
            username: git
            passwordFile: /secrets/git-credentials/token
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["private-repo"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: git-auth-credentials
      secret:
        secretName: git-credentials
        items:
          - key: token
            path: token
  volumeMounts:
    - name: git-auth-credentials
      mountPath: /secrets/git-credentials
      readOnly: true
```

### Scenario 3: API Source

**Before:**
```yaml
spec:
  registries:
    - name: upstream
      format: toolhive
      api:
        endpoint: http://upstream-registry.default.svc:8080
      syncPolicy:
        interval: "30m"
  authConfig:
    mode: anonymous
```

**After:**
```yaml
spec:
  configYAML: |
    sources:
      - name: upstream
        format: upstream
        api:
          endpoint: http://upstream-registry.default.svc:8080
        syncPolicy:
          interval: 30m
    registries:
      - name: default
        sources: ["upstream"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
```

Note: No volumes or volumeMounts needed — API sources are remote. API sources must
use `format: upstream` (the only format supported for API sources).

### Scenario 4: OAuth Authentication with Secrets

**Before:**
```yaml
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: prod-registry
        key: registry.json
  authConfig:
    mode: oauth
    oauth:
      resourceUrl: https://registry.example.com
      realm: mcp-registry
      scopesSupported: ["mcp-registry:read", "mcp-registry:write"]
      providers:
        - name: keycloak
          issuerUrl: https://keycloak.example.com/realms/mcp
          audience: mcp-registry
          clientId: mcp-registry
          clientSecretRef:
            name: oauth-client-secret
            key: secret
          caCertRef:
            name: keycloak-ca
            key: ca.crt
```

**After:**
```yaml
spec:
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
    registries:
      - name: default
        sources: ["production"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: oauth
      oauth:
        resourceUrl: https://registry.example.com
        realm: mcp-registry
        scopesSupported: ["mcp-registry:read", "mcp-registry:write"]
        providers:
          - name: keycloak
            issuerUrl: https://keycloak.example.com/realms/mcp
            audience: mcp-registry
            clientId: mcp-registry
            clientSecretFile: /secrets/oauth-client-secret/secret
            caCertPath: /config/certs/keycloak-ca/ca.crt
  volumes:
    - name: registry-data-production
      configMap:
        name: prod-registry
        items:
          - key: registry.json
            path: registry.json
    - name: oauth-client-secret
      secret:
        secretName: oauth-client-secret
        items:
          - key: secret
            path: secret
    - name: keycloak-ca
      configMap:
        name: keycloak-ca
        items:
          - key: ca.crt
            path: ca.crt
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
    - name: oauth-client-secret
      mountPath: /secrets/oauth-client-secret
      readOnly: true
    - name: keycloak-ca
      mountPath: /config/certs/keycloak-ca
      readOnly: true
```

### Scenario 5: Database with PGPass

**Before:**
```yaml
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: prod-registry
        key: registry.json
  databaseConfig:
    host: postgres
    port: 5432
    user: db_app
    migrationUser: db_migrator
    database: registry
    sslMode: require
    maxOpenConns: 20
    dbAppUserPasswordSecretRef:
      name: db-credentials
      key: app_password
    dbMigrationUserPasswordSecretRef:
      name: db-credentials
      key: migration_password
  authConfig:
    mode: anonymous
```

**After:**

The user creates the pgpass secret themselves with the content they control:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-registry-pgpass
stringData:
  .pgpass: |
    postgres:5432:registry:db_app:myapppassword
    postgres:5432:registry:db_migrator:mymigrationpassword
```

Then references it via `pgpassSecretRef`. The operator handles the init container,
emptyDir, `chmod 0600`, and `PGPASSFILE` env var invisibly — the user never writes
any of that:

```yaml
spec:
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
    registries:
      - name: default
        sources: ["production"]
    database:
      host: postgres
      port: 5432
      user: db_app
      migrationUser: db_migrator
      database: registry
      sslMode: require
      maxOpenConns: 20
    auth:
      mode: anonymous
  pgpassSecretRef:
    name: my-registry-pgpass
    key: .pgpass
  volumes:
    - name: registry-data-production
      configMap:
        name: prod-registry
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
```

Behind the scenes, the operator generates the following into the pod spec (the user
does not write any of this):
- A `pgpass-secret` volume from the referenced Secret
- A `pgpass` emptyDir volume
- A `setup-pgpass` init container (`cgr.dev/chainguard/busybox:latest`) that runs
  `cp /secret/.pgpass /pgpass/.pgpass && chmod 0600 /pgpass/.pgpass`
- A volume mount at `/home/appuser/.pgpass` with `subPath: .pgpass`
- A `PGPASSFILE=/home/appuser/.pgpass` environment variable

### Scenario 6: Multiple Sources

**Before:**
```yaml
spec:
  registries:
    - name: local-data
      format: toolhive
      configMapRef:
        name: registry-data
        key: servers.json
    - name: github-registry
      format: toolhive
      git:
        repository: https://github.com/org/registry
        branch: main
        path: registry.json
      syncPolicy:
        interval: "1h"
    - name: upstream-api
      format: toolhive
      api:
        endpoint: http://upstream.default.svc:8080
      syncPolicy:
        interval: "30m"
  authConfig:
    mode: anonymous
```

**After:**
```yaml
spec:
  configYAML: |
    sources:
      - name: local-data
        format: toolhive
        file:
          path: /config/registry/local-data/registry.json
      - name: github-registry
        format: toolhive
        git:
          repository: https://github.com/org/registry
          branch: main
          path: registry.json
        syncPolicy:
          interval: 1h
      - name: upstream-api
        format: upstream
        api:
          endpoint: http://upstream.default.svc:8080
        syncPolicy:
          interval: 30m
    registries:
      - name: default
        sources: ["local-data", "github-registry", "upstream-api"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: registry-data-local
      configMap:
        name: registry-data
        items:
          - key: servers.json
            path: registry.json
  volumeMounts:
    - name: registry-data-local
      mountPath: /config/registry/local-data
      readOnly: true
```

### Scenario 7: Minimal (no volumes needed)

**After:**
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: minimal-registry
spec:
  configYAML: |
    sources:
      - name: k8s
        format: upstream
        kubernetes: {}
    registries:
      - name: default
        sources: ["k8s"]
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
```

No volumes, no volumeMounts. The simplest possible MCPRegistry. Note that `database`
and `auth` sections are required by the registry server.

### Scenario 8: Future Config Fields (the whole point)

When the registry server adds new config sections (e.g., telemetry, authz, claims,
dynamic database auth), users just update their `configYAML` string. **Zero operator
changes required:**

```yaml
spec:
  configYAML: |
    sources:
      - name: k8s
        format: upstream
        kubernetes:
          namespaces: ["production", "staging"]
          claimMapping:
            team: metadata.labels.team
        claims:
          team: platform
    registries:
      - name: default
        sources: ["k8s"]
        claims:
          environment: production
    auth:
      mode: oauth
      publicPaths: ["/health", "/readiness"]
      authz:
        roles:
          superAdmin:
            - role: admin
          manageSources:
            - team: platform
          manageRegistries:
            - team: platform
      oauth:
        providers:
          - name: keycloak
            issuerUrl: https://keycloak.example.com/realms/mcp
            audience: mcp-registry
    database:
      host: postgres
      port: 5432
      user: db_app
      database: registry
      maxMetaSize: 1048576
      dynamicAuth:
        awsRdsIam:
          region: us-east-1
    telemetry:
      enabled: true
      endpoint: otel-collector:4318
      tracing:
        enabled: true
        sampling: 0.1
      metrics:
        enabled: true
```

None of these fields (claims, authz, publicPaths, dynamicAuth, telemetry, maxMetaSize,
kubernetes namespaces/claimMapping) require any operator code changes.

## What Changes in the Operator (Phase 1)

Phase 1 is **additive only** — no existing code is deleted or modified in a breaking
way. The legacy typed path continues to work identically.

### Files Added

| File | Description |
|------|-------------|
| `pkg/registryapi/new_path.go` (or similar) | `reconcileNewPath()` — new config path logic: creates ConfigMap from raw YAML, builds deployment with user volumes/mounts, pgpass init container |
| `pkg/registryapi/config/configmap.go` (or similar) | `ToConfigMap()` — 20-line function that puts a raw YAML string into a ConfigMap with content checksum |
| New path unit tests | Tests for the new reconciliation path, validation, volume injection |
| New path integration tests | Tests for end-to-end new path with configYAML |
| New example YAMLs | Examples using configYAML alongside existing examples |

### Files Modified

| File | Change |
|------|--------|
| `mcpregistry_types.go` | Add `ConfigYAML`, `Volumes`, `VolumeMounts`, `PGPassSecretRef` fields; mark `Registries`, `DatabaseConfig`, `AuthConfig` as deprecated in comments; relax `Registries` from required to optional |
| `mcpregistry_controller.go` | Add validation for mutual exclusivity; branch to new or legacy path |
| `manager.go` | Add `reconcileNewPath()` method; rename existing flow to `reconcileLegacyPath()` |
| `deployment.go` | Add `buildRegistryAPIDeploymentNewPath()` for the new path deployment builder |
| `podtemplatespec.go` | Add `WithPGPassSecretRefMount()` variant that takes `SecretKeySelector` instead of generated secret name |
| `types.go` | No changes needed — pgpass constants are reused by new path |
| `zz_generated.deepcopy.go` | Regenerated (larger — new fields added) |
| CRD manifests | Regenerated (new fields added, `registries` minItems removed) |
| CRD XValidation rule | Changed from `size(self.spec.registries) > 0` to allow either `configYAML` or `registries` |

### Files Unchanged

| File | Reason |
|------|--------|
| `config/config.go` | Legacy config generation — used by legacy path, untouched |
| `config/config_test.go` | Legacy config tests — still valid |
| `pgpass.go` | Legacy pgpass generation — used by legacy path, untouched |
| `podtemplatespec.go` — `WithRegistrySourceMounts` | Used by legacy path, untouched |
| `podtemplatespec.go` — `WithGitAuthMount` | Used by legacy path, untouched |
| `podtemplatespec.go` — `WithPGPassMount` | Used by legacy path, untouched |
| `podtemplatespec.go` — `WithRegistryStorageMount` | Used by legacy path, untouched |
| `podtemplatespec.go` — `WithRegistryServerConfigMount` | Used by both paths |
| `podtemplatespec.go` — `MergePodTemplateSpecs` | Used by both paths |
| `podtemplatespec.go` — `BuildRegistryAPIContainer` | Used by both paths |
| `rbac.go` | Used by both paths |
| `service.go` | Used by both paths |

## PodTemplateSpec Merge Semantics and Conflict Prevention

The new design has **three sources** that contribute to the final pod spec:

1. **Operator-generated** — config volume, pgpass init container (when
   `pgpassSecretRef` is set)
2. **`spec.volumes[]` / `spec.volumeMounts[]`** — first-class CRD fields for user volumes
3. **`spec.podTemplateSpec`** — user-provided PodTemplateSpec for infrastructure overrides
   (affinity, tolerations, resources, imagePullSecrets, and any other pod-level concerns)

These three sources merge using the existing `MergePodTemplateSpecs()` function, which
gives the user PodTemplateSpec precedence over operator defaults. Understanding where
each source enters the pipeline is critical to avoiding conflicts.

### Injection Order

The deployment builder applies options in this order:

```go
// 1. Operator-generated defaults (unconditional)
opts := []PodTemplateSpecOption{
    WithLabels(labels),
    WithAnnotations(configHash),
    WithServiceAccountName(serviceAccount),
    WithContainer(BuildRegistryAPIContainer(image)),    // creates "registry-api" container
    WithRegistryServerConfigMount(containerName, cm),   // adds config volume + mount + args
}

// 2. User-provided volumes/mounts from first-class CRD fields (injected into defaults side)
for _, vol := range mcpRegistry.Spec.Volumes {
    opts = append(opts, WithVolume(vol))
}
for _, mount := range mcpRegistry.Spec.VolumeMounts {
    opts = append(opts, WithVolumeMount(containerName, mount))
}

// 3. Operator-generated pgpass (conditional)
if mcpRegistry.Spec.PGPassSecretRef != nil {
    opts = append(opts, WithPGPassMount(containerName, *mcpRegistry.Spec.PGPassSecretRef))
}

// 4. All of the above builds the "defaults" PodTemplateSpec
// 5. MergePodTemplateSpecs(defaults, userPodTemplateSpec) — user PTS wins on conflicts
builder := NewPodTemplateSpecBuilderFrom(userPTS)  // stores user PTS separately
podTemplateSpec := builder.Apply(opts...).Build()   // merges defaults + user PTS
```

**Key rule**: `spec.volumes[]` and `spec.volumeMounts[]` land on the **defaults side**.
`spec.podTemplateSpec` is the **user side** and takes precedence in the merge.

### Operator-Reserved Resource Names

The operator creates the following named resources in the pod spec. These names are
**reserved** — if a user provides resources with these names, they will either be
silently skipped (on the defaults side, via `WithVolume` idempotency) or silently
overridden (if supplied via `podTemplateSpec`, which takes precedence).

**Unconditional (always present):**

| Type | Name | Mount Path |
|------|------|------------|
| Volume | `registry-server-config` | — |
| VolumeMount | `registry-server-config` | `/config` |
| Container | `registry-api` | — |

**Conditional (when `pgpassSecretRef` is set):**

| Type | Name | Mount Path |
|------|------|------------|
| Volume | `pgpass-secret` | — |
| Volume | `pgpass` | — |
| VolumeMount | `pgpass` | `/home/appuser/.pgpass` |
| InitContainer | `setup-pgpass` | — |
| EnvVar | `PGPASSFILE` | — |

### Conflict Scenarios and Mitigations

#### Conflict 1: Volume name collision between `spec.volumes[]` and operator-reserved names

If `spec.volumes[]` contains a volume named `registry-server-config`, the `WithVolume`
idempotency check will **skip** the user's volume (the operator's was added first).
This is silent and could confuse users.

**Mitigation**: The reconciler should validate `spec.volumes[]` at reconciliation time
and reject any volume whose name matches a reserved name. Set the `Ready` condition to
`False` with a descriptive message like "volume name 'registry-server-config' is
reserved by the operator".

#### Conflict 2: Volume name collision via `spec.podTemplateSpec`

If the user's `podTemplateSpec` contains a volume with a reserved name (e.g.,
`registry-server-config`), the merge gives the **user PTS precedence** — the operator's
volume definition is silently replaced. This breaks the config mount.

**Mitigation**: Same validation — scan `podTemplateSpec` for reserved volume names
before merging. Reject with a condition error.

#### Conflict 3: MountPath collision (most dangerous)

The merge logic deduplicates volumes and mounts by **name only**, not by mount path.
If a user adds a volume mount with a different name but the same mount path as an
operator-generated mount (e.g., a mount at `/config`), both mounts appear
in the final spec. Kubernetes rejects this with a "must be unique" error on the pod.

**Mitigation**: After the merge, before creating the Deployment, scan all volume mounts
on the `registry-api` container for duplicate mount paths. Surface any duplicates as a
condition error on the MCPRegistry status.

#### Conflict 4: InitContainer name collision with `setup-pgpass`

If the user's `podTemplateSpec` includes an init container named `setup-pgpass`, the
merge will combine it with the operator's init container. The user's `command`, `image`,
`volumeMounts`, `securityContext`, and `resources` would take precedence if set,
potentially breaking the pgpass permission setup.

**Mitigation**: Include `setup-pgpass` in the reserved names validation when
`pgpassSecretRef` is set.

#### Conflict 5: Double injection via `spec.volumes[]` AND `spec.podTemplateSpec`

A user could declare the same logical volume in both `spec.volumes[]` (first-class
field) and `spec.podTemplateSpec.spec.volumes[]`. Since they land on different sides
of the merge, the `podTemplateSpec` version wins. This is technically correct but
confusing.

**Mitigation**: Document clearly in the CRD field descriptions:
- `spec.volumes[]` — for volumes that support `configYAML` (registry data, secrets)
- `spec.podTemplateSpec` — for infrastructure concerns (imagePullSecrets, tolerations,
  affinity, resource overrides)
- If the same volume name appears in both, `podTemplateSpec` wins

#### Conflict 6: PGPASSFILE env var override

If the user's `podTemplateSpec` container `registry-api` sets `PGPASSFILE`, it
overrides the operator's value. This is intentional — it lets advanced users point to
a custom location — but could cause silent breakage if unintended.

**Mitigation**: Document this as expected behavior. No validation needed — it's a
legitimate escape hatch.

### Validation Implementation

The reconciler should perform the following checks before building the Deployment:

```go
// Reserved volume names — reject if user provides these
reservedVolumeNames := map[string]bool{
    "registry-server-config": true,
}
if mcpRegistry.Spec.PGPassSecretRef != nil {
    reservedVolumeNames["pgpass-secret"] = true
    reservedVolumeNames["pgpass"] = true
}

// Check spec.volumes[]
for _, vol := range mcpRegistry.Spec.Volumes {
    if reservedVolumeNames[vol.Name] {
        return error: volume name is reserved
    }
}

// Check spec.podTemplateSpec volumes (if parseable)
if userPTS != nil {
    for _, vol := range userPTS.Spec.Volumes {
        if reservedVolumeNames[vol.Name] {
            return error: volume name is reserved
        }
    }
}

// Reserved init container names
if mcpRegistry.Spec.PGPassSecretRef != nil {
    if userPTS has init container named "setup-pgpass" {
        return error: init container name is reserved
    }
}

// After merge: check for duplicate mount paths
seen := map[string]string{} // mountPath -> volumeMountName
for _, mount := range mergedContainer.VolumeMounts {
    if existing, ok := seen[mount.MountPath]; ok {
        return error: duplicate mount path from mounts named X and Y
    }
    seen[mount.MountPath] = mount.Name
}
```

### Summary

| Source | Lands on | Precedence | Use for |
|--------|----------|------------|---------|
| Operator-generated (config volume, pgpass) | Defaults side | Lowest | System plumbing — users should not override |
| `spec.volumes[]` / `spec.volumeMounts[]` | Defaults side (after operator) | Middle | Mounting secrets, ConfigMaps, PVCs for `configYAML` |
| `spec.podTemplateSpec` | User side | Highest | Affinity, tolerations, resources, imagePullSecrets |

The validation checks prevent accidental collisions. Intentional overrides via
`podTemplateSpec` are still possible for advanced users who know what they're doing —
they just can't use the reserved names.

## What Users Gain and Lose

### Gains

| Capability | Description |
|---|---|
| **Config independence** | Registry server config changes never require CRD/operator updates |
| **Immediate access to new features** | New config sections usable day-one without waiting for operator release |
| **Full volume control** | Users can mount any volume type Kubernetes supports |
| **No more CRD breaking changes** | Config format evolution doesn't force CRD version bumps |
| **Gradual migration** | Both paths coexist — users migrate at their own pace |
| **Simpler operator (Phase 2)** | ~2,260 fewer lines after legacy removal |

### Loses

| Capability | Description | Mitigation |
|---|---|---|
| **CRD-level validation** | No kubebuilder markers on config fields | Runtime validation by registry server; consider optional validating webhook |
| **`kubectl explain` for config** | `spec.configYAML` is just a string | Document config format in REGISTRY.md; provide examples |
| **Path computation** | User must know mount path conventions | Document conventions; provide examples for every source type |
| **Secret ref resolution** | User writes file paths, not SecretKeySelectors | Standard Kubernetes volume pattern; well-understood |
| **Default kubernetes source injection** | User must include it explicitly | Documented in examples; already removed in PR #4653 |

## PGPass Discussion

### Why pgpass needs a dedicated CRD field

A natural question is: "Why can't users just mount the pgpass secret using the
`volumes` and `volumeMounts` fields like any other secret?"

The answer is a chain of constraints specific to pgpass:

1. **PostgreSQL's libpq rejects pgpass files that aren't mode `0600`** — this is a
   hard requirement in the PostgreSQL client library, not something the registry
   server controls.

2. **Kubernetes secret volumes mount files as root-owned** — the file UID is 0
   regardless of `defaultMode`.

3. **The registry-api container runs as non-root (UID 65532)** — a root-owned `0600`
   file is unreadable by UID 65532.

4. **`fsGroup` doesn't help** — setting `securityContext.fsGroup: 65532` changes the
   group ownership, but Kubernetes also sets the group-read bit, making the file `0640`.
   libpq rejects `0640` because it has group permissions.

5. **The only working pattern is an init container** — it copies the secret file to an
   emptyDir as UID 65532, runs `chmod 0600`, and the app container mounts the emptyDir
   result. This requires two volumes (secret + emptyDir), an init container with
   specific security context and resource limits, a subPath mount, and a `PGPASSFILE`
   environment variable — all wired together correctly.

This cannot be expressed through `volumes`/`volumeMounts` alone. If a user tried to
mount a pgpass secret as a regular volume, the registry server would fail at startup
with a "pgpass file has group or world access" error from libpq.

### Recommendation: `pgpassSecretRef`

The recommended approach is `pgpassSecretRef` — the user creates the pgpass Secret
with the content they control, and the operator handles only the Kubernetes permission
mechanics:

```go
// PGPassSecretRef references a Secret containing a pre-created pgpass file.
// +optional
PGPassSecretRef *corev1.SecretKeySelector `json:"pgpassSecretRef,omitempty"`
```

**What the user does:**
1. Creates a Secret with pgpass-formatted content (they control host, port, database,
   users, and passwords)
2. Sets `pgpassSecretRef` to reference that Secret

**What the operator does (invisibly):**
1. Creates a `pgpass-secret` volume from the referenced Secret
2. Creates a `pgpass` emptyDir volume
3. Creates a `setup-pgpass` init container that copies the file with `chmod 0600`
4. Mounts the result at `/home/appuser/.pgpass` with subPath
5. Sets `PGPASSFILE=/home/appuser/.pgpass` environment variable

**What the operator does NOT do:**
- Read, generate, or transform the pgpass file content
- Know about database hostnames, ports, usernames, or passwords
- Aggregate multiple secrets into one file

This gives users full control over the pgpass content while hiding the one piece of
Kubernetes plumbing that is genuinely impossible to express declaratively.

### Alternatives Considered

**Alternative A: User manages everything (no `pgpassSecretRef` field)**

The user handles the init container via `podTemplateSpec`. This works but requires ~30
lines of init container boilerplate that is easy to get wrong (image, command, security
context, resource limits, volume mounts, env var). The init container pattern is not
obvious — most users would not know they need it, and would spend time debugging
"pgpass file has group or world access" errors before discovering the root/non-root
ownership issue.

**Alternative B: Operator generates pgpass content from password secret refs**

The operator reads two password secrets and constructs the pgpass file. This is what
the current architecture does. It works well but couples the operator to database
configuration details (host, port, database, usernames) and requires the operator to
aggregate secrets — adding complexity that this RFC aims to remove.

**Alternative C: Registry server accepts passwords via environment variables**

If the registry server supported `DB_APP_PASSWORD` / `DB_MIGRATION_PASSWORD` env vars,
the entire pgpass problem disappears — env vars from secrets are native Kubernetes with
no permission issues. This is the cleanest long-term solution but requires a registry
server change and is independent of this RFC.

---

## Phase 2: Deprecation Removal

This section is a self-contained reference for a future implementation that removes the
legacy typed config path after users have migrated to `configYAML`. It should be
executed as a separate PR after at least one release with both paths available.

### Preconditions

- At least one release has shipped with both paths available
- Users have been notified of the deprecation via release notes
- No known users are still on the legacy path (or a hard cutoff date has passed)

### CRD Type Changes

**Remove these fields from `MCPRegistrySpec`:**

```go
// DELETE these fields:
Registries     []MCPRegistryConfig        `json:"registries,omitempty"`
DatabaseConfig *MCPRegistryDatabaseConfig  `json:"databaseConfig,omitempty"`
AuthConfig     *MCPRegistryAuthConfig      `json:"authConfig,omitempty"`
```

**Make `ConfigYAML` required:**

```go
// CHANGE from optional to required:
// +kubebuilder:validation:Required
// +kubebuilder:validation:MinLength=1
ConfigYAML string `json:"configYAML"`
```

**Remove these types entirely from `mcpregistry_types.go`:**

- `MCPRegistryConfig`
- `GitSource`
- `GitAuthConfig`
- `APISource`
- `PVCSource`
- `SyncPolicy`
- `RegistryFilter`
- `NameFilter`
- `TagFilter`
- `MCPRegistryDatabaseConfig`
- `MCPRegistryAuthConfig`
- `MCPRegistryOAuthConfig`
- `MCPRegistryOAuthProviderConfig`
- `MCPRegistryAuthMode` (type and constants `MCPRegistryAuthModeAnonymous`,
  `MCPRegistryAuthModeOAuth`)

**Remove these helper methods from `MCPRegistry`:**

- `HasDatabaseConfig()`
- `GetDatabaseConfig()`
- `GetDatabasePort()`
- `BuildPGPassSecretName()`
- `GetStorageName()` (no longer needed without legacy storage)

### Operator Code Removal

**Delete these files entirely:**

| File | Lines | Description |
|------|-------|-------------|
| `pkg/registryapi/config/config.go` — all `build*` functions and config struct types | ~690 | Legacy config generation: `BuildConfig()`, `buildRegistryConfig()`, `buildGitSourceConfig()`, `buildGitAuthConfig()`, `buildAPISourceConfig()`, `buildDatabaseConfig()`, `buildAuthConfig()`, `buildOAuthConfig()`, `buildOAuthProviderConfig()`, `buildFilePath()`, `buildFilePathWithCustomName()`, `buildSecretFilePath()`, `buildCACertFilePath()`, `buildGitPasswordFilePath()`. Also all config YAML struct types: `Config`, `RegistryConfig`, `DatabaseConfig`, `AuthConfig`, `OAuthConfig`, `OAuthProviderConfig`, `GitConfig`, `GitAuthConfig`, `APIConfig`, `FileConfig`, `KubernetesConfig`, `SyncPolicyConfig`, `FilterConfig`, `NameFilterConfig`, `TagFilterConfig`, `AuthMode` constants. |
| `pkg/registryapi/config/config_test.go` | ~976 | All legacy config generation tests |
| `pkg/registryapi/pgpass.go` | ~78 | Legacy pgpass Secret generation: `ensurePGPassSecret()`, `GetPGPassSecretKey()` |

**Delete these functions from existing files:**

| File | Function | Description |
|------|----------|-------------|
| `podtemplatespec.go` | `WithRegistrySourceMounts()` | Legacy ConfigMap/PVC volume generation from typed `Registries[]` |
| `podtemplatespec.go` | `WithGitAuthMount()` | Legacy git auth secret volume from typed `SecretKeySelector` |
| `podtemplatespec.go` | `WithPGPassMount()` (legacy variant) | Legacy pgpass mount that takes a generated secret name |
| `podtemplatespec.go` | `WithRegistryStorageMount()` | Legacy emptyDir mount at `/data` (registry server doesn't use it) |

**Remove these constants from `types.go`:**

```go
// DELETE:
RegistryDataVolumeName    = "registry-data"
RegistryDataMountPath     = "/data/registry"
gitAuthSecretsBasePath    = "/secrets"
```

**Remove legacy code paths from:**

| File | Change |
|------|--------|
| `manager.go` | Remove `reconcileLegacyPath()` method; remove `ensurePGPassSecret()` call; `ReconcileAPIService()` becomes the new path only |
| `deployment.go` | Remove `buildRegistryAPIDeploymentLegacyPath()` or equivalent; remove git auth iteration, legacy pgpass conditional |
| `mcpregistry_controller.go` | Remove legacy path branch; remove mutual exclusivity validation (only new path remains); remove legacy-specific status conditions |

**Remove the `ConfigManager` interface:**

The `ConfigManager` interface in `config/config.go` and its `configManager` implementation
are deleted. The `NewConfigManager()` constructor is deleted. All callers switch to the
`ToConfigMap()` function.

### Test Changes

**Delete legacy path tests:**
- All tests in `config/config_test.go`
- Legacy path integration tests that use typed `Registries[]`
- Legacy builder helpers in `test-integration/mcp-registry/registry_helpers.go` that
  construct typed MCPRegistry objects (e.g., `WithConfigMapSource()`, `WithGitSource()`,
  `WithAPISource()`, `WithPVCSource()`, `WithGitAuth()`, etc.)

**Keep / update:**
- New path tests (added in Phase 1)
- Integration tests that use `configYAML`
- Builder helpers that construct new-path MCPRegistry objects

### Example YAML Changes

**Delete legacy examples:**
- All examples in `examples/operator/mcp-registries/` that use `spec.registries[]`

**Keep:**
- Examples using `spec.configYAML` (added in Phase 1)

### CRD Manifest Changes

- Regenerate CRD manifests (`task operator-manifests`)
- Regenerate deepcopy (`task operator-generate`)
- Regenerate CRD API docs (`task crdref-gen`)
- Remove the XValidation CEL rule for mutual exclusivity (only one path remains)
- Add XValidation CEL rule: `size(self.spec.configYAML) > 0` (configYAML is required)

### Migration Notes for Users

Users migrating from the legacy path to `configYAML` need to:

1. Write their registry server config as a YAML string in `spec.configYAML`
2. Move ConfigMap/PVC/Secret references from typed fields to `spec.volumes[]` and
   `spec.volumeMounts[]`
3. If using `databaseConfig`, create a pgpass Secret manually and reference it via
   `spec.pgpassSecretRef`
4. Remove `spec.registries`, `spec.databaseConfig`, and `spec.authConfig`

The migration scenarios in the [Migration: Before → After](#migration-before--after-for-every-scenario)
section of this RFC provide exact before/after YAML for every use case.
