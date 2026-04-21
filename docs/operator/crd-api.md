# API Reference

## Packages
- [toolhive.stacklok.dev/audit](#toolhivestacklokdevaudit)
- [toolhive.stacklok.dev/authtypes](#toolhivestacklokdevauthtypes)
- [toolhive.stacklok.dev/config](#toolhivestacklokdevconfig)
- [toolhive.stacklok.dev/telemetry](#toolhivestacklokdevtelemetry)
- [toolhive.stacklok.dev/v1alpha1](#toolhivestacklokdevv1alpha1)
- [toolhive.stacklok.dev/v1beta1](#toolhivestacklokdevv1beta1)


## toolhive.stacklok.dev/audit








#### pkg.audit.Config



Config represents the audit logging configuration.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether audit logging is enabled.<br />When true, enables audit logging with the configured options. | false | Optional: \{\} <br /> |
| `component` _string_ | Component is the component name to use in audit events. |  | Optional: \{\} <br /> |
| `eventTypes` _string array_ | EventTypes specifies which event types to audit. If empty, all events are audited. |  | Optional: \{\} <br /> |
| `excludeEventTypes` _string array_ | ExcludeEventTypes specifies which event types to exclude from auditing.<br />This takes precedence over EventTypes. |  | Optional: \{\} <br /> |
| `includeRequestData` _boolean_ | IncludeRequestData determines whether to include request data in audit logs. | false | Optional: \{\} <br /> |
| `includeResponseData` _boolean_ | IncludeResponseData determines whether to include response data in audit logs. | false | Optional: \{\} <br /> |
| `detectApplicationErrors` _boolean_ | DetectApplicationErrors controls whether the audit middleware inspects<br />JSON-RPC response bodies for application-level errors when the HTTP<br />status code indicates success (2xx). When enabled, a small prefix of<br />the response body is buffered to detect JSON-RPC error fields,<br />independent of the IncludeResponseData setting. | true | Optional: \{\} <br /> |
| `maxDataSize` _integer_ | MaxDataSize limits the size of request/response data included in audit logs (in bytes). | 1024 | Optional: \{\} <br /> |
| `logFile` _string_ | LogFile specifies the file path for audit logs. If empty, logs to stdout. |  | Optional: \{\} <br /> |













## toolhive.stacklok.dev/authtypes


#### auth.types.BackendAuthStrategy



BackendAuthStrategy defines how to authenticate to a specific backend.

This struct provides type-safe configuration for different authentication strategies
using HeaderInjection or TokenExchange fields based on the Type field.



_Appears in:_
- [vmcp.config.OutgoingAuthConfig](#vmcpconfigoutgoingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange", "upstream_inject" |  |  |
| `headerInjection` _[auth.types.HeaderInjectionConfig](#authtypesheaderinjectionconfig)_ | HeaderInjection contains configuration for header injection auth strategy.<br />Used when Type = "header_injection". |  |  |
| `tokenExchange` _[auth.types.TokenExchangeConfig](#authtypestokenexchangeconfig)_ | TokenExchange contains configuration for token exchange auth strategy.<br />Used when Type = "token_exchange". |  |  |
| `upstreamInject` _[auth.types.UpstreamInjectConfig](#authtypesupstreaminjectconfig)_ | UpstreamInject contains configuration for upstream inject auth strategy.<br />Used when Type = "upstream_inject". |  |  |


#### auth.types.HeaderInjectionConfig



HeaderInjectionConfig configures the header injection auth strategy.
This strategy injects a static or environment-sourced header value into requests.



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `headerName` _string_ | HeaderName is the name of the header to inject (e.g., "Authorization"). |  |  |
| `headerValue` _string_ | HeaderValue is the static header value to inject.<br />Either HeaderValue or HeaderValueEnv should be set, not both. |  |  |
| `headerValueEnv` _string_ | HeaderValueEnv is the environment variable name containing the header value.<br />The value will be resolved at runtime from this environment variable.<br />Either HeaderValue or HeaderValueEnv should be set, not both. |  |  |


#### auth.types.TokenExchangeConfig



TokenExchangeConfig configures the OAuth 2.0 token exchange auth strategy.
This strategy exchanges incoming tokens for backend-specific tokens using RFC 8693.



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenUrl` _string_ | TokenURL is the OAuth token endpoint URL for token exchange. |  |  |
| `clientId` _string_ | ClientID is the OAuth client ID for the token exchange request. |  |  |
| `clientSecret` _string_ | ClientSecret is the OAuth client secret (use ClientSecretEnv for security). |  |  |
| `clientSecretEnv` _string_ | ClientSecretEnv is the environment variable name containing the client secret.<br />The value will be resolved at runtime from this environment variable. |  |  |
| `audience` _string_ | Audience is the target audience for the exchanged token. |  |  |
| `scopes` _string array_ | Scopes are the requested scopes for the exchanged token. |  |  |
| `subjectTokenType` _string_ | SubjectTokenType is the token type of the incoming subject token.<br />Defaults to "urn:ietf:params:oauth:token-type:access_token" if not specified. |  |  |
| `subjectProviderName` _string_ | SubjectProviderName selects which upstream provider's token to use as the<br />subject token. When set, the token is looked up from Identity.UpstreamTokens<br />instead of using Identity.Token.<br />When left empty and an embedded authorization server is configured, the system<br />automatically populates this field with the first configured upstream provider name.<br />Set it explicitly to override that default or to select a specific provider when<br />multiple upstreams are configured. |  |  |


#### auth.types.UpstreamInjectConfig



UpstreamInjectConfig configures the upstream inject auth strategy.
This strategy uses the embedded authorization server to obtain and inject
upstream IDP tokens into backend requests.



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `providerName` _string_ | ProviderName is the name of the upstream provider configured in the<br />embedded authorization server. Must match an entry in AuthServer.Upstreams. |  |  |



## toolhive.stacklok.dev/config


#### vmcp.config.AggregationConfig



AggregationConfig defines tool aggregation, filtering, and conflict resolution strategies.

Tool Visibility vs Routing:
  - ExcludeAllTools, per-workload ExcludeAll, and Filter control which tools are
    advertised to MCP clients (visible in tools/list responses).
  - ALL backend tools remain available in the internal routing table, allowing
    composite tools to call hidden backend tools.
  - This enables curated experiences where raw backend tools are hidden from
    MCP clients but accessible through composite tool workflows.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conflictResolution` _[pkg.vmcp.ConflictResolutionStrategy](#pkgvmcpconflictresolutionstrategy)_ | ConflictResolution defines the strategy for resolving tool name conflicts.<br />- prefix: Automatically prefix tool names with workload identifier<br />- priority: First workload in priority order wins<br />- manual: Explicitly define overrides for all conflicts | prefix | Enum: [prefix priority manual] <br />Optional: \{\} <br /> |
| `conflictResolutionConfig` _[vmcp.config.ConflictResolutionConfig](#vmcpconfigconflictresolutionconfig)_ | ConflictResolutionConfig provides configuration for the chosen strategy. |  | Optional: \{\} <br /> |
| `tools` _[vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig) array_ | Tools defines per-workload tool filtering and overrides. |  | Optional: \{\} <br /> |
| `excludeAllTools` _boolean_ | ExcludeAllTools hides all backend tools from MCP clients when true.<br />Hidden tools are NOT advertised in tools/list responses, but they ARE<br />available in the routing table for composite tools to use.<br />This enables the use case where you want to hide raw backend tools from<br />direct client access while exposing curated composite tool workflows. |  | Optional: \{\} <br /> |


#### vmcp.config.AuthzConfig



AuthzConfig configures authorization.



_Appears in:_
- [vmcp.config.IncomingAuthConfig](#vmcpconfigincomingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the authz type: "cedar", "none" |  |  |
| `policies` _string array_ | Policies contains Cedar policy definitions (when Type = "cedar"). |  |  |
| `primaryUpstreamProvider` _string_ | PrimaryUpstreamProvider names the upstream IDP provider whose access<br />token should be used as the source of JWT claims for Cedar evaluation.<br />When empty, claims from the ToolHive-issued token are used.<br />Must match an upstream provider name configured in the embedded auth server<br />(e.g. "default", "github"). Only relevant when the embedded auth server is active. |  | Optional: \{\} <br /> |


#### vmcp.config.CircuitBreakerConfig



CircuitBreakerConfig configures circuit breaker behavior.



_Appears in:_
- [vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether circuit breaker is enabled. | false | Optional: \{\} <br /> |
| `failureThreshold` _integer_ | FailureThreshold is the number of failures before opening the circuit.<br />Must be >= 1. | 5 | Minimum: 1 <br />Optional: \{\} <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the duration to wait before attempting to close the circuit.<br />Must be >= 1s to prevent thrashing. | 60s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |


#### vmcp.config.CompositeToolConfig



CompositeToolConfig defines a composite tool workflow.
This matches the YAML structure from the proposal (lines 173-255).



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)
- [api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the workflow name (unique identifier). |  |  |
| `description` _string_ | Description describes what the workflow does. |  |  |
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  | Optional: \{\} <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum workflow execution time. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `steps` _[vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig) array_ | Steps are the workflow steps to execute. |  |  |
| `output` _[vmcp.config.OutputConfig](#vmcpconfigoutputconfig)_ | Output defines the structured output schema for this workflow.<br />If not specified, the workflow returns the last step's output (backward compatible). |  | Optional: \{\} <br /> |


#### vmcp.config.CompositeToolRef



CompositeToolRef defines a reference to a VirtualMCPCompositeToolDefinition resource.
The referenced resource must be in the same namespace as the VirtualMCPServer.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the VirtualMCPCompositeToolDefinition resource in the same namespace. |  | Required: \{\} <br /> |


#### vmcp.config.Config



Config is the unified configuration model for Virtual MCP Server.
This is platform-agnostic and used by both CLI and Kubernetes deployments.

Platform-specific adapters (CLI YAML loader, Kubernetes CRD converter)
transform their native formats into this model.

_Validation:_
- Type: object

_Appears in:_
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the virtual MCP server name. |  | Optional: \{\} <br /> |
| `groupRef` _string_ | Group references an existing MCPGroup that defines backend workloads.<br />In standalone CLI mode, this is set from the YAML config file.<br />In Kubernetes, the operator populates this from spec.groupRef during conversion. |  | Optional: \{\} <br /> |
| `backends` _[vmcp.config.StaticBackendConfig](#vmcpconfigstaticbackendconfig) array_ | Backends defines pre-configured backend servers for static mode.<br />When OutgoingAuth.Source is "inline", this field contains the full list of backend<br />servers with their URLs and transport types, eliminating the need for K8s API access.<br />When OutgoingAuth.Source is "discovered", this field is empty and backends are<br />discovered at runtime via Kubernetes API. |  | Optional: \{\} <br /> |
| `incomingAuth` _[vmcp.config.IncomingAuthConfig](#vmcpconfigincomingauthconfig)_ | IncomingAuth configures how clients authenticate to the virtual MCP server.<br />When using the Kubernetes operator, this is populated by the converter from<br />VirtualMCPServerSpec.IncomingAuth and any values set here will be superseded. |  | Optional: \{\} <br /> |
| `outgoingAuth` _[vmcp.config.OutgoingAuthConfig](#vmcpconfigoutgoingauthconfig)_ | OutgoingAuth configures how the virtual MCP server authenticates to backends.<br />When using the Kubernetes operator, this is populated by the converter from<br />VirtualMCPServerSpec.OutgoingAuth and any values set here will be superseded. |  | Optional: \{\} <br /> |
| `aggregation` _[vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)_ | Aggregation defines tool aggregation and conflict resolution strategies.<br />Supports ToolConfigRef for Kubernetes-native MCPToolConfig resource references. |  | Optional: \{\} <br /> |
| `compositeTools` _[vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig) array_ | CompositeTools defines inline composite tool workflows.<br />Full workflow definitions are embedded in the configuration.<br />For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs. |  | Optional: \{\} <br /> |
| `compositeToolRefs` _[vmcp.config.CompositeToolRef](#vmcpconfigcompositetoolref) array_ | CompositeToolRefs references VirtualMCPCompositeToolDefinition resources<br />for complex, reusable workflows. Only applicable when running in Kubernetes.<br />Referenced resources must be in the same namespace as the VirtualMCPServer. |  | Optional: \{\} <br /> |
| `operational` _[vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)_ | Operational configures operational settings. |  |  |
| `metadata` _object (keys:string, values:string)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `telemetry` _[pkg.telemetry.Config](#pkgtelemetryconfig)_ | Telemetry configures OpenTelemetry-based observability for the Virtual MCP server<br />including distributed tracing, OTLP metrics export, and Prometheus metrics endpoint.<br />Deprecated (Kubernetes operator only): When deploying via the operator, use<br />VirtualMCPServer.spec.telemetryConfigRef to reference a shared MCPTelemetryConfig<br />resource instead. This field remains valid for standalone (non-operator) deployments. |  | Optional: \{\} <br /> |
| `audit` _[pkg.audit.Config](#pkgauditconfig)_ | Audit configures audit logging for the Virtual MCP server.<br />When present, audit logs include MCP protocol operations.<br />See audit.Config for available configuration options. |  | Optional: \{\} <br /> |
| `optimizer` _[vmcp.config.OptimizerConfig](#vmcpconfigoptimizerconfig)_ | Optimizer configures the MCP optimizer for context optimization on large toolsets.<br />When enabled, vMCP exposes only find_tool and call_tool operations to clients<br />instead of all backend tools directly. This reduces token usage by allowing<br />LLMs to discover relevant tools on demand rather than receiving all tool definitions. |  | Optional: \{\} <br /> |
| `sessionStorage` _[vmcp.config.SessionStorageConfig](#vmcpconfigsessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When provider is "redis", the operator injects Redis connection parameters<br />(address, db, keyPrefix) here. The Redis password is provided separately via<br />the THV_SESSION_REDIS_PASSWORD environment variable. |  | Optional: \{\} <br /> |


#### vmcp.config.ConflictResolutionConfig



ConflictResolutionConfig provides configuration for conflict resolution strategies.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `prefixFormat` _string_ | PrefixFormat defines the prefix format for the "prefix" strategy.<br />Supports placeholders: \{workload\}, \{workload\}_, \{workload\}. | \{workload\}_ | Optional: \{\} <br /> |
| `priorityOrder` _string array_ | PriorityOrder defines the workload priority order for the "priority" strategy. |  | Optional: \{\} <br /> |






#### vmcp.config.ElicitationResponseConfig



ElicitationResponseConfig defines how to handle user responses to elicitation requests.



_Appears in:_
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `action` _string_ | Action defines the action to take when the user declines or cancels<br />- skip_remaining: Skip remaining steps in the workflow<br />- abort: Abort the entire workflow execution<br />- continue: Continue to the next step | abort | Enum: [skip_remaining abort continue] <br />Optional: \{\} <br /> |


#### vmcp.config.FailureHandlingConfig



FailureHandlingConfig configures failure handling behavior.



_Appears in:_
- [vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `healthCheckInterval` _[vmcp.config.Duration](#vmcpconfigduration)_ | HealthCheckInterval is the interval between health checks. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `unhealthyThreshold` _integer_ | UnhealthyThreshold is the number of consecutive failures before marking unhealthy. | 3 | Optional: \{\} <br /> |
| `healthCheckTimeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | HealthCheckTimeout is the maximum duration for a single health check operation.<br />Should be less than HealthCheckInterval to prevent checks from queuing up. | 10s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `statusReportingInterval` _[vmcp.config.Duration](#vmcpconfigduration)_ | StatusReportingInterval is the interval for reporting status updates to Kubernetes.<br />This controls how often the vMCP runtime reports backend health and phase changes.<br />Lower values provide faster status updates but increase API server load. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `partialFailureMode` _string_ | PartialFailureMode defines behavior when some backends are unavailable.<br />- fail: Fail entire request if any backend is unavailable<br />- best_effort: Continue with available backends | fail | Enum: [fail best_effort] <br />Optional: \{\} <br /> |
| `circuitBreaker` _[vmcp.config.CircuitBreakerConfig](#vmcpconfigcircuitbreakerconfig)_ | CircuitBreaker configures circuit breaker behavior. |  | Optional: \{\} <br /> |


#### vmcp.config.IncomingAuthConfig



IncomingAuthConfig configures client authentication to the virtual MCP server.

Note: When using the Kubernetes operator (VirtualMCPServer CRD), the
VirtualMCPServerSpec.IncomingAuth field is the authoritative source for
authentication configuration. The operator's converter will resolve the CRD's
IncomingAuth (which supports Kubernetes-native references like SecretKeyRef,
ConfigMapRef, etc.) and populate this IncomingAuthConfig with the resolved values.
Any values set here directly will be superseded by the CRD configuration.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the auth type: "oidc", "local", "anonymous" |  |  |
| `oidc` _[vmcp.config.OIDCConfig](#vmcpconfigoidcconfig)_ | OIDC contains OIDC configuration (when Type = "oidc"). |  |  |
| `authz` _[vmcp.config.AuthzConfig](#vmcpconfigauthzconfig)_ | Authz contains authorization configuration (optional). |  |  |




#### vmcp.config.OIDCConfig



OIDCConfig configures OpenID Connect authentication.



_Appears in:_
- [vmcp.config.IncomingAuthConfig](#vmcpconfigincomingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the OIDC issuer URL. |  | Pattern: `^https?://` <br /> |
| `clientId` _string_ | ClientID is the OAuth client ID. |  |  |
| `clientSecretEnv` _string_ | ClientSecretEnv is the name of the environment variable containing the client secret.<br />This is the secure way to reference secrets - the actual secret value is never stored<br />in configuration files, only the environment variable name.<br />The secret value will be resolved from this environment variable at runtime. |  |  |
| `audience` _string_ | Audience is the required token audience. |  |  |
| `resource` _string_ | Resource is the OAuth 2.0 resource indicator (RFC 8707).<br />Used in WWW-Authenticate header and OAuth discovery metadata (RFC 9728).<br />If not specified, defaults to Audience. |  |  |
| `jwksUrl` _string_ | JWKSURL is the explicit JWKS endpoint URL.<br />When set, skips OIDC discovery and fetches the JWKS directly from this URL.<br />This is useful when the OIDC issuer does not serve a /.well-known/openid-configuration. |  | Optional: \{\} <br /> |
| `introspectionUrl` _string_ | IntrospectionURL is the token introspection endpoint URL (RFC 7662).<br />When set, enables token introspection for opaque (non-JWT) tokens. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the required OAuth scopes. |  |  |
| `protectedResourceAllowPrivateIp` _boolean_ | ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses<br />Use with caution - only enable for trusted internal IDPs or testing |  |  |
| `jwksAllowPrivateIp` _boolean_ | JwksAllowPrivateIP allows OIDC discovery and JWKS fetches to private IP addresses.<br />Enable when the embedded auth server runs on a loopback address and<br />the OIDC middleware needs to fetch its JWKS from that address.<br />Use with caution - only enable for trusted internal IDPs or testing. |  |  |
| `insecureAllowHttp` _boolean_ | InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing<br />WARNING: This is insecure and should NEVER be used in production |  |  |


#### vmcp.config.OperationalConfig



OperationalConfig contains operational settings.
OperationalConfig defines operational settings like timeouts and health checks.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `logLevel` _string_ | LogLevel sets the logging level for the Virtual MCP server.<br />The only valid value is "debug" to enable debug logging.<br />When omitted or empty, the server uses info level logging. |  | Enum: [debug] <br />Optional: \{\} <br /> |
| `timeouts` _[vmcp.config.TimeoutConfig](#vmcpconfigtimeoutconfig)_ | Timeouts configures timeout settings. |  | Optional: \{\} <br /> |
| `failureHandling` _[vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)_ | FailureHandling configures failure handling behavior. |  | Optional: \{\} <br /> |


#### vmcp.config.OptimizerConfig



OptimizerConfig configures the MCP optimizer.
When enabled, vMCP exposes only find_tool and call_tool operations to clients
instead of all backend tools directly.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `embeddingService` _string_ | EmbeddingService is the full base URL of the embedding service endpoint<br />(e.g., http://my-embedding.default.svc.cluster.local:8080) for semantic<br />tool discovery.<br />In a Kubernetes environment, it is more convenient to use the<br />VirtualMCPServerSpec.EmbeddingServerRef field instead of setting this<br />directly. EmbeddingServerRef references an EmbeddingServer CRD by name,<br />and the operator automatically resolves the referenced resource's<br />Status.URL to populate this field. This provides managed lifecycle<br />(the operator watches the EmbeddingServer for readiness and URL changes)<br />and avoids hardcoding service URLs in the config. If both<br />EmbeddingServerRef and this field are set, EmbeddingServerRef takes<br />precedence and this value is overridden with a warning. |  | Optional: \{\} <br /> |
| `embeddingServiceTimeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | EmbeddingServiceTimeout is the HTTP request timeout for calls to the embedding service.<br />Defaults to 30s if not specified. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `maxToolsToReturn` _integer_ | MaxToolsToReturn is the maximum number of tool results returned by a search query.<br />Defaults to 8 if not specified or zero. |  | Maximum: 50 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `hybridSearchSemanticRatio` _string_ | HybridSearchSemanticRatio controls the balance between semantic (meaning-based)<br />and keyword search results. 0.0 = all keyword, 1.0 = all semantic.<br />Defaults to "0.5" if not specified or empty.<br />Serialized as a string because CRDs do not support float types portably. |  | Pattern: `^([0-9]*[.])?[0-9]+$` <br />Optional: \{\} <br /> |
| `semanticDistanceThreshold` _string_ | SemanticDistanceThreshold is the maximum distance for semantic search results.<br />Results exceeding this threshold are filtered out from semantic search.<br />This threshold does not apply to keyword search.<br />Range: 0 = identical, 2 = completely unrelated.<br />Defaults to "1.0" if not specified or empty.<br />Serialized as a string because CRDs do not support float types portably. |  | Pattern: `^([0-9]*[.])?[0-9]+$` <br />Optional: \{\} <br /> |


#### vmcp.config.OutgoingAuthConfig



OutgoingAuthConfig configures backend authentication.

Note: When using the Kubernetes operator (VirtualMCPServer CRD), the
VirtualMCPServerSpec.OutgoingAuth field is the authoritative source for
backend authentication configuration. The operator's converter will resolve
the CRD's OutgoingAuth (which supports Kubernetes-native references like
SecretKeyRef, ConfigMapRef, etc.) and populate this OutgoingAuthConfig with
the resolved values. Any values set here directly will be superseded by the
CRD configuration.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _string_ | Source defines how to discover backend auth: "inline", "discovered"<br />- inline: Explicit configuration in OutgoingAuth<br />- discovered: Auto-discover from backend MCPServer.externalAuthConfigRef (Kubernetes only) |  |  |
| `default` _[auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)_ | Default is the default auth strategy for backends without explicit config. |  |  |
| `backends` _object (keys:string, values:[auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy))_ | Backends contains per-backend auth configuration. |  |  |


#### vmcp.config.OutputConfig



OutputConfig defines the structured output schema for a composite tool workflow.
This follows the same pattern as the Parameters field, defining both the
MCP output schema (type, description) and runtime value construction (value, default).



_Appears in:_
- [vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig)
- [api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `properties` _object (keys:string, values:[vmcp.config.OutputProperty](#vmcpconfigoutputproperty))_ | Properties defines the output properties.<br />Map key is the property name, value is the property definition. |  |  |
| `required` _string array_ | Required lists property names that must be present in the output. |  | Optional: \{\} <br /> |


#### vmcp.config.OutputProperty



OutputProperty defines a single output property.
For non-object types, Value is required.
For object types, either Value or Properties must be specified (but not both).



_Appears in:_
- [vmcp.config.OutputConfig](#vmcpconfigoutputconfig)
- [vmcp.config.OutputProperty](#vmcpconfigoutputproperty)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the JSON Schema type: "string", "integer", "number", "boolean", "object", "array" |  | Enum: [string integer number boolean object array] <br />Required: \{\} <br /> |
| `description` _string_ | Description is a human-readable description exposed to clients and models |  | Optional: \{\} <br /> |
| `value` _string_ | Value is a template string for constructing the runtime value.<br />For object types, this can be a JSON string that will be deserialized.<br />Supports template syntax: \{\{.steps.step_id.output.field\}\}, \{\{.params.param_name\}\} |  | Optional: \{\} <br /> |
| `properties` _object (keys:string, values:[vmcp.config.OutputProperty](#vmcpconfigoutputproperty))_ | Properties defines nested properties for object types.<br />Each nested property has full metadata (type, description, value/properties). |  | Schemaless: \{\} <br />Type: object <br />Optional: \{\} <br /> |
| `default` _[pkg.json.Any](#pkgjsonany)_ | Default is the fallback value if template expansion fails.<br />Type coercion is applied to match the declared Type. |  | Schemaless: \{\} <br />Optional: \{\} <br /> |


#### vmcp.config.SessionStorageConfig



SessionStorageConfig configures session storage for stateful horizontal scaling.
The Redis password is not stored here; it is injected as the THV_SESSION_REDIS_PASSWORD
environment variable by the operator when spec.sessionStorage.passwordRef is set.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `provider` _string_ | Provider is the session storage backend type. |  | Enum: [memory redis] <br />Required: \{\} <br /> |
| `address` _string_ | Address is the Redis server address (required when provider is redis). |  | Optional: \{\} <br /> |
| `db` _integer_ | DB is the Redis database number. | 0 | Minimum: 0 <br />Optional: \{\} <br /> |
| `keyPrefix` _string_ | KeyPrefix is an optional prefix for all Redis keys used by ToolHive. |  | Optional: \{\} <br /> |


#### vmcp.config.StaticBackendConfig



StaticBackendConfig defines a pre-configured backend server for static mode.
This allows vMCP to operate without Kubernetes API access by embedding all backend
information directly in the configuration.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the backend identifier.<br />Must match the backend name from the MCPGroup for auth config resolution. |  | Required: \{\} <br /> |
| `url` _string_ | URL is the backend's MCP server base URL. |  | Pattern: `^https?://` <br />Required: \{\} <br /> |
| `transport` _string_ | Transport is the MCP transport protocol: "sse" or "streamable-http"<br />Only network transports supported by vMCP client are allowed. |  | Enum: [sse streamable-http] <br />Required: \{\} <br /> |
| `type` _string_ | Type is the backend workload type: "entry" for MCPServerEntry backends, or empty<br />for container/proxy backends. Entry backends connect directly to remote MCP servers. |  | Enum: [entry ] <br />Optional: \{\} <br /> |
| `caBundlePath` _string_ | CABundlePath is the file path to a custom CA certificate bundle for TLS verification.<br />Only valid when Type is "entry". The operator mounts CA bundles at<br />/etc/toolhive/ca-bundles/<name>/ca.crt. |  | Optional: \{\} <br /> |
| `metadata` _object (keys:string, values:string)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |


#### vmcp.config.StepErrorHandling



StepErrorHandling defines error handling behavior for workflow steps.



_Appears in:_
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `action` _string_ | Action defines the action to take on error | abort | Enum: [abort continue retry] <br />Optional: \{\} <br /> |
| `retryCount` _integer_ | RetryCount is the maximum number of retries<br />Only used when Action is "retry" |  | Optional: \{\} <br /> |
| `retryDelay` _[vmcp.config.Duration](#vmcpconfigduration)_ | RetryDelay is the delay between retry attempts<br />Only used when Action is "retry" |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |


#### vmcp.config.TimeoutConfig



TimeoutConfig configures timeout settings.



_Appears in:_
- [vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `default` _[vmcp.config.Duration](#vmcpconfigduration)_ | Default is the default timeout for backend requests. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `perWorkload` _object (keys:string, values:[vmcp.config.Duration](#vmcpconfigduration))_ | PerWorkload defines per-workload timeout overrides. |  | Optional: \{\} <br /> |


#### vmcp.config.ToolAnnotationsOverride

_Underlying type:_ _[vmcp.config.struct{Title *string "json:\"title,omitempty\" yaml:\"title,omitempty\""; ReadOnlyHint *bool "json:\"readOnlyHint,omitempty\" yaml:\"readOnlyHint,omitempty\""; DestructiveHint *bool "json:\"destructiveHint,omitempty\" yaml:\"destructiveHint,omitempty\""; IdempotentHint *bool "json:\"idempotentHint,omitempty\" yaml:\"idempotentHint,omitempty\""; OpenWorldHint *bool "json:\"openWorldHint,omitempty\" yaml:\"openWorldHint,omitempty\""}](#vmcpconfigstruct{title *string "json:\"title,omitempty\" yaml:\"title,omitempty\""; readonlyhint *bool "json:\"readonlyhint,omitempty\" yaml:\"readonlyhint,omitempty\""; destructivehint *bool "json:\"destructivehint,omitempty\" yaml:\"destructivehint,omitempty\""; idempotenthint *bool "json:\"idempotenthint,omitempty\" yaml:\"idempotenthint,omitempty\""; openworldhint *bool "json:\"openworldhint,omitempty\" yaml:\"openworldhint,omitempty\""})_

ToolAnnotationsOverride defines overrides for tool annotation fields.
All fields use pointers so nil means "don't override" while zero values
(empty string, false) mean "explicitly set to this value."



_Appears in:_
- [vmcp.config.ToolOverride](#vmcpconfigtooloverride)



#### vmcp.config.ToolConfigRef



ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.
Only used when running in Kubernetes with the operator.



_Appears in:_
- [vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPToolConfig resource in the same namespace. |  | Required: \{\} <br /> |


#### vmcp.config.ToolOverride



ToolOverride defines tool name, description, and annotation overrides.



_Appears in:_
- [vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the new tool name (for renaming). |  | Optional: \{\} <br /> |
| `description` _string_ | Description is the new tool description. |  | Optional: \{\} <br /> |
| `annotations` _[vmcp.config.ToolAnnotationsOverride](#vmcpconfigtoolannotationsoverride)_ | Annotations overrides specific tool annotation fields.<br />Only specified fields are overridden; others pass through from the backend. |  | Optional: \{\} <br /> |




#### vmcp.config.WorkflowStepConfig



WorkflowStepConfig defines a single workflow step.
This matches the proposal's step configuration (lines 180-255).



_Appears in:_
- [vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig)
- [api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `id` _string_ | ID is the unique identifier for this step. |  | Required: \{\} <br /> |
| `type` _string_ | Type is the step type (tool, elicitation, etc.) | tool | Enum: [tool elicitation forEach] <br />Optional: \{\} <br /> |
| `tool` _string_ | Tool is the tool to call (format: "workload.tool_name")<br />Only used when Type is "tool" |  | Optional: \{\} <br /> |
| `arguments` _[pkg.json.Map](#pkgjsonmap)_ | Arguments is a map of argument values with template expansion support.<br />Supports Go template syntax with .params and .steps for string values.<br />Non-string values (integers, booleans, arrays, objects) are passed as-is.<br />Note: the templating is only supported on the first level of the key-value pairs. |  | Type: object <br />Optional: \{\} <br /> |
| `condition` _string_ | Condition is a template expression that determines if the step should execute |  | Optional: \{\} <br /> |
| `dependsOn` _string array_ | DependsOn lists step IDs that must complete before this step |  | Optional: \{\} <br /> |
| `onError` _[vmcp.config.StepErrorHandling](#vmcpconfigsteperrorhandling)_ | OnError defines error handling behavior |  | Optional: \{\} <br /> |
| `message` _string_ | Message is the elicitation message<br />Only used when Type is "elicitation" |  | Optional: \{\} <br /> |
| `schema` _[pkg.json.Map](#pkgjsonmap)_ | Schema defines the expected response schema for elicitation |  | Type: object <br />Optional: \{\} <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum execution time for this step |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |
| `onDecline` _[vmcp.config.ElicitationResponseConfig](#vmcpconfigelicitationresponseconfig)_ | OnDecline defines the action to take when the user explicitly declines the elicitation<br />Only used when Type is "elicitation" |  | Optional: \{\} <br /> |
| `onCancel` _[vmcp.config.ElicitationResponseConfig](#vmcpconfigelicitationresponseconfig)_ | OnCancel defines the action to take when the user cancels/dismisses the elicitation<br />Only used when Type is "elicitation" |  | Optional: \{\} <br /> |
| `defaultResults` _[pkg.json.Map](#pkgjsonmap)_ | DefaultResults provides fallback output values when this step is skipped<br />(due to condition evaluating to false) or fails (when onError.action is "continue").<br />Each key corresponds to an output field name referenced by downstream steps.<br />Required if the step may be skipped AND downstream steps reference this step's output. |  | Schemaless: \{\} <br />Optional: \{\} <br /> |
| `collection` _string_ | Collection is a Go template expression that resolves to a JSON array or a slice.<br />Only used when Type is "forEach". |  | Optional: \{\} <br /> |
| `itemVar` _string_ | ItemVar is the variable name used to reference the current item in forEach templates.<br />Defaults to "item" if not specified.<br />Only used when Type is "forEach". |  | Optional: \{\} <br /> |
| `maxParallel` _integer_ | MaxParallel limits the number of concurrent iterations in a forEach step.<br />Defaults to the DAG executor's maxParallel (10).<br />Only used when Type is "forEach". |  | Optional: \{\} <br /> |
| `maxIterations` _integer_ | MaxIterations limits the number of items that can be iterated over.<br />Defaults to 100, hard cap at 1000.<br />Only used when Type is "forEach". |  | Optional: \{\} <br /> |
| `step` _[vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)_ | InnerStep defines the step to execute for each item in the collection.<br />Only used when Type is "forEach". Only tool-type inner steps are supported. |  | Type: object <br />Optional: \{\} <br /> |


#### vmcp.config.WorkloadToolConfig



WorkloadToolConfig defines tool filtering and overrides for a specific workload.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `workload` _string_ | Workload is the name of the backend MCPServer workload. |  | Required: \{\} <br /> |
| `toolConfigRef` _[vmcp.config.ToolConfigRef](#vmcpconfigtoolconfigref)_ | ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.<br />If specified, Filter and Overrides are ignored.<br />Only used when running in Kubernetes with the operator. |  | Optional: \{\} <br /> |
| `filter` _string array_ | Filter is an allow-list of tool names to advertise to MCP clients.<br />Tools NOT in this list are hidden from clients (not in tools/list response)<br />but remain available in the routing table for composite tools to use.<br />This enables selective exposure of backend tools while allowing composite<br />workflows to orchestrate all backend capabilities.<br />Only used if ToolConfigRef is not specified. |  | Optional: \{\} <br /> |
| `overrides` _object (keys:string, values:[vmcp.config.ToolOverride](#vmcpconfigtooloverride))_ | Overrides is an inline map of tool overrides for renaming and description changes.<br />Overrides are applied to tools before conflict resolution and affect both<br />advertising and routing (the overridden name is used everywhere).<br />Only used if ToolConfigRef is not specified. |  | Optional: \{\} <br /> |
| `excludeAll` _boolean_ | ExcludeAll hides all tools from this workload from MCP clients when true.<br />Hidden tools are NOT advertised in tools/list responses, but they ARE<br />available in the routing table for composite tools to use.<br />This enables the use case where you want to hide raw backend tools from<br />direct client access while exposing curated composite tool workflows. |  | Optional: \{\} <br /> |





## toolhive.stacklok.dev/telemetry


#### pkg.telemetry.Config



Config holds the configuration for OpenTelemetry instrumentation.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint is the OTLP endpoint URL |  | Optional: \{\} <br /> |
| `serviceName` _string_ | ServiceName is the service name for telemetry.<br />When omitted, defaults to the server name (e.g., VirtualMCPServer name). |  | Optional: \{\} <br /> |
| `serviceVersion` _string_ | ServiceVersion is the service version for telemetry.<br />When omitted, defaults to the ToolHive version. |  | Optional: \{\} <br /> |
| `tracingEnabled` _boolean_ | TracingEnabled controls whether distributed tracing is enabled.<br />When false, no tracer provider is created even if an endpoint is configured. | false | Optional: \{\} <br /> |
| `metricsEnabled` _boolean_ | MetricsEnabled controls whether OTLP metrics are enabled.<br />When false, OTLP metrics are not sent even if an endpoint is configured.<br />This is independent of EnablePrometheusMetricsPath. | false | Optional: \{\} <br /> |
| `samplingRate` _string_ | SamplingRate is the trace sampling rate (0.0-1.0) as a string.<br />Only used when TracingEnabled is true.<br />Example: "0.05" for 5% sampling. | 0.05 | Optional: \{\} <br /> |
| `headers` _object (keys:string, values:string)_ | Headers contains authentication headers for the OTLP endpoint. |  | Optional: \{\} <br /> |
| `insecure` _boolean_ | Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint. | false | Optional: \{\} <br /> |
| `enablePrometheusMetricsPath` _boolean_ | EnablePrometheusMetricsPath controls whether to expose Prometheus-style /metrics endpoint.<br />The metrics are served on the main transport port at /metrics.<br />This is separate from OTLP metrics which are sent to the Endpoint. | false | Optional: \{\} <br /> |
| `environmentVariables` _string array_ | EnvironmentVariables is a list of environment variable names that should be<br />included in telemetry spans as attributes. Only variables in this list will<br />be read from the host machine and included in spans for observability.<br />Example: ["NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"] |  | Optional: \{\} <br /> |
| `customAttributes` _object (keys:string, values:string)_ | CustomAttributes contains custom resource attributes to be added to all telemetry signals.<br />These are parsed from CLI flags (--otel-custom-attributes) or environment variables<br />(OTEL_RESOURCE_ATTRIBUTES) as key=value pairs. |  | Optional: \{\} <br /> |
| `useLegacyAttributes` _boolean_ | UseLegacyAttributes controls whether legacy (pre-MCP OTEL semconv) attribute names<br />are emitted alongside the new standard attribute names. When true, spans include both<br />old and new attribute names for backward compatibility with existing dashboards.<br />Currently defaults to true; this will change to false in a future release. | true | Optional: \{\} <br /> |
| `caCertPath` _string_ | CACertPath is the file path to a CA certificate bundle for the OTLP endpoint.<br />When set, the OTLP exporters use this CA to verify the collector's TLS certificate<br />instead of relying solely on the system CA pool. |  | Optional: \{\} <br /> |













## toolhive.stacklok.dev/v1alpha1
### Resource Types
- [api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver)
- [api.v1alpha1.EmbeddingServerList](#apiv1alpha1embeddingserverlist)
- [api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig)
- [api.v1alpha1.MCPExternalAuthConfigList](#apiv1alpha1mcpexternalauthconfiglist)
- [api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup)
- [api.v1alpha1.MCPGroupList](#apiv1alpha1mcpgrouplist)
- [api.v1alpha1.MCPOIDCConfig](#apiv1alpha1mcpoidcconfig)
- [api.v1alpha1.MCPOIDCConfigList](#apiv1alpha1mcpoidcconfiglist)
- [api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry)
- [api.v1alpha1.MCPRegistryList](#apiv1alpha1mcpregistrylist)
- [api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy)
- [api.v1alpha1.MCPRemoteProxyList](#apiv1alpha1mcpremoteproxylist)
- [api.v1alpha1.MCPServer](#apiv1alpha1mcpserver)
- [api.v1alpha1.MCPServerEntry](#apiv1alpha1mcpserverentry)
- [api.v1alpha1.MCPServerEntryList](#apiv1alpha1mcpserverentrylist)
- [api.v1alpha1.MCPServerList](#apiv1alpha1mcpserverlist)
- [api.v1alpha1.MCPTelemetryConfig](#apiv1alpha1mcptelemetryconfig)
- [api.v1alpha1.MCPTelemetryConfigList](#apiv1alpha1mcptelemetryconfiglist)
- [api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig)
- [api.v1alpha1.MCPToolConfigList](#apiv1alpha1mcptoolconfiglist)
- [api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition)
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionList](#apiv1alpha1virtualmcpcompositetooldefinitionlist)
- [api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver)
- [api.v1alpha1.VirtualMCPServerList](#apiv1alpha1virtualmcpserverlist)




















































## toolhive.stacklok.dev/v1beta1
### Resource Types
- [api.v1beta1.EmbeddingServer](#apiv1beta1embeddingserver)
- [api.v1beta1.EmbeddingServerList](#apiv1beta1embeddingserverlist)
- [api.v1beta1.MCPExternalAuthConfig](#apiv1beta1mcpexternalauthconfig)
- [api.v1beta1.MCPExternalAuthConfigList](#apiv1beta1mcpexternalauthconfiglist)
- [api.v1beta1.MCPGroup](#apiv1beta1mcpgroup)
- [api.v1beta1.MCPGroupList](#apiv1beta1mcpgrouplist)
- [api.v1beta1.MCPOIDCConfig](#apiv1beta1mcpoidcconfig)
- [api.v1beta1.MCPOIDCConfigList](#apiv1beta1mcpoidcconfiglist)
- [api.v1beta1.MCPRegistry](#apiv1beta1mcpregistry)
- [api.v1beta1.MCPRegistryList](#apiv1beta1mcpregistrylist)
- [api.v1beta1.MCPRemoteProxy](#apiv1beta1mcpremoteproxy)
- [api.v1beta1.MCPRemoteProxyList](#apiv1beta1mcpremoteproxylist)
- [api.v1beta1.MCPServer](#apiv1beta1mcpserver)
- [api.v1beta1.MCPServerEntry](#apiv1beta1mcpserverentry)
- [api.v1beta1.MCPServerEntryList](#apiv1beta1mcpserverentrylist)
- [api.v1beta1.MCPServerList](#apiv1beta1mcpserverlist)
- [api.v1beta1.MCPTelemetryConfig](#apiv1beta1mcptelemetryconfig)
- [api.v1beta1.MCPTelemetryConfigList](#apiv1beta1mcptelemetryconfiglist)
- [api.v1beta1.MCPToolConfig](#apiv1beta1mcptoolconfig)
- [api.v1beta1.MCPToolConfigList](#apiv1beta1mcptoolconfiglist)
- [api.v1beta1.VirtualMCPCompositeToolDefinition](#apiv1beta1virtualmcpcompositetooldefinition)
- [api.v1beta1.VirtualMCPCompositeToolDefinitionList](#apiv1beta1virtualmcpcompositetooldefinitionlist)
- [api.v1beta1.VirtualMCPServer](#apiv1beta1virtualmcpserver)
- [api.v1beta1.VirtualMCPServerList](#apiv1beta1virtualmcpserverlist)



#### api.v1beta1.AWSStsConfig



AWSStsConfig holds configuration for AWS STS authentication with SigV4 request signing.
This configuration exchanges incoming authentication tokens (typically OIDC JWT) for AWS STS
temporary credentials, then signs requests to AWS services using SigV4.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `region` _string_ | Region is the AWS region for the STS endpoint and service (e.g., "us-east-1", "eu-west-1") |  | MinLength: 1 <br />Pattern: `^[a-z]\{2\}(-[a-z]+)+-\d+$` <br />Required: \{\} <br /> |
| `service` _string_ | Service is the AWS service name for SigV4 signing<br />Defaults to "aws-mcp" for AWS MCP Server endpoints | aws-mcp | Optional: \{\} <br /> |
| `fallbackRoleArn` _string_ | FallbackRoleArn is the IAM role ARN to assume when no role mappings match<br />Used as the default role when RoleMappings is empty or no mapping matches<br />At least one of FallbackRoleArn or RoleMappings must be configured (enforced by webhook) |  | Pattern: `^arn:(aws\|aws-cn\|aws-us-gov):iam::\d\{12\}:role/[\w+=,.@\-_/]+$` <br />Optional: \{\} <br /> |
| `roleMappings` _[api.v1beta1.RoleMapping](#apiv1beta1rolemapping) array_ | RoleMappings defines claim-based role selection rules<br />Allows mapping JWT claims (e.g., groups, roles) to specific IAM roles<br />Lower priority values are evaluated first (higher priority) |  | Optional: \{\} <br /> |
| `roleClaim` _string_ | RoleClaim is the JWT claim to use for role mapping evaluation<br />Defaults to "groups" to match common OIDC group claims | groups | Optional: \{\} <br /> |
| `sessionDuration` _integer_ | SessionDuration is the duration in seconds for the STS session<br />Must be between 900 (15 minutes) and 43200 (12 hours)<br />Defaults to 3600 (1 hour) if not specified | 3600 | Maximum: 43200 <br />Minimum: 900 <br />Optional: \{\} <br /> |
| `sessionNameClaim` _string_ | SessionNameClaim is the JWT claim to use for role session name<br />Defaults to "sub" to use the subject claim | sub | Optional: \{\} <br /> |


#### api.v1beta1.AuditConfig



AuditConfig defines audit logging configuration for the MCP server



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether audit logging is enabled<br />When true, enables audit logging with default configuration | false | Optional: \{\} <br /> |


#### api.v1beta1.AuthServerRef



AuthServerRef defines a reference to a resource that configures an embedded
OAuth 2.0/OIDC authorization server. Currently only MCPExternalAuthConfig is supported;
the enum will be extended when a dedicated auth server CRD is introduced.



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind identifies the type of the referenced resource. | MCPExternalAuthConfig | Enum: [MCPExternalAuthConfig] <br /> |
| `name` _string_ | Name is the name of the referenced resource in the same namespace. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.AuthServerStorageConfig



AuthServerStorageConfig configures the storage backend for the embedded auth server.



_Appears in:_
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[api.v1beta1.AuthServerStorageType](#apiv1beta1authserverstoragetype)_ | Type specifies the storage backend type.<br />Valid values: "memory" (default), "redis". | memory | Enum: [memory redis] <br /> |
| `redis` _[api.v1beta1.RedisStorageConfig](#apiv1beta1redisstorageconfig)_ | Redis configures the Redis storage backend.<br />Required when type is "redis". |  | Optional: \{\} <br /> |


#### api.v1beta1.AuthServerStorageType

_Underlying type:_ _string_

AuthServerStorageType represents the type of storage backend for the embedded auth server



_Appears in:_
- [api.v1beta1.AuthServerStorageConfig](#apiv1beta1authserverstorageconfig)

| Field | Description |
| --- | --- |
| `memory` | AuthServerStorageTypeMemory is the in-memory storage backend (default)<br /> |
| `redis` | AuthServerStorageTypeRedis is the Redis storage backend<br /> |


#### api.v1beta1.AuthzConfigRef



AuthzConfigRef defines a reference to authorization configuration



_Appears in:_
- [api.v1beta1.IncomingAuthConfig](#apiv1beta1incomingauthconfig)
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of authorization configuration | configMap | Enum: [configMap inline] <br /> |
| `configMap` _[api.v1beta1.ConfigMapAuthzRef](#apiv1beta1configmapauthzref)_ | ConfigMap references a ConfigMap containing authorization configuration<br />Only used when Type is "configMap" |  | Optional: \{\} <br /> |
| `inline` _[api.v1beta1.InlineAuthzConfig](#apiv1beta1inlineauthzconfig)_ | Inline contains direct authorization configuration<br />Only used when Type is "inline" |  | Optional: \{\} <br /> |


#### api.v1beta1.BackendAuthConfig



BackendAuthConfig defines authentication configuration for a backend MCPServer



_Appears in:_
- [api.v1beta1.OutgoingAuthConfig](#apiv1beta1outgoingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type defines the authentication type |  | Enum: [discovered externalAuthConfigRef] <br />Required: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references an MCPExternalAuthConfig resource<br />Only used when Type is "externalAuthConfigRef" |  | Optional: \{\} <br /> |


#### api.v1beta1.BearerTokenConfig



BearerTokenConfig holds configuration for bearer token authentication.
This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets.
For security reasons, only secret references are supported (no plaintext values).



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | TokenSecretRef references a Kubernetes Secret containing the bearer token |  | Required: \{\} <br /> |


#### api.v1beta1.CABundleSource



CABundleSource defines a source for CA certificate bundles.



_Appears in:_
- [api.v1beta1.InlineOIDCSharedConfig](#apiv1beta1inlineoidcsharedconfig)
- [api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)
- [api.v1beta1.MCPTelemetryOTelConfig](#apiv1beta1mcptelemetryotelconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `configMapRef` _[ConfigMapKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#configmapkeyselector-v1-core)_ | ConfigMapRef references a ConfigMap containing the CA certificate bundle.<br />If Key is not specified, it defaults to "ca.crt". |  | Optional: \{\} <br /> |


#### api.v1beta1.ConfigMapAuthzRef



ConfigMapAuthzRef references a ConfigMap containing authorization configuration



_Appears in:_
- [api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the authorization configuration | authz.json | Optional: \{\} <br /> |




#### api.v1beta1.EmbeddedAuthServerConfig



EmbeddedAuthServerConfig holds configuration for the embedded OAuth2/OIDC authorization server.
This enables running an authorization server that delegates authentication to upstream IDPs.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the issuer identifier for this authorization server.<br />This will be included in the "iss" claim of issued tokens.<br />Must be a valid HTTPS URL (or HTTP for localhost) without query, fragment, or trailing slash (per RFC 8414). |  | Pattern: `^https?://[^\s?#]+[^/\s?#]$` <br />Required: \{\} <br /> |
| `authorizationEndpointBaseUrl` _string_ | AuthorizationEndpointBaseURL overrides the base URL used for the authorization_endpoint<br />in the OAuth discovery document. When set, the discovery document will advertise<br />`\{authorizationEndpointBaseUrl\}/oauth/authorize` instead of `\{issuer\}/oauth/authorize`.<br />All other endpoints (token, registration, JWKS) remain derived from the issuer.<br />This is useful when the browser-facing authorization endpoint needs to be on a<br />different host than the issuer used for backend-to-backend calls.<br />Must be a valid HTTPS URL (or HTTP for localhost) without query, fragment, or trailing slash. |  | Pattern: `^https?://[^\s?#]+[^/\s?#]$` <br />Optional: \{\} <br /> |
| `signingKeySecretRefs` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref) array_ | SigningKeySecretRefs references Kubernetes Secrets containing signing keys for JWT operations.<br />Supports key rotation by allowing multiple keys (oldest keys are used for verification only).<br />If not specified, an ephemeral signing key will be auto-generated (development only -<br />JWTs will be invalid after restart). |  | MaxItems: 5 <br />Optional: \{\} <br /> |
| `hmacSecretRefs` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref) array_ | HMACSecretRefs references Kubernetes Secrets containing symmetric secrets for signing<br />authorization codes and refresh tokens (opaque tokens).<br />Current secret must be at least 32 bytes and cryptographically random.<br />Supports secret rotation via multiple entries (first is current, rest are for verification).<br />If not specified, an ephemeral secret will be auto-generated (development only -<br />auth codes and refresh tokens will be invalid after restart). |  | Optional: \{\} <br /> |
| `tokenLifespans` _[api.v1beta1.TokenLifespanConfig](#apiv1beta1tokenlifespanconfig)_ | TokenLifespans configures the duration that various tokens are valid.<br />If not specified, defaults are applied (access: 1h, refresh: 7d, authCode: 10m). |  | Optional: \{\} <br /> |
| `upstreamProviders` _[api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig) array_ | UpstreamProviders configures connections to upstream Identity Providers.<br />The embedded auth server delegates authentication to these providers.<br />MCPServer and MCPRemoteProxy support a single upstream; VirtualMCPServer supports multiple. |  | MinItems: 1 <br />Required: \{\} <br /> |
| `storage` _[api.v1beta1.AuthServerStorageConfig](#apiv1beta1authserverstorageconfig)_ | Storage configures the storage backend for the embedded auth server.<br />If not specified, defaults to in-memory storage. |  | Optional: \{\} <br /> |


#### api.v1beta1.EmbeddingResourceOverrides



EmbeddingResourceOverrides defines overrides for annotations and labels on created resources



_Appears in:_
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `statefulSet` _[api.v1beta1.EmbeddingStatefulSetOverrides](#apiv1beta1embeddingstatefulsetoverrides)_ | StatefulSet defines overrides for the StatefulSet resource |  | Optional: \{\} <br /> |
| `service` _[api.v1beta1.ResourceMetadataOverrides](#apiv1beta1resourcemetadataoverrides)_ | Service defines overrides for the Service resource |  | Optional: \{\} <br /> |
| `persistentVolumeClaim` _[api.v1beta1.ResourceMetadataOverrides](#apiv1beta1resourcemetadataoverrides)_ | PersistentVolumeClaim defines overrides for the PVC resource |  | Optional: \{\} <br /> |


#### api.v1beta1.EmbeddingServer



EmbeddingServer is the Schema for the embeddingservers API



_Appears in:_
- [api.v1beta1.EmbeddingServerList](#apiv1beta1embeddingserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `EmbeddingServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)_ |  |  |  |
| `status` _[api.v1beta1.EmbeddingServerStatus](#apiv1beta1embeddingserverstatus)_ |  |  |  |


#### api.v1beta1.EmbeddingServerList



EmbeddingServerList contains a list of EmbeddingServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `EmbeddingServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.EmbeddingServer](#apiv1beta1embeddingserver) array_ |  |  |  |


#### api.v1beta1.EmbeddingServerPhase

_Underlying type:_ _string_

EmbeddingServerPhase is the phase of the EmbeddingServer

_Validation:_
- Enum: [Pending Downloading Ready Failed Terminating]

_Appears in:_
- [api.v1beta1.EmbeddingServerStatus](#apiv1beta1embeddingserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | EmbeddingServerPhasePending means the EmbeddingServer is being created<br /> |
| `Downloading` | EmbeddingServerPhaseDownloading means the model is being downloaded<br /> |
| `Ready` | EmbeddingServerPhaseReady means the EmbeddingServer is ready<br /> |
| `Failed` | EmbeddingServerPhaseFailed means the EmbeddingServer failed to start<br /> |
| `Terminating` | EmbeddingServerPhaseTerminating means the EmbeddingServer is being deleted<br /> |


#### api.v1beta1.EmbeddingServerRef



EmbeddingServerRef references an existing EmbeddingServer resource by name.
This follows the same pattern as ExternalAuthConfigRef and ToolConfigRef.



_Appears in:_
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the EmbeddingServer resource |  | Required: \{\} <br /> |


#### api.v1beta1.EmbeddingServerSpec



EmbeddingServerSpec defines the desired state of EmbeddingServer



_Appears in:_
- [api.v1beta1.EmbeddingServer](#apiv1beta1embeddingserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `model` _string_ | Model is the HuggingFace embedding model to use (e.g., "sentence-transformers/all-MiniLM-L6-v2") | BAAI/bge-small-en-v1.5 | Optional: \{\} <br /> |
| `hfTokenSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | HFTokenSecretRef is a reference to a Kubernetes Secret containing the huggingface token.<br />If provided, the secret value will be provided to the embedding server for authentication with huggingface. |  | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image for the embedding inference server.<br />Images must be from HuggingFace Text Embeddings Inference (https://github.com/huggingface/text-embeddings-inference). | ghcr.io/huggingface/text-embeddings-inference:cpu-latest | Optional: \{\} <br /> |
| `imagePullPolicy` _string_ | ImagePullPolicy defines the pull policy for the container image | IfNotPresent | Enum: [Always Never IfNotPresent] <br />Optional: \{\} <br /> |
| `port` _integer_ | Port is the port to expose the embedding service on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `args` _string array_ | Args are additional arguments to pass to the embedding inference server |  | Optional: \{\} <br /> |
| `env` _[api.v1beta1.EnvVar](#apiv1beta1envvar) array_ | Env are environment variables to set in the container |  | Optional: \{\} <br /> |
| `resources` _[api.v1beta1.ResourceRequirements](#apiv1beta1resourcerequirements)_ | Resources defines compute resources for the embedding server |  | Optional: \{\} <br /> |
| `modelCache` _[api.v1beta1.ModelCacheConfig](#apiv1beta1modelcacheconfig)_ | ModelCache configures persistent storage for downloaded models<br />When enabled, models are cached in a PVC and reused across pod restarts |  | Optional: \{\} <br /> |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec allows customizing the pod (node selection, tolerations, etc.)<br />This field accepts a PodTemplateSpec object as JSON/YAML.<br />Note that to modify the specific container the embedding server runs in, you must specify<br />the 'embedding' container name in the PodTemplateSpec. |  | Type: object <br />Optional: \{\} <br /> |
| `resourceOverrides` _[api.v1beta1.EmbeddingResourceOverrides](#apiv1beta1embeddingresourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  | Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the number of embedding server replicas to run | 1 | Minimum: 1 <br />Optional: \{\} <br /> |


#### api.v1beta1.EmbeddingServerStatus



EmbeddingServerStatus defines the observed state of EmbeddingServer



_Appears in:_
- [api.v1beta1.EmbeddingServer](#apiv1beta1embeddingserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the EmbeddingServer's state |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.EmbeddingServerPhase](#apiv1beta1embeddingserverphase)_ | Phase is the current phase of the EmbeddingServer |  | Enum: [Pending Downloading Ready Failed Terminating] <br />Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the current phase |  | Optional: \{\} <br /> |
| `url` _string_ | URL is the URL where the embedding service can be accessed |  | Optional: \{\} <br /> |
| `readyReplicas` _integer_ | ReadyReplicas is the number of ready replicas |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller |  | Optional: \{\} <br /> |


#### api.v1beta1.EmbeddingStatefulSetOverrides



EmbeddingStatefulSetOverrides defines overrides specific to the embedding statefulset



_Appears in:_
- [api.v1beta1.EmbeddingResourceOverrides](#apiv1beta1embeddingresourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  | Optional: \{\} <br /> |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  | Optional: \{\} <br /> |
| `podTemplateMetadataOverrides` _[api.v1beta1.ResourceMetadataOverrides](#apiv1beta1resourcemetadataoverrides)_ | PodTemplateMetadataOverrides defines metadata overrides for the pod template |  | Optional: \{\} <br /> |


#### api.v1beta1.EnvVar



EnvVar represents an environment variable in a container



_Appears in:_
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)
- [api.v1beta1.ProxyDeploymentOverrides](#apiv1beta1proxydeploymentoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the environment variable |  | Required: \{\} <br /> |
| `value` _string_ | Value of the environment variable |  | Required: \{\} <br /> |


#### api.v1beta1.ExternalAuthConfigRef



ExternalAuthConfigRef defines a reference to a MCPExternalAuthConfig resource.
The referenced MCPExternalAuthConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1beta1.BackendAuthConfig](#apiv1beta1backendauthconfig)
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPExternalAuthConfig resource |  | Required: \{\} <br /> |


#### api.v1beta1.ExternalAuthType

_Underlying type:_ _string_

ExternalAuthType represents the type of external authentication



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description |
| --- | --- |
| `tokenExchange` | ExternalAuthTypeTokenExchange is the type for RFC-8693 token exchange<br /> |
| `headerInjection` | ExternalAuthTypeHeaderInjection is the type for custom header injection<br /> |
| `bearerToken` | ExternalAuthTypeBearerToken is the type for bearer token authentication<br />This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets<br /> |
| `unauthenticated` | ExternalAuthTypeUnauthenticated is the type for no authentication<br />This should only be used for backends on trusted networks (e.g., localhost, VPC)<br />or when authentication is handled by network-level security<br /> |
| `embeddedAuthServer` | ExternalAuthTypeEmbeddedAuthServer is the type for embedded OAuth2/OIDC authorization server<br />This enables running an embedded auth server that delegates to upstream IDPs<br /> |
| `awsSts` | ExternalAuthTypeAWSSts is the type for AWS STS authentication<br /> |
| `upstreamInject` | ExternalAuthTypeUpstreamInject is the type for upstream token injection<br />This injects an upstream IDP access token as the Authorization: Bearer header<br /> |


#### api.v1beta1.HeaderForwardConfig



HeaderForwardConfig defines header forward configuration for remote servers.



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `addPlaintextHeaders` _object (keys:string, values:string)_ | AddPlaintextHeaders is a map of header names to literal values to inject into requests.<br />WARNING: Values are stored in plaintext and visible via kubectl commands.<br />Use addHeadersFromSecret for sensitive data like API keys or tokens. |  | Optional: \{\} <br /> |
| `addHeadersFromSecret` _[api.v1beta1.HeaderFromSecret](#apiv1beta1headerfromsecret) array_ | AddHeadersFromSecret references Kubernetes Secrets for sensitive header values. |  | Optional: \{\} <br /> |


#### api.v1beta1.HeaderFromSecret



HeaderFromSecret defines a header whose value comes from a Kubernetes Secret.



_Appears in:_
- [api.v1beta1.HeaderForwardConfig](#apiv1beta1headerforwardconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `headerName` _string_ | HeaderName is the HTTP header name (e.g., "X-API-Key") |  | MaxLength: 255 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `valueSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ValueSecretRef references the Secret and key containing the header value |  | Required: \{\} <br /> |


#### api.v1beta1.HeaderInjectionConfig



HeaderInjectionConfig holds configuration for custom HTTP header injection authentication.
This allows injecting a secret-based header value into requests to backend MCP servers.
For security reasons, only secret references are supported (no plaintext values).



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `headerName` _string_ | HeaderName is the name of the HTTP header to inject |  | MinLength: 1 <br />Required: \{\} <br /> |
| `valueSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ValueSecretRef references a Kubernetes Secret containing the header value |  | Required: \{\} <br /> |


#### api.v1beta1.IncomingAuthConfig



IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server



_Appears in:_
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type defines the authentication type: anonymous or oidc<br />When no authentication is required, explicitly set this to "anonymous" |  | Enum: [anonymous oidc] <br />Required: \{\} <br /> |
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this VirtualMCPServer.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration<br />Reuses MCPServer authz patterns |  | Optional: \{\} <br /> |


#### api.v1beta1.InlineAuthzConfig



InlineAuthzConfig contains direct authorization configuration



_Appears in:_
- [api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policies` _string array_ | Policies is a list of Cedar policy strings |  | MinItems: 1 <br />Required: \{\} <br /> |
| `entitiesJson` _string_ | EntitiesJSON is a JSON string representing Cedar entities | [] | Optional: \{\} <br /> |


#### api.v1beta1.InlineOIDCSharedConfig



InlineOIDCSharedConfig contains direct OIDC configuration.
This contains shared fields without audience and scopes, which are specified per-server
via MCPOIDCConfigReference.



_Appears in:_
- [api.v1beta1.MCPOIDCConfigSpec](#apiv1beta1mcpoidcconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the OIDC issuer URL |  | Required: \{\} <br /> |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from |  | Optional: \{\} <br /> |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint |  | Optional: \{\} <br /> |
| `clientId` _string_ | ClientID is the OIDC client ID |  | Optional: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef is a reference to a Kubernetes Secret containing the client secret |  | Optional: \{\} <br /> |
| `caBundleRef` _[api.v1beta1.CABundleSource](#apiv1beta1cabundlesource)_ | CABundleRef references a ConfigMap containing the CA certificate bundle.<br />When specified, ToolHive auto-mounts the ConfigMap and auto-computes ThvCABundlePath. |  | Optional: \{\} <br /> |
| `jwksAuthTokenPath` _string_ | JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests |  | Optional: \{\} <br /> |
| `jwksAllowPrivateIP` _boolean_ | JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses.<br />Note: at runtime, if either JWKSAllowPrivateIP or ProtectedResourceAllowPrivateIP<br />is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection). | false | Optional: \{\} <br /> |
| `protectedResourceAllowPrivateIP` _boolean_ | ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses.<br />Note: at runtime, if either ProtectedResourceAllowPrivateIP or JWKSAllowPrivateIP<br />is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection). | false | Optional: \{\} <br /> |
| `insecureAllowHTTP` _boolean_ | InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing.<br />WARNING: This is insecure and should NEVER be used in production. | false | Optional: \{\} <br /> |


#### api.v1beta1.KubernetesServiceAccountOIDCConfig



KubernetesServiceAccountOIDCConfig configures OIDC for Kubernetes service account token validation.
This contains shared fields without audience, which is specified per-server via MCPOIDCConfigReference.



_Appears in:_
- [api.v1beta1.MCPOIDCConfigSpec](#apiv1beta1mcpoidcconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceAccount` _string_ | ServiceAccount is the name of the service account to validate tokens for.<br />If empty, uses the pod's service account. |  | Optional: \{\} <br /> |
| `namespace` _string_ | Namespace is the namespace of the service account.<br />If empty, uses the MCPServer's namespace. |  | Optional: \{\} <br /> |
| `issuer` _string_ | Issuer is the OIDC issuer URL. | https://kubernetes.default.svc | Optional: \{\} <br /> |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from.<br />If empty, OIDC discovery will be used to automatically determine the JWKS URL. |  | Optional: \{\} <br /> |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint.<br />If empty, OIDC discovery will be used to automatically determine the introspection URL. |  | Optional: \{\} <br /> |
| `useClusterAuth` _boolean_ | UseClusterAuth enables using the Kubernetes cluster's CA bundle and service account token.<br />When true, uses /var/run/secrets/kubernetes.io/serviceaccount/ca.crt for TLS verification<br />and /var/run/secrets/kubernetes.io/serviceaccount/token for bearer token authentication.<br />Defaults to true if not specified. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPExternalAuthConfig



MCPExternalAuthConfig is the Schema for the mcpexternalauthconfigs API.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigList](#apiv1beta1mcpexternalauthconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPExternalAuthConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPExternalAuthConfigStatus](#apiv1beta1mcpexternalauthconfigstatus)_ |  |  |  |


#### api.v1beta1.MCPExternalAuthConfigList



MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPExternalAuthConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPExternalAuthConfig](#apiv1beta1mcpexternalauthconfig) array_ |  |  |  |


#### api.v1beta1.MCPExternalAuthConfigSpec



MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfig](#apiv1beta1mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[api.v1beta1.ExternalAuthType](#apiv1beta1externalauthtype)_ | Type is the type of external authentication to configure |  | Enum: [tokenExchange headerInjection bearerToken unauthenticated embeddedAuthServer awsSts upstreamInject] <br />Required: \{\} <br /> |
| `tokenExchange` _[api.v1beta1.TokenExchangeConfig](#apiv1beta1tokenexchangeconfig)_ | TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange<br />Only used when Type is "tokenExchange" |  | Optional: \{\} <br /> |
| `headerInjection` _[api.v1beta1.HeaderInjectionConfig](#apiv1beta1headerinjectionconfig)_ | HeaderInjection configures custom HTTP header injection<br />Only used when Type is "headerInjection" |  | Optional: \{\} <br /> |
| `bearerToken` _[api.v1beta1.BearerTokenConfig](#apiv1beta1bearertokenconfig)_ | BearerToken configures bearer token authentication<br />Only used when Type is "bearerToken" |  | Optional: \{\} <br /> |
| `embeddedAuthServer` _[api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)_ | EmbeddedAuthServer configures an embedded OAuth2/OIDC authorization server<br />Only used when Type is "embeddedAuthServer" |  | Optional: \{\} <br /> |
| `awsSts` _[api.v1beta1.AWSStsConfig](#apiv1beta1awsstsconfig)_ | AWSSts configures AWS STS authentication with SigV4 request signing<br />Only used when Type is "awsSts" |  | Optional: \{\} <br /> |
| `upstreamInject` _[api.v1beta1.UpstreamInjectSpec](#apiv1beta1upstreaminjectspec)_ | UpstreamInject configures upstream token injection for backend requests.<br />Only used when Type is "upstreamInject". |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPExternalAuthConfigStatus



MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfig](#apiv1beta1mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPExternalAuthConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.<br />It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPExternalAuthConfig.<br />Each entry identifies the workload by kind and name. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPGroup



MCPGroup is the Schema for the mcpgroups API



_Appears in:_
- [api.v1beta1.MCPGroupList](#apiv1beta1mcpgrouplist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPGroup` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPGroupSpec](#apiv1beta1mcpgroupspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPGroupStatus](#apiv1beta1mcpgroupstatus)_ |  |  |  |


#### api.v1beta1.MCPGroupList



MCPGroupList contains a list of MCPGroup





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPGroupList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPGroup](#apiv1beta1mcpgroup) array_ |  |  |  |


#### api.v1beta1.MCPGroupPhase

_Underlying type:_ _string_

MCPGroupPhase represents the lifecycle phase of an MCPGroup

_Validation:_
- Enum: [Ready Pending Failed]

_Appears in:_
- [api.v1beta1.MCPGroupStatus](#apiv1beta1mcpgroupstatus)

| Field | Description |
| --- | --- |
| `Ready` | MCPGroupPhaseReady indicates the MCPGroup is ready<br /> |
| `Pending` | MCPGroupPhasePending indicates the MCPGroup is pending<br /> |
| `Failed` | MCPGroupPhaseFailed indicates the MCPGroup has failed<br /> |


#### api.v1beta1.MCPGroupRef



MCPGroupRef defines a reference to an MCPGroup resource.
The referenced MCPGroup must be in the same namespace.



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPGroup resource in the same namespace |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.MCPGroupSpec



MCPGroupSpec defines the desired state of MCPGroup



_Appears in:_
- [api.v1beta1.MCPGroup](#apiv1beta1mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `description` _string_ | Description provides human-readable context |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPGroupStatus



MCPGroupStatus defines observed state



_Appears in:_
- [api.v1beta1.MCPGroup](#apiv1beta1mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.MCPGroupPhase](#apiv1beta1mcpgroupphase)_ | Phase indicates current state | Pending | Enum: [Ready Pending Failed] <br />Optional: \{\} <br /> |
| `servers` _string array_ | Servers lists MCPServer names in this group |  | Optional: \{\} <br /> |
| `serverCount` _integer_ | ServerCount is the number of MCPServers |  | Optional: \{\} <br /> |
| `remoteProxies` _string array_ | RemoteProxies lists MCPRemoteProxy names in this group |  | Optional: \{\} <br /> |
| `remoteProxyCount` _integer_ | RemoteProxyCount is the number of MCPRemoteProxies |  | Optional: \{\} <br /> |
| `entries` _string array_ | Entries lists MCPServerEntry names in this group |  | Optional: \{\} <br /> |
| `entryCount` _integer_ | EntryCount is the number of MCPServerEntries |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent observations |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPOIDCConfig



MCPOIDCConfig is the Schema for the mcpoidcconfigs API.
MCPOIDCConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1beta1.MCPOIDCConfigList](#apiv1beta1mcpoidcconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPOIDCConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPOIDCConfigSpec](#apiv1beta1mcpoidcconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPOIDCConfigStatus](#apiv1beta1mcpoidcconfigstatus)_ |  |  |  |


#### api.v1beta1.MCPOIDCConfigList



MCPOIDCConfigList contains a list of MCPOIDCConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPOIDCConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPOIDCConfig](#apiv1beta1mcpoidcconfig) array_ |  |  |  |


#### api.v1beta1.MCPOIDCConfigReference



MCPOIDCConfigReference is a reference to an MCPOIDCConfig resource with per-server overrides.
The referenced MCPOIDCConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1beta1.IncomingAuthConfig](#apiv1beta1incomingauthconfig)
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPOIDCConfig resource |  | MinLength: 1 <br />Required: \{\} <br /> |
| `audience` _string_ | Audience is the expected audience for token validation.<br />This MUST be unique per server to prevent token replay attacks. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `scopes` _string array_ | Scopes is the list of OAuth scopes to advertise in the well-known endpoint (RFC 9728).<br />If empty, defaults to ["openid"]. |  | Optional: \{\} <br /> |
| `resourceUrl` _string_ | ResourceURL is the public URL for OAuth protected resource metadata (RFC 9728).<br />When the server is exposed via Ingress or gateway, set this to the external<br />URL that MCP clients connect to. If not specified, defaults to the internal<br />Kubernetes service URL. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPOIDCConfigSourceType

_Underlying type:_ _string_

MCPOIDCConfigSourceType represents the type of OIDC configuration source for MCPOIDCConfig



_Appears in:_
- [api.v1beta1.MCPOIDCConfigSpec](#apiv1beta1mcpoidcconfigspec)

| Field | Description |
| --- | --- |
| `kubernetesServiceAccount` | MCPOIDCConfigTypeKubernetesServiceAccount is the type for Kubernetes service account token validation<br /> |
| `inline` | MCPOIDCConfigTypeInline is the type for inline OIDC configuration<br /> |


#### api.v1beta1.MCPOIDCConfigSpec



MCPOIDCConfigSpec defines the desired state of MCPOIDCConfig.
MCPOIDCConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [api.v1beta1.MCPOIDCConfig](#apiv1beta1mcpoidcconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[api.v1beta1.MCPOIDCConfigSourceType](#apiv1beta1mcpoidcconfigsourcetype)_ | Type is the type of OIDC configuration source |  | Enum: [kubernetesServiceAccount inline] <br />Required: \{\} <br /> |
| `kubernetesServiceAccount` _[api.v1beta1.KubernetesServiceAccountOIDCConfig](#apiv1beta1kubernetesserviceaccountoidcconfig)_ | KubernetesServiceAccount configures OIDC for Kubernetes service account token validation.<br />Only used when Type is "kubernetesServiceAccount". |  | Optional: \{\} <br /> |
| `inline` _[api.v1beta1.InlineOIDCSharedConfig](#apiv1beta1inlineoidcsharedconfig)_ | Inline contains direct OIDC configuration.<br />Only used when Type is "inline". |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPOIDCConfigStatus



MCPOIDCConfigStatus defines the observed state of MCPOIDCConfig



_Appears in:_
- [api.v1beta1.MCPOIDCConfig](#apiv1beta1mcpoidcconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPOIDCConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPOIDCConfig. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPOIDCConfig.<br />Each entry identifies the workload by kind and name. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPRegistry



MCPRegistry is the Schema for the mcpregistries API



_Appears in:_
- [api.v1beta1.MCPRegistryList](#apiv1beta1mcpregistrylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPRegistry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPRegistrySpec](#apiv1beta1mcpregistryspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPRegistryStatus](#apiv1beta1mcpregistrystatus)_ |  |  |  |


#### api.v1beta1.MCPRegistryList



MCPRegistryList contains a list of MCPRegistry





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPRegistryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPRegistry](#apiv1beta1mcpregistry) array_ |  |  |  |


#### api.v1beta1.MCPRegistryPhase

_Underlying type:_ _string_

MCPRegistryPhase represents the phase of the MCPRegistry

_Validation:_
- Enum: [Pending Ready Failed Terminating]

_Appears in:_
- [api.v1beta1.MCPRegistryStatus](#apiv1beta1mcpregistrystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRegistryPhasePending means the MCPRegistry is being initialized<br /> |
| `Ready` | MCPRegistryPhaseReady means the MCPRegistry is ready and operational<br /> |
| `Failed` | MCPRegistryPhaseFailed means the MCPRegistry has failed<br /> |
| `Terminating` | MCPRegistryPhaseTerminating means the MCPRegistry is being deleted<br /> |


#### api.v1beta1.MCPRegistrySpec



MCPRegistrySpec defines the desired state of MCPRegistry



_Appears in:_
- [api.v1beta1.MCPRegistry](#apiv1beta1mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `configYAML` _string_ | ConfigYAML is the complete registry server config.yaml content.<br />The operator creates a ConfigMap from this string and mounts it<br />at /config/config.yaml in the registry-api container.<br />The operator does NOT parse, validate, or transform this content —<br />configuration validation is the registry server's responsibility.<br />Security note: this content is stored in a ConfigMap, not a Secret.<br />Do not inline credentials (passwords, tokens, client secrets) in this<br />field. Instead, reference credentials via file paths and mount the<br />actual secrets using the Volumes and VolumeMounts fields. For database<br />passwords, use PGPassSecretRef. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `volumes` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#json-v1-apiextensions-k8s-io) array_ | Volumes defines additional volumes to add to the registry API pod.<br />Each entry is a standard Kubernetes Volume object (JSON/YAML).<br />The operator appends them to the pod spec alongside its own config volume.<br />Use these to mount:<br />  - Secrets (git auth tokens, OAuth client secrets, CA certs)<br />  - ConfigMaps (registry data files)<br />  - PersistentVolumeClaims (registry data on persistent storage)<br />  - Any other volume type the registry server needs |  | Optional: \{\} <br /> |
| `volumeMounts` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#json-v1-apiextensions-k8s-io) array_ | VolumeMounts defines additional volume mounts for the registry-api container.<br />Each entry is a standard Kubernetes VolumeMount object (JSON/YAML).<br />The operator appends them to the container's volume mounts alongside the config mount.<br />Mount paths must match the file paths referenced in configYAML.<br />For example, if configYAML references passwordFile: /secrets/git-creds/token,<br />a corresponding volume mount must exist with mountPath: /secrets/git-creds. |  | Optional: \{\} <br /> |
| `pgpassSecretRef` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#secretkeyselector-v1-core)_ | PGPassSecretRef references a Secret containing a pre-created pgpass file.<br />Why this is a dedicated field instead of a regular volume/volumeMount:<br />PostgreSQL's libpq rejects pgpass files that aren't mode 0600. Kubernetes<br />secret volumes mount files as root-owned, and the registry-api container<br />runs as non-root (UID 65532). A root-owned 0600 file is unreadable by<br />UID 65532, and using fsGroup changes permissions to 0640 which libpq also<br />rejects. The only solution is an init container that copies the file to an<br />emptyDir as the app user and runs chmod 0600. This cannot be expressed<br />through volumes/volumeMounts alone -- it requires an init container, two<br />extra volumes (secret + emptyDir), a subPath mount, and an environment<br />variable, all wired together correctly.<br />When specified, the operator generates all of that plumbing invisibly.<br />The user creates the Secret with pgpass-formatted content; the operator<br />handles only the Kubernetes permission mechanics.<br />Example Secret:<br />	apiVersion: v1<br />	kind: Secret<br />	metadata:<br />	  name: my-pgpass<br />	stringData:<br />	  .pgpass: \|<br />	    postgres:5432:registry:db_app:mypassword<br />	    postgres:5432:registry:db_migrator:otherpassword<br />Then reference it:<br />	pgpassSecretRef:<br />	  name: my-pgpass<br />	  key: .pgpass |  | Optional: \{\} <br /> |
| `displayName` _string_ | DisplayName is a human-readable name for the registry. |  | Optional: \{\} <br /> |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the registry API server.<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the registry API server runs in, you must specify<br />the `registry-api` container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br />Optional: \{\} <br /> |


#### api.v1beta1.MCPRegistryStatus



MCPRegistryStatus defines the observed state of MCPRegistry



_Appears in:_
- [api.v1beta1.MCPRegistry](#apiv1beta1mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRegistry's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.MCPRegistryPhase](#apiv1beta1mcpregistryphase)_ | Phase represents the current overall phase of the MCPRegistry |  | Enum: [Pending Ready Failed Terminating] <br />Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the current phase |  | Optional: \{\} <br /> |
| `url` _string_ | URL is the URL where the registry API can be accessed |  | Optional: \{\} <br /> |
| `readyReplicas` _integer_ | ReadyReplicas is the number of ready registry API replicas |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPRemoteProxy



MCPRemoteProxy is the Schema for the mcpremoteproxies API
It enables proxying remote MCP servers with authentication, authorization, audit logging, and tool filtering



_Appears in:_
- [api.v1beta1.MCPRemoteProxyList](#apiv1beta1mcpremoteproxylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPRemoteProxy` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPRemoteProxyStatus](#apiv1beta1mcpremoteproxystatus)_ |  |  |  |


#### api.v1beta1.MCPRemoteProxyList



MCPRemoteProxyList contains a list of MCPRemoteProxy





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPRemoteProxyList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPRemoteProxy](#apiv1beta1mcpremoteproxy) array_ |  |  |  |


#### api.v1beta1.MCPRemoteProxyPhase

_Underlying type:_ _string_

MCPRemoteProxyPhase is a label for the condition of a MCPRemoteProxy at the current time

_Validation:_
- Enum: [Pending Ready Failed Terminating]

_Appears in:_
- [api.v1beta1.MCPRemoteProxyStatus](#apiv1beta1mcpremoteproxystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRemoteProxyPhasePending means the proxy is being created<br /> |
| `Ready` | MCPRemoteProxyPhaseReady means the proxy is ready and operational<br /> |
| `Failed` | MCPRemoteProxyPhaseFailed means the proxy failed to start or encountered an error<br /> |
| `Terminating` | MCPRemoteProxyPhaseTerminating means the proxy is being deleted<br /> |


#### api.v1beta1.MCPRemoteProxySpec



MCPRemoteProxySpec defines the desired state of MCPRemoteProxy



_Appears in:_
- [api.v1beta1.MCPRemoteProxy](#apiv1beta1mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `remoteUrl` _string_ | RemoteURL is the URL of the remote MCP server to proxy |  | Pattern: `^https?://` <br />Required: \{\} <br /> |
| `proxyPort` _integer_ | ProxyPort is the port to expose the MCP proxy on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `transport` _string_ | Transport is the transport method for the remote proxy (sse or streamable-http) | streamable-http | Enum: [sse streamable-http] <br /> |
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this MCPRemoteProxy.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource. |  | Optional: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.<br />When specified, the proxy will exchange validated incoming tokens for remote service tokens.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy. |  | Optional: \{\} <br /> |
| `authServerRef` _[api.v1beta1.AuthServerRef](#apiv1beta1authserverref)_ | AuthServerRef optionally references a resource that configures an embedded<br />OAuth 2.0/OIDC authorization server to authenticate MCP clients.<br />Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer). |  | Optional: \{\} <br /> |
| `headerForward` _[api.v1beta1.HeaderForwardConfig](#apiv1beta1headerforwardconfig)_ | HeaderForward configures headers to inject into requests to the remote MCP server.<br />Use this to add custom headers like X-Tenant-ID or correlation IDs. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the proxy |  | Optional: \{\} <br /> |
| `audit` _[api.v1beta1.AuditConfig](#apiv1beta1auditconfig)_ | Audit defines audit logging configuration for the proxy |  | Optional: \{\} <br /> |
| `toolConfigRef` _[api.v1beta1.ToolConfigRef](#apiv1beta1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this allows filtering and overriding tools from the remote MCP server. |  | Optional: \{\} <br /> |
| `telemetryConfigRef` _[api.v1beta1.MCPTelemetryConfigReference](#apiv1beta1mcptelemetryconfigreference)_ | TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.<br />The referenced MCPTelemetryConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `resources` _[api.v1beta1.ResourceRequirements](#apiv1beta1resourcerequirements)_ | Resources defines the resource requirements for the proxy container |  | Optional: \{\} <br /> |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the proxy.<br />If not specified, a ServiceAccount will be created automatically and used by the proxy. |  | Optional: \{\} <br /> |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false | Optional: \{\} <br /> |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  | Optional: \{\} <br /> |
| `resourceOverrides` _[api.v1beta1.ResourceOverrides](#apiv1beta1resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  | Optional: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup this proxy belongs to.<br />The referenced MCPGroup must be in the same namespace. |  | Optional: \{\} <br /> |
| `sessionAffinity` _string_ | SessionAffinity controls whether the Service routes repeated client connections to the same pod.<br />MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.<br />Set to "None" for stateless servers or when using an external load balancer with its own affinity. | ClientIP | Enum: [ClientIP None] <br />Optional: \{\} <br /> |


#### api.v1beta1.MCPRemoteProxyStatus



MCPRemoteProxyStatus defines the observed state of MCPRemoteProxy



_Appears in:_
- [api.v1beta1.MCPRemoteProxy](#apiv1beta1mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1beta1.MCPRemoteProxyPhase](#apiv1beta1mcpremoteproxyphase)_ | Phase is the current phase of the MCPRemoteProxy |  | Enum: [Pending Ready Failed Terminating] <br />Optional: \{\} <br /> |
| `url` _string_ | URL is the internal cluster URL where the proxy can be accessed |  | Optional: \{\} <br /> |
| `externalUrl` _string_ | ExternalURL is the external URL where the proxy can be accessed (if exposed externally) |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed MCPRemoteProxy |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRemoteProxy's state |  | Optional: \{\} <br /> |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  | Optional: \{\} <br /> |
| `telemetryConfigHash` _string_ | TelemetryConfigHash stores the hash of the referenced MCPTelemetryConfig for change detection |  | Optional: \{\} <br /> |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  | Optional: \{\} <br /> |
| `authServerConfigHash` _string_ | AuthServerConfigHash is the hash of the referenced authServerRef spec,<br />used to detect configuration changes and trigger reconciliation. |  | Optional: \{\} <br /> |
| `oidcConfigHash` _string_ | OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection |  | Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the current phase |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPServer



MCPServer is the Schema for the mcpservers API



_Appears in:_
- [api.v1beta1.MCPServerList](#apiv1beta1mcpserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPServerStatus](#apiv1beta1mcpserverstatus)_ |  |  |  |


#### api.v1beta1.MCPServerEntry



MCPServerEntry is the Schema for the mcpserverentries API.
It declares a remote MCP server endpoint for vMCP discovery and routing
without deploying any infrastructure.



_Appears in:_
- [api.v1beta1.MCPServerEntryList](#apiv1beta1mcpserverentrylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPServerEntry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPServerEntryStatus](#apiv1beta1mcpserverentrystatus)_ |  |  |  |


#### api.v1beta1.MCPServerEntryList



MCPServerEntryList contains a list of MCPServerEntry.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPServerEntryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPServerEntry](#apiv1beta1mcpserverentry) array_ |  |  |  |


#### api.v1beta1.MCPServerEntryPhase

_Underlying type:_ _string_

MCPServerEntryPhase represents the lifecycle phase of an MCPServerEntry.

_Validation:_
- Enum: [Valid Pending Failed]

_Appears in:_
- [api.v1beta1.MCPServerEntryStatus](#apiv1beta1mcpserverentrystatus)

| Field | Description |
| --- | --- |
| `Valid` | MCPServerEntryPhaseValid indicates all validations passed and the entry is usable.<br /> |
| `Pending` | MCPServerEntryPhasePending is the initial state before the first reconciliation.<br /> |
| `Failed` | MCPServerEntryPhaseFailed indicates one or more referenced resources are missing or invalid.<br /> |


#### api.v1beta1.MCPServerEntrySpec



MCPServerEntrySpec defines the desired state of MCPServerEntry.
MCPServerEntry is a zero-infrastructure catalog entry that declares a remote MCP
server endpoint. Unlike MCPRemoteProxy, it creates no pods, services, or deployments.



_Appears in:_
- [api.v1beta1.MCPServerEntry](#apiv1beta1mcpserverentry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `remoteUrl` _string_ | RemoteURL is the URL of the remote MCP server.<br />Both HTTP and HTTPS schemes are accepted at admission time. |  | Pattern: `^https?://` <br />Required: \{\} <br /> |
| `transport` _string_ | Transport is the transport method for the remote server (sse or streamable-http).<br />No default is set (unlike MCPRemoteProxy) because MCPServerEntry points at external<br />servers the user doesn't control — requiring explicit transport avoids silent mismatches. |  | Enum: [sse streamable-http] <br />Required: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup this entry belongs to.<br />Required — every MCPServerEntry must be part of a group for vMCP discovery. |  | Required: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange<br />when connecting to the remote MCP server. The referenced MCPExternalAuthConfig must<br />exist in the same namespace as this MCPServerEntry. |  | Optional: \{\} <br /> |
| `headerForward` _[api.v1beta1.HeaderForwardConfig](#apiv1beta1headerforwardconfig)_ | HeaderForward configures headers to inject into requests to the remote MCP server.<br />Use this to add custom headers like API keys or correlation IDs. |  | Optional: \{\} <br /> |
| `caBundleRef` _[api.v1beta1.CABundleSource](#apiv1beta1cabundlesource)_ | CABundleRef references a ConfigMap containing CA certificates for TLS verification<br />when connecting to the remote MCP server. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPServerEntryStatus



MCPServerEntryStatus defines the observed state of MCPServerEntry.



_Appears in:_
- [api.v1beta1.MCPServerEntry](#apiv1beta1mcpserverentry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller. |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.MCPServerEntryPhase](#apiv1beta1mcpserverentryphase)_ | Phase indicates the current lifecycle phase of the MCPServerEntry. | Pending | Enum: [Valid Pending Failed] <br />Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPServerEntry's state. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPServerList



MCPServerList contains a list of MCPServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPServer](#apiv1beta1mcpserver) array_ |  |  |  |


#### api.v1beta1.MCPServerPhase

_Underlying type:_ _string_

MCPServerPhase is the phase of the MCPServer

_Validation:_
- Enum: [Pending Ready Failed Terminating Stopped]

_Appears in:_
- [api.v1beta1.MCPServerStatus](#apiv1beta1mcpserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPServerPhasePending means the MCPServer is being created<br /> |
| `Ready` | MCPServerPhaseReady means the MCPServer is ready<br /> |
| `Failed` | MCPServerPhaseFailed means the MCPServer failed to start<br /> |
| `Terminating` | MCPServerPhaseTerminating means the MCPServer is being deleted<br /> |
| `Stopped` | MCPServerPhaseStopped means the MCPServer is scaled to zero<br /> |


#### api.v1beta1.MCPServerSpec



MCPServerSpec defines the desired state of MCPServer



_Appears in:_
- [api.v1beta1.MCPServer](#apiv1beta1mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Image is the container image for the MCP server |  | Required: \{\} <br /> |
| `transport` _string_ | Transport is the transport method for the MCP server (stdio, streamable-http or sse) | stdio | Enum: [stdio streamable-http sse] <br /> |
| `proxyMode` _string_ | ProxyMode is the proxy mode for stdio transport (sse or streamable-http)<br />This setting is ONLY applicable when Transport is "stdio".<br />For direct transports (sse, streamable-http), this field is ignored.<br />The default value is applied by Kubernetes but will be ignored for non-stdio transports. | streamable-http | Enum: [sse streamable-http] <br />Optional: \{\} <br /> |
| `proxyPort` _integer_ | ProxyPort is the port to expose the proxy runner on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `mcpPort` _integer_ | MCPPort is the port that MCP server listens to |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `args` _string array_ | Args are additional arguments to pass to the MCP server |  | Optional: \{\} <br /> |
| `env` _[api.v1beta1.EnvVar](#apiv1beta1envvar) array_ | Env are environment variables to set in the MCP server container |  | Optional: \{\} <br /> |
| `volumes` _[api.v1beta1.Volume](#apiv1beta1volume) array_ | Volumes are volumes to mount in the MCP server container |  | Optional: \{\} <br /> |
| `resources` _[api.v1beta1.ResourceRequirements](#apiv1beta1resourcerequirements)_ | Resources defines the resource requirements for the MCP server container |  | Optional: \{\} <br /> |
| `secrets` _[api.v1beta1.SecretRef](#apiv1beta1secretref) array_ | Secrets are references to secrets to mount in the MCP server container |  | Optional: \{\} <br /> |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the MCP server.<br />If not specified, a ServiceAccount will be created automatically and used by the MCP server. |  | Optional: \{\} <br /> |
| `permissionProfile` _[api.v1beta1.PermissionProfileRef](#apiv1beta1permissionprofileref)_ | PermissionProfile defines the permission profile to use |  | Optional: \{\} <br /> |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the MCP server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the MCP server runs in, you must specify<br />the `mcp` container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br />Optional: \{\} <br /> |
| `resourceOverrides` _[api.v1beta1.ResourceOverrides](#apiv1beta1resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  | Optional: \{\} <br /> |
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this MCPServer.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the MCP server |  | Optional: \{\} <br /> |
| `audit` _[api.v1beta1.AuditConfig](#apiv1beta1auditconfig)_ | Audit defines audit logging configuration for the MCP server |  | Optional: \{\} <br /> |
| `toolConfigRef` _[api.v1beta1.ToolConfigRef](#apiv1beta1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for external authentication.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPServer. |  | Optional: \{\} <br /> |
| `authServerRef` _[api.v1beta1.AuthServerRef](#apiv1beta1authserverref)_ | AuthServerRef optionally references a resource that configures an embedded<br />OAuth 2.0/OIDC authorization server to authenticate MCP clients.<br />Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer). |  | Optional: \{\} <br /> |
| `telemetryConfigRef` _[api.v1beta1.MCPTelemetryConfigReference](#apiv1beta1mcptelemetryconfigreference)_ | TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.<br />The referenced MCPTelemetryConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false | Optional: \{\} <br /> |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  | Optional: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup this server belongs to.<br />The referenced MCPGroup must be in the same namespace. |  | Optional: \{\} <br /> |
| `sessionAffinity` _string_ | SessionAffinity controls whether the Service routes repeated client connections to the same pod.<br />MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.<br />Set to "None" for stateless servers or when using an external load balancer with its own affinity. | ClientIP | Enum: [ClientIP None] <br />Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the desired number of proxy runner (thv run) pod replicas.<br />MCPServer creates two separate Deployments: one for the proxy runner and one<br />for the MCP server backend. This field controls the proxy runner Deployment.<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `backendReplicas` _integer_ | BackendReplicas is the desired number of MCP server backend pod replicas.<br />This controls the backend Deployment (the MCP server container itself),<br />independent of the proxy runner controlled by Replicas.<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `sessionStorage` _[api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When nil, no session storage is configured. |  | Optional: \{\} <br /> |
| `rateLimiting` _[api.v1beta1.RateLimitConfig](#apiv1beta1ratelimitconfig)_ | RateLimiting defines rate limiting configuration for the MCP server.<br />Requires Redis session storage to be configured for distributed rate limiting. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPServerStatus



MCPServerStatus defines the observed state of MCPServer



_Appears in:_
- [api.v1beta1.MCPServer](#apiv1beta1mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPServer's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller |  | Optional: \{\} <br /> |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  | Optional: \{\} <br /> |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  | Optional: \{\} <br /> |
| `authServerConfigHash` _string_ | AuthServerConfigHash is the hash of the referenced authServerRef spec,<br />used to detect configuration changes and trigger reconciliation. |  | Optional: \{\} <br /> |
| `oidcConfigHash` _string_ | OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection |  | Optional: \{\} <br /> |
| `telemetryConfigHash` _string_ | TelemetryConfigHash is the hash of the referenced MCPTelemetryConfig spec for change detection |  | Optional: \{\} <br /> |
| `url` _string_ | URL is the URL where the MCP server can be accessed |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.MCPServerPhase](#apiv1beta1mcpserverphase)_ | Phase is the current phase of the MCPServer |  | Enum: [Pending Ready Failed Terminating Stopped] <br />Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the current phase |  | Optional: \{\} <br /> |
| `readyReplicas` _integer_ | ReadyReplicas is the number of ready proxy replicas |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPTelemetryConfig



MCPTelemetryConfig is the Schema for the mcptelemetryconfigs API.
MCPTelemetryConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1beta1.MCPTelemetryConfigList](#apiv1beta1mcptelemetryconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPTelemetryConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPTelemetryConfigSpec](#apiv1beta1mcptelemetryconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPTelemetryConfigStatus](#apiv1beta1mcptelemetryconfigstatus)_ |  |  |  |


#### api.v1beta1.MCPTelemetryConfigList



MCPTelemetryConfigList contains a list of MCPTelemetryConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPTelemetryConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPTelemetryConfig](#apiv1beta1mcptelemetryconfig) array_ |  |  |  |


#### api.v1beta1.MCPTelemetryConfigReference



MCPTelemetryConfigReference is a reference to an MCPTelemetryConfig resource
with per-server overrides. The referenced MCPTelemetryConfig must be in the
same namespace as the MCPServer.



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPTelemetryConfig resource |  | MinLength: 1 <br />Required: \{\} <br /> |
| `serviceName` _string_ | ServiceName overrides the telemetry service name for this specific server.<br />This MUST be unique per server for proper observability (e.g., distinguishing<br />traces and metrics from different servers sharing the same collector).<br />If empty, defaults to the server name with "thv-" prefix at runtime. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPTelemetryConfigSpec



MCPTelemetryConfigSpec defines the desired state of MCPTelemetryConfig.
The spec uses a nested structure with openTelemetry and prometheus sub-objects
for clear separation of concerns.



_Appears in:_
- [api.v1beta1.MCPTelemetryConfig](#apiv1beta1mcptelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `openTelemetry` _[api.v1beta1.MCPTelemetryOTelConfig](#apiv1beta1mcptelemetryotelconfig)_ | OpenTelemetry defines OpenTelemetry configuration (OTLP endpoint, tracing, metrics) |  | Optional: \{\} <br /> |
| `prometheus` _[api.v1beta1.PrometheusConfig](#apiv1beta1prometheusconfig)_ | Prometheus defines Prometheus-specific configuration |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPTelemetryConfigStatus



MCPTelemetryConfigStatus defines the observed state of MCPTelemetryConfig



_Appears in:_
- [api.v1beta1.MCPTelemetryConfig](#apiv1beta1mcptelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPTelemetryConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPTelemetryConfig. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads lists workloads that reference this MCPTelemetryConfig |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPTelemetryOTelConfig



MCPTelemetryOTelConfig defines OpenTelemetry configuration for shared MCPTelemetryConfig resources.
Unlike OpenTelemetryConfig (used by inline MCPServer telemetry), this type:
  - Omits ServiceName (per-server field set via MCPTelemetryConfigReference)
  - Uses map[string]string for Headers (not []string)
  - Adds SensitiveHeaders for Kubernetes Secret-backed credentials
  - Adds ResourceAttributes for shared OTel resource attributes



_Appears in:_
- [api.v1beta1.MCPTelemetryConfigSpec](#apiv1beta1mcptelemetryconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OpenTelemetry is enabled | false | Optional: \{\} <br /> |
| `endpoint` _string_ | Endpoint is the OTLP endpoint URL for tracing and metrics |  | Optional: \{\} <br /> |
| `insecure` _boolean_ | Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint | false | Optional: \{\} <br /> |
| `headers` _object (keys:string, values:string)_ | Headers contains authentication headers for the OTLP endpoint.<br />For secret-backed credentials, use sensitiveHeaders instead. |  | Optional: \{\} <br /> |
| `sensitiveHeaders` _[api.v1beta1.SensitiveHeader](#apiv1beta1sensitiveheader) array_ | SensitiveHeaders contains headers whose values are stored in Kubernetes Secrets.<br />Use this for credential headers (e.g., API keys, bearer tokens) instead of<br />embedding secrets in the headers field. |  | Optional: \{\} <br /> |
| `resourceAttributes` _object (keys:string, values:string)_ | ResourceAttributes contains custom resource attributes to be added to all telemetry signals.<br />These become OTel resource attributes (e.g., deployment.environment, service.namespace).<br />Note: service.name is intentionally excluded — it is set per-server via<br />MCPTelemetryConfigReference.ServiceName. |  | Optional: \{\} <br /> |
| `metrics` _[api.v1beta1.OpenTelemetryMetricsConfig](#apiv1beta1opentelemetrymetricsconfig)_ | Metrics defines OpenTelemetry metrics-specific configuration |  | Optional: \{\} <br /> |
| `tracing` _[api.v1beta1.OpenTelemetryTracingConfig](#apiv1beta1opentelemetrytracingconfig)_ | Tracing defines OpenTelemetry tracing configuration |  | Optional: \{\} <br /> |
| `useLegacyAttributes` _boolean_ | UseLegacyAttributes controls whether legacy attribute names are emitted alongside<br />the new MCP OTEL semantic convention names. Defaults to true for backward compatibility.<br />This will change to false in a future release and eventually be removed. | true | Optional: \{\} <br /> |
| `caBundleRef` _[api.v1beta1.CABundleSource](#apiv1beta1cabundlesource)_ | CABundleRef references a ConfigMap containing a CA certificate bundle for the OTLP endpoint.<br />When specified, the operator mounts the ConfigMap into the proxyrunner pod and configures<br />the OTLP exporters to trust the custom CA. This is useful when the OTLP collector uses<br />TLS with certificates signed by an internal or private CA. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPToolConfig



MCPToolConfig is the Schema for the mcptoolconfigs API.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1beta1.MCPToolConfigList](#apiv1beta1mcptoolconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPToolConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPToolConfigSpec](#apiv1beta1mcptoolconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPToolConfigStatus](#apiv1beta1mcptoolconfigstatus)_ |  |  |  |


#### api.v1beta1.MCPToolConfigList



MCPToolConfigList contains a list of MCPToolConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPToolConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPToolConfig](#apiv1beta1mcptoolconfig) array_ |  |  |  |


#### api.v1beta1.MCPToolConfigSpec



MCPToolConfigSpec defines the desired state of MCPToolConfig.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [api.v1beta1.MCPToolConfig](#apiv1beta1mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `toolsFilter` _string array_ | ToolsFilter is a list of tool names to filter (allow list).<br />Only tools in this list will be exposed by the MCP server.<br />If empty, all tools are exposed. |  | Optional: \{\} <br /> |
| `toolsOverride` _object (keys:string, values:[api.v1beta1.ToolOverride](#apiv1beta1tooloverride))_ | ToolsOverride is a map from actual tool names to their overridden configuration.<br />This allows renaming tools and/or changing their descriptions. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPToolConfigStatus



MCPToolConfigStatus defines the observed state of MCPToolConfig



_Appears in:_
- [api.v1beta1.MCPToolConfig](#apiv1beta1mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPToolConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPToolConfig.<br />It corresponds to the MCPToolConfig's generation, which is updated on mutation by the API Server. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPToolConfig.<br />Each entry identifies the workload by kind and name. |  | Optional: \{\} <br /> |


#### api.v1beta1.ModelCacheConfig



ModelCacheConfig configures persistent storage for model caching



_Appears in:_
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether model caching is enabled | true | Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName is the storage class to use for the PVC<br />If not specified, uses the cluster's default storage class |  | Optional: \{\} <br /> |
| `size` _string_ | Size is the size of the PVC for model caching (e.g., "10Gi") | 10Gi | Optional: \{\} <br /> |
| `accessMode` _string_ | AccessMode is the access mode for the PVC | ReadWriteOnce | Enum: [ReadWriteOnce ReadWriteMany ReadOnlyMany] <br />Optional: \{\} <br /> |


#### api.v1beta1.NetworkPermissions



NetworkPermissions defines the network permissions for an MCP server



_Appears in:_
- [api.v1beta1.PermissionProfileSpec](#apiv1beta1permissionprofilespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode specifies the network mode for the container (e.g., "host", "bridge", "none")<br />When empty, the default container runtime network mode is used |  | Optional: \{\} <br /> |
| `outbound` _[api.v1beta1.OutboundNetworkPermissions](#apiv1beta1outboundnetworkpermissions)_ | Outbound defines the outbound network permissions |  | Optional: \{\} <br /> |


#### api.v1beta1.OAuth2UpstreamConfig



OAuth2UpstreamConfig contains configuration for pure OAuth 2.0 providers.
OAuth 2.0 providers require explicit endpoint configuration.



_Appears in:_
- [api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `authorizationEndpoint` _string_ | AuthorizationEndpoint is the URL for the OAuth authorization endpoint. |  | Pattern: `^https?://.*$` <br />Required: \{\} <br /> |
| `tokenEndpoint` _string_ | TokenEndpoint is the URL for the OAuth token endpoint. |  | Pattern: `^https?://.*$` <br />Required: \{\} <br /> |
| `userInfo` _[api.v1beta1.UserInfoConfig](#apiv1beta1userinfoconfig)_ | UserInfo contains configuration for fetching user information from the upstream provider.<br />Required for OAuth2 providers to resolve user identity. |  | Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier registered with the upstream IDP. |  | Required: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.<br />Optional for public clients using PKCE instead of client secret. |  | Optional: \{\} <br /> |
| `redirectUri` _string_ | RedirectURI is the callback URL where the upstream IDP will redirect after authentication.<br />When not specified, defaults to `\{resourceUrl\}/oauth/callback` where `resourceUrl` is the<br />URL associated with the resource (e.g., MCPServer or vMCP) using this config. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the OAuth scopes to request from the upstream IDP. |  | Optional: \{\} <br /> |
| `tokenResponseMapping` _[api.v1beta1.TokenResponseMapping](#apiv1beta1tokenresponsemapping)_ | TokenResponseMapping configures custom field extraction from non-standard token responses.<br />Some OAuth providers (e.g., GovSlack) nest token fields under non-standard paths<br />instead of returning them at the top level. When set, ToolHive performs the token<br />exchange HTTP call directly and extracts fields using the configured dot-notation paths.<br />If nil, standard OAuth 2.0 token response parsing is used. |  | Optional: \{\} <br /> |
| `additionalAuthorizationParams` _object (keys:string, values:string)_ | AdditionalAuthorizationParams are extra query parameters to include in<br />authorization requests sent to the upstream provider.<br />This is useful for providers that require custom parameters, such as<br />Google's access_type=offline for obtaining refresh tokens.<br />Framework-managed parameters (response_type, client_id, redirect_uri,<br />scope, state, code_challenge, code_challenge_method, nonce) are not allowed. |  | MaxProperties: 16 <br />Optional: \{\} <br /> |


#### api.v1beta1.OIDCUpstreamConfig



OIDCUpstreamConfig contains configuration for OIDC providers.
OIDC providers support automatic endpoint discovery via the issuer URL.



_Appears in:_
- [api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuerUrl` _string_ | IssuerURL is the OIDC issuer URL for automatic endpoint discovery.<br />Must be a valid HTTPS URL. |  | Pattern: `^https://.*$` <br />Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier registered with the upstream IDP. |  | Required: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.<br />Optional for public clients using PKCE instead of client secret. |  | Optional: \{\} <br /> |
| `redirectUri` _string_ | RedirectURI is the callback URL where the upstream IDP will redirect after authentication.<br />When not specified, defaults to `\{resourceUrl\}/oauth/callback` where `resourceUrl` is the<br />URL associated with the resource (e.g., MCPServer or vMCP) using this config. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the OAuth scopes to request from the upstream IDP.<br />If not specified, defaults to ["openid", "offline_access"].<br />When using additionalAuthorizationParams with provider-specific refresh token<br />mechanisms (e.g., Google's access_type=offline), set explicit scopes to avoid<br />sending both offline_access and the provider-specific parameter. |  | Optional: \{\} <br /> |
| `userInfoOverride` _[api.v1beta1.UserInfoConfig](#apiv1beta1userinfoconfig)_ | UserInfoOverride allows customizing UserInfo fetching behavior for OIDC providers.<br />By default, the UserInfo endpoint is discovered automatically via OIDC discovery.<br />Use this to override the endpoint URL, HTTP method, or field mappings for providers<br />that return non-standard claim names in their UserInfo response. |  | Optional: \{\} <br /> |
| `additionalAuthorizationParams` _object (keys:string, values:string)_ | AdditionalAuthorizationParams are extra query parameters to include in<br />authorization requests sent to the upstream provider.<br />This is useful for providers that require custom parameters, such as<br />Google's access_type=offline for obtaining refresh tokens.<br />Note: when using access_type=offline, also set explicit scopes to avoid<br />the default offline_access scope being sent alongside it.<br />Framework-managed parameters (response_type, client_id, redirect_uri,<br />scope, state, code_challenge, code_challenge_method, nonce) are not allowed. |  | MaxProperties: 16 <br />Optional: \{\} <br /> |


#### api.v1beta1.OpenTelemetryMetricsConfig



OpenTelemetryMetricsConfig defines OpenTelemetry metrics configuration



_Appears in:_
- [api.v1beta1.MCPTelemetryOTelConfig](#apiv1beta1mcptelemetryotelconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP metrics are sent | false | Optional: \{\} <br /> |


#### api.v1beta1.OpenTelemetryTracingConfig



OpenTelemetryTracingConfig defines OpenTelemetry tracing configuration



_Appears in:_
- [api.v1beta1.MCPTelemetryOTelConfig](#apiv1beta1mcptelemetryotelconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP tracing is sent | false | Optional: \{\} <br /> |
| `samplingRate` _string_ | SamplingRate is the trace sampling rate (0.0-1.0) | 0.05 | Pattern: `^(0(\.\d+)?\|1(\.0+)?)$` <br />Optional: \{\} <br /> |


#### api.v1beta1.OutboundNetworkPermissions



OutboundNetworkPermissions defines the outbound network permissions



_Appears in:_
- [api.v1beta1.NetworkPermissions](#apiv1beta1networkpermissions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `insecureAllowAll` _boolean_ | InsecureAllowAll allows all outbound network connections (not recommended) | false | Optional: \{\} <br /> |
| `allowHost` _string array_ | AllowHost is a list of hosts to allow connections to |  | Optional: \{\} <br /> |
| `allowPort` _integer array_ | AllowPort is a list of ports to allow connections to |  | Optional: \{\} <br /> |


#### api.v1beta1.OutgoingAuthConfig



OutgoingAuthConfig configures authentication from Virtual MCP to backend MCPServers



_Appears in:_
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _string_ | Source defines how backend authentication configurations are determined<br />- discovered: Automatically discover from backend's MCPServer.spec.externalAuthConfigRef<br />- inline: Explicit per-backend configuration in VirtualMCPServer | discovered | Enum: [discovered inline] <br />Optional: \{\} <br /> |
| `default` _[api.v1beta1.BackendAuthConfig](#apiv1beta1backendauthconfig)_ | Default defines default behavior for backends without explicit auth config |  | Optional: \{\} <br /> |
| `backends` _object (keys:string, values:[api.v1beta1.BackendAuthConfig](#apiv1beta1backendauthconfig))_ | Backends defines per-backend authentication overrides<br />Works in all modes (discovered, inline) |  | Optional: \{\} <br /> |


#### api.v1beta1.PermissionProfileRef



PermissionProfileRef defines a reference to a permission profile



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of permission profile reference | builtin | Enum: [builtin configmap] <br /> |
| `name` _string_ | Name is the name of the permission profile<br />If Type is "builtin", Name must be one of: "none", "network"<br />If Type is "configmap", Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the permission profile<br />Only used when Type is "configmap" |  | Optional: \{\} <br /> |




#### api.v1beta1.PrometheusConfig



PrometheusConfig defines Prometheus-specific configuration



_Appears in:_
- [api.v1beta1.MCPTelemetryConfigSpec](#apiv1beta1mcptelemetryconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether Prometheus metrics endpoint is exposed | false | Optional: \{\} <br /> |


#### api.v1beta1.ProxyDeploymentOverrides



ProxyDeploymentOverrides defines overrides specific to the proxy deployment



_Appears in:_
- [api.v1beta1.ResourceOverrides](#apiv1beta1resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  | Optional: \{\} <br /> |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  | Optional: \{\} <br /> |
| `podTemplateMetadataOverrides` _[api.v1beta1.ResourceMetadataOverrides](#apiv1beta1resourcemetadataoverrides)_ |  |  |  |
| `env` _[api.v1beta1.EnvVar](#apiv1beta1envvar) array_ | Env are environment variables to set in the proxy container (thv run process)<br />These affect the toolhive proxy itself, not the MCP server it manages<br />Use TOOLHIVE_DEBUG=true to enable debug logging in the proxy |  | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#localobjectreference-v1-core) array_ | ImagePullSecrets allows specifying image pull secrets for the proxy runner<br />These are applied to both the Deployment and the ServiceAccount |  | Optional: \{\} <br /> |


#### api.v1beta1.RateLimitBucket



RateLimitBucket defines a token bucket configuration with a maximum capacity
and a refill period. Used by both shared (global) and per-user rate limits.



_Appears in:_
- [api.v1beta1.RateLimitConfig](#apiv1beta1ratelimitconfig)
- [api.v1beta1.ToolRateLimitConfig](#apiv1beta1toolratelimitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `maxTokens` _integer_ | MaxTokens is the maximum number of tokens (bucket capacity).<br />This is also the burst size: the maximum number of requests that can be served<br />instantaneously before the bucket is depleted. |  | Minimum: 1 <br />Required: \{\} <br /> |
| `refillPeriod` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#duration-v1-meta)_ | RefillPeriod is the duration to fully refill the bucket from zero to maxTokens.<br />The effective refill rate is maxTokens / refillPeriod tokens per second.<br />Format: Go duration string (e.g., "1m0s", "30s", "1h0m0s"). |  | Required: \{\} <br /> |


#### api.v1beta1.RateLimitConfig



RateLimitConfig defines rate limiting configuration for an MCP server.
At least one of shared, perUser, or tools must be configured.



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `shared` _[api.v1beta1.RateLimitBucket](#apiv1beta1ratelimitbucket)_ | Shared is a token bucket shared across all users for the entire server. |  | Optional: \{\} <br /> |
| `perUser` _[api.v1beta1.RateLimitBucket](#apiv1beta1ratelimitbucket)_ | PerUser is a token bucket applied independently to each authenticated user<br />at the server level. Requires authentication to be enabled.<br />Each unique userID creates Redis keys that expire after 2x refillPeriod.<br />Memory formula: unique_users_per_TTL_window * (1 + num_tools_with_per_user_limits) keys. |  | Optional: \{\} <br /> |
| `tools` _[api.v1beta1.ToolRateLimitConfig](#apiv1beta1toolratelimitconfig) array_ | Tools defines per-tool rate limit overrides.<br />Each entry applies additional rate limits to calls targeting a specific tool name.<br />A request must pass both the server-level limit and the per-tool limit. |  | Optional: \{\} <br /> |


#### api.v1beta1.RedisACLUserConfig



RedisACLUserConfig configures Redis ACL user authentication.



_Appears in:_
- [api.v1beta1.RedisStorageConfig](#apiv1beta1redisstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `usernameSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | UsernameSecretRef references a Secret containing the Redis ACL username. |  | Required: \{\} <br /> |
| `passwordSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | PasswordSecretRef references a Secret containing the Redis ACL password. |  | Required: \{\} <br /> |


#### api.v1beta1.RedisSentinelConfig



RedisSentinelConfig configures Redis Sentinel connection.



_Appears in:_
- [api.v1beta1.RedisStorageConfig](#apiv1beta1redisstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `masterName` _string_ | MasterName is the name of the Redis master monitored by Sentinel. |  | Required: \{\} <br /> |
| `sentinelAddrs` _string array_ | SentinelAddrs is a list of Sentinel host:port addresses.<br />Mutually exclusive with SentinelService. |  | Optional: \{\} <br /> |
| `sentinelService` _[api.v1beta1.SentinelServiceRef](#apiv1beta1sentinelserviceref)_ | SentinelService enables automatic discovery from a Kubernetes Service.<br />Mutually exclusive with SentinelAddrs. |  | Optional: \{\} <br /> |
| `db` _integer_ | DB is the Redis database number. | 0 | Optional: \{\} <br /> |


#### api.v1beta1.RedisStorageConfig



RedisStorageConfig configures Redis connection for auth server storage.
Redis is deployed in Sentinel mode with ACL user authentication (the only supported configuration).



_Appears in:_
- [api.v1beta1.AuthServerStorageConfig](#apiv1beta1authserverstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `sentinelConfig` _[api.v1beta1.RedisSentinelConfig](#apiv1beta1redissentinelconfig)_ | SentinelConfig holds Redis Sentinel configuration. |  | Required: \{\} <br /> |
| `aclUserConfig` _[api.v1beta1.RedisACLUserConfig](#apiv1beta1redisacluserconfig)_ | ACLUserConfig configures Redis ACL user authentication. |  | Required: \{\} <br /> |
| `dialTimeout` _string_ | DialTimeout is the timeout for establishing connections.<br />Format: Go duration string (e.g., "5s", "1m"). | 5s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `readTimeout` _string_ | ReadTimeout is the timeout for socket reads.<br />Format: Go duration string (e.g., "3s", "1m"). | 3s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `writeTimeout` _string_ | WriteTimeout is the timeout for socket writes.<br />Format: Go duration string (e.g., "3s", "1m"). | 3s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `tls` _[api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)_ | TLS configures TLS for connections to the Redis/Valkey master.<br />Presence of this field enables TLS. Omit to use plaintext. |  | Optional: \{\} <br /> |
| `sentinelTls` _[api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)_ | SentinelTLS configures TLS for connections to Sentinel instances.<br />Presence of this field enables TLS. Omit to use plaintext.<br />When omitted, sentinel connections use plaintext (no fallback to TLS config). |  | Optional: \{\} <br /> |


#### api.v1beta1.RedisTLSConfig



RedisTLSConfig configures TLS for Redis connections.
Presence of this struct on a connection type enables TLS for that connection.



_Appears in:_
- [api.v1beta1.RedisStorageConfig](#apiv1beta1redisstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `insecureSkipVerify` _boolean_ | InsecureSkipVerify skips TLS certificate verification.<br />Use when connecting to services with self-signed certificates. |  | Optional: \{\} <br /> |
| `caCertSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | CACertSecretRef references a Secret containing a PEM-encoded CA certificate<br />for verifying the server. When not specified, system root CAs are used. |  | Optional: \{\} <br /> |


#### api.v1beta1.ResourceList



ResourceList is a set of (resource name, quantity) pairs



_Appears in:_
- [api.v1beta1.ResourceRequirements](#apiv1beta1resourcerequirements)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cpu` _string_ | CPU is the CPU limit in cores (e.g., "500m" for 0.5 cores) |  | Optional: \{\} <br /> |
| `memory` _string_ | Memory is the memory limit in bytes (e.g., "64Mi" for 64 megabytes) |  | Optional: \{\} <br /> |


#### api.v1beta1.ResourceMetadataOverrides



ResourceMetadataOverrides defines metadata overrides for a resource



_Appears in:_
- [api.v1beta1.EmbeddingResourceOverrides](#apiv1beta1embeddingresourceoverrides)
- [api.v1beta1.EmbeddingStatefulSetOverrides](#apiv1beta1embeddingstatefulsetoverrides)
- [api.v1beta1.ProxyDeploymentOverrides](#apiv1beta1proxydeploymentoverrides)
- [api.v1beta1.ResourceOverrides](#apiv1beta1resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  | Optional: \{\} <br /> |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  | Optional: \{\} <br /> |


#### api.v1beta1.ResourceOverrides



ResourceOverrides defines overrides for annotations and labels on created resources



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `proxyDeployment` _[api.v1beta1.ProxyDeploymentOverrides](#apiv1beta1proxydeploymentoverrides)_ | ProxyDeployment defines overrides for the Proxy Deployment resource (toolhive proxy) |  | Optional: \{\} <br /> |
| `proxyService` _[api.v1beta1.ResourceMetadataOverrides](#apiv1beta1resourcemetadataoverrides)_ | ProxyService defines overrides for the Proxy Service resource (points to the proxy deployment) |  | Optional: \{\} <br /> |


#### api.v1beta1.ResourceRequirements



ResourceRequirements describes the compute resource requirements



_Appears in:_
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `limits` _[api.v1beta1.ResourceList](#apiv1beta1resourcelist)_ | Limits describes the maximum amount of compute resources allowed |  | Optional: \{\} <br /> |
| `requests` _[api.v1beta1.ResourceList](#apiv1beta1resourcelist)_ | Requests describes the minimum amount of compute resources required |  | Optional: \{\} <br /> |


#### api.v1beta1.RoleMapping



RoleMapping defines a rule for mapping JWT claims to IAM roles.
Mappings are evaluated in priority order (lower number = higher priority), and the first
matching rule determines which IAM role to assume.
Exactly one of Claim or Matcher must be specified.



_Appears in:_
- [api.v1beta1.AWSStsConfig](#apiv1beta1awsstsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `claim` _string_ | Claim is a simple claim value to match against<br />The claim type is specified by AWSStsConfig.RoleClaim<br />For example, if RoleClaim is "groups", this would be a group name<br />Internally compiled to a CEL expression: "<claim_value>" in claims["<role_claim>"]<br />Mutually exclusive with Matcher |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `matcher` _string_ | Matcher is a CEL expression for complex matching against JWT claims<br />The expression has access to a "claims" variable containing all JWT claims as map[string]any<br />Examples:<br />  - "admins" in claims["groups"]<br />  - claims["sub"] == "user123" && !("act" in claims)<br />Mutually exclusive with Claim |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `roleArn` _string_ | RoleArn is the IAM role ARN to assume when this mapping matches |  | Pattern: `^arn:(aws\|aws-cn\|aws-us-gov):iam::\d\{12\}:role/[\w+=,.@\-_/]+$` <br />Required: \{\} <br /> |
| `priority` _integer_ | Priority determines evaluation order (lower values = higher priority)<br />Allows fine-grained control over role selection precedence<br />When omitted, this mapping has the lowest possible priority and<br />configuration order acts as tie-breaker via stable sort |  | Minimum: 0 <br />Optional: \{\} <br /> |


#### api.v1beta1.SecretKeyRef



SecretKeyRef is a reference to a key within a Secret



_Appears in:_
- [api.v1beta1.BearerTokenConfig](#apiv1beta1bearertokenconfig)
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)
- [api.v1beta1.HeaderFromSecret](#apiv1beta1headerfromsecret)
- [api.v1beta1.HeaderInjectionConfig](#apiv1beta1headerinjectionconfig)
- [api.v1beta1.InlineOIDCSharedConfig](#apiv1beta1inlineoidcsharedconfig)
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)
- [api.v1beta1.OIDCUpstreamConfig](#apiv1beta1oidcupstreamconfig)
- [api.v1beta1.RedisACLUserConfig](#apiv1beta1redisacluserconfig)
- [api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)
- [api.v1beta1.SensitiveHeader](#apiv1beta1sensitiveheader)
- [api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)
- [api.v1beta1.TokenExchangeConfig](#apiv1beta1tokenexchangeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key within the secret |  | Required: \{\} <br /> |


#### api.v1beta1.SecretRef



SecretRef is a reference to a secret



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the secret itself |  | Required: \{\} <br /> |
| `targetEnvName` _string_ | TargetEnvName is the environment variable to be used when setting up the secret in the MCP server<br />If left unspecified, it defaults to the key |  | Optional: \{\} <br /> |


#### api.v1beta1.SensitiveHeader



SensitiveHeader represents a header whose value is stored in a Kubernetes Secret.
This allows credential headers (e.g., API keys, bearer tokens) to be securely
referenced without embedding secrets inline in the MCPTelemetryConfig resource.



_Appears in:_
- [api.v1beta1.MCPTelemetryOTelConfig](#apiv1beta1mcptelemetryotelconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the header name (e.g., "Authorization", "X-API-Key") |  | MinLength: 1 <br />Required: \{\} <br /> |
| `secretKeyRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | SecretKeyRef is a reference to a Kubernetes Secret key containing the header value |  | Required: \{\} <br /> |


#### api.v1beta1.SentinelServiceRef

_Underlying type:_ _[api.v1beta1.struct{Name string "json:\"name\""; Namespace string "json:\"namespace,omitempty\""; Port int32 "json:\"port,omitempty\""}](#apiv1beta1struct{name string "json:\"name\""; namespace string "json:\"namespace,omitempty\""; port int32 "json:\"port,omitempty\""})_

SentinelServiceRef references a Kubernetes Service for Sentinel discovery.



_Appears in:_
- [api.v1beta1.RedisSentinelConfig](#apiv1beta1redissentinelconfig)



#### api.v1beta1.SessionStorageConfig



SessionStorageConfig defines session storage configuration for horizontal scaling.

This is the CRD/K8s-aware surface: it uses SecretKeyRef for secret resolution.
The reconciler resolves PasswordRef to a plain string and builds a
session.RedisConfig (pkg/transport/session) for the actual storage backend.
The operator also populates pkg/vmcp/config.SessionStorageConfig (without PasswordRef)
into the vMCP ConfigMap so the vMCP process receives connection parameters at startup.



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `provider` _string_ | Provider is the session storage backend type |  | Enum: [memory redis] <br />Required: \{\} <br /> |
| `address` _string_ | Address is the Redis server address (required when provider is redis) |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `db` _integer_ | DB is the Redis database number | 0 | Minimum: 0 <br />Optional: \{\} <br /> |
| `keyPrefix` _string_ | KeyPrefix is an optional prefix for all Redis keys used by ToolHive |  | Optional: \{\} <br /> |
| `passwordRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | PasswordRef is a reference to a Secret key containing the Redis password |  | Optional: \{\} <br /> |


#### api.v1beta1.TokenExchangeConfig



TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
This configuration is used to exchange incoming authentication tokens for tokens
that can be used with external services.
The structure matches the tokenexchange.Config from pkg/auth/tokenexchange/middleware.go



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenUrl` _string_ | TokenURL is the OAuth 2.0 token endpoint URL for token exchange |  | Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier<br />Optional for some token exchange flows (e.g., Google Cloud Workforce Identity) |  | Optional: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef is a reference to a secret containing the OAuth 2.0 client secret<br />Optional for some token exchange flows (e.g., Google Cloud Workforce Identity) |  | Optional: \{\} <br /> |
| `audience` _string_ | Audience is the target audience for the exchanged token |  | Required: \{\} <br /> |
| `scopes` _string array_ | Scopes is a list of OAuth 2.0 scopes to request for the exchanged token |  | Optional: \{\} <br /> |
| `subjectTokenType` _string_ | SubjectTokenType is the type of the incoming subject token.<br />Accepts short forms: "access_token" (default), "id_token", "jwt"<br />Or full URNs: "urn:ietf:params:oauth:token-type:access_token",<br />              "urn:ietf:params:oauth:token-type:id_token",<br />              "urn:ietf:params:oauth:token-type:jwt"<br />For Google Workload Identity Federation with OIDC providers (like Okta), use "id_token" |  | Pattern: `^(access_token\|id_token\|jwt\|urn:ietf:params:oauth:token-type:(access_token\|id_token\|jwt))?$` <br />Optional: \{\} <br /> |
| `externalTokenHeaderName` _string_ | ExternalTokenHeaderName is the name of the custom header to use for the exchanged token.<br />If set, the exchanged token will be added to this custom header (e.g., "X-Upstream-Token").<br />If empty or not set, the exchanged token will replace the Authorization header (default behavior). |  | Optional: \{\} <br /> |
| `subjectProviderName` _string_ | SubjectProviderName is the name of the upstream provider whose token is used as the<br />RFC 8693 subject token instead of identity.Token when performing token exchange.<br />When left empty and an embedded authorization server is configured on the VirtualMCPServer,<br />the controller automatically populates this field with the first configured upstream<br />provider name. Set it explicitly to override that default or to select a specific<br />provider when multiple upstreams are configured. |  | Optional: \{\} <br /> |


#### api.v1beta1.TokenLifespanConfig



TokenLifespanConfig holds configuration for token lifetimes.



_Appears in:_
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `accessTokenLifespan` _string_ | AccessTokenLifespan is the duration that access tokens are valid.<br />Format: Go duration string (e.g., "1h", "30m", "24h").<br />If empty, defaults to 1 hour. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `refreshTokenLifespan` _string_ | RefreshTokenLifespan is the duration that refresh tokens are valid.<br />Format: Go duration string (e.g., "168h", "7d" as "168h").<br />If empty, defaults to 7 days (168h). |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `authCodeLifespan` _string_ | AuthCodeLifespan is the duration that authorization codes are valid.<br />Format: Go duration string (e.g., "10m", "5m").<br />If empty, defaults to 10 minutes. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |


#### api.v1beta1.TokenResponseMapping



TokenResponseMapping maps non-standard token response fields to standard OAuth 2.0 fields
using dot-notation JSON paths. This supports upstream providers like GovSlack that nest
the access token under paths like "authed_user.access_token".



_Appears in:_
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `accessTokenPath` _string_ | AccessTokenPath is the dot-notation path to the access token in the response.<br />Example: "authed_user.access_token" |  | MinLength: 1 <br />Required: \{\} <br /> |
| `scopePath` _string_ | ScopePath is the dot-notation path to the scope string in the response.<br />If not specified, defaults to "scope". |  | Optional: \{\} <br /> |
| `refreshTokenPath` _string_ | RefreshTokenPath is the dot-notation path to the refresh token in the response.<br />If not specified, defaults to "refresh_token". |  | Optional: \{\} <br /> |
| `expiresInPath` _string_ | ExpiresInPath is the dot-notation path to the expires_in value (in seconds).<br />If not specified, defaults to "expires_in". |  | Optional: \{\} <br /> |


#### api.v1beta1.ToolAnnotationsOverride



ToolAnnotationsOverride defines overrides for tool annotation fields.
All fields use pointers so nil means "don't override" while zero values
(empty string, false) mean "explicitly set to this value."



_Appears in:_
- [api.v1beta1.ToolOverride](#apiv1beta1tooloverride)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `title` _string_ | Title overrides the human-readable title annotation. |  | Optional: \{\} <br /> |
| `readOnlyHint` _boolean_ | ReadOnlyHint overrides the read-only hint annotation. |  | Optional: \{\} <br /> |
| `destructiveHint` _boolean_ | DestructiveHint overrides the destructive hint annotation. |  | Optional: \{\} <br /> |
| `idempotentHint` _boolean_ | IdempotentHint overrides the idempotent hint annotation. |  | Optional: \{\} <br /> |
| `openWorldHint` _boolean_ | OpenWorldHint overrides the open-world hint annotation. |  | Optional: \{\} <br /> |


#### api.v1beta1.ToolConfigRef



ToolConfigRef defines a reference to a MCPToolConfig resource.
The referenced MCPToolConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPToolConfig resource in the same namespace |  | Required: \{\} <br /> |


#### api.v1beta1.ToolOverride



ToolOverride represents a tool override configuration.
Both Name and Description can be overridden independently, but
they can't be both empty.



_Appears in:_
- [api.v1beta1.MCPToolConfigSpec](#apiv1beta1mcptoolconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the redefined name of the tool |  | Optional: \{\} <br /> |
| `description` _string_ | Description is the redefined description of the tool |  | Optional: \{\} <br /> |
| `annotations` _[api.v1beta1.ToolAnnotationsOverride](#apiv1beta1toolannotationsoverride)_ | Annotations overrides specific tool annotation fields.<br />Only specified fields are overridden; others pass through from the backend. |  | Optional: \{\} <br /> |


#### api.v1beta1.ToolRateLimitConfig



ToolRateLimitConfig defines rate limits for a specific tool.
At least one of shared or perUser must be configured.



_Appears in:_
- [api.v1beta1.RateLimitConfig](#apiv1beta1ratelimitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the MCP tool name this limit applies to. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `shared` _[api.v1beta1.RateLimitBucket](#apiv1beta1ratelimitbucket)_ | Shared token bucket for this specific tool. |  | Optional: \{\} <br /> |
| `perUser` _[api.v1beta1.RateLimitBucket](#apiv1beta1ratelimitbucket)_ | PerUser token bucket configuration for this tool. |  | Optional: \{\} <br /> |


#### api.v1beta1.UpstreamInjectSpec



UpstreamInjectSpec holds configuration for upstream token injection.
This strategy injects an upstream IDP access token obtained by the embedded
authorization server into backend requests as the Authorization: Bearer header.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `providerName` _string_ | ProviderName is the name of the upstream IDP provider whose access token<br />should be injected as the Authorization: Bearer header. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.UpstreamProviderConfig



UpstreamProviderConfig defines configuration for an upstream Identity Provider.



_Appears in:_
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name uniquely identifies this upstream provider.<br />Used for routing decisions and session binding in multi-upstream scenarios.<br />Must be lowercase alphanumeric with hyphens (DNS-label-like). |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Required: \{\} <br /> |
| `type` _[api.v1beta1.UpstreamProviderType](#apiv1beta1upstreamprovidertype)_ | Type specifies the provider type: "oidc" or "oauth2" |  | Enum: [oidc oauth2] <br />Required: \{\} <br /> |
| `oidcConfig` _[api.v1beta1.OIDCUpstreamConfig](#apiv1beta1oidcupstreamconfig)_ | OIDCConfig contains OIDC-specific configuration.<br />Required when Type is "oidc", must be nil when Type is "oauth2". |  | Optional: \{\} <br /> |
| `oauth2Config` _[api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)_ | OAuth2Config contains OAuth 2.0-specific configuration.<br />Required when Type is "oauth2", must be nil when Type is "oidc". |  | Optional: \{\} <br /> |


#### api.v1beta1.UpstreamProviderType

_Underlying type:_ _string_

UpstreamProviderType identifies the type of upstream Identity Provider.



_Appears in:_
- [api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig)

| Field | Description |
| --- | --- |
| `oidc` | UpstreamProviderTypeOIDC is for OIDC providers with discovery support<br /> |
| `oauth2` | UpstreamProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints<br /> |


#### api.v1beta1.UserInfoConfig



UserInfoConfig contains configuration for fetching user information from an upstream provider.
This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints
like GitHub's /user API.



_Appears in:_
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)
- [api.v1beta1.OIDCUpstreamConfig](#apiv1beta1oidcupstreamconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpointUrl` _string_ | EndpointURL is the URL of the userinfo endpoint. |  | Pattern: `^https?://.*$` <br />Required: \{\} <br /> |
| `httpMethod` _string_ | HTTPMethod is the HTTP method to use for the userinfo request.<br />If not specified, defaults to GET. |  | Enum: [GET POST] <br />Optional: \{\} <br /> |
| `additionalHeaders` _object (keys:string, values:string)_ | AdditionalHeaders contains extra headers to include in the userinfo request.<br />Useful for providers that require specific headers (e.g., GitHub's Accept header). |  | Optional: \{\} <br /> |
| `fieldMapping` _[api.v1beta1.UserInfoFieldMapping](#apiv1beta1userinfofieldmapping)_ | FieldMapping contains custom field mapping configuration for non-standard providers.<br />If nil, standard OIDC field names are used ("sub", "name", "email"). |  | Optional: \{\} <br /> |


#### api.v1beta1.UserInfoFieldMapping

_Underlying type:_ _[api.v1beta1.struct{SubjectFields []string "json:\"subjectFields,omitempty\""; NameFields []string "json:\"nameFields,omitempty\""; EmailFields []string "json:\"emailFields,omitempty\""}](#apiv1beta1struct{subjectfields []string "json:\"subjectfields,omitempty\""; namefields []string "json:\"namefields,omitempty\""; emailfields []string "json:\"emailfields,omitempty\""})_

UserInfoFieldMapping maps provider-specific field names to standard UserInfo fields.
This allows adapting non-standard provider responses to the canonical UserInfo structure.
Each field supports an ordered list of claim names to try. The first non-empty value
found will be used.

Example for GitHub:

	fieldMapping:
	  subjectFields: ["id", "login"]
	  nameFields: ["name", "login"]
	  emailFields: ["email"]



_Appears in:_
- [api.v1beta1.UserInfoConfig](#apiv1beta1userinfoconfig)



#### api.v1beta1.ValidationStatus

_Underlying type:_ _string_

ValidationStatus represents the validation state of a workflow

_Validation:_
- Enum: [Valid Invalid Unknown]

_Appears in:_
- [api.v1beta1.VirtualMCPCompositeToolDefinitionStatus](#apiv1beta1virtualmcpcompositetooldefinitionstatus)

| Field | Description |
| --- | --- |
| `Valid` | ValidationStatusValid indicates the workflow is valid<br /> |
| `Invalid` | ValidationStatusInvalid indicates the workflow has validation errors<br /> |
| `Unknown` | ValidationStatusUnknown indicates validation hasn't been performed yet<br /> |


#### api.v1beta1.VirtualMCPCompositeToolDefinition



VirtualMCPCompositeToolDefinition is the Schema for the virtualmcpcompositetooldefinitions API
VirtualMCPCompositeToolDefinition defines reusable composite workflows that can be referenced
by multiple VirtualMCPServer instances



_Appears in:_
- [api.v1beta1.VirtualMCPCompositeToolDefinitionList](#apiv1beta1virtualmcpcompositetooldefinitionlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinition` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)_ |  |  |  |
| `status` _[api.v1beta1.VirtualMCPCompositeToolDefinitionStatus](#apiv1beta1virtualmcpcompositetooldefinitionstatus)_ |  |  |  |


#### api.v1beta1.VirtualMCPCompositeToolDefinitionList



VirtualMCPCompositeToolDefinitionList contains a list of VirtualMCPCompositeToolDefinition





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinitionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.VirtualMCPCompositeToolDefinition](#apiv1beta1virtualmcpcompositetooldefinition) array_ |  |  |  |


#### api.v1beta1.VirtualMCPCompositeToolDefinitionSpec



VirtualMCPCompositeToolDefinitionSpec defines the desired state of VirtualMCPCompositeToolDefinition.
This embeds the CompositeToolConfig from pkg/vmcp/config to share the configuration model
between CLI and operator usage.



_Appears in:_
- [api.v1beta1.VirtualMCPCompositeToolDefinition](#apiv1beta1virtualmcpcompositetooldefinition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the workflow name (unique identifier). |  |  |
| `description` _string_ | Description describes what the workflow does. |  |  |
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  | Optional: \{\} <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum workflow execution time. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `steps` _[vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig) array_ | Steps are the workflow steps to execute. |  |  |
| `output` _[vmcp.config.OutputConfig](#vmcpconfigoutputconfig)_ | Output defines the structured output schema for this workflow.<br />If not specified, the workflow returns the last step's output (backward compatible). |  | Optional: \{\} <br /> |


#### api.v1beta1.VirtualMCPCompositeToolDefinitionStatus



VirtualMCPCompositeToolDefinitionStatus defines the observed state of VirtualMCPCompositeToolDefinition



_Appears in:_
- [api.v1beta1.VirtualMCPCompositeToolDefinition](#apiv1beta1virtualmcpcompositetooldefinition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `validationStatus` _[api.v1beta1.ValidationStatus](#apiv1beta1validationstatus)_ | ValidationStatus indicates the validation state of the workflow<br />- Valid: Workflow structure is valid<br />- Invalid: Workflow has validation errors |  | Enum: [Valid Invalid Unknown] <br />Optional: \{\} <br /> |
| `validationErrors` _string array_ | ValidationErrors contains validation error messages if ValidationStatus is Invalid |  | Optional: \{\} <br /> |
| `referencingVirtualServers` _string array_ | ReferencingVirtualServers lists VirtualMCPServer resources that reference this workflow<br />This helps track which servers need to be reconciled when this workflow changes |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this VirtualMCPCompositeToolDefinition<br />It corresponds to the resource's generation, which is updated on mutation by the API Server |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the workflow's state |  | Optional: \{\} <br /> |


#### api.v1beta1.VirtualMCPServer



VirtualMCPServer is the Schema for the virtualmcpservers API
VirtualMCPServer aggregates multiple backend MCPServers into a unified endpoint



_Appears in:_
- [api.v1beta1.VirtualMCPServerList](#apiv1beta1virtualmcpserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `VirtualMCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)_ |  |  |  |
| `status` _[api.v1beta1.VirtualMCPServerStatus](#apiv1beta1virtualmcpserverstatus)_ |  |  |  |


#### api.v1beta1.VirtualMCPServerList



VirtualMCPServerList contains a list of VirtualMCPServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `VirtualMCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.VirtualMCPServer](#apiv1beta1virtualmcpserver) array_ |  |  |  |


#### api.v1beta1.VirtualMCPServerPhase

_Underlying type:_ _string_

VirtualMCPServerPhase represents the lifecycle phase of a VirtualMCPServer

_Validation:_
- Enum: [Pending Ready Degraded Failed]

_Appears in:_
- [api.v1beta1.VirtualMCPServerStatus](#apiv1beta1virtualmcpserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | VirtualMCPServerPhasePending indicates the VirtualMCPServer is being initialized<br /> |
| `Ready` | VirtualMCPServerPhaseReady indicates the VirtualMCPServer is ready and serving requests<br /> |
| `Degraded` | VirtualMCPServerPhaseDegraded indicates the VirtualMCPServer is running but some backends are unavailable<br /> |
| `Failed` | VirtualMCPServerPhaseFailed indicates the VirtualMCPServer has failed<br /> |


#### api.v1beta1.VirtualMCPServerSpec



VirtualMCPServerSpec defines the desired state of VirtualMCPServer



_Appears in:_
- [api.v1beta1.VirtualMCPServer](#apiv1beta1virtualmcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `incomingAuth` _[api.v1beta1.IncomingAuthConfig](#apiv1beta1incomingauthconfig)_ | IncomingAuth configures authentication for clients connecting to the Virtual MCP server.<br />Must be explicitly set - use "anonymous" type when no authentication is required.<br />This field takes precedence over config.IncomingAuth and should be preferred because it<br />supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure<br />dynamic discovery of credentials, rather than requiring secrets to be embedded in config. |  | Required: \{\} <br /> |
| `outgoingAuth` _[api.v1beta1.OutgoingAuthConfig](#apiv1beta1outgoingauthconfig)_ | OutgoingAuth configures authentication from Virtual MCP to backend MCPServers.<br />This field takes precedence over config.OutgoingAuth and should be preferred because it<br />supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure<br />dynamic discovery of credentials, rather than requiring secrets to be embedded in config. |  | Optional: \{\} <br /> |
| `serviceType` _string_ | ServiceType specifies the Kubernetes service type for the Virtual MCP server | ClusterIP | Enum: [ClusterIP NodePort LoadBalancer] <br />Optional: \{\} <br /> |
| `sessionAffinity` _string_ | SessionAffinity controls whether the Service routes repeated client connections to the same pod.<br />MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.<br />Set to "None" for stateless servers or when using an external load balancer with its own affinity. | ClientIP | Enum: [ClientIP None] <br />Optional: \{\} <br /> |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the Virtual MCP server.<br />If not specified, a ServiceAccount will be created automatically and used by the Virtual MCP server. |  | Optional: \{\} <br /> |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the Virtual MCP server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the Virtual MCP server runs in, you must specify<br />the 'vmcp' container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br />Optional: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup that defines backend workloads.<br />The referenced MCPGroup must exist in the same namespace. |  | Required: \{\} <br /> |
| `config` _[vmcp.config.Config](#vmcpconfigconfig)_ | Config is the Virtual MCP server configuration.<br />The audit config from here is also supported, but not required. |  | Type: object <br />Optional: \{\} <br /> |
| `telemetryConfigRef` _[api.v1beta1.MCPTelemetryConfigReference](#apiv1beta1mcptelemetryconfigreference)_ | TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.<br />The referenced MCPTelemetryConfig must exist in the same namespace as this VirtualMCPServer.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `embeddingServerRef` _[api.v1beta1.EmbeddingServerRef](#apiv1beta1embeddingserverref)_ | EmbeddingServerRef references an existing EmbeddingServer resource by name.<br />When the optimizer is enabled, this field is required to point to a ready EmbeddingServer<br />that provides embedding capabilities.<br />The referenced EmbeddingServer must exist in the same namespace and be ready. |  | Optional: \{\} <br /> |
| `authServerConfig` _[api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)_ | AuthServerConfig configures an embedded OAuth authorization server.<br />When set, the vMCP server acts as an OIDC issuer, drives users through<br />upstream IDPs, and issues ToolHive JWTs. The embedded AS becomes the<br />IncomingAuth OIDC provider — its issuer must match IncomingAuth.OIDCConfigRef<br />so that tokens it issues are accepted by the vMCP's incoming auth middleware.<br />When nil, IncomingAuth uses an external IDP and behavior is unchanged. |  | Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the desired number of vMCP pod replicas.<br />VirtualMCPServer creates a single Deployment for the vMCP aggregator process,<br />so there is only one replicas field (unlike MCPServer which has separate<br />Replicas and BackendReplicas for its two Deployments).<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `sessionStorage` _[api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When nil, no session storage is configured. |  | Optional: \{\} <br /> |


#### api.v1beta1.VirtualMCPServerStatus



VirtualMCPServerStatus defines the observed state of VirtualMCPServer



_Appears in:_
- [api.v1beta1.VirtualMCPServer](#apiv1beta1virtualmcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the VirtualMCPServer's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this VirtualMCPServer |  | Optional: \{\} <br /> |
| `phase` _[api.v1beta1.VirtualMCPServerPhase](#apiv1beta1virtualmcpserverphase)_ | Phase is the current phase of the VirtualMCPServer | Pending | Enum: [Pending Ready Degraded Failed] <br />Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the current phase |  | Optional: \{\} <br /> |
| `url` _string_ | URL is the URL where the Virtual MCP server can be accessed |  | Optional: \{\} <br /> |
| `discoveredBackends` _[api.v1beta1.DiscoveredBackend](#apiv1beta1discoveredbackend) array_ | DiscoveredBackends lists discovered backend configurations from the MCPGroup |  | Optional: \{\} <br /> |
| `backendCount` _integer_ | BackendCount is the number of routable backends (ready + unauthenticated).<br />Excludes unavailable, degraded, and unknown backends. |  | Optional: \{\} <br /> |
| `oidcConfigHash` _string_ | OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection.<br />Only populated when IncomingAuth.OIDCConfigRef is set. |  | Optional: \{\} <br /> |
| `telemetryConfigHash` _string_ | TelemetryConfigHash is the hash of the referenced MCPTelemetryConfig spec for change detection.<br />Only populated when TelemetryConfigRef is set. |  | Optional: \{\} <br /> |


#### api.v1beta1.Volume



Volume represents a volume to mount in a container



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the volume |  | Required: \{\} <br /> |
| `hostPath` _string_ | HostPath is the path on the host to mount |  | Required: \{\} <br /> |
| `mountPath` _string_ | MountPath is the path in the container to mount to |  | Required: \{\} <br /> |
| `readOnly` _boolean_ | ReadOnly specifies whether the volume should be mounted read-only | false | Optional: \{\} <br /> |


#### api.v1beta1.WorkloadReference



WorkloadReference identifies a workload that references a shared configuration resource.
Namespace is implicit — cross-namespace references are not supported.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigStatus](#apiv1beta1mcpexternalauthconfigstatus)
- [api.v1beta1.MCPOIDCConfigStatus](#apiv1beta1mcpoidcconfigstatus)
- [api.v1beta1.MCPTelemetryConfigStatus](#apiv1beta1mcptelemetryconfigstatus)
- [api.v1beta1.MCPToolConfigStatus](#apiv1beta1mcptoolconfigstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind is the type of workload resource |  | Enum: [MCPServer VirtualMCPServer MCPRemoteProxy] <br />Required: \{\} <br /> |
| `name` _string_ | Name is the name of the workload resource |  | MinLength: 1 <br />Required: \{\} <br /> |


