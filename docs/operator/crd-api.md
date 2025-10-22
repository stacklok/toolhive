# API Reference

## Packages
- [toolhive.stacklok.dev/v1alpha1](#toolhivestacklokdevv1alpha1)


## toolhive.stacklok.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the toolhive v1alpha1 API group

### Resource Types
- [MCPExternalAuthConfig](#mcpexternalauthconfig)
- [MCPExternalAuthConfigList](#mcpexternalauthconfiglist)
- [MCPGroup](#mcpgroup)
- [MCPGroupList](#mcpgrouplist)
- [MCPRegistry](#mcpregistry)
- [MCPRegistryList](#mcpregistrylist)
- [MCPRemoteProxy](#mcpremoteproxy)
- [MCPRemoteProxyList](#mcpremoteproxylist)
- [MCPServer](#mcpserver)
- [MCPServerList](#mcpserverlist)
- [MCPToolConfig](#mcptoolconfig)
- [MCPToolConfigList](#mcptoolconfiglist)



#### APIPhase

_Underlying type:_ _string_

APIPhase represents the API service state

_Validation:_
- Enum: [NotStarted Deploying Ready Unhealthy Error]

_Appears in:_
- [APIStatus](#apistatus)

| Field | Description |
| --- | --- |
| `NotStarted` | APIPhaseNotStarted means API deployment has not been created<br /> |
| `Deploying` | APIPhaseDeploying means API is being deployed<br /> |
| `Ready` | APIPhaseReady means API is ready to serve requests<br /> |
| `Unhealthy` | APIPhaseUnhealthy means API is deployed but not healthy<br /> |
| `Error` | APIPhaseError means API deployment failed<br /> |


#### APISource



APISource defines API source configuration for ToolHive Registry APIs
Phase 1: Supports ToolHive API endpoints (no pagination)
Phase 2: Will add support for upstream MCP Registry API with pagination



_Appears in:_
- [MCPRegistrySource](#mcpregistrysource)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint is the base API URL (without path)<br />The controller will append the appropriate paths:<br />Phase 1 (ToolHive API):<br />  - /v0/servers - List all servers (single response, no pagination)<br />  - /v0/servers/\{name\} - Get specific server (future)<br />  - /v0/info - Get registry metadata (future)<br />Example: "http://my-registry-api.default.svc.cluster.local/api" |  | MinLength: 1 <br />Pattern: `^https?://.*` <br />Required: \{\} <br /> |


#### APIStatus



APIStatus provides detailed information about the API service



_Appears in:_
- [MCPRegistryStatus](#mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[APIPhase](#apiphase)_ | Phase represents the current API service phase |  | Enum: [NotStarted Deploying Ready Unhealthy Error] <br /> |
| `message` _string_ | Message provides additional information about the API status |  |  |
| `endpoint` _string_ | Endpoint is the URL where the API is accessible |  |  |
| `readySince` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | ReadySince is the timestamp when the API became ready |  |  |


#### AuditConfig



AuditConfig defines audit logging configuration for the MCP server



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether audit logging is enabled<br />When true, enables audit logging with default configuration | false |  |


#### AuthzConfigRef



AuthzConfigRef defines a reference to authorization configuration



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of authorization configuration | configMap | Enum: [configMap inline] <br /> |
| `configMap` _[ConfigMapAuthzRef](#configmapauthzref)_ | ConfigMap references a ConfigMap containing authorization configuration<br />Only used when Type is "configMap" |  |  |
| `inline` _[InlineAuthzConfig](#inlineauthzconfig)_ | Inline contains direct authorization configuration<br />Only used when Type is "inline" |  |  |


#### ConfigMapAuthzRef



ConfigMapAuthzRef references a ConfigMap containing authorization configuration



_Appears in:_
- [AuthzConfigRef](#authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the authorization configuration | authz.json |  |


#### ConfigMapOIDCRef



ConfigMapOIDCRef references a ConfigMap containing OIDC configuration



_Appears in:_
- [OIDCConfigRef](#oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the OIDC configuration | oidc.json |  |


#### ConfigMapSource



ConfigMapSource defines ConfigMap source configuration



_Appears in:_
- [MCPRegistrySource](#mcpregistrysource)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | MinLength: 1 <br />Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the registry data | registry.json | MinLength: 1 <br /> |


#### EnvVar



EnvVar represents an environment variable in a container



_Appears in:_
- [MCPServerSpec](#mcpserverspec)
- [ProxyDeploymentOverrides](#proxydeploymentoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the environment variable |  | Required: \{\} <br /> |
| `value` _string_ | Value of the environment variable |  | Required: \{\} <br /> |


#### ExternalAuthConfigRef



ExternalAuthConfigRef defines a reference to a MCPExternalAuthConfig resource.
The referenced MCPExternalAuthConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPExternalAuthConfig resource |  | Required: \{\} <br /> |


#### GitSource



GitSource defines Git repository source configuration



_Appears in:_
- [MCPRegistrySource](#mcpregistrysource)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repository` _string_ | Repository is the Git repository URL (HTTP/HTTPS/SSH) |  | MinLength: 1 <br />Pattern: `^(file:///\|https?://\|git@\|ssh://\|git://).*` <br />Required: \{\} <br /> |
| `branch` _string_ | Branch is the Git branch to use (mutually exclusive with Tag and Commit) |  | MinLength: 1 <br /> |
| `tag` _string_ | Tag is the Git tag to use (mutually exclusive with Branch and Commit) |  | MinLength: 1 <br /> |
| `commit` _string_ | Commit is the Git commit SHA to use (mutually exclusive with Branch and Tag) |  | MinLength: 1 <br /> |
| `path` _string_ | Path is the path to the registry file within the repository | registry.json | Pattern: `^.*\.json$` <br /> |


#### InlineAuthzConfig



InlineAuthzConfig contains direct authorization configuration



_Appears in:_
- [AuthzConfigRef](#authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policies` _string array_ | Policies is a list of Cedar policy strings |  | MinItems: 1 <br />Required: \{\} <br /> |
| `entitiesJson` _string_ | EntitiesJSON is a JSON string representing Cedar entities | [] |  |


#### InlineOIDCConfig



InlineOIDCConfig contains direct OIDC configuration



_Appears in:_
- [OIDCConfigRef](#oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the OIDC issuer URL |  | Required: \{\} <br /> |
| `audience` _string_ | Audience is the expected audience for the token |  |  |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from |  |  |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint |  |  |
| `clientId` _string_ | ClientID is the OIDC client ID |  |  |
| `clientSecret` _string_ | ClientSecret is the client secret for introspection (optional) |  |  |
| `thvCABundlePath` _string_ | ThvCABundlePath is the path to CA certificate bundle file for HTTPS requests<br />The file must be mounted into the pod (e.g., via ConfigMap or Secret volume) |  |  |
| `jwksAuthTokenPath` _string_ | JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests<br />The file must be mounted into the pod (e.g., via Secret volume) |  |  |
| `jwksAllowPrivateIP` _boolean_ | JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses<br />Use with caution - only enable for trusted internal IDPs | false |  |


#### KubernetesOIDCConfig



KubernetesOIDCConfig configures OIDC for Kubernetes service account token validation



_Appears in:_
- [OIDCConfigRef](#oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceAccount` _string_ | ServiceAccount is the name of the service account to validate tokens for<br />If empty, uses the pod's service account |  |  |
| `namespace` _string_ | Namespace is the namespace of the service account<br />If empty, uses the MCPServer's namespace |  |  |
| `audience` _string_ | Audience is the expected audience for the token | toolhive |  |
| `issuer` _string_ | Issuer is the OIDC issuer URL | https://kubernetes.default.svc |  |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from<br />If empty, OIDC discovery will be used to automatically determine the JWKS URL |  |  |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint<br />If empty, OIDC discovery will be used to automatically determine the introspection URL |  |  |
| `useClusterAuth` _boolean_ | UseClusterAuth enables using the Kubernetes cluster's CA bundle and service account token<br />When true, uses /var/run/secrets/kubernetes.io/serviceaccount/ca.crt for TLS verification<br />and /var/run/secrets/kubernetes.io/serviceaccount/token for bearer token authentication<br />Defaults to true if not specified |  |  |


#### MCPExternalAuthConfig



MCPExternalAuthConfig is the Schema for the mcpexternalauthconfigs API.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [MCPExternalAuthConfigList](#mcpexternalauthconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPExternalAuthConfigSpec](#mcpexternalauthconfigspec)_ |  |  |  |
| `status` _[MCPExternalAuthConfigStatus](#mcpexternalauthconfigstatus)_ |  |  |  |


#### MCPExternalAuthConfigList



MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPExternalAuthConfig](#mcpexternalauthconfig) array_ |  |  |  |


#### MCPExternalAuthConfigSpec



MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [MCPExternalAuthConfig](#mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of external authentication to configure |  | Enum: [tokenExchange] <br />Required: \{\} <br /> |
| `tokenExchange` _[TokenExchangeConfig](#tokenexchangeconfig)_ | TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange<br />Only used when Type is "tokenExchange" |  |  |


#### MCPExternalAuthConfigStatus



MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig



_Appears in:_
- [MCPExternalAuthConfig](#mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.<br />It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server. |  |  |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  |  |
| `referencingServers` _string array_ | ReferencingServers is a list of MCPServer resources that reference this MCPExternalAuthConfig<br />This helps track which servers need to be reconciled when this config changes |  |  |


#### MCPGroup



MCPGroup is the Schema for the mcpgroups API



_Appears in:_
- [MCPGroupList](#mcpgrouplist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroup` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPGroupSpec](#mcpgroupspec)_ |  |  |  |
| `status` _[MCPGroupStatus](#mcpgroupstatus)_ |  |  |  |


#### MCPGroupList



MCPGroupList contains a list of MCPGroup





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroupList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPGroup](#mcpgroup) array_ |  |  |  |


#### MCPGroupPhase

_Underlying type:_ _string_

MCPGroupPhase represents the lifecycle phase of an MCPGroup

_Validation:_
- Enum: [Ready Pending Failed]

_Appears in:_
- [MCPGroupStatus](#mcpgroupstatus)

| Field | Description |
| --- | --- |
| `Ready` | MCPGroupPhaseReady indicates the MCPGroup is ready<br /> |
| `Pending` | MCPGroupPhasePending indicates the MCPGroup is pending<br /> |
| `Failed` | MCPGroupPhaseFailed indicates the MCPGroup has failed<br /> |


#### MCPGroupSpec



MCPGroupSpec defines the desired state of MCPGroup



_Appears in:_
- [MCPGroup](#mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `description` _string_ | Description provides human-readable context |  |  |


#### MCPGroupStatus



MCPGroupStatus defines observed state



_Appears in:_
- [MCPGroup](#mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[MCPGroupPhase](#mcpgroupphase)_ | Phase indicates current state | Pending | Enum: [Ready Pending Failed] <br /> |
| `servers` _string array_ | Servers lists server names in this group |  |  |
| `serverCount` _integer_ | ServerCount is the number of servers |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent observations |  |  |


#### MCPRegistry



MCPRegistry is the Schema for the mcpregistries API



_Appears in:_
- [MCPRegistryList](#mcpregistrylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPRegistrySpec](#mcpregistryspec)_ |  |  |  |
| `status` _[MCPRegistryStatus](#mcpregistrystatus)_ |  |  |  |


#### MCPRegistryList



MCPRegistryList contains a list of MCPRegistry





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPRegistry](#mcpregistry) array_ |  |  |  |


#### MCPRegistryPhase

_Underlying type:_ _string_

MCPRegistryPhase represents the phase of the MCPRegistry

_Validation:_
- Enum: [Pending Ready Failed Syncing Terminating]

_Appears in:_
- [MCPRegistryStatus](#mcpregistrystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRegistryPhasePending means the MCPRegistry is being initialized<br /> |
| `Ready` | MCPRegistryPhaseReady means the MCPRegistry is ready and operational<br /> |
| `Failed` | MCPRegistryPhaseFailed means the MCPRegistry has failed<br /> |
| `Syncing` | MCPRegistryPhaseSyncing means the MCPRegistry is currently syncing data<br /> |
| `Terminating` | MCPRegistryPhaseTerminating means the MCPRegistry is being deleted<br /> |


#### MCPRegistrySource



MCPRegistrySource defines the source configuration for registry data



_Appears in:_
- [MCPRegistrySpec](#mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of source (configmap, git, api) | configmap | Enum: [configmap git api] <br /> |
| `format` _string_ | Format is the data format (toolhive, upstream) | toolhive | Enum: [toolhive upstream] <br /> |
| `configmap` _[ConfigMapSource](#configmapsource)_ | ConfigMap defines the ConfigMap source configuration<br />Only used when Type is "configmap" |  |  |
| `git` _[GitSource](#gitsource)_ | Git defines the Git repository source configuration<br />Only used when Type is "git" |  |  |
| `api` _[APISource](#apisource)_ | API defines the API source configuration<br />Only used when Type is "api" |  |  |


#### MCPRegistrySpec



MCPRegistrySpec defines the desired state of MCPRegistry



_Appears in:_
- [MCPRegistry](#mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `displayName` _string_ | DisplayName is a human-readable name for the registry |  |  |
| `source` _[MCPRegistrySource](#mcpregistrysource)_ | Source defines the configuration for the registry data source |  | Required: \{\} <br /> |
| `syncPolicy` _[SyncPolicy](#syncpolicy)_ | SyncPolicy defines the automatic synchronization behavior for the registry.<br />If specified, enables automatic synchronization at the given interval.<br />Manual synchronization is always supported via annotation-based triggers<br />regardless of this setting. |  |  |
| `filter` _[RegistryFilter](#registryfilter)_ | Filter defines include/exclude patterns for registry content |  |  |
| `enforceServers` _boolean_ | EnforceServers indicates whether MCPServers in this namespace must have their images<br />present in at least one registry in the namespace. When any registry in the namespace<br />has this field set to true, enforcement is enabled for the entire namespace.<br />MCPServers with images not found in any registry will be rejected.<br />When false (default), MCPServers can be deployed regardless of registry presence. | false |  |


#### MCPRegistryStatus



MCPRegistryStatus defines the observed state of MCPRegistry



_Appears in:_
- [MCPRegistry](#mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[MCPRegistryPhase](#mcpregistryphase)_ | Phase represents the current overall phase of the MCPRegistry<br />Derived from sync and API status |  | Enum: [Pending Ready Failed Syncing Terminating] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |
| `syncStatus` _[SyncStatus](#syncstatus)_ | SyncStatus provides detailed information about data synchronization |  |  |
| `apiStatus` _[APIStatus](#apistatus)_ | APIStatus provides detailed information about the API service |  |  |
| `lastAppliedFilterHash` _string_ | LastAppliedFilterHash is the hash of the last applied filter |  |  |
| `storageRef` _[StorageReference](#storagereference)_ | StorageRef is a reference to the internal storage location |  |  |
| `lastManualSyncTrigger` _string_ | LastManualSyncTrigger tracks the last processed manual sync annotation value<br />Used to detect new manual sync requests via toolhive.stacklok.dev/sync-trigger annotation |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRegistry's state |  |  |


#### MCPRemoteProxy



MCPRemoteProxy is the Schema for the mcpremoteproxies API
It enables proxying remote MCP servers with authentication, authorization, audit logging, and tool filtering



_Appears in:_
- [MCPRemoteProxyList](#mcpremoteproxylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxy` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPRemoteProxySpec](#mcpremoteproxyspec)_ |  |  |  |
| `status` _[MCPRemoteProxyStatus](#mcpremoteproxystatus)_ |  |  |  |


#### MCPRemoteProxyList



MCPRemoteProxyList contains a list of MCPRemoteProxy





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxyList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPRemoteProxy](#mcpremoteproxy) array_ |  |  |  |


#### MCPRemoteProxyPhase

_Underlying type:_ _string_

MCPRemoteProxyPhase is a label for the condition of a MCPRemoteProxy at the current time

_Validation:_
- Enum: [Pending Ready Failed Terminating]

_Appears in:_
- [MCPRemoteProxyStatus](#mcpremoteproxystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRemoteProxyPhasePending means the proxy is being created<br /> |
| `Ready` | MCPRemoteProxyPhaseReady means the proxy is ready and operational<br /> |
| `Failed` | MCPRemoteProxyPhaseFailed means the proxy failed to start or encountered an error<br /> |
| `Terminating` | MCPRemoteProxyPhaseTerminating means the proxy is being deleted<br /> |


#### MCPRemoteProxySpec



MCPRemoteProxySpec defines the desired state of MCPRemoteProxy



_Appears in:_
- [MCPRemoteProxy](#mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `remoteURL` _string_ | RemoteURL is the URL of the remote MCP server to proxy |  | Pattern: `^https?://` <br />Required: \{\} <br /> |
| `port` _integer_ | Port is the port to expose the MCP proxy on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `transport` _string_ | Transport is the transport method for the remote proxy (sse or streamable-http) | streamable-http | Enum: [sse streamable-http] <br /> |
| `oidcConfig` _[OIDCConfigRef](#oidcconfigref)_ | OIDCConfig defines OIDC authentication configuration for the proxy<br />This validates incoming tokens from clients. Required for proxy mode. |  | Required: \{\} <br /> |
| `externalAuthConfigRef` _[ExternalAuthConfigRef](#externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.<br />When specified, the proxy will exchange validated incoming tokens for remote service tokens.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy. |  |  |
| `authzConfig` _[AuthzConfigRef](#authzconfigref)_ | AuthzConfig defines authorization policy configuration for the proxy |  |  |
| `audit` _[AuditConfig](#auditconfig)_ | Audit defines audit logging configuration for the proxy |  |  |
| `toolConfigRef` _[ToolConfigRef](#toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this allows filtering and overriding tools from the remote MCP server. |  |  |
| `telemetry` _[TelemetryConfig](#telemetryconfig)_ | Telemetry defines observability configuration for the proxy |  |  |
| `resources` _[ResourceRequirements](#resourcerequirements)_ | Resources defines the resource requirements for the proxy container |  |  |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false |  |
| `resourceOverrides` _[ResourceOverrides](#resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  |  |


#### MCPRemoteProxyStatus



MCPRemoteProxyStatus defines the observed state of MCPRemoteProxy



_Appears in:_
- [MCPRemoteProxy](#mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[MCPRemoteProxyPhase](#mcpremoteproxyphase)_ | Phase is the current phase of the MCPRemoteProxy |  | Enum: [Pending Ready Failed Terminating] <br /> |
| `url` _string_ | URL is the internal cluster URL where the proxy can be accessed |  |  |
| `externalURL` _string_ | ExternalURL is the external URL where the proxy can be accessed (if exposed externally) |  |  |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed MCPRemoteProxy |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRemoteProxy's state |  |  |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  |  |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  |  |
| `message` _string_ | Message provides additional information about the current phase |  |  |


#### MCPServer



MCPServer is the Schema for the mcpservers API



_Appears in:_
- [MCPServerList](#mcpserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPServerSpec](#mcpserverspec)_ |  |  |  |
| `status` _[MCPServerStatus](#mcpserverstatus)_ |  |  |  |


#### MCPServerList



MCPServerList contains a list of MCPServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPServer](#mcpserver) array_ |  |  |  |


#### MCPServerPhase

_Underlying type:_ _string_

MCPServerPhase is the phase of the MCPServer

_Validation:_
- Enum: [Pending Running Failed Terminating]

_Appears in:_
- [MCPServerStatus](#mcpserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPServerPhasePending means the MCPServer is being created<br /> |
| `Running` | MCPServerPhaseRunning means the MCPServer is running<br /> |
| `Failed` | MCPServerPhaseFailed means the MCPServer failed to start<br /> |
| `Terminating` | MCPServerPhaseTerminating means the MCPServer is being deleted<br /> |


#### MCPServerSpec



MCPServerSpec defines the desired state of MCPServer



_Appears in:_
- [MCPServer](#mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Image is the container image for the MCP server |  | Required: \{\} <br /> |
| `transport` _string_ | Transport is the transport method for the MCP server (stdio, streamable-http or sse) | stdio | Enum: [stdio streamable-http sse] <br /> |
| `proxyMode` _string_ | ProxyMode is the proxy mode for stdio transport (sse or streamable-http)<br />This setting is only used when Transport is "stdio" | sse | Enum: [sse streamable-http] <br /> |
| `port` _integer_ | Port is the port to expose the MCP server on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `targetPort` _integer_ | TargetPort is the port that MCP server listens to |  | Maximum: 65535 <br />Minimum: 1 <br /> |
| `args` _string array_ | Args are additional arguments to pass to the MCP server |  |  |
| `env` _[EnvVar](#envvar) array_ | Env are environment variables to set in the MCP server container |  |  |
| `volumes` _[Volume](#volume) array_ | Volumes are volumes to mount in the MCP server container |  |  |
| `resources` _[ResourceRequirements](#resourcerequirements)_ | Resources defines the resource requirements for the MCP server container |  |  |
| `secrets` _[SecretRef](#secretref) array_ | Secrets are references to secrets to mount in the MCP server container |  |  |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the MCP server.<br />If not specified, a ServiceAccount will be created automatically and used by the MCP server. |  |  |
| `permissionProfile` _[PermissionProfileRef](#permissionprofileref)_ | PermissionProfile defines the permission profile to use |  |  |
| `podTemplateSpec` _[PodTemplateSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#podtemplatespec-v1-core)_ | PodTemplateSpec defines the pod template to use for the MCP server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the MCP server runs in, you must specify<br />the `mcp` container name in the PodTemplateSpec. |  |  |
| `resourceOverrides` _[ResourceOverrides](#resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  |  |
| `oidcConfig` _[OIDCConfigRef](#oidcconfigref)_ | OIDCConfig defines OIDC authentication configuration for the MCP server |  |  |
| `authzConfig` _[AuthzConfigRef](#authzconfigref)_ | AuthzConfig defines authorization policy configuration for the MCP server |  |  |
| `audit` _[AuditConfig](#auditconfig)_ | Audit defines audit logging configuration for the MCP server |  |  |
| `tools` _string array_ | ToolsFilter is the filter on tools applied to the MCP server<br />Deprecated: Use ToolConfigRef instead |  |  |
| `toolConfigRef` _[ToolConfigRef](#toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this takes precedence over the inline ToolsFilter field. |  |  |
| `externalAuthConfigRef` _[ExternalAuthConfigRef](#externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for external authentication.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPServer. |  |  |
| `telemetry` _[TelemetryConfig](#telemetryconfig)_ | Telemetry defines observability configuration for the MCP server |  |  |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false |  |
| `groupRef` _string_ | GroupRef is the name of the MCPGroup this server belongs to<br />Must reference an existing MCPGroup in the same namespace |  |  |


#### MCPServerStatus



MCPServerStatus defines the observed state of MCPServer



_Appears in:_
- [MCPServer](#mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPServer's state |  |  |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  |  |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  |  |
| `url` _string_ | URL is the URL where the MCP server can be accessed |  |  |
| `phase` _[MCPServerPhase](#mcpserverphase)_ | Phase is the current phase of the MCPServer |  | Enum: [Pending Running Failed Terminating] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |


#### MCPToolConfig



MCPToolConfig is the Schema for the mcptoolconfigs API.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [MCPToolConfigList](#mcptoolconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MCPToolConfigSpec](#mcptoolconfigspec)_ |  |  |  |
| `status` _[MCPToolConfigStatus](#mcptoolconfigstatus)_ |  |  |  |


#### MCPToolConfigList



MCPToolConfigList contains a list of MCPToolConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[MCPToolConfig](#mcptoolconfig) array_ |  |  |  |


#### MCPToolConfigSpec



MCPToolConfigSpec defines the desired state of MCPToolConfig.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [MCPToolConfig](#mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `toolsFilter` _string array_ | ToolsFilter is a list of tool names to filter (allow list).<br />Only tools in this list will be exposed by the MCP server.<br />If empty, all tools are exposed. |  |  |
| `toolsOverride` _object (keys:string, values:[ToolOverride](#tooloverride))_ | ToolsOverride is a map from actual tool names to their overridden configuration.<br />This allows renaming tools and/or changing their descriptions. |  |  |


#### MCPToolConfigStatus



MCPToolConfigStatus defines the observed state of MCPToolConfig



_Appears in:_
- [MCPToolConfig](#mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPToolConfig.<br />It corresponds to the MCPToolConfig's generation, which is updated on mutation by the API Server. |  |  |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  |  |
| `referencingServers` _string array_ | ReferencingServers is a list of MCPServer resources that reference this MCPToolConfig<br />This helps track which servers need to be reconciled when this config changes |  |  |


#### NameFilter



NameFilter defines name-based filtering



_Appears in:_
- [RegistryFilter](#registryfilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `include` _string array_ | Include is a list of glob patterns to include |  |  |
| `exclude` _string array_ | Exclude is a list of glob patterns to exclude |  |  |


#### NetworkPermissions



NetworkPermissions defines the network permissions for an MCP server



_Appears in:_
- [PermissionProfileSpec](#permissionprofilespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode specifies the network mode for the container (e.g., "host", "bridge", "none")<br />When empty, the default container runtime network mode is used |  |  |
| `outbound` _[OutboundNetworkPermissions](#outboundnetworkpermissions)_ | Outbound defines the outbound network permissions |  |  |


#### OIDCConfigRef



OIDCConfigRef defines a reference to OIDC configuration



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of OIDC configuration | kubernetes | Enum: [kubernetes configMap inline] <br /> |
| `resourceUrl` _string_ | ResourceURL is the explicit resource URL for OAuth discovery endpoint (RFC 9728)<br />If not specified, defaults to the in-cluster Kubernetes service URL |  |  |
| `kubernetes` _[KubernetesOIDCConfig](#kubernetesoidcconfig)_ | Kubernetes configures OIDC for Kubernetes service account token validation<br />Only used when Type is "kubernetes" |  |  |
| `configMap` _[ConfigMapOIDCRef](#configmapoidcref)_ | ConfigMap references a ConfigMap containing OIDC configuration<br />Only used when Type is "configmap" |  |  |
| `inline` _[InlineOIDCConfig](#inlineoidcconfig)_ | Inline contains direct OIDC configuration<br />Only used when Type is "inline" |  |  |


#### OpenTelemetryConfig



OpenTelemetryConfig defines pure OpenTelemetry configuration



_Appears in:_
- [TelemetryConfig](#telemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OpenTelemetry is enabled | false |  |
| `endpoint` _string_ | Endpoint is the OTLP endpoint URL for tracing and metrics |  |  |
| `serviceName` _string_ | ServiceName is the service name for telemetry<br />If not specified, defaults to the MCPServer name |  |  |
| `headers` _string array_ | Headers contains authentication headers for the OTLP endpoint<br />Specified as key=value pairs |  |  |
| `insecure` _boolean_ | Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint | false |  |
| `metrics` _[OpenTelemetryMetricsConfig](#opentelemetrymetricsconfig)_ | Metrics defines OpenTelemetry metrics-specific configuration |  |  |
| `tracing` _[OpenTelemetryTracingConfig](#opentelemetrytracingconfig)_ | Tracing defines OpenTelemetry tracing configuration |  |  |


#### OpenTelemetryMetricsConfig



OpenTelemetryMetricsConfig defines OpenTelemetry metrics configuration



_Appears in:_
- [OpenTelemetryConfig](#opentelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP metrics are sent | false |  |


#### OpenTelemetryTracingConfig



OpenTelemetryTracingConfig defines OpenTelemetry tracing configuration



_Appears in:_
- [OpenTelemetryConfig](#opentelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP tracing is sent | false |  |
| `samplingRate` _string_ | SamplingRate is the trace sampling rate (0.0-1.0) | 0.05 |  |


#### OutboundNetworkPermissions



OutboundNetworkPermissions defines the outbound network permissions



_Appears in:_
- [NetworkPermissions](#networkpermissions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `insecureAllowAll` _boolean_ | InsecureAllowAll allows all outbound network connections (not recommended) | false |  |
| `allowHost` _string array_ | AllowHost is a list of hosts to allow connections to |  |  |
| `allowPort` _integer array_ | AllowPort is a list of ports to allow connections to |  |  |


#### PermissionProfileRef



PermissionProfileRef defines a reference to a permission profile



_Appears in:_
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of permission profile reference | builtin | Enum: [builtin configmap] <br /> |
| `name` _string_ | Name is the name of the permission profile<br />If Type is "builtin", Name must be one of: "none", "network"<br />If Type is "configmap", Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the permission profile<br />Only used when Type is "configmap" |  |  |




#### PrometheusConfig



PrometheusConfig defines Prometheus-specific configuration



_Appears in:_
- [TelemetryConfig](#telemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether Prometheus metrics endpoint is exposed | false |  |


#### ProxyDeploymentOverrides



ProxyDeploymentOverrides defines overrides specific to the proxy deployment



_Appears in:_
- [ResourceOverrides](#resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  |  |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  |  |
| `podTemplateMetadataOverrides` _[ResourceMetadataOverrides](#resourcemetadataoverrides)_ |  |  |  |
| `env` _[EnvVar](#envvar) array_ | Env are environment variables to set in the proxy container (thv run process)<br />These affect the toolhive proxy itself, not the MCP server it manages |  |  |


#### RegistryFilter



RegistryFilter defines include/exclude patterns for registry content



_Appears in:_
- [MCPRegistrySpec](#mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `names` _[NameFilter](#namefilter)_ | NameFilters defines name-based filtering |  |  |
| `tags` _[TagFilter](#tagfilter)_ | Tags defines tag-based filtering |  |  |


#### ResourceList



ResourceList is a set of (resource name, quantity) pairs



_Appears in:_
- [ResourceRequirements](#resourcerequirements)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cpu` _string_ | CPU is the CPU limit in cores (e.g., "500m" for 0.5 cores) |  |  |
| `memory` _string_ | Memory is the memory limit in bytes (e.g., "64Mi" for 64 megabytes) |  |  |


#### ResourceMetadataOverrides



ResourceMetadataOverrides defines metadata overrides for a resource



_Appears in:_
- [ProxyDeploymentOverrides](#proxydeploymentoverrides)
- [ResourceOverrides](#resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  |  |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  |  |


#### ResourceOverrides



ResourceOverrides defines overrides for annotations and labels on created resources



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `proxyDeployment` _[ProxyDeploymentOverrides](#proxydeploymentoverrides)_ | ProxyDeployment defines overrides for the Proxy Deployment resource (toolhive proxy) |  |  |
| `proxyService` _[ResourceMetadataOverrides](#resourcemetadataoverrides)_ | ProxyService defines overrides for the Proxy Service resource (points to the proxy deployment) |  |  |


#### ResourceRequirements



ResourceRequirements describes the compute resource requirements



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `limits` _[ResourceList](#resourcelist)_ | Limits describes the maximum amount of compute resources allowed |  |  |
| `requests` _[ResourceList](#resourcelist)_ | Requests describes the minimum amount of compute resources required |  |  |


#### SecretKeyRef



SecretKeyRef is a reference to a key within a Secret



_Appears in:_
- [TokenExchangeConfig](#tokenexchangeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key within the secret |  | Required: \{\} <br /> |


#### SecretRef



SecretRef is a reference to a secret



_Appears in:_
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the secret itself |  | Required: \{\} <br /> |
| `targetEnvName` _string_ | TargetEnvName is the environment variable to be used when setting up the secret in the MCP server<br />If left unspecified, it defaults to the key |  |  |


#### StorageReference



StorageReference defines a reference to internal storage



_Appears in:_
- [MCPRegistryStatus](#mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the storage type (configmap) |  | Enum: [configmap] <br /> |
| `configMapRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#localobjectreference-v1-core)_ | ConfigMapRef is a reference to a ConfigMap storage<br />Only used when Type is "configmap" |  |  |


#### SyncPhase

_Underlying type:_ _string_

SyncPhase represents the data synchronization state

_Validation:_
- Enum: [Syncing Complete Failed]

_Appears in:_
- [SyncStatus](#syncstatus)

| Field | Description |
| --- | --- |
| `Syncing` | SyncPhaseSyncing means sync is currently in progress<br /> |
| `Complete` | SyncPhaseComplete means sync completed successfully<br /> |
| `Failed` | SyncPhaseFailed means sync failed<br /> |


#### SyncPolicy



SyncPolicy defines automatic synchronization behavior.
When specified, enables automatic synchronization at the given interval.
Manual synchronization via annotation-based triggers is always available
regardless of this policy setting.



_Appears in:_
- [MCPRegistrySpec](#mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interval` _string_ | Interval is the sync interval for automatic synchronization (Go duration format)<br />Examples: "1h", "30m", "24h" |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Required: \{\} <br /> |


#### SyncStatus



SyncStatus provides detailed information about data synchronization



_Appears in:_
- [MCPRegistryStatus](#mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[SyncPhase](#syncphase)_ | Phase represents the current synchronization phase |  | Enum: [Syncing Complete Failed] <br /> |
| `message` _string_ | Message provides additional information about the sync status |  |  |
| `lastAttempt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | LastAttempt is the timestamp of the last sync attempt |  |  |
| `attemptCount` _integer_ | AttemptCount is the number of sync attempts since last success |  | Minimum: 0 <br /> |
| `lastSyncTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | LastSyncTime is the timestamp of the last successful sync |  |  |
| `lastSyncHash` _string_ | LastSyncHash is the hash of the last successfully synced data<br />Used to detect changes in source data |  |  |
| `serverCount` _integer_ | ServerCount is the total number of servers in the registry |  | Minimum: 0 <br /> |


#### TagFilter



TagFilter defines tag-based filtering



_Appears in:_
- [RegistryFilter](#registryfilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `include` _string array_ | Include is a list of tags to include |  |  |
| `exclude` _string array_ | Exclude is a list of tags to exclude |  |  |


#### TelemetryConfig



TelemetryConfig defines observability configuration for the MCP server



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `openTelemetry` _[OpenTelemetryConfig](#opentelemetryconfig)_ | OpenTelemetry defines OpenTelemetry configuration |  |  |
| `prometheus` _[PrometheusConfig](#prometheusconfig)_ | Prometheus defines Prometheus-specific configuration |  |  |


#### TokenExchangeConfig



TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
This configuration is used to exchange incoming authentication tokens for tokens
that can be used with external services.
The structure matches the tokenexchange.Config from pkg/auth/tokenexchange/middleware.go



_Appears in:_
- [MCPExternalAuthConfigSpec](#mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenUrl` _string_ | TokenURL is the OAuth 2.0 token endpoint URL for token exchange |  | Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier |  | Required: \{\} <br /> |
| `clientSecretRef` _[SecretKeyRef](#secretkeyref)_ | ClientSecretRef is a reference to a secret containing the OAuth 2.0 client secret |  | Required: \{\} <br /> |
| `audience` _string_ | Audience is the target audience for the exchanged token |  | Required: \{\} <br /> |
| `scopes` _string array_ | Scopes is a list of OAuth 2.0 scopes to request for the exchanged token |  |  |
| `externalTokenHeaderName` _string_ | ExternalTokenHeaderName is the name of the custom header to use for the exchanged token.<br />If set, the exchanged token will be added to this custom header (e.g., "X-Upstream-Token").<br />If empty or not set, the exchanged token will replace the Authorization header (default behavior). |  |  |


#### ToolConfigRef



ToolConfigRef defines a reference to a MCPToolConfig resource.
The referenced MCPToolConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [MCPRemoteProxySpec](#mcpremoteproxyspec)
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPToolConfig resource in the same namespace |  | Required: \{\} <br /> |


#### ToolOverride



ToolOverride represents a tool override configuration.
Both Name and Description can be overridden independently, but
they can't be both empty.



_Appears in:_
- [MCPToolConfigSpec](#mcptoolconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the redefined name of the tool |  |  |
| `description` _string_ | Description is the redefined description of the tool |  |  |


#### Volume



Volume represents a volume to mount in a container



_Appears in:_
- [MCPServerSpec](#mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the volume |  | Required: \{\} <br /> |
| `hostPath` _string_ | HostPath is the path on the host to mount |  | Required: \{\} <br /> |
| `mountPath` _string_ | MountPath is the path in the container to mount to |  | Required: \{\} <br /> |
| `readOnly` _boolean_ | ReadOnly specifies whether the volume should be mounted read-only | false |  |


