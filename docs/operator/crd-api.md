# API Reference

## Packages
- [toolhive.stacklok.dev/audit](#toolhivestacklokdevaudit)
- [toolhive.stacklok.dev/authtypes](#toolhivestacklokdevauthtypes)
- [toolhive.stacklok.dev/config](#toolhivestacklokdevconfig)
- [toolhive.stacklok.dev/telemetry](#toolhivestacklokdevtelemetry)
- [toolhive.stacklok.dev/v1alpha1](#toolhivestacklokdevv1alpha1)


## toolhive.stacklok.dev/audit








#### pkg.audit.Config



Config represents the audit logging configuration.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether audit logging is enabled.<br />When true, enables audit logging with the configured options. | false |  |
| `component` _string_ | Component is the component name to use in audit events. |  |  |
| `eventTypes` _string array_ | EventTypes specifies which event types to audit. If empty, all events are audited. |  |  |
| `excludeEventTypes` _string array_ | ExcludeEventTypes specifies which event types to exclude from auditing.<br />This takes precedence over EventTypes. |  |  |
| `includeRequestData` _boolean_ | IncludeRequestData determines whether to include request data in audit logs. | false |  |
| `includeResponseData` _boolean_ | IncludeResponseData determines whether to include response data in audit logs. | false |  |
| `maxDataSize` _integer_ | MaxDataSize limits the size of request/response data included in audit logs (in bytes). | 1024 |  |
| `logFile` _string_ | LogFile specifies the file path for audit logs. If empty, logs to stdout. |  |  |













## toolhive.stacklok.dev/authtypes


#### auth.types.BackendAuthStrategy



BackendAuthStrategy defines how to authenticate to a specific backend.

This struct provides type-safe configuration for different authentication strategies
using HeaderInjection or TokenExchange fields based on the Type field.



_Appears in:_
- [vmcp.config.OutgoingAuthConfig](#vmcpconfigoutgoingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange" |  |  |
| `headerInjection` _[auth.types.HeaderInjectionConfig](#authtypesheaderinjectionconfig)_ | HeaderInjection contains configuration for header injection auth strategy.<br />Used when Type = "header_injection". |  |  |
| `tokenExchange` _[auth.types.TokenExchangeConfig](#authtypestokenexchangeconfig)_ | TokenExchange contains configuration for token exchange auth strategy.<br />Used when Type = "token_exchange". |  |  |


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



## toolhive.stacklok.dev/config


#### vmcp.config.AggregationConfig



AggregationConfig defines tool aggregation and conflict resolution strategies.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conflictResolution` _[pkg.vmcp.ConflictResolutionStrategy](#pkgvmcpconflictresolutionstrategy)_ | ConflictResolution defines the strategy for resolving tool name conflicts.<br />- prefix: Automatically prefix tool names with workload identifier<br />- priority: First workload in priority order wins<br />- manual: Explicitly define overrides for all conflicts | prefix | Enum: [prefix priority manual] <br /> |
| `conflictResolutionConfig` _[vmcp.config.ConflictResolutionConfig](#vmcpconfigconflictresolutionconfig)_ | ConflictResolutionConfig provides configuration for the chosen strategy. |  |  |
| `tools` _[vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig) array_ | Tools defines per-workload tool filtering and overrides. |  |  |
| `excludeAllTools` _boolean_ | ExcludeAllTools excludes all tools from aggregation when true. |  |  |


#### vmcp.config.AuthzConfig



AuthzConfig configures authorization.



_Appears in:_
- [vmcp.config.IncomingAuthConfig](#vmcpconfigincomingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the authz type: "cedar", "none" |  |  |
| `policies` _string array_ | Policies contains Cedar policy definitions (when Type = "cedar"). |  |  |


#### vmcp.config.CircuitBreakerConfig



CircuitBreakerConfig configures circuit breaker behavior.



_Appears in:_
- [vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether circuit breaker is enabled. | false |  |
| `failureThreshold` _integer_ | FailureThreshold is the number of failures before opening the circuit. | 5 |  |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the duration to wait before attempting to close the circuit. | 60s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |


#### vmcp.config.CompositeToolConfig



CompositeToolConfig defines a composite tool workflow.
This matches the YAML structure from the proposal (lines 173-255).



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionSpec](#apiv1alpha1virtualmcpcompositetooldefinitionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the workflow name (unique identifier). |  |  |
| `description` _string_ | Description describes what the workflow does. |  |  |
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  |  |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum workflow execution time. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `steps` _[vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig) array_ | Steps are the workflow steps to execute. |  |  |
| `output` _[vmcp.config.OutputConfig](#vmcpconfigoutputconfig)_ | Output defines the structured output schema for this workflow.<br />If not specified, the workflow returns the last step's output (backward compatible). |  |  |


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
- [api.v1alpha1.VirtualMCPServerSpec](#apiv1alpha1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the virtual MCP server name. |  |  |
| `groupRef` _string_ | Group references an existing MCPGroup that defines backend workloads.<br />In Kubernetes, the referenced MCPGroup must exist in the same namespace. |  | Required: \{\} <br /> |
| `backends` _[vmcp.config.StaticBackendConfig](#vmcpconfigstaticbackendconfig) array_ | Backends defines pre-configured backend servers for static mode.<br />When OutgoingAuth.Source is "inline", this field contains the full list of backend<br />servers with their URLs and transport types, eliminating the need for K8s API access.<br />When OutgoingAuth.Source is "discovered", this field is empty and backends are<br />discovered at runtime via Kubernetes API. |  |  |
| `incomingAuth` _[vmcp.config.IncomingAuthConfig](#vmcpconfigincomingauthconfig)_ | IncomingAuth configures how clients authenticate to the virtual MCP server.<br />When using the Kubernetes operator, this is populated by the converter from<br />VirtualMCPServerSpec.IncomingAuth and any values set here will be superseded. |  |  |
| `outgoingAuth` _[vmcp.config.OutgoingAuthConfig](#vmcpconfigoutgoingauthconfig)_ | OutgoingAuth configures how the virtual MCP server authenticates to backends.<br />When using the Kubernetes operator, this is populated by the converter from<br />VirtualMCPServerSpec.OutgoingAuth and any values set here will be superseded. |  |  |
| `aggregation` _[vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)_ | Aggregation defines tool aggregation and conflict resolution strategies.<br />Supports ToolConfigRef for Kubernetes-native MCPToolConfig resource references. |  |  |
| `compositeTools` _[vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig) array_ | CompositeTools defines inline composite tool workflows.<br />Full workflow definitions are embedded in the configuration.<br />For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs. |  |  |
| `compositeToolRefs` _[vmcp.config.CompositeToolRef](#vmcpconfigcompositetoolref) array_ | CompositeToolRefs references VirtualMCPCompositeToolDefinition resources<br />for complex, reusable workflows. Only applicable when running in Kubernetes.<br />Referenced resources must be in the same namespace as the VirtualMCPServer. |  |  |
| `operational` _[vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)_ | Operational configures operational settings. |  |  |
| `metadata` _object (keys:string, values:string)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `telemetry` _[pkg.telemetry.Config](#pkgtelemetryconfig)_ | Telemetry configures OpenTelemetry-based observability for the Virtual MCP server<br />including distributed tracing, OTLP metrics export, and Prometheus metrics endpoint. |  |  |
| `audit` _[pkg.audit.Config](#pkgauditconfig)_ | Audit configures audit logging for the Virtual MCP server.<br />When present, audit logs include MCP protocol operations.<br />See audit.Config for available configuration options. |  |  |
| `optimizer` _[vmcp.config.OptimizerConfig](#vmcpconfigoptimizerconfig)_ | Optimizer configures the MCP optimizer for context optimization on large toolsets.<br />When enabled, vMCP exposes optim.find_tool and optim.call_tool operations to clients<br />instead of all backend tools directly. This reduces token usage by allowing<br />LLMs to discover relevant tools on demand rather than receiving all tool definitions. |  |  |


#### vmcp.config.ConflictResolutionConfig



ConflictResolutionConfig provides configuration for conflict resolution strategies.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `prefixFormat` _string_ | PrefixFormat defines the prefix format for the "prefix" strategy.<br />Supports placeholders: \{workload\}, \{workload\}_, \{workload\}. | \{workload\}_ |  |
| `priorityOrder` _string array_ | PriorityOrder defines the workload priority order for the "priority" strategy. |  |  |






#### vmcp.config.ElicitationResponseConfig



ElicitationResponseConfig defines how to handle user responses to elicitation requests.



_Appears in:_
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `action` _string_ | Action defines the action to take when the user declines or cancels<br />- skip_remaining: Skip remaining steps in the workflow<br />- abort: Abort the entire workflow execution<br />- continue: Continue to the next step | abort | Enum: [skip_remaining abort continue] <br /> |


#### vmcp.config.FailureHandlingConfig



FailureHandlingConfig configures failure handling behavior.



_Appears in:_
- [vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `healthCheckInterval` _[vmcp.config.Duration](#vmcpconfigduration)_ | HealthCheckInterval is the interval between health checks. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `unhealthyThreshold` _integer_ | UnhealthyThreshold is the number of consecutive failures before marking unhealthy. | 3 |  |
| `partialFailureMode` _string_ | PartialFailureMode defines behavior when some backends are unavailable.<br />- fail: Fail entire request if any backend is unavailable<br />- best_effort: Continue with available backends | fail | Enum: [fail best_effort] <br /> |
| `circuitBreaker` _[vmcp.config.CircuitBreakerConfig](#vmcpconfigcircuitbreakerconfig)_ | CircuitBreaker configures circuit breaker behavior. |  |  |


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
| `scopes` _string array_ | Scopes are the required OAuth scopes. |  |  |
| `protectedResourceAllowPrivateIp` _boolean_ | ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses<br />Use with caution - only enable for trusted internal IDPs or testing |  |  |
| `insecureAllowHttp` _boolean_ | InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing<br />WARNING: This is insecure and should NEVER be used in production |  |  |


#### vmcp.config.OperationalConfig



OperationalConfig contains operational settings.
OperationalConfig defines operational settings like timeouts and health checks.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `logLevel` _string_ | LogLevel sets the logging level for the Virtual MCP server.<br />The only valid value is "debug" to enable debug logging.<br />When omitted or empty, the server uses info level logging. |  | Enum: [debug] <br /> |
| `timeouts` _[vmcp.config.TimeoutConfig](#vmcpconfigtimeoutconfig)_ | Timeouts configures timeout settings. |  |  |
| `failureHandling` _[vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)_ | FailureHandling configures failure handling behavior. |  |  |


#### vmcp.config.OptimizerConfig



OptimizerConfig configures the MCP optimizer for semantic tool discovery.
The optimizer reduces token usage by allowing LLMs to discover relevant tools
on demand rather than receiving all tool definitions upfront.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled determines whether the optimizer is active.<br />When true, vMCP exposes optim.find_tool and optim.call_tool instead of all backend tools. |  |  |
| `embeddingBackend` _string_ | EmbeddingBackend specifies the embedding provider: "ollama", "openai-compatible", or "placeholder".<br />- "ollama": Uses local Ollama HTTP API for embeddings<br />- "openai-compatible": Uses OpenAI-compatible API (vLLM, OpenAI, etc.)<br />- "placeholder": Uses deterministic hash-based embeddings (for testing/development) |  | Enum: [ollama openai-compatible placeholder] <br /> |
| `embeddingURL` _string_ | EmbeddingURL is the base URL for the embedding service (Ollama or OpenAI-compatible API).<br />Required when EmbeddingBackend is "ollama" or "openai-compatible".<br />Examples:<br />- Ollama: "http://localhost:11434"<br />- vLLM: "http://vllm-service:8000/v1"<br />- OpenAI: "https://api.openai.com/v1" |  |  |
| `embeddingModel` _string_ | EmbeddingModel is the model name to use for embeddings.<br />Required when EmbeddingBackend is "ollama" or "openai-compatible".<br />Examples:<br />- Ollama: "nomic-embed-text", "all-minilm"<br />- vLLM: "BAAI/bge-small-en-v1.5"<br />- OpenAI: "text-embedding-3-small" |  |  |
| `embeddingDimension` _integer_ | EmbeddingDimension is the dimension of the embedding vectors.<br />Common values:<br />- 384: all-MiniLM-L6-v2, nomic-embed-text<br />- 768: BAAI/bge-small-en-v1.5<br />- 1536: OpenAI text-embedding-3-small |  | Minimum: 1 <br /> |
| `persistPath` _string_ | PersistPath is the optional filesystem path for persisting the chromem-go database.<br />If empty, the database will be in-memory only (ephemeral).<br />When set, tool metadata and embeddings are persisted to disk for faster restarts. |  |  |
| `ftsDBPath` _string_ | FTSDBPath is the path to the SQLite FTS5 database for BM25 text search.<br />If empty, defaults to ":memory:" for in-memory FTS5, or "\{PersistPath\}/fts.db" if PersistPath is set.<br />Hybrid search (semantic + BM25) is always enabled. |  |  |
| `hybridSearchRatio` _integer_ | HybridSearchRatio controls the mix of semantic vs BM25 results in hybrid search.<br />Value range: 0-100 (representing percentage, 0 = all BM25, 100 = all semantic).<br />Default: 70 (70% semantic, 30% BM25)<br />Only used when FTSDBPath is set. |  | Maximum: 100 <br />Minimum: 0 <br /> |


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
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionSpec](#apiv1alpha1virtualmcpcompositetooldefinitionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `properties` _object (keys:string, values:[vmcp.config.OutputProperty](#vmcpconfigoutputproperty))_ | Properties defines the output properties.<br />Map key is the property name, value is the property definition. |  |  |
| `required` _string array_ | Required lists property names that must be present in the output. |  |  |


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
| `description` _string_ | Description is a human-readable description exposed to clients and models |  |  |
| `value` _string_ | Value is a template string for constructing the runtime value.<br />For object types, this can be a JSON string that will be deserialized.<br />Supports template syntax: \{\{.steps.step_id.output.field\}\}, \{\{.params.param_name\}\} |  |  |
| `properties` _object (keys:string, values:[vmcp.config.OutputProperty](#vmcpconfigoutputproperty))_ | Properties defines nested properties for object types.<br />Each nested property has full metadata (type, description, value/properties). |  | Schemaless: \{\} <br />Type: object <br /> |
| `default` _[pkg.json.Any](#pkgjsonany)_ | Default is the fallback value if template expansion fails.<br />Type coercion is applied to match the declared Type. |  | Schemaless: \{\} <br /> |


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
| `metadata` _object (keys:string, values:string)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |


#### vmcp.config.StepErrorHandling



StepErrorHandling defines error handling behavior for workflow steps.



_Appears in:_
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `action` _string_ | Action defines the action to take on error | abort | Enum: [abort continue retry] <br /> |
| `retryCount` _integer_ | RetryCount is the maximum number of retries<br />Only used when Action is "retry" |  |  |
| `retryDelay` _[vmcp.config.Duration](#vmcpconfigduration)_ | RetryDelay is the delay between retry attempts<br />Only used when Action is "retry" |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |


#### vmcp.config.TimeoutConfig



TimeoutConfig configures timeout settings.



_Appears in:_
- [vmcp.config.OperationalConfig](#vmcpconfigoperationalconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `default` _[vmcp.config.Duration](#vmcpconfigduration)_ | Default is the default timeout for backend requests. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `perWorkload` _object (keys:string, values:[vmcp.config.Duration](#vmcpconfigduration))_ | PerWorkload defines per-workload timeout overrides. |  |  |


#### vmcp.config.ToolConfigRef



ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.
Only used when running in Kubernetes with the operator.



_Appears in:_
- [vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPToolConfig resource in the same namespace. |  | Required: \{\} <br /> |


#### vmcp.config.ToolOverride



ToolOverride defines tool name and description overrides.



_Appears in:_
- [vmcp.config.WorkloadToolConfig](#vmcpconfigworkloadtoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the new tool name (for renaming). |  |  |
| `description` _string_ | Description is the new tool description. |  |  |




#### vmcp.config.WorkflowStepConfig



WorkflowStepConfig defines a single workflow step.
This matches the proposal's step configuration (lines 180-255).



_Appears in:_
- [vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig)
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionSpec](#apiv1alpha1virtualmcpcompositetooldefinitionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `id` _string_ | ID is the unique identifier for this step. |  | Required: \{\} <br /> |
| `type` _string_ | Type is the step type (tool, elicitation, etc.) | tool | Enum: [tool elicitation] <br /> |
| `tool` _string_ | Tool is the tool to call (format: "workload.tool_name")<br />Only used when Type is "tool" |  |  |
| `arguments` _[pkg.json.Map](#pkgjsonmap)_ | Arguments is a map of argument values with template expansion support.<br />Supports Go template syntax with .params and .steps for string values.<br />Non-string values (integers, booleans, arrays, objects) are passed as-is.<br />Note: the templating is only supported on the first level of the key-value pairs. |  | Type: object <br /> |
| `condition` _string_ | Condition is a template expression that determines if the step should execute |  |  |
| `dependsOn` _string array_ | DependsOn lists step IDs that must complete before this step |  |  |
| `onError` _[vmcp.config.StepErrorHandling](#vmcpconfigsteperrorhandling)_ | OnError defines error handling behavior |  |  |
| `message` _string_ | Message is the elicitation message<br />Only used when Type is "elicitation" |  |  |
| `schema` _[pkg.json.Map](#pkgjsonmap)_ | Schema defines the expected response schema for elicitation |  | Type: object <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum execution time for this step |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `onDecline` _[vmcp.config.ElicitationResponseConfig](#vmcpconfigelicitationresponseconfig)_ | OnDecline defines the action to take when the user explicitly declines the elicitation<br />Only used when Type is "elicitation" |  |  |
| `onCancel` _[vmcp.config.ElicitationResponseConfig](#vmcpconfigelicitationresponseconfig)_ | OnCancel defines the action to take when the user cancels/dismisses the elicitation<br />Only used when Type is "elicitation" |  |  |
| `defaultResults` _[pkg.json.Map](#pkgjsonmap)_ | DefaultResults provides fallback output values when this step is skipped<br />(due to condition evaluating to false) or fails (when onError.action is "continue").<br />Each key corresponds to an output field name referenced by downstream steps.<br />Required if the step may be skipped AND downstream steps reference this step's output. |  | Schemaless: \{\} <br /> |


#### vmcp.config.WorkloadToolConfig



WorkloadToolConfig defines tool filtering and overrides for a specific workload.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `workload` _string_ | Workload is the name of the backend MCPServer workload. |  | Required: \{\} <br /> |
| `toolConfigRef` _[vmcp.config.ToolConfigRef](#vmcpconfigtoolconfigref)_ | ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.<br />If specified, Filter and Overrides are ignored.<br />Only used when running in Kubernetes with the operator. |  |  |
| `filter` _string array_ | Filter is an inline list of tool names to allow (allow list).<br />Only used if ToolConfigRef is not specified. |  |  |
| `overrides` _object (keys:string, values:[vmcp.config.ToolOverride](#vmcpconfigtooloverride))_ | Overrides is an inline map of tool overrides.<br />Only used if ToolConfigRef is not specified. |  |  |
| `excludeAll` _boolean_ | ExcludeAll excludes all tools from this workload when true. |  |  |





## toolhive.stacklok.dev/telemetry


#### pkg.telemetry.Config



Config holds the configuration for OpenTelemetry instrumentation.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint is the OTLP endpoint URL |  |  |
| `serviceName` _string_ | ServiceName is the service name for telemetry.<br />When omitted, defaults to the server name (e.g., VirtualMCPServer name). |  |  |
| `serviceVersion` _string_ | ServiceVersion is the service version for telemetry.<br />When omitted, defaults to the ToolHive version. |  |  |
| `tracingEnabled` _boolean_ | TracingEnabled controls whether distributed tracing is enabled.<br />When false, no tracer provider is created even if an endpoint is configured. | false |  |
| `metricsEnabled` _boolean_ | MetricsEnabled controls whether OTLP metrics are enabled.<br />When false, OTLP metrics are not sent even if an endpoint is configured.<br />This is independent of EnablePrometheusMetricsPath. | false |  |
| `samplingRate` _string_ | SamplingRate is the trace sampling rate (0.0-1.0) as a string.<br />Only used when TracingEnabled is true.<br />Example: "0.05" for 5% sampling. | 0.05 |  |
| `headers` _object (keys:string, values:string)_ | Headers contains authentication headers for the OTLP endpoint. |  |  |
| `insecure` _boolean_ | Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint. | false |  |
| `enablePrometheusMetricsPath` _boolean_ | EnablePrometheusMetricsPath controls whether to expose Prometheus-style /metrics endpoint.<br />The metrics are served on the main transport port at /metrics.<br />This is separate from OTLP metrics which are sent to the Endpoint. | false |  |
| `environmentVariables` _string array_ | EnvironmentVariables is a list of environment variable names that should be<br />included in telemetry spans as attributes. Only variables in this list will<br />be read from the host machine and included in spans for observability.<br />Example: ["NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"] |  |  |
| `customAttributes` _object (keys:string, values:string)_ | CustomAttributes contains custom resource attributes to be added to all telemetry signals.<br />These are parsed from CLI flags (--otel-custom-attributes) or environment variables<br />(OTEL_RESOURCE_ATTRIBUTES) as key=value pairs. |  |  |











## toolhive.stacklok.dev/v1alpha1
### Resource Types
- [api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver)
- [api.v1alpha1.EmbeddingServerList](#apiv1alpha1embeddingserverlist)
- [api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig)
- [api.v1alpha1.MCPExternalAuthConfigList](#apiv1alpha1mcpexternalauthconfiglist)
- [api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup)
- [api.v1alpha1.MCPGroupList](#apiv1alpha1mcpgrouplist)
- [api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry)
- [api.v1alpha1.MCPRegistryList](#apiv1alpha1mcpregistrylist)
- [api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy)
- [api.v1alpha1.MCPRemoteProxyList](#apiv1alpha1mcpremoteproxylist)
- [api.v1alpha1.MCPServer](#apiv1alpha1mcpserver)
- [api.v1alpha1.MCPServerList](#apiv1alpha1mcpserverlist)
- [api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig)
- [api.v1alpha1.MCPToolConfigList](#apiv1alpha1mcptoolconfiglist)
- [api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition)
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionList](#apiv1alpha1virtualmcpcompositetooldefinitionlist)
- [api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver)
- [api.v1alpha1.VirtualMCPServerList](#apiv1alpha1virtualmcpserverlist)



#### api.v1alpha1.APIPhase

_Underlying type:_ _string_

APIPhase represents the API service state

_Validation:_
- Enum: [NotStarted Deploying Ready Unhealthy Error]

_Appears in:_
- [api.v1alpha1.APIStatus](#apiv1alpha1apistatus)

| Field | Description |
| --- | --- |
| `NotStarted` | APIPhaseNotStarted means API deployment has not been created<br /> |
| `Deploying` | APIPhaseDeploying means API is being deployed<br /> |
| `Ready` | APIPhaseReady means API is ready to serve requests<br /> |
| `Unhealthy` | APIPhaseUnhealthy means API is deployed but not healthy<br /> |
| `Error` | APIPhaseError means API deployment failed<br /> |


#### api.v1alpha1.APISource



APISource defines API source configuration for ToolHive Registry APIs
Phase 1: Supports ToolHive API endpoints (no pagination)
Phase 2: Will add support for upstream MCP Registry API with pagination



_Appears in:_
- [api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint is the base API URL (without path)<br />The controller will append the appropriate paths:<br />Phase 1 (ToolHive API):<br />  - /v0/servers - List all servers (single response, no pagination)<br />  - /v0/servers/\{name\} - Get specific server (future)<br />  - /v0/info - Get registry metadata (future)<br />Example: "http://my-registry-api.default.svc.cluster.local/api" |  | MinLength: 1 <br />Pattern: `^https?://.*` <br />Required: \{\} <br /> |


#### api.v1alpha1.APIStatus



APIStatus provides detailed information about the API service



_Appears in:_
- [api.v1alpha1.MCPRegistryStatus](#apiv1alpha1mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1alpha1.APIPhase](#apiv1alpha1apiphase)_ | Phase represents the current API service phase |  | Enum: [NotStarted Deploying Ready Unhealthy Error] <br /> |
| `message` _string_ | Message provides additional information about the API status |  |  |
| `endpoint` _string_ | Endpoint is the URL where the API is accessible |  |  |
| `readySince` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | ReadySince is the timestamp when the API became ready |  |  |


#### api.v1alpha1.AuditConfig



AuditConfig defines audit logging configuration for the MCP server



_Appears in:_
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether audit logging is enabled<br />When true, enables audit logging with default configuration | false |  |


#### api.v1alpha1.AuthzConfigRef



AuthzConfigRef defines a reference to authorization configuration



_Appears in:_
- [api.v1alpha1.IncomingAuthConfig](#apiv1alpha1incomingauthconfig)
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of authorization configuration | configMap | Enum: [configMap inline] <br /> |
| `configMap` _[api.v1alpha1.ConfigMapAuthzRef](#apiv1alpha1configmapauthzref)_ | ConfigMap references a ConfigMap containing authorization configuration<br />Only used when Type is "configMap" |  |  |
| `inline` _[api.v1alpha1.InlineAuthzConfig](#apiv1alpha1inlineauthzconfig)_ | Inline contains direct authorization configuration<br />Only used when Type is "inline" |  |  |


#### api.v1alpha1.BackendAuthConfig



BackendAuthConfig defines authentication configuration for a backend MCPServer



_Appears in:_
- [api.v1alpha1.OutgoingAuthConfig](#apiv1alpha1outgoingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type defines the authentication type |  | Enum: [discovered external_auth_config_ref] <br />Required: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1alpha1.ExternalAuthConfigRef](#apiv1alpha1externalauthconfigref)_ | ExternalAuthConfigRef references an MCPExternalAuthConfig resource<br />Only used when Type is "external_auth_config_ref" |  |  |


#### api.v1alpha1.BearerTokenConfig



BearerTokenConfig holds configuration for bearer token authentication.
This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets.
For security reasons, only secret references are supported (no plaintext values).



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfigSpec](#apiv1alpha1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenSecretRef` _[api.v1alpha1.SecretKeyRef](#apiv1alpha1secretkeyref)_ | TokenSecretRef references a Kubernetes Secret containing the bearer token |  | Required: \{\} <br /> |


#### api.v1alpha1.CABundleSource



CABundleSource defines a source for CA certificate bundles.



_Appears in:_
- [api.v1alpha1.ConfigMapOIDCRef](#apiv1alpha1configmapoidcref)
- [api.v1alpha1.InlineOIDCConfig](#apiv1alpha1inlineoidcconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `configMapRef` _[ConfigMapKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#configmapkeyselector-v1-core)_ | ConfigMapRef references a ConfigMap containing the CA certificate bundle.<br />If Key is not specified, it defaults to "ca.crt". |  |  |


#### api.v1alpha1.ConfigMapAuthzRef



ConfigMapAuthzRef references a ConfigMap containing authorization configuration



_Appears in:_
- [api.v1alpha1.AuthzConfigRef](#apiv1alpha1authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the authorization configuration | authz.json |  |


#### api.v1alpha1.ConfigMapOIDCRef



ConfigMapOIDCRef references a ConfigMap containing OIDC configuration



_Appears in:_
- [api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the OIDC configuration | oidc.json |  |
| `caBundleRef` _[api.v1alpha1.CABundleSource](#apiv1alpha1cabundlesource)_ | CABundleRef references a ConfigMap containing the CA certificate bundle.<br />When specified, ToolHive auto-mounts the ConfigMap and auto-computes ThvCABundlePath.<br />If the ConfigMap data contains an explicit thvCABundlePath key, it takes precedence. |  |  |




#### api.v1alpha1.EmbeddingResourceOverrides



EmbeddingResourceOverrides defines overrides for annotations and labels on created resources



_Appears in:_
- [api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `statefulSet` _[api.v1alpha1.EmbeddingStatefulSetOverrides](#apiv1alpha1embeddingstatefulsetoverrides)_ | StatefulSet defines overrides for the StatefulSet resource |  |  |
| `service` _[api.v1alpha1.ResourceMetadataOverrides](#apiv1alpha1resourcemetadataoverrides)_ | Service defines overrides for the Service resource |  |  |
| `persistentVolumeClaim` _[api.v1alpha1.ResourceMetadataOverrides](#apiv1alpha1resourcemetadataoverrides)_ | PersistentVolumeClaim defines overrides for the PVC resource |  |  |


#### api.v1alpha1.EmbeddingServer



EmbeddingServer is the Schema for the embeddingservers API



_Appears in:_
- [api.v1alpha1.EmbeddingServerList](#apiv1alpha1embeddingserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `EmbeddingServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)_ |  |  |  |
| `status` _[api.v1alpha1.EmbeddingServerStatus](#apiv1alpha1embeddingserverstatus)_ |  |  |  |


#### api.v1alpha1.EmbeddingServerList



EmbeddingServerList contains a list of EmbeddingServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `EmbeddingServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver) array_ |  |  |  |


#### api.v1alpha1.EmbeddingServerPhase

_Underlying type:_ _string_

EmbeddingServerPhase is the phase of the EmbeddingServer

_Validation:_
- Enum: [Pending Downloading Running Failed Terminating]

_Appears in:_
- [api.v1alpha1.EmbeddingServerStatus](#apiv1alpha1embeddingserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | EmbeddingServerPhasePending means the EmbeddingServer is being created<br /> |
| `Downloading` | EmbeddingServerPhaseDownloading means the model is being downloaded<br /> |
| `Running` | EmbeddingServerPhaseRunning means the EmbeddingServer is running and ready<br /> |
| `Failed` | EmbeddingServerPhaseFailed means the EmbeddingServer failed to start<br /> |
| `Terminating` | EmbeddingServerPhaseTerminating means the EmbeddingServer is being deleted<br /> |


#### api.v1alpha1.EmbeddingServerSpec



EmbeddingServerSpec defines the desired state of EmbeddingServer



_Appears in:_
- [api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `model` _string_ | Model is the HuggingFace embedding model to use (e.g., "sentence-transformers/all-MiniLM-L6-v2") |  | Required: \{\} <br /> |
| `hfTokenSecretRef` _[api.v1alpha1.SecretKeyRef](#apiv1alpha1secretkeyref)_ | HFTokenSecretRef is a reference to a Kubernetes Secret containing the huggingface token.<br />If provided, the secret value will be provided to the embedding server for authentication with huggingface. |  |  |
| `image` _string_ | Image is the container image for huggingface-embedding-inference | ghcr.io/huggingface/text-embeddings-inference:latest | Required: \{\} <br /> |
| `imagePullPolicy` _string_ | ImagePullPolicy defines the pull policy for the container image | IfNotPresent | Enum: [Always Never IfNotPresent] <br /> |
| `port` _integer_ | Port is the port to expose the embedding service on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `args` _string array_ | Args are additional arguments to pass to the embedding inference server |  |  |
| `env` _[api.v1alpha1.EnvVar](#apiv1alpha1envvar) array_ | Env are environment variables to set in the container |  |  |
| `resources` _[api.v1alpha1.ResourceRequirements](#apiv1alpha1resourcerequirements)_ | Resources defines compute resources for the embedding server |  |  |
| `modelCache` _[api.v1alpha1.ModelCacheConfig](#apiv1alpha1modelcacheconfig)_ | ModelCache configures persistent storage for downloaded models<br />When enabled, models are cached in a PVC and reused across pod restarts |  |  |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec allows customizing the pod (node selection, tolerations, etc.)<br />This field accepts a PodTemplateSpec object as JSON/YAML.<br />Note that to modify the specific container the embedding server runs in, you must specify<br />the 'embedding' container name in the PodTemplateSpec. |  | Type: object <br /> |
| `resourceOverrides` _[api.v1alpha1.EmbeddingResourceOverrides](#apiv1alpha1embeddingresourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  |  |
| `replicas` _integer_ | Replicas is the number of embedding server replicas to run | 1 | Minimum: 1 <br /> |


#### api.v1alpha1.EmbeddingServerStatus



EmbeddingServerStatus defines the observed state of EmbeddingServer



_Appears in:_
- [api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the EmbeddingServer's state |  |  |
| `phase` _[api.v1alpha1.EmbeddingServerPhase](#apiv1alpha1embeddingserverphase)_ | Phase is the current phase of the EmbeddingServer |  | Enum: [Pending Downloading Running Failed Terminating] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |
| `url` _string_ | URL is the URL where the embedding service can be accessed |  |  |
| `readyReplicas` _integer_ | ReadyReplicas is the number of ready replicas |  |  |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation most recently observed by the controller |  |  |


#### api.v1alpha1.EmbeddingStatefulSetOverrides



EmbeddingStatefulSetOverrides defines overrides specific to the embedding statefulset



_Appears in:_
- [api.v1alpha1.EmbeddingResourceOverrides](#apiv1alpha1embeddingresourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  |  |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  |  |
| `podTemplateMetadataOverrides` _[api.v1alpha1.ResourceMetadataOverrides](#apiv1alpha1resourcemetadataoverrides)_ | PodTemplateMetadataOverrides defines metadata overrides for the pod template |  |  |


#### api.v1alpha1.EnvVar



EnvVar represents an environment variable in a container



_Appears in:_
- [api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)
- [api.v1alpha1.ProxyDeploymentOverrides](#apiv1alpha1proxydeploymentoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the environment variable |  | Required: \{\} <br /> |
| `value` _string_ | Value of the environment variable |  | Required: \{\} <br /> |


#### api.v1alpha1.ExternalAuthConfigRef



ExternalAuthConfigRef defines a reference to a MCPExternalAuthConfig resource.
The referenced MCPExternalAuthConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1alpha1.BackendAuthConfig](#apiv1alpha1backendauthconfig)
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPExternalAuthConfig resource |  | Required: \{\} <br /> |


#### api.v1alpha1.ExternalAuthType

_Underlying type:_ _string_

ExternalAuthType represents the type of external authentication



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfigSpec](#apiv1alpha1mcpexternalauthconfigspec)

| Field | Description |
| --- | --- |
| `tokenExchange` | ExternalAuthTypeTokenExchange is the type for RFC-8693 token exchange<br /> |
| `headerInjection` | ExternalAuthTypeHeaderInjection is the type for custom header injection<br /> |
| `bearerToken` | ExternalAuthTypeBearerToken is the type for bearer token authentication<br />This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets<br /> |
| `unauthenticated` | ExternalAuthTypeUnauthenticated is the type for no authentication<br />This should only be used for backends on trusted networks (e.g., localhost, VPC)<br />or when authentication is handled by network-level security<br /> |


#### api.v1alpha1.GitSource



GitSource defines Git repository source configuration



_Appears in:_
- [api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repository` _string_ | Repository is the Git repository URL (HTTP/HTTPS/SSH) |  | MinLength: 1 <br />Pattern: `^(file:///\|https?://\|git@\|ssh://\|git://).*` <br />Required: \{\} <br /> |
| `branch` _string_ | Branch is the Git branch to use (mutually exclusive with Tag and Commit) |  | MinLength: 1 <br /> |
| `tag` _string_ | Tag is the Git tag to use (mutually exclusive with Branch and Commit) |  | MinLength: 1 <br /> |
| `commit` _string_ | Commit is the Git commit SHA to use (mutually exclusive with Branch and Tag) |  | MinLength: 1 <br /> |
| `path` _string_ | Path is the path to the registry file within the repository | registry.json | Pattern: `^.*\.json$` <br /> |


#### api.v1alpha1.HeaderInjectionConfig



HeaderInjectionConfig holds configuration for custom HTTP header injection authentication.
This allows injecting a secret-based header value into requests to backend MCP servers.
For security reasons, only secret references are supported (no plaintext values).



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfigSpec](#apiv1alpha1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `headerName` _string_ | HeaderName is the name of the HTTP header to inject |  | MinLength: 1 <br />Required: \{\} <br /> |
| `valueSecretRef` _[api.v1alpha1.SecretKeyRef](#apiv1alpha1secretkeyref)_ | ValueSecretRef references a Kubernetes Secret containing the header value |  | Required: \{\} <br /> |


#### api.v1alpha1.IncomingAuthConfig



IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server



_Appears in:_
- [api.v1alpha1.VirtualMCPServerSpec](#apiv1alpha1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type defines the authentication type: anonymous or oidc<br />When no authentication is required, explicitly set this to "anonymous" |  | Enum: [anonymous oidc] <br />Required: \{\} <br /> |
| `oidcConfig` _[api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)_ | OIDCConfig defines OIDC authentication configuration<br />Reuses MCPServer OIDC patterns |  |  |
| `authzConfig` _[api.v1alpha1.AuthzConfigRef](#apiv1alpha1authzconfigref)_ | AuthzConfig defines authorization policy configuration<br />Reuses MCPServer authz patterns |  |  |


#### api.v1alpha1.InlineAuthzConfig



InlineAuthzConfig contains direct authorization configuration



_Appears in:_
- [api.v1alpha1.AuthzConfigRef](#apiv1alpha1authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policies` _string array_ | Policies is a list of Cedar policy strings |  | MinItems: 1 <br />Required: \{\} <br /> |
| `entitiesJson` _string_ | EntitiesJSON is a JSON string representing Cedar entities | [] |  |


#### api.v1alpha1.InlineOIDCConfig



InlineOIDCConfig contains direct OIDC configuration



_Appears in:_
- [api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the OIDC issuer URL |  | Required: \{\} <br /> |
| `audience` _string_ | Audience is the expected audience for the token |  |  |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from |  |  |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint |  |  |
| `clientId` _string_ | ClientID is the OIDC client ID |  |  |
| `clientSecret` _string_ | ClientSecret is the client secret for introspection (optional)<br />Deprecated: Use ClientSecretRef instead for better security |  |  |
| `clientSecretRef` _[api.v1alpha1.SecretKeyRef](#apiv1alpha1secretkeyref)_ | ClientSecretRef is a reference to a Kubernetes Secret containing the client secret<br />If both ClientSecret and ClientSecretRef are provided, ClientSecretRef takes precedence |  |  |
| `thvCABundlePath` _string_ | ThvCABundlePath is the path to CA certificate bundle file for HTTPS requests.<br />Deprecated: Use CABundleRef instead. ThvCABundlePath requires the CA bundle to<br />already exist in the proxy runner container (e.g., Kubernetes service account CA at<br />/var/run/secrets/kubernetes.io/serviceaccount/ca.crt). For custom CA certificates,<br />use CABundleRef which automatically mounts the ConfigMap and computes the path.<br />This field will be removed when the API graduates to v1beta1. |  |  |
| `caBundleRef` _[api.v1alpha1.CABundleSource](#apiv1alpha1cabundlesource)_ | CABundleRef references a ConfigMap containing the CA certificate bundle.<br />When specified, ToolHive auto-mounts the ConfigMap and auto-computes ThvCABundlePath.<br />If ThvCABundlePath is explicitly set, it takes precedence over CABundleRef. |  |  |
| `jwksAuthTokenPath` _string_ | JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests<br />The file must be mounted into the pod (e.g., via Secret volume) |  |  |
| `jwksAllowPrivateIP` _boolean_ | JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses<br />Use with caution - only enable for trusted internal IDPs | false |  |
| `protectedResourceAllowPrivateIP` _boolean_ | ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses<br />Use with caution - only enable for trusted internal IDPs or testing | false |  |
| `insecureAllowHTTP` _boolean_ | InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing<br />WARNING: This is insecure and should NEVER be used in production<br />Only enable for local development, testing, or trusted internal networks | false |  |
| `scopes` _string array_ | Scopes is the list of OAuth scopes to advertise in the well-known endpoint (RFC 9728)<br />If empty, defaults to ["openid"] |  |  |


#### api.v1alpha1.KubernetesOIDCConfig



KubernetesOIDCConfig configures OIDC for Kubernetes service account token validation



_Appears in:_
- [api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceAccount` _string_ | ServiceAccount is the name of the service account to validate tokens for<br />If empty, uses the pod's service account |  |  |
| `namespace` _string_ | Namespace is the namespace of the service account<br />If empty, uses the MCPServer's namespace |  |  |
| `audience` _string_ | Audience is the expected audience for the token | toolhive |  |
| `issuer` _string_ | Issuer is the OIDC issuer URL | https://kubernetes.default.svc |  |
| `jwksUrl` _string_ | JWKSURL is the URL to fetch the JWKS from<br />If empty, OIDC discovery will be used to automatically determine the JWKS URL |  |  |
| `introspectionUrl` _string_ | IntrospectionURL is the URL for token introspection endpoint<br />If empty, OIDC discovery will be used to automatically determine the introspection URL |  |  |
| `useClusterAuth` _boolean_ | UseClusterAuth enables using the Kubernetes cluster's CA bundle and service account token<br />When true, uses /var/run/secrets/kubernetes.io/serviceaccount/ca.crt for TLS verification<br />and /var/run/secrets/kubernetes.io/serviceaccount/token for bearer token authentication<br />Defaults to true if not specified |  |  |


#### api.v1alpha1.MCPExternalAuthConfig



MCPExternalAuthConfig is the Schema for the mcpexternalauthconfigs API.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfigList](#apiv1alpha1mcpexternalauthconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPExternalAuthConfigSpec](#apiv1alpha1mcpexternalauthconfigspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPExternalAuthConfigStatus](#apiv1alpha1mcpexternalauthconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPExternalAuthConfigList



MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig) array_ |  |  |  |


#### api.v1alpha1.MCPExternalAuthConfigSpec



MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[api.v1alpha1.ExternalAuthType](#apiv1alpha1externalauthtype)_ | Type is the type of external authentication to configure |  | Enum: [tokenExchange headerInjection bearerToken unauthenticated] <br />Required: \{\} <br /> |
| `tokenExchange` _[api.v1alpha1.TokenExchangeConfig](#apiv1alpha1tokenexchangeconfig)_ | TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange<br />Only used when Type is "tokenExchange" |  |  |
| `headerInjection` _[api.v1alpha1.HeaderInjectionConfig](#apiv1alpha1headerinjectionconfig)_ | HeaderInjection configures custom HTTP header injection<br />Only used when Type is "headerInjection" |  |  |
| `bearerToken` _[api.v1alpha1.BearerTokenConfig](#apiv1alpha1bearertokenconfig)_ | BearerToken configures bearer token authentication<br />Only used when Type is "bearerToken" |  |  |


#### api.v1alpha1.MCPExternalAuthConfigStatus



MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.<br />It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server. |  |  |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  |  |
| `referencingServers` _string array_ | ReferencingServers is a list of MCPServer resources that reference this MCPExternalAuthConfig<br />This helps track which servers need to be reconciled when this config changes |  |  |


#### api.v1alpha1.MCPGroup



MCPGroup is the Schema for the mcpgroups API



_Appears in:_
- [api.v1alpha1.MCPGroupList](#apiv1alpha1mcpgrouplist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroup` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPGroupSpec](#apiv1alpha1mcpgroupspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPGroupStatus](#apiv1alpha1mcpgroupstatus)_ |  |  |  |


#### api.v1alpha1.MCPGroupList



MCPGroupList contains a list of MCPGroup





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroupList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup) array_ |  |  |  |


#### api.v1alpha1.MCPGroupPhase

_Underlying type:_ _string_

MCPGroupPhase represents the lifecycle phase of an MCPGroup

_Validation:_
- Enum: [Ready Pending Failed]

_Appears in:_
- [api.v1alpha1.MCPGroupStatus](#apiv1alpha1mcpgroupstatus)

| Field | Description |
| --- | --- |
| `Ready` | MCPGroupPhaseReady indicates the MCPGroup is ready<br /> |
| `Pending` | MCPGroupPhasePending indicates the MCPGroup is pending<br /> |
| `Failed` | MCPGroupPhaseFailed indicates the MCPGroup has failed<br /> |


#### api.v1alpha1.MCPGroupSpec



MCPGroupSpec defines the desired state of MCPGroup



_Appears in:_
- [api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `description` _string_ | Description provides human-readable context |  |  |


#### api.v1alpha1.MCPGroupStatus



MCPGroupStatus defines observed state



_Appears in:_
- [api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1alpha1.MCPGroupPhase](#apiv1alpha1mcpgroupphase)_ | Phase indicates current state | Pending | Enum: [Ready Pending Failed] <br /> |
| `servers` _string array_ | Servers lists MCPServer names in this group |  |  |
| `serverCount` _integer_ | ServerCount is the number of MCPServers |  |  |
| `remoteProxies` _string array_ | RemoteProxies lists MCPRemoteProxy names in this group |  |  |
| `remoteProxyCount` _integer_ | RemoteProxyCount is the number of MCPRemoteProxies |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent observations |  |  |


#### api.v1alpha1.MCPRegistry



MCPRegistry is the Schema for the mcpregistries API



_Appears in:_
- [api.v1alpha1.MCPRegistryList](#apiv1alpha1mcpregistrylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPRegistrySpec](#apiv1alpha1mcpregistryspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPRegistryStatus](#apiv1alpha1mcpregistrystatus)_ |  |  |  |


#### api.v1alpha1.MCPRegistryAuthConfig



MCPRegistryAuthConfig defines authentication configuration for the registry API server.



_Appears in:_
- [api.v1alpha1.MCPRegistrySpec](#apiv1alpha1mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[api.v1alpha1.MCPRegistryAuthMode](#apiv1alpha1mcpregistryauthmode)_ | Mode specifies the authentication mode (anonymous or oauth)<br />Defaults to "anonymous" if not specified.<br />Use "oauth" to enable OAuth/OIDC authentication. | anonymous | Enum: [anonymous oauth] <br /> |
| `oauth` _[api.v1alpha1.MCPRegistryOAuthConfig](#apiv1alpha1mcpregistryoauthconfig)_ | OAuth defines OAuth/OIDC specific authentication settings<br />Only used when Mode is "oauth" |  |  |


#### api.v1alpha1.MCPRegistryAuthMode

_Underlying type:_ _string_

MCPRegistryAuthMode represents the authentication mode for the registry API server



_Appears in:_
- [api.v1alpha1.MCPRegistryAuthConfig](#apiv1alpha1mcpregistryauthconfig)

| Field | Description |
| --- | --- |
| `anonymous` | MCPRegistryAuthModeAnonymous allows unauthenticated access<br /> |
| `oauth` | MCPRegistryAuthModeOAuth enables OAuth/OIDC authentication<br /> |


#### api.v1alpha1.MCPRegistryConfig



MCPRegistryConfig defines the configuration for a registry data source



_Appears in:_
- [api.v1alpha1.MCPRegistrySpec](#apiv1alpha1mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is a unique identifier for this registry configuration within the MCPRegistry |  | MinLength: 1 <br />Required: \{\} <br /> |
| `format` _string_ | Format is the data format (toolhive, upstream) | toolhive | Enum: [toolhive upstream] <br /> |
| `configMapRef` _[ConfigMapKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#configmapkeyselector-v1-core)_ | ConfigMapRef defines the ConfigMap source configuration<br />Mutually exclusive with Git, API, and PVCRef |  |  |
| `git` _[api.v1alpha1.GitSource](#apiv1alpha1gitsource)_ | Git defines the Git repository source configuration<br />Mutually exclusive with ConfigMapRef, API, and PVCRef |  |  |
| `api` _[api.v1alpha1.APISource](#apiv1alpha1apisource)_ | API defines the API source configuration<br />Mutually exclusive with ConfigMapRef, Git, and PVCRef |  |  |
| `pvcRef` _[api.v1alpha1.PVCSource](#apiv1alpha1pvcsource)_ | PVCRef defines the PersistentVolumeClaim source configuration<br />Mutually exclusive with ConfigMapRef, Git, and API |  |  |
| `syncPolicy` _[api.v1alpha1.SyncPolicy](#apiv1alpha1syncpolicy)_ | SyncPolicy defines the automatic synchronization behavior for this registry.<br />If specified, enables automatic synchronization at the given interval.<br />Manual synchronization is always supported via annotation-based triggers<br />regardless of this setting. |  |  |
| `filter` _[api.v1alpha1.RegistryFilter](#apiv1alpha1registryfilter)_ | Filter defines include/exclude patterns for registry content |  |  |


#### api.v1alpha1.MCPRegistryDatabaseConfig



MCPRegistryDatabaseConfig defines PostgreSQL database configuration for the registry API server.
Uses a two-user security model: separate users for operations and migrations.



_Appears in:_
- [api.v1alpha1.MCPRegistrySpec](#apiv1alpha1mcpregistryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Host is the database server hostname | postgres |  |
| `port` _integer_ | Port is the database server port | 5432 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `user` _string_ | User is the application user (limited privileges: SELECT, INSERT, UPDATE, DELETE)<br />Credentials should be provided via pgpass file or environment variables | db_app |  |
| `migrationUser` _string_ | MigrationUser is the migration user (elevated privileges: CREATE, ALTER, DROP)<br />Used for running database schema migrations<br />Credentials should be provided via pgpass file or environment variables | db_migrator |  |
| `database` _string_ | Database is the database name | registry |  |
| `sslMode` _string_ | SSLMode is the SSL mode for the connection<br />Valid values: disable, allow, prefer, require, verify-ca, verify-full | prefer | Enum: [disable allow prefer require verify-ca verify-full] <br /> |
| `maxOpenConns` _integer_ | MaxOpenConns is the maximum number of open connections to the database | 10 | Minimum: 1 <br /> |
| `maxIdleConns` _integer_ | MaxIdleConns is the maximum number of idle connections in the pool | 2 | Minimum: 0 <br /> |
| `connMaxLifetime` _string_ | ConnMaxLifetime is the maximum amount of time a connection may be reused (Go duration format)<br />Examples: "30m", "1h", "24h" | 30m | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br /> |
| `dbAppUserPasswordSecretRef` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#secretkeyselector-v1-core)_ | DBAppUserPasswordSecretRef references a Kubernetes Secret containing the password for the application database user.<br />The operator will use this password along with DBMigrationUserPasswordSecretRef to generate a pgpass file<br />that is mounted to the registry API container. |  | Required: \{\} <br /> |
| `dbMigrationUserPasswordSecretRef` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#secretkeyselector-v1-core)_ | DBMigrationUserPasswordSecretRef references a Kubernetes Secret containing the password for the migration database user.<br />The operator will use this password along with DBAppUserPasswordSecretRef to generate a pgpass file<br />that is mounted to the registry API container. |  | Required: \{\} <br /> |


#### api.v1alpha1.MCPRegistryList



MCPRegistryList contains a list of MCPRegistry





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry) array_ |  |  |  |


#### api.v1alpha1.MCPRegistryOAuthConfig



MCPRegistryOAuthConfig defines OAuth/OIDC specific authentication settings



_Appears in:_
- [api.v1alpha1.MCPRegistryAuthConfig](#apiv1alpha1mcpregistryauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `resourceUrl` _string_ | ResourceURL is the URL identifying this protected resource (RFC 9728)<br />Used in the /.well-known/oauth-protected-resource endpoint |  |  |
| `providers` _[api.v1alpha1.MCPRegistryOAuthProviderConfig](#apiv1alpha1mcpregistryoauthproviderconfig) array_ | Providers defines the OAuth/OIDC providers for authentication<br />Multiple providers can be configured (e.g., Kubernetes + external IDP) |  | MinItems: 1 <br /> |
| `scopesSupported` _string array_ | ScopesSupported defines the OAuth scopes supported by this resource (RFC 9728)<br />Defaults to ["mcp-registry:read", "mcp-registry:write"] if not specified |  |  |
| `realm` _string_ | Realm is the protection space identifier for WWW-Authenticate header (RFC 7235)<br />Defaults to "mcp-registry" if not specified |  |  |


#### api.v1alpha1.MCPRegistryOAuthProviderConfig



MCPRegistryOAuthProviderConfig defines configuration for an OAuth/OIDC provider



_Appears in:_
- [api.v1alpha1.MCPRegistryOAuthConfig](#apiv1alpha1mcpregistryoauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is a unique identifier for this provider (e.g., "kubernetes", "keycloak") |  | MinLength: 1 <br />Required: \{\} <br /> |
| `issuerUrl` _string_ | IssuerURL is the OIDC issuer URL (e.g., https://accounts.google.com)<br />The JWKS URL will be discovered automatically from .well-known/openid-configuration<br />unless JwksUrl is explicitly specified |  | MinLength: 1 <br />Pattern: `^https?://.*` <br />Required: \{\} <br /> |
| `jwksUrl` _string_ | JwksUrl is the URL to fetch the JSON Web Key Set (JWKS) from<br />If specified, OIDC discovery is skipped and this URL is used directly<br />Example: https://kubernetes.default.svc/openid/v1/jwks |  | Pattern: `^https?://.*` <br /> |
| `audience` _string_ | Audience is the expected audience claim in the token (REQUIRED)<br />Per RFC 6749 Section 4.1.3, tokens must be validated against expected audience<br />For Kubernetes, this is typically the API server URL |  | MinLength: 1 <br />Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth client ID for token introspection (optional) |  |  |
| `clientSecretRef` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#secretkeyselector-v1-core)_ | ClientSecretRef is a reference to a Secret containing the client secret<br />The secret should have a key "clientSecret" containing the secret value |  |  |
| `caCertRef` _[ConfigMapKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#configmapkeyselector-v1-core)_ | CACertRef is a reference to a ConfigMap containing the CA certificate bundle<br />for verifying the provider's TLS certificate.<br />Required for Kubernetes in-cluster authentication or self-signed certificates |  |  |
| `caCertPath` _string_ | CaCertPath is the path to the CA certificate bundle for verifying the provider's TLS certificate.<br />Required for Kubernetes in-cluster authentication or self-signed certificates |  |  |
| `authTokenRef` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#secretkeyselector-v1-core)_ | AuthTokenRef is a reference to a Secret containing a bearer token for authenticating<br />to OIDC/JWKS endpoints. Useful when the OIDC discovery or JWKS endpoint requires authentication.<br />Example: ServiceAccount token for Kubernetes API server |  |  |
| `authTokenFile` _string_ | AuthTokenFile is the path to a file containing a bearer token for authenticating to OIDC/JWKS endpoints.<br />Useful when the OIDC discovery or JWKS endpoint requires authentication.<br />Example: /var/run/secrets/kubernetes.io/serviceaccount/token |  |  |
| `introspectionUrl` _string_ | IntrospectionURL is the OAuth 2.0 Token Introspection endpoint (RFC 7662)<br />Used for validating opaque (non-JWT) tokens<br />If not specified, only JWT tokens can be validated via JWKS |  | Pattern: `^https?://.*` <br /> |
| `allowPrivateIP` _boolean_ | AllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses<br />Required when the OAuth provider (e.g., Kubernetes API server) is running on a private network<br />Example: Set to true when using https://kubernetes.default.svc as the issuer URL | false |  |


#### api.v1alpha1.MCPRegistryPhase

_Underlying type:_ _string_

MCPRegistryPhase represents the phase of the MCPRegistry

_Validation:_
- Enum: [Pending Ready Failed Syncing Terminating]

_Appears in:_
- [api.v1alpha1.MCPRegistryStatus](#apiv1alpha1mcpregistrystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRegistryPhasePending means the MCPRegistry is being initialized<br /> |
| `Ready` | MCPRegistryPhaseReady means the MCPRegistry is ready and operational<br /> |
| `Failed` | MCPRegistryPhaseFailed means the MCPRegistry has failed<br /> |
| `Syncing` | MCPRegistryPhaseSyncing means the MCPRegistry is currently syncing data<br /> |
| `Terminating` | MCPRegistryPhaseTerminating means the MCPRegistry is being deleted<br /> |


#### api.v1alpha1.MCPRegistrySpec



MCPRegistrySpec defines the desired state of MCPRegistry



_Appears in:_
- [api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `displayName` _string_ | DisplayName is a human-readable name for the registry |  |  |
| `registries` _[api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig) array_ | Registries defines the configuration for the registry data sources |  | MinItems: 1 <br />Required: \{\} <br /> |
| `enforceServers` _boolean_ | EnforceServers indicates whether MCPServers in this namespace must have their images<br />present in at least one registry in the namespace. When any registry in the namespace<br />has this field set to true, enforcement is enabled for the entire namespace.<br />MCPServers with images not found in any registry will be rejected.<br />When false (default), MCPServers can be deployed regardless of registry presence. | false |  |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the registry API server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the registry API server runs in, you must specify<br />the `registry-api` container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br /> |
| `databaseConfig` _[api.v1alpha1.MCPRegistryDatabaseConfig](#apiv1alpha1mcpregistrydatabaseconfig)_ | DatabaseConfig defines the PostgreSQL database configuration for the registry API server.<br />If not specified, defaults will be used:<br />  - Host: "postgres"<br />  - Port: 5432<br />  - User: "db_app"<br />  - MigrationUser: "db_migrator"<br />  - Database: "registry"<br />  - SSLMode: "prefer"<br />  - MaxOpenConns: 10<br />  - MaxIdleConns: 2<br />  - ConnMaxLifetime: "30m" |  |  |
| `authConfig` _[api.v1alpha1.MCPRegistryAuthConfig](#apiv1alpha1mcpregistryauthconfig)_ | AuthConfig defines the authentication configuration for the registry API server.<br />If not specified, defaults to anonymous authentication. |  |  |


#### api.v1alpha1.MCPRegistryStatus



MCPRegistryStatus defines the observed state of MCPRegistry



_Appears in:_
- [api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1alpha1.MCPRegistryPhase](#apiv1alpha1mcpregistryphase)_ | Phase represents the current overall phase of the MCPRegistry<br />Derived from sync and API status |  | Enum: [Pending Ready Failed Syncing Terminating] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |
| `syncStatus` _[api.v1alpha1.SyncStatus](#apiv1alpha1syncstatus)_ | SyncStatus provides detailed information about data synchronization |  |  |
| `apiStatus` _[api.v1alpha1.APIStatus](#apiv1alpha1apistatus)_ | APIStatus provides detailed information about the API service |  |  |
| `lastAppliedFilterHash` _string_ | LastAppliedFilterHash is the hash of the last applied filter |  |  |
| `storageRef` _[api.v1alpha1.StorageReference](#apiv1alpha1storagereference)_ | StorageRef is a reference to the internal storage location |  |  |
| `lastManualSyncTrigger` _string_ | LastManualSyncTrigger tracks the last processed manual sync annotation value<br />Used to detect new manual sync requests via toolhive.stacklok.dev/sync-trigger annotation |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRegistry's state |  |  |


#### api.v1alpha1.MCPRemoteProxy



MCPRemoteProxy is the Schema for the mcpremoteproxies API
It enables proxying remote MCP servers with authentication, authorization, audit logging, and tool filtering



_Appears in:_
- [api.v1alpha1.MCPRemoteProxyList](#apiv1alpha1mcpremoteproxylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxy` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPRemoteProxyStatus](#apiv1alpha1mcpremoteproxystatus)_ |  |  |  |


#### api.v1alpha1.MCPRemoteProxyList



MCPRemoteProxyList contains a list of MCPRemoteProxy





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxyList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy) array_ |  |  |  |


#### api.v1alpha1.MCPRemoteProxyPhase

_Underlying type:_ _string_

MCPRemoteProxyPhase is a label for the condition of a MCPRemoteProxy at the current time

_Validation:_
- Enum: [Pending Ready Failed Terminating]

_Appears in:_
- [api.v1alpha1.MCPRemoteProxyStatus](#apiv1alpha1mcpremoteproxystatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPRemoteProxyPhasePending means the proxy is being created<br /> |
| `Ready` | MCPRemoteProxyPhaseReady means the proxy is ready and operational<br /> |
| `Failed` | MCPRemoteProxyPhaseFailed means the proxy failed to start or encountered an error<br /> |
| `Terminating` | MCPRemoteProxyPhaseTerminating means the proxy is being deleted<br /> |


#### api.v1alpha1.MCPRemoteProxySpec



MCPRemoteProxySpec defines the desired state of MCPRemoteProxy



_Appears in:_
- [api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `remoteURL` _string_ | RemoteURL is the URL of the remote MCP server to proxy |  | Pattern: `^https?://` <br />Required: \{\} <br /> |
| `port` _integer_ | Port is the port to expose the MCP proxy on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `transport` _string_ | Transport is the transport method for the remote proxy (sse or streamable-http) | streamable-http | Enum: [sse streamable-http] <br /> |
| `oidcConfig` _[api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)_ | OIDCConfig defines OIDC authentication configuration for the proxy<br />This validates incoming tokens from clients. Required for proxy mode. |  | Required: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1alpha1.ExternalAuthConfigRef](#apiv1alpha1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.<br />When specified, the proxy will exchange validated incoming tokens for remote service tokens.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy. |  |  |
| `authzConfig` _[api.v1alpha1.AuthzConfigRef](#apiv1alpha1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the proxy |  |  |
| `audit` _[api.v1alpha1.AuditConfig](#apiv1alpha1auditconfig)_ | Audit defines audit logging configuration for the proxy |  |  |
| `toolConfigRef` _[api.v1alpha1.ToolConfigRef](#apiv1alpha1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this allows filtering and overriding tools from the remote MCP server. |  |  |
| `telemetry` _[api.v1alpha1.TelemetryConfig](#apiv1alpha1telemetryconfig)_ | Telemetry defines observability configuration for the proxy |  |  |
| `resources` _[api.v1alpha1.ResourceRequirements](#apiv1alpha1resourcerequirements)_ | Resources defines the resource requirements for the proxy container |  |  |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the proxy.<br />If not specified, a ServiceAccount will be created automatically and used by the proxy. |  |  |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false |  |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  |  |
| `resourceOverrides` _[api.v1alpha1.ResourceOverrides](#apiv1alpha1resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  |  |
| `groupRef` _string_ | GroupRef is the name of the MCPGroup this proxy belongs to<br />Must reference an existing MCPGroup in the same namespace |  |  |


#### api.v1alpha1.MCPRemoteProxyStatus



MCPRemoteProxyStatus defines the observed state of MCPRemoteProxy



_Appears in:_
- [api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1alpha1.MCPRemoteProxyPhase](#apiv1alpha1mcpremoteproxyphase)_ | Phase is the current phase of the MCPRemoteProxy |  | Enum: [Pending Ready Failed Terminating] <br /> |
| `url` _string_ | URL is the internal cluster URL where the proxy can be accessed |  |  |
| `externalURL` _string_ | ExternalURL is the external URL where the proxy can be accessed (if exposed externally) |  |  |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed MCPRemoteProxy |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPRemoteProxy's state |  |  |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  |  |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  |  |
| `message` _string_ | Message provides additional information about the current phase |  |  |


#### api.v1alpha1.MCPServer



MCPServer is the Schema for the mcpservers API



_Appears in:_
- [api.v1alpha1.MCPServerList](#apiv1alpha1mcpserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPServerStatus](#apiv1alpha1mcpserverstatus)_ |  |  |  |


#### api.v1alpha1.MCPServerList



MCPServerList contains a list of MCPServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPServer](#apiv1alpha1mcpserver) array_ |  |  |  |


#### api.v1alpha1.MCPServerPhase

_Underlying type:_ _string_

MCPServerPhase is the phase of the MCPServer

_Validation:_
- Enum: [Pending Running Failed Terminating]

_Appears in:_
- [api.v1alpha1.MCPServerStatus](#apiv1alpha1mcpserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | MCPServerPhasePending means the MCPServer is being created<br /> |
| `Running` | MCPServerPhaseRunning means the MCPServer is running<br /> |
| `Failed` | MCPServerPhaseFailed means the MCPServer failed to start<br /> |
| `Terminating` | MCPServerPhaseTerminating means the MCPServer is being deleted<br /> |


#### api.v1alpha1.MCPServerSpec



MCPServerSpec defines the desired state of MCPServer



_Appears in:_
- [api.v1alpha1.MCPServer](#apiv1alpha1mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Image is the container image for the MCP server |  | Required: \{\} <br /> |
| `transport` _string_ | Transport is the transport method for the MCP server (stdio, streamable-http or sse) | stdio | Enum: [stdio streamable-http sse] <br /> |
| `proxyMode` _string_ | ProxyMode is the proxy mode for stdio transport (sse or streamable-http)<br />This setting is only used when Transport is "stdio" | streamable-http | Enum: [sse streamable-http] <br /> |
| `port` _integer_ | Port is the port to expose the MCP server on<br />Deprecated: Use ProxyPort instead | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `targetPort` _integer_ | TargetPort is the port that MCP server listens to<br />Deprecated: Use McpPort instead |  | Maximum: 65535 <br />Minimum: 1 <br /> |
| `proxyPort` _integer_ | ProxyPort is the port to expose the proxy runner on | 8080 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `mcpPort` _integer_ | McpPort is the port that MCP server listens to |  | Maximum: 65535 <br />Minimum: 1 <br /> |
| `args` _string array_ | Args are additional arguments to pass to the MCP server |  |  |
| `env` _[api.v1alpha1.EnvVar](#apiv1alpha1envvar) array_ | Env are environment variables to set in the MCP server container |  |  |
| `volumes` _[api.v1alpha1.Volume](#apiv1alpha1volume) array_ | Volumes are volumes to mount in the MCP server container |  |  |
| `resources` _[api.v1alpha1.ResourceRequirements](#apiv1alpha1resourcerequirements)_ | Resources defines the resource requirements for the MCP server container |  |  |
| `secrets` _[api.v1alpha1.SecretRef](#apiv1alpha1secretref) array_ | Secrets are references to secrets to mount in the MCP server container |  |  |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the MCP server.<br />If not specified, a ServiceAccount will be created automatically and used by the MCP server. |  |  |
| `permissionProfile` _[api.v1alpha1.PermissionProfileRef](#apiv1alpha1permissionprofileref)_ | PermissionProfile defines the permission profile to use |  |  |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the MCP server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the MCP server runs in, you must specify<br />the `mcp` container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br /> |
| `resourceOverrides` _[api.v1alpha1.ResourceOverrides](#apiv1alpha1resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  |  |
| `oidcConfig` _[api.v1alpha1.OIDCConfigRef](#apiv1alpha1oidcconfigref)_ | OIDCConfig defines OIDC authentication configuration for the MCP server |  |  |
| `authzConfig` _[api.v1alpha1.AuthzConfigRef](#apiv1alpha1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the MCP server |  |  |
| `audit` _[api.v1alpha1.AuditConfig](#apiv1alpha1auditconfig)_ | Audit defines audit logging configuration for the MCP server |  |  |
| `tools` _string array_ | ToolsFilter is the filter on tools applied to the MCP server<br />Deprecated: Use ToolConfigRef instead |  |  |
| `toolConfigRef` _[api.v1alpha1.ToolConfigRef](#apiv1alpha1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this takes precedence over the inline ToolsFilter field. |  |  |
| `externalAuthConfigRef` _[api.v1alpha1.ExternalAuthConfigRef](#apiv1alpha1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for external authentication.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPServer. |  |  |
| `telemetry` _[api.v1alpha1.TelemetryConfig](#apiv1alpha1telemetryconfig)_ | Telemetry defines observability configuration for the MCP server |  |  |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false |  |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  |  |
| `groupRef` _string_ | GroupRef is the name of the MCPGroup this server belongs to<br />Must reference an existing MCPGroup in the same namespace |  |  |


#### api.v1alpha1.MCPServerStatus



MCPServerStatus defines the observed state of MCPServer



_Appears in:_
- [api.v1alpha1.MCPServer](#apiv1alpha1mcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPServer's state |  |  |
| `toolConfigHash` _string_ | ToolConfigHash stores the hash of the referenced ToolConfig for change detection |  |  |
| `externalAuthConfigHash` _string_ | ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec |  |  |
| `url` _string_ | URL is the URL where the MCP server can be accessed |  |  |
| `phase` _[api.v1alpha1.MCPServerPhase](#apiv1alpha1mcpserverphase)_ | Phase is the current phase of the MCPServer |  | Enum: [Pending Running Failed Terminating] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |


#### api.v1alpha1.MCPToolConfig



MCPToolConfig is the Schema for the mcptoolconfigs API.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources within the same namespace. Cross-namespace references
are not supported for security and isolation reasons.



_Appears in:_
- [api.v1alpha1.MCPToolConfigList](#apiv1alpha1mcptoolconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.MCPToolConfigSpec](#apiv1alpha1mcptoolconfigspec)_ |  |  |  |
| `status` _[api.v1alpha1.MCPToolConfigStatus](#apiv1alpha1mcptoolconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPToolConfigList



MCPToolConfigList contains a list of MCPToolConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig) array_ |  |  |  |


#### api.v1alpha1.MCPToolConfigSpec



MCPToolConfigSpec defines the desired state of MCPToolConfig.
MCPToolConfig resources are namespace-scoped and can only be referenced by
MCPServer resources in the same namespace.



_Appears in:_
- [api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `toolsFilter` _string array_ | ToolsFilter is a list of tool names to filter (allow list).<br />Only tools in this list will be exposed by the MCP server.<br />If empty, all tools are exposed. |  |  |
| `toolsOverride` _object (keys:string, values:[api.v1alpha1.ToolOverride](#apiv1alpha1tooloverride))_ | ToolsOverride is a map from actual tool names to their overridden configuration.<br />This allows renaming tools and/or changing their descriptions. |  |  |


#### api.v1alpha1.MCPToolConfigStatus



MCPToolConfigStatus defines the observed state of MCPToolConfig



_Appears in:_
- [api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPToolConfig.<br />It corresponds to the MCPToolConfig's generation, which is updated on mutation by the API Server. |  |  |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  |  |
| `referencingServers` _string array_ | ReferencingServers is a list of MCPServer resources that reference this MCPToolConfig<br />This helps track which servers need to be reconciled when this config changes |  |  |


#### api.v1alpha1.ModelCacheConfig



ModelCacheConfig configures persistent storage for model caching



_Appears in:_
- [api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether model caching is enabled | true |  |
| `storageClassName` _string_ | StorageClassName is the storage class to use for the PVC<br />If not specified, uses the cluster's default storage class |  |  |
| `size` _string_ | Size is the size of the PVC for model caching (e.g., "10Gi") | 10Gi |  |
| `accessMode` _string_ | AccessMode is the access mode for the PVC | ReadWriteOnce | Enum: [ReadWriteOnce ReadWriteMany ReadOnlyMany] <br /> |


#### api.v1alpha1.NameFilter



NameFilter defines name-based filtering



_Appears in:_
- [api.v1alpha1.RegistryFilter](#apiv1alpha1registryfilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `include` _string array_ | Include is a list of glob patterns to include |  |  |
| `exclude` _string array_ | Exclude is a list of glob patterns to exclude |  |  |


#### api.v1alpha1.NetworkPermissions



NetworkPermissions defines the network permissions for an MCP server



_Appears in:_
- [api.v1alpha1.PermissionProfileSpec](#apiv1alpha1permissionprofilespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode specifies the network mode for the container (e.g., "host", "bridge", "none")<br />When empty, the default container runtime network mode is used |  |  |
| `outbound` _[api.v1alpha1.OutboundNetworkPermissions](#apiv1alpha1outboundnetworkpermissions)_ | Outbound defines the outbound network permissions |  |  |


#### api.v1alpha1.OIDCConfigRef



OIDCConfigRef defines a reference to OIDC configuration



_Appears in:_
- [api.v1alpha1.IncomingAuthConfig](#apiv1alpha1incomingauthconfig)
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of OIDC configuration | kubernetes | Enum: [kubernetes configMap inline] <br /> |
| `resourceUrl` _string_ | ResourceURL is the explicit resource URL for OAuth discovery endpoint (RFC 9728)<br />If not specified, defaults to the in-cluster Kubernetes service URL |  |  |
| `kubernetes` _[api.v1alpha1.KubernetesOIDCConfig](#apiv1alpha1kubernetesoidcconfig)_ | Kubernetes configures OIDC for Kubernetes service account token validation<br />Only used when Type is "kubernetes" |  |  |
| `configMap` _[api.v1alpha1.ConfigMapOIDCRef](#apiv1alpha1configmapoidcref)_ | ConfigMap references a ConfigMap containing OIDC configuration<br />Only used when Type is "configmap" |  |  |
| `inline` _[api.v1alpha1.InlineOIDCConfig](#apiv1alpha1inlineoidcconfig)_ | Inline contains direct OIDC configuration<br />Only used when Type is "inline" |  |  |


#### api.v1alpha1.OpenTelemetryConfig



OpenTelemetryConfig defines pure OpenTelemetry configuration



_Appears in:_
- [api.v1alpha1.TelemetryConfig](#apiv1alpha1telemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OpenTelemetry is enabled | false |  |
| `endpoint` _string_ | Endpoint is the OTLP endpoint URL for tracing and metrics |  |  |
| `serviceName` _string_ | ServiceName is the service name for telemetry<br />If not specified, defaults to the MCPServer name |  |  |
| `headers` _string array_ | Headers contains authentication headers for the OTLP endpoint<br />Specified as key=value pairs |  |  |
| `insecure` _boolean_ | Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint | false |  |
| `metrics` _[api.v1alpha1.OpenTelemetryMetricsConfig](#apiv1alpha1opentelemetrymetricsconfig)_ | Metrics defines OpenTelemetry metrics-specific configuration |  |  |
| `tracing` _[api.v1alpha1.OpenTelemetryTracingConfig](#apiv1alpha1opentelemetrytracingconfig)_ | Tracing defines OpenTelemetry tracing configuration |  |  |


#### api.v1alpha1.OpenTelemetryMetricsConfig



OpenTelemetryMetricsConfig defines OpenTelemetry metrics configuration



_Appears in:_
- [api.v1alpha1.OpenTelemetryConfig](#apiv1alpha1opentelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP metrics are sent | false |  |


#### api.v1alpha1.OpenTelemetryTracingConfig



OpenTelemetryTracingConfig defines OpenTelemetry tracing configuration



_Appears in:_
- [api.v1alpha1.OpenTelemetryConfig](#apiv1alpha1opentelemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether OTLP tracing is sent | false |  |
| `samplingRate` _string_ | SamplingRate is the trace sampling rate (0.0-1.0) | 0.05 |  |


#### api.v1alpha1.OutboundNetworkPermissions



OutboundNetworkPermissions defines the outbound network permissions



_Appears in:_
- [api.v1alpha1.NetworkPermissions](#apiv1alpha1networkpermissions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `insecureAllowAll` _boolean_ | InsecureAllowAll allows all outbound network connections (not recommended) | false |  |
| `allowHost` _string array_ | AllowHost is a list of hosts to allow connections to |  |  |
| `allowPort` _integer array_ | AllowPort is a list of ports to allow connections to |  |  |


#### api.v1alpha1.OutgoingAuthConfig



OutgoingAuthConfig configures authentication from Virtual MCP to backend MCPServers



_Appears in:_
- [api.v1alpha1.VirtualMCPServerSpec](#apiv1alpha1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _string_ | Source defines how backend authentication configurations are determined<br />- discovered: Automatically discover from backend's MCPServer.spec.externalAuthConfigRef<br />- inline: Explicit per-backend configuration in VirtualMCPServer | discovered | Enum: [discovered inline] <br /> |
| `default` _[api.v1alpha1.BackendAuthConfig](#apiv1alpha1backendauthconfig)_ | Default defines default behavior for backends without explicit auth config |  |  |
| `backends` _object (keys:string, values:[api.v1alpha1.BackendAuthConfig](#apiv1alpha1backendauthconfig))_ | Backends defines per-backend authentication overrides<br />Works in all modes (discovered, inline) |  |  |


#### api.v1alpha1.PVCSource



PVCSource defines PersistentVolumeClaim source configuration



_Appears in:_
- [api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `claimName` _string_ | ClaimName is the name of the PersistentVolumeClaim |  | MinLength: 1 <br />Required: \{\} <br /> |
| `path` _string_ | Path is the relative path to the registry file within the PVC.<br />The PVC is mounted at /config/registry/\{registryName\}/.<br />The full file path becomes: /config/registry/\{registryName\}/\{path\}<br />This design:<br />- Each registry gets its own mount point (consistent with ConfigMap sources)<br />- Multiple registries can share the same PVC by mounting it at different paths<br />- Users control PVC organization freely via the path field<br />Examples:<br />  Registry "production" using PVC "shared-data" with path "prod/registry.json":<br />    PVC contains /prod/registry.json → accessed at /config/registry/production/prod/registry.json<br />  Registry "development" using SAME PVC "shared-data" with path "dev/registry.json":<br />    PVC contains /dev/registry.json → accessed at /config/registry/development/dev/registry.json<br />    (Same PVC, different mount path)<br />  Registry "staging" using DIFFERENT PVC "other-pvc" with path "registry.json":<br />    PVC contains /registry.json → accessed at /config/registry/staging/registry.json<br />    (Different PVC, independent mount)<br />  Registry "team-a" with path "v1/servers.json":<br />    PVC contains /v1/servers.json → accessed at /config/registry/team-a/v1/servers.json<br />    (Subdirectories allowed in path) | registry.json | Pattern: `^.*\.json$` <br /> |


#### api.v1alpha1.PermissionProfileRef



PermissionProfileRef defines a reference to a permission profile



_Appears in:_
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the type of permission profile reference | builtin | Enum: [builtin configmap] <br /> |
| `name` _string_ | Name is the name of the permission profile<br />If Type is "builtin", Name must be one of: "none", "network"<br />If Type is "configmap", Name is the name of the ConfigMap |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the ConfigMap that contains the permission profile<br />Only used when Type is "configmap" |  |  |




#### api.v1alpha1.PrometheusConfig



PrometheusConfig defines Prometheus-specific configuration



_Appears in:_
- [api.v1alpha1.TelemetryConfig](#apiv1alpha1telemetryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether Prometheus metrics endpoint is exposed | false |  |


#### api.v1alpha1.ProxyDeploymentOverrides



ProxyDeploymentOverrides defines overrides specific to the proxy deployment



_Appears in:_
- [api.v1alpha1.ResourceOverrides](#apiv1alpha1resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  |  |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  |  |
| `podTemplateMetadataOverrides` _[api.v1alpha1.ResourceMetadataOverrides](#apiv1alpha1resourcemetadataoverrides)_ |  |  |  |
| `env` _[api.v1alpha1.EnvVar](#apiv1alpha1envvar) array_ | Env are environment variables to set in the proxy container (thv run process)<br />These affect the toolhive proxy itself, not the MCP server it manages<br />Use TOOLHIVE_DEBUG=true to enable debug logging in the proxy |  |  |


#### api.v1alpha1.RegistryFilter



RegistryFilter defines include/exclude patterns for registry content



_Appears in:_
- [api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `names` _[api.v1alpha1.NameFilter](#apiv1alpha1namefilter)_ | NameFilters defines name-based filtering |  |  |
| `tags` _[api.v1alpha1.TagFilter](#apiv1alpha1tagfilter)_ | Tags defines tag-based filtering |  |  |


#### api.v1alpha1.ResourceList



ResourceList is a set of (resource name, quantity) pairs



_Appears in:_
- [api.v1alpha1.ResourceRequirements](#apiv1alpha1resourcerequirements)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cpu` _string_ | CPU is the CPU limit in cores (e.g., "500m" for 0.5 cores) |  |  |
| `memory` _string_ | Memory is the memory limit in bytes (e.g., "64Mi" for 64 megabytes) |  |  |


#### api.v1alpha1.ResourceMetadataOverrides



ResourceMetadataOverrides defines metadata overrides for a resource



_Appears in:_
- [api.v1alpha1.EmbeddingResourceOverrides](#apiv1alpha1embeddingresourceoverrides)
- [api.v1alpha1.EmbeddingStatefulSetOverrides](#apiv1alpha1embeddingstatefulsetoverrides)
- [api.v1alpha1.ProxyDeploymentOverrides](#apiv1alpha1proxydeploymentoverrides)
- [api.v1alpha1.ResourceOverrides](#apiv1alpha1resourceoverrides)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `annotations` _object (keys:string, values:string)_ | Annotations to add or override on the resource |  |  |
| `labels` _object (keys:string, values:string)_ | Labels to add or override on the resource |  |  |


#### api.v1alpha1.ResourceOverrides



ResourceOverrides defines overrides for annotations and labels on created resources



_Appears in:_
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `proxyDeployment` _[api.v1alpha1.ProxyDeploymentOverrides](#apiv1alpha1proxydeploymentoverrides)_ | ProxyDeployment defines overrides for the Proxy Deployment resource (toolhive proxy) |  |  |
| `proxyService` _[api.v1alpha1.ResourceMetadataOverrides](#apiv1alpha1resourcemetadataoverrides)_ | ProxyService defines overrides for the Proxy Service resource (points to the proxy deployment) |  |  |


#### api.v1alpha1.ResourceRequirements



ResourceRequirements describes the compute resource requirements



_Appears in:_
- [api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `limits` _[api.v1alpha1.ResourceList](#apiv1alpha1resourcelist)_ | Limits describes the maximum amount of compute resources allowed |  |  |
| `requests` _[api.v1alpha1.ResourceList](#apiv1alpha1resourcelist)_ | Requests describes the minimum amount of compute resources required |  |  |


#### api.v1alpha1.SecretKeyRef



SecretKeyRef is a reference to a key within a Secret



_Appears in:_
- [api.v1alpha1.BearerTokenConfig](#apiv1alpha1bearertokenconfig)
- [api.v1alpha1.EmbeddingServerSpec](#apiv1alpha1embeddingserverspec)
- [api.v1alpha1.HeaderInjectionConfig](#apiv1alpha1headerinjectionconfig)
- [api.v1alpha1.InlineOIDCConfig](#apiv1alpha1inlineoidcconfig)
- [api.v1alpha1.TokenExchangeConfig](#apiv1alpha1tokenexchangeconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key within the secret |  | Required: \{\} <br /> |


#### api.v1alpha1.SecretRef



SecretRef is a reference to a secret



_Appears in:_
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the secret |  | Required: \{\} <br /> |
| `key` _string_ | Key is the key in the secret itself |  | Required: \{\} <br /> |
| `targetEnvName` _string_ | TargetEnvName is the environment variable to be used when setting up the secret in the MCP server<br />If left unspecified, it defaults to the key |  |  |


#### api.v1alpha1.StorageReference



StorageReference defines a reference to internal storage



_Appears in:_
- [api.v1alpha1.MCPRegistryStatus](#apiv1alpha1mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the storage type (configmap) |  | Enum: [configmap] <br /> |
| `configMapRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#localobjectreference-v1-core)_ | ConfigMapRef is a reference to a ConfigMap storage<br />Only used when Type is "configmap" |  |  |


#### api.v1alpha1.SyncPhase

_Underlying type:_ _string_

SyncPhase represents the data synchronization state

_Validation:_
- Enum: [Syncing Complete Failed]

_Appears in:_
- [api.v1alpha1.SyncStatus](#apiv1alpha1syncstatus)

| Field | Description |
| --- | --- |
| `Syncing` | SyncPhaseSyncing means sync is currently in progress<br /> |
| `Complete` | SyncPhaseComplete means sync completed successfully<br /> |
| `Failed` | SyncPhaseFailed means sync failed<br /> |


#### api.v1alpha1.SyncPolicy



SyncPolicy defines automatic synchronization behavior.
When specified, enables automatic synchronization at the given interval.
Manual synchronization via annotation-based triggers is always available
regardless of this policy setting.



_Appears in:_
- [api.v1alpha1.MCPRegistryConfig](#apiv1alpha1mcpregistryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interval` _string_ | Interval is the sync interval for automatic synchronization (Go duration format)<br />Examples: "1h", "30m", "24h" |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Required: \{\} <br /> |


#### api.v1alpha1.SyncStatus



SyncStatus provides detailed information about data synchronization



_Appears in:_
- [api.v1alpha1.MCPRegistryStatus](#apiv1alpha1mcpregistrystatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[api.v1alpha1.SyncPhase](#apiv1alpha1syncphase)_ | Phase represents the current synchronization phase |  | Enum: [Syncing Complete Failed] <br /> |
| `message` _string_ | Message provides additional information about the sync status |  |  |
| `lastAttempt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | LastAttempt is the timestamp of the last sync attempt |  |  |
| `attemptCount` _integer_ | AttemptCount is the number of sync attempts since last success |  | Minimum: 0 <br /> |
| `lastSyncTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | LastSyncTime is the timestamp of the last successful sync |  |  |
| `lastSyncHash` _string_ | LastSyncHash is the hash of the last successfully synced data<br />Used to detect changes in source data |  |  |
| `serverCount` _integer_ | ServerCount is the total number of servers in the registry |  | Minimum: 0 <br /> |


#### api.v1alpha1.TagFilter



TagFilter defines tag-based filtering



_Appears in:_
- [api.v1alpha1.RegistryFilter](#apiv1alpha1registryfilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `include` _string array_ | Include is a list of tags to include |  |  |
| `exclude` _string array_ | Exclude is a list of tags to exclude |  |  |


#### api.v1alpha1.TelemetryConfig



TelemetryConfig defines observability configuration for the MCP server



_Appears in:_
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `openTelemetry` _[api.v1alpha1.OpenTelemetryConfig](#apiv1alpha1opentelemetryconfig)_ | OpenTelemetry defines OpenTelemetry configuration |  |  |
| `prometheus` _[api.v1alpha1.PrometheusConfig](#apiv1alpha1prometheusconfig)_ | Prometheus defines Prometheus-specific configuration |  |  |


#### api.v1alpha1.TokenExchangeConfig



TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
This configuration is used to exchange incoming authentication tokens for tokens
that can be used with external services.
The structure matches the tokenexchange.Config from pkg/auth/tokenexchange/middleware.go



_Appears in:_
- [api.v1alpha1.MCPExternalAuthConfigSpec](#apiv1alpha1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenUrl` _string_ | TokenURL is the OAuth 2.0 token endpoint URL for token exchange |  | Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier<br />Optional for some token exchange flows (e.g., Google Cloud Workforce Identity) |  |  |
| `clientSecretRef` _[api.v1alpha1.SecretKeyRef](#apiv1alpha1secretkeyref)_ | ClientSecretRef is a reference to a secret containing the OAuth 2.0 client secret<br />Optional for some token exchange flows (e.g., Google Cloud Workforce Identity) |  |  |
| `audience` _string_ | Audience is the target audience for the exchanged token |  | Required: \{\} <br /> |
| `scopes` _string array_ | Scopes is a list of OAuth 2.0 scopes to request for the exchanged token |  |  |
| `subjectTokenType` _string_ | SubjectTokenType is the type of the incoming subject token.<br />Accepts short forms: "access_token" (default), "id_token", "jwt"<br />Or full URNs: "urn:ietf:params:oauth:token-type:access_token",<br />              "urn:ietf:params:oauth:token-type:id_token",<br />              "urn:ietf:params:oauth:token-type:jwt"<br />For Google Workload Identity Federation with OIDC providers (like Okta), use "id_token" |  | Pattern: `^(access_token\|id_token\|jwt\|urn:ietf:params:oauth:token-type:(access_token\|id_token\|jwt))?$` <br /> |
| `externalTokenHeaderName` _string_ | ExternalTokenHeaderName is the name of the custom header to use for the exchanged token.<br />If set, the exchanged token will be added to this custom header (e.g., "X-Upstream-Token").<br />If empty or not set, the exchanged token will replace the Authorization header (default behavior). |  |  |


#### api.v1alpha1.ToolConfigRef



ToolConfigRef defines a reference to a MCPToolConfig resource.
The referenced MCPToolConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1alpha1.MCPRemoteProxySpec](#apiv1alpha1mcpremoteproxyspec)
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPToolConfig resource in the same namespace |  | Required: \{\} <br /> |


#### api.v1alpha1.ToolOverride



ToolOverride represents a tool override configuration.
Both Name and Description can be overridden independently, but
they can't be both empty.



_Appears in:_
- [api.v1alpha1.MCPToolConfigSpec](#apiv1alpha1mcptoolconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the redefined name of the tool |  |  |
| `description` _string_ | Description is the redefined description of the tool |  |  |


#### api.v1alpha1.ValidationStatus

_Underlying type:_ _string_

ValidationStatus represents the validation state of a workflow

_Validation:_
- Enum: [Valid Invalid Unknown]

_Appears in:_
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionStatus](#apiv1alpha1virtualmcpcompositetooldefinitionstatus)

| Field | Description |
| --- | --- |
| `Valid` | ValidationStatusValid indicates the workflow is valid<br /> |
| `Invalid` | ValidationStatusInvalid indicates the workflow has validation errors<br /> |
| `Unknown` | ValidationStatusUnknown indicates validation hasn't been performed yet<br /> |


#### api.v1alpha1.VirtualMCPCompositeToolDefinition



VirtualMCPCompositeToolDefinition is the Schema for the virtualmcpcompositetooldefinitions API
VirtualMCPCompositeToolDefinition defines reusable composite workflows that can be referenced
by multiple VirtualMCPServer instances



_Appears in:_
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionList](#apiv1alpha1virtualmcpcompositetooldefinitionlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinition` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.VirtualMCPCompositeToolDefinitionSpec](#apiv1alpha1virtualmcpcompositetooldefinitionspec)_ |  |  |  |
| `status` _[api.v1alpha1.VirtualMCPCompositeToolDefinitionStatus](#apiv1alpha1virtualmcpcompositetooldefinitionstatus)_ |  |  |  |


#### api.v1alpha1.VirtualMCPCompositeToolDefinitionList



VirtualMCPCompositeToolDefinitionList contains a list of VirtualMCPCompositeToolDefinition





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinitionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition) array_ |  |  |  |


#### api.v1alpha1.VirtualMCPCompositeToolDefinitionSpec



VirtualMCPCompositeToolDefinitionSpec defines the desired state of VirtualMCPCompositeToolDefinition.
This embeds the CompositeToolConfig from pkg/vmcp/config to share the configuration model
between CLI and operator usage.



_Appears in:_
- [api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the workflow name (unique identifier). |  |  |
| `description` _string_ | Description describes what the workflow does. |  |  |
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  |  |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the maximum workflow execution time. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br /> |
| `steps` _[vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig) array_ | Steps are the workflow steps to execute. |  |  |
| `output` _[vmcp.config.OutputConfig](#vmcpconfigoutputconfig)_ | Output defines the structured output schema for this workflow.<br />If not specified, the workflow returns the last step's output (backward compatible). |  |  |


#### api.v1alpha1.VirtualMCPCompositeToolDefinitionStatus



VirtualMCPCompositeToolDefinitionStatus defines the observed state of VirtualMCPCompositeToolDefinition



_Appears in:_
- [api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `validationStatus` _[api.v1alpha1.ValidationStatus](#apiv1alpha1validationstatus)_ | ValidationStatus indicates the validation state of the workflow<br />- Valid: Workflow structure is valid<br />- Invalid: Workflow has validation errors |  | Enum: [Valid Invalid Unknown] <br /> |
| `validationErrors` _string array_ | ValidationErrors contains validation error messages if ValidationStatus is Invalid |  |  |
| `referencingVirtualServers` _string array_ | ReferencingVirtualServers lists VirtualMCPServer resources that reference this workflow<br />This helps track which servers need to be reconciled when this workflow changes |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this VirtualMCPCompositeToolDefinition<br />It corresponds to the resource's generation, which is updated on mutation by the API Server |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the workflow's state |  |  |


#### api.v1alpha1.VirtualMCPServer



VirtualMCPServer is the Schema for the virtualmcpservers API
VirtualMCPServer aggregates multiple backend MCPServers into a unified endpoint



_Appears in:_
- [api.v1alpha1.VirtualMCPServerList](#apiv1alpha1virtualmcpserverlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1alpha1.VirtualMCPServerSpec](#apiv1alpha1virtualmcpserverspec)_ |  |  |  |
| `status` _[api.v1alpha1.VirtualMCPServerStatus](#apiv1alpha1virtualmcpserverstatus)_ |  |  |  |


#### api.v1alpha1.VirtualMCPServerList



VirtualMCPServerList contains a list of VirtualMCPServer





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  |  |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  |  |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver) array_ |  |  |  |


#### api.v1alpha1.VirtualMCPServerPhase

_Underlying type:_ _string_

VirtualMCPServerPhase represents the lifecycle phase of a VirtualMCPServer

_Validation:_
- Enum: [Pending Ready Degraded Failed]

_Appears in:_
- [api.v1alpha1.VirtualMCPServerStatus](#apiv1alpha1virtualmcpserverstatus)

| Field | Description |
| --- | --- |
| `Pending` | VirtualMCPServerPhasePending indicates the VirtualMCPServer is being initialized<br /> |
| `Ready` | VirtualMCPServerPhaseReady indicates the VirtualMCPServer is ready and serving requests<br /> |
| `Degraded` | VirtualMCPServerPhaseDegraded indicates the VirtualMCPServer is running but some backends are unavailable<br /> |
| `Failed` | VirtualMCPServerPhaseFailed indicates the VirtualMCPServer has failed<br /> |


#### api.v1alpha1.VirtualMCPServerSpec



VirtualMCPServerSpec defines the desired state of VirtualMCPServer



_Appears in:_
- [api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `incomingAuth` _[api.v1alpha1.IncomingAuthConfig](#apiv1alpha1incomingauthconfig)_ | IncomingAuth configures authentication for clients connecting to the Virtual MCP server.<br />Must be explicitly set - use "anonymous" type when no authentication is required.<br />This field takes precedence over config.IncomingAuth and should be preferred because it<br />supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure<br />dynamic discovery of credentials, rather than requiring secrets to be embedded in config. |  | Required: \{\} <br /> |
| `outgoingAuth` _[api.v1alpha1.OutgoingAuthConfig](#apiv1alpha1outgoingauthconfig)_ | OutgoingAuth configures authentication from Virtual MCP to backend MCPServers.<br />This field takes precedence over config.OutgoingAuth and should be preferred because it<br />supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure<br />dynamic discovery of credentials, rather than requiring secrets to be embedded in config. |  |  |
| `serviceType` _string_ | ServiceType specifies the Kubernetes service type for the Virtual MCP server | ClusterIP | Enum: [ClusterIP NodePort LoadBalancer] <br /> |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the Virtual MCP server.<br />If not specified, a ServiceAccount will be created automatically and used by the Virtual MCP server. |  |  |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the Virtual MCP server<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the Virtual MCP server runs in, you must specify<br />the 'vmcp' container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br /> |
| `config` _[vmcp.config.Config](#vmcpconfigconfig)_ | Config is the Virtual MCP server configuration<br />The only field currently required within config is `config.groupRef`.<br />GroupRef references an existing MCPGroup that defines backend workloads.<br />The referenced MCPGroup must exist in the same namespace.<br />The telemetry and audit config from here are also supported, but not required. |  | Type: object <br /> |


#### api.v1alpha1.VirtualMCPServerStatus



VirtualMCPServerStatus defines the observed state of VirtualMCPServer



_Appears in:_
- [api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the VirtualMCPServer's state |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this VirtualMCPServer |  |  |
| `phase` _[api.v1alpha1.VirtualMCPServerPhase](#apiv1alpha1virtualmcpserverphase)_ | Phase is the current phase of the VirtualMCPServer | Pending | Enum: [Pending Ready Degraded Failed] <br /> |
| `message` _string_ | Message provides additional information about the current phase |  |  |
| `url` _string_ | URL is the URL where the Virtual MCP server can be accessed |  |  |
| `discoveredBackends` _[api.v1alpha1.DiscoveredBackend](#apiv1alpha1discoveredbackend) array_ | DiscoveredBackends lists discovered backend configurations from the MCPGroup |  |  |
| `backendCount` _integer_ | BackendCount is the number of healthy/ready backends<br />(excludes unavailable, degraded, and unknown backends) |  |  |


#### api.v1alpha1.Volume



Volume represents a volume to mount in a container



_Appears in:_
- [api.v1alpha1.MCPServerSpec](#apiv1alpha1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the volume |  | Required: \{\} <br /> |
| `hostPath` _string_ | HostPath is the path on the host to mount |  | Required: \{\} <br /> |
| `mountPath` _string_ | MountPath is the path in the container to mount to |  | Required: \{\} <br /> |
| `readOnly` _boolean_ | ReadOnly specifies whether the volume should be mounted read-only | false |  |


