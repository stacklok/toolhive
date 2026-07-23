# API Reference

## Packages
- [toolhive.stacklok.dev/audit](#toolhivestacklokdevaudit)
- [toolhive.stacklok.dev/authtypes](#toolhivestacklokdevauthtypes)
- [toolhive.stacklok.dev/config](#toolhivestacklokdevconfig)
- [toolhive.stacklok.dev/json](#toolhivestacklokdevjson)
- [toolhive.stacklok.dev/ratelimit](#toolhivestacklokdevratelimit)
- [toolhive.stacklok.dev/telemetry](#toolhivestacklokdevtelemetry)
- [toolhive.stacklok.dev/v1alpha1](#toolhivestacklokdevv1alpha1)
- [toolhive.stacklok.dev/v1beta1](#toolhivestacklokdevv1beta1)
- [toolhive.stacklok.dev/vmcp](#toolhivestacklokdevvmcp)


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


#### auth.types.AwsStsConfig



AwsStsConfig configures AWS STS authentication with SigV4 request signing.
This strategy exchanges incoming tokens for AWS STS temporary credentials.



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `region` _string_ | Region is the AWS region for the STS endpoint and service. |  |  |
| `service` _string_ | Service is the AWS service name for SigV4 signing. |  |  |
| `fallbackRoleArn` _string_ | FallbackRoleArn is the IAM role ARN to assume when no role mappings match. |  |  |
| `roleMappings` _[auth.types.RoleMapping](#authtypesrolemapping) array_ | RoleMappings defines claim-based role selection rules. |  |  |
| `roleClaim` _string_ | RoleClaim is the JWT claim to use for role mapping evaluation. |  |  |
| `sessionDuration` _integer_ | SessionDuration is the duration in seconds for the STS session. |  |  |
| `sessionNameClaim` _string_ | SessionNameClaim is the JWT claim to use for the role session name. |  |  |
| `subjectProviderName` _string_ | SubjectProviderName selects which upstream provider's token to use as the<br />web identity token for AssumeRoleWithWebIdentity. When set, the token is<br />looked up from Identity.UpstreamTokens instead of the request's<br />Authorization header. |  |  |


#### auth.types.BackendAuthStrategy



BackendAuthStrategy defines how to authenticate to a specific backend.

This struct provides type-safe configuration for different authentication strategies
using HeaderInjection or TokenExchange fields based on the Type field.



_Appears in:_
- [vmcp.config.OutgoingAuthConfig](#vmcpconfigoutgoingauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange", "upstream_inject", "aws_sts", "obo", "xaa" |  |  |
| `headerInjection` _[auth.types.HeaderInjectionConfig](#authtypesheaderinjectionconfig)_ | HeaderInjection contains configuration for header injection auth strategy.<br />Used when Type = "header_injection". |  |  |
| `tokenExchange` _[auth.types.TokenExchangeConfig](#authtypestokenexchangeconfig)_ | TokenExchange contains configuration for token exchange auth strategy.<br />Used when Type = "token_exchange". |  |  |
| `upstreamInject` _[auth.types.UpstreamInjectConfig](#authtypesupstreaminjectconfig)_ | UpstreamInject contains configuration for upstream inject auth strategy.<br />Used when Type = "upstream_inject". |  |  |
| `awsSts` _[auth.types.AwsStsConfig](#authtypesawsstsconfig)_ | AwsSts contains configuration for AWS STS auth strategy.<br />Used when Type = "aws_sts". |  |  |
| `obo` _[auth.types.OBOConfig](#authtypesoboconfig)_ | OBO contains configuration for on-behalf-of (OBO) auth strategy.<br />Used when Type = "obo". The default upstream build returns ErrEnterpriseRequired;<br />an out-of-tree build registers a real strategy via auth.RegisterOBOStrategy. |  |  |
| `xaa` _[auth.types.XAAConfig](#authtypesxaaconfig)_ | XAA contains configuration for XAA (Cross-Application Access) auth strategy.<br />Used when Type = "xaa". |  |  |


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


#### auth.types.OBOConfig



OBOConfig configures the on-behalf-of (OBO) authentication strategy.
This strategy uses the Entra jwt-bearer / on_behalf_of grant to exchange
the incoming user token for a backend-scoped token on behalf of the user.

Field names follow the OBO runtime contract (the enterprise obo.MiddlewareParameters),
not the RFC-8693 TokenExchangeConfig, because OBO uses a distinct Entra-specific grant.



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenUrl` _string_ | TokenURL is the Entra token endpoint URL for the OBO exchange. |  | Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth client ID for the OBO request. |  |  |
| `clientSecret` _string_ | ClientSecret is the OAuth client secret (use ClientSecretEnv for security). |  |  |
| `clientSecretEnv` _string_ | ClientSecretEnv is the environment variable name containing the client secret.<br />The value will be resolved at runtime from this environment variable. |  |  |
| `audience` _string_ | Audience is the target audience (resource URI) for the exchanged token. |  |  |
| `scopes` _string array_ | Scopes are the requested scopes for the exchanged token. |  |  |
| `subjectTokenProviderName` _string_ | SubjectTokenProviderName selects which upstream provider's token to use as the<br />subject (assertion) token for the OBO exchange. When set, the token is looked<br />up from Identity.UpstreamTokens[SubjectTokenProviderName]; when omitted, the<br />inbound end-user token (Identity.Token) is used directly.<br />Matches the operator CRD's SubjectTokenProviderName field; the enterprise OBO<br />converter maps both to the runtime contract without renaming. |  |  |
| `cacheSkewSeconds` _integer_ | CacheSkewSeconds is the number of seconds to subtract from a cached token's<br />expiry when deciding whether to refresh it. Defaults to zero (no skew).<br />The operator CRD stores this as CacheSkew *metav1.Duration and converts it<br />to an integer-seconds value for the vMCP runtime contract. |  |  |


#### auth.types.RoleMapping



RoleMapping defines a rule for mapping JWT claims to IAM roles.
Mappings are evaluated in priority order (lower number = higher priority).



_Appears in:_
- [auth.types.AwsStsConfig](#authtypesawsstsconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `claim` _string_ | Claim is a simple claim value to match against the RoleClaim field. |  |  |
| `matcher` _string_ | Matcher is a CEL expression for complex matching against JWT claims. |  |  |
| `roleArn` _string_ | RoleArn is the IAM role ARN to assume when this mapping matches. |  |  |
| `priority` _integer_ | Priority determines evaluation order (lower values = higher priority).<br />Mirrors awssts.RoleMapping.Priority, which is *int because the role mapper<br />uses math.MaxInt for nil-priority semantics in effectivePriority. |  |  |


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


#### auth.types.XAAConfig



XAAConfig configures the XAA (Cross-Application Access) auth strategy.
XAA implements draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG) as a
two-step flow:
  - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT
  - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token



_Appears in:_
- [auth.types.BackendAuthStrategy](#authtypesbackendauthstrategy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `idpTokenUrl` _string_ | IDPTokenURL is the IdP token endpoint for IdP exchange (RFC 8693 exchange). |  |  |
| `idpClientId` _string_ | IDPClientID is the OAuth client ID at the IdP for IdP exchange. |  |  |
| `idpClientSecret` _string_ | IDPClientSecret is the client secret at the IdP for IdP exchange. |  |  |
| `idpClientSecretEnv` _string_ | IDPClientSecretEnv is the env var containing the IdP client secret. |  |  |
| `targetTokenUrl` _string_ | TargetTokenURL is the target AS token endpoint for target grant (JWT Bearer grant). |  |  |
| `insecureTargetTokenUrl` _boolean_ | InsecureTargetTokenURL allows plain HTTP for TargetTokenURL.<br />WARNING: this is insecure and must only be set for in-cluster or<br />development/testing endpoints — never in production. |  |  |
| `targetClientId` _string_ | TargetClientID is the OAuth client ID at the target AS for target grant. |  |  |
| `targetClientSecret` _string_ | TargetClientSecret is the client secret at the target AS for target grant. |  |  |
| `targetClientSecretEnv` _string_ | TargetClientSecretEnv is the env var containing the target AS client secret. |  |  |
| `targetAudience` _string_ | TargetAudience is the resource AS URL for the ID-JAG audience claim (required). |  |  |
| `targetResource` _string_ | TargetResource is the RFC 8707 resource indicator sent as the `resource`<br />parameter in IdP exchange's RFC 8693 token exchange (draft §4.3, OPTIONAL). It<br />identifies the target resource server — not the access-token audience, which<br />is governed by TargetAudience. For MCP backends, set to the MCP server URL. |  |  |
| `scopes` _string array_ | Scopes are the requested scopes for IdP exchange and target grant. |  |  |
| `subjectProviderName` _string_ | SubjectProviderName selects which upstream provider's ID token to use.<br />Auto-populated when embedded AS is active. |  |  |
| `subjectTokenType` _string_ | SubjectTokenType is the token-type URN of the upstream subject token<br />used in IdP exchange. Defaults to TokenTypeIDToken when empty. Currently only<br />urn:ietf:params:oauth:token-type:id_token is accepted; the field exists<br />to allow future expansion to SAML upstreams without an API break. |  |  |



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
| `entitiesJson` _string_ | EntitiesJSON is a JSON string representing Cedar entities. Required for<br />enterprise policies that rely on transitive relationships (e.g.<br />`ClaimGroup → PlatformRole`) — without it the Cedar authorizer is<br />constructed with an empty entity store and `in` checks against absent<br />entities silently evaluate to false. Defaults to "[]" when empty. |  | Optional: \{\} <br /> |
| `primaryUpstreamProvider` _string_ | PrimaryUpstreamProvider names the upstream IDP provider whose access<br />token should be used as the source of JWT claims for Cedar evaluation.<br />When empty, claims from the ToolHive-issued token are used.<br />Must match an upstream provider name configured in the embedded auth server<br />(e.g. "default", "github"). Only relevant when the embedded auth server is active. |  | Optional: \{\} <br /> |
| `groupClaimName` _string_ | GroupClaimName is the JWT claim key that contains group membership for<br />the principal. When set, takes priority over the well-known defaults<br />("groups", "roles", "cognito:groups"). Use this for IDPs that place<br />groups under a URI-style claim (e.g. "https://example.com/groups").<br />When empty, only the well-known claim names are checked. |  | Optional: \{\} <br /> |
| `roleClaimName` _string_ | RoleClaimName is the JWT claim key that contains role membership for the<br />principal. When set, the claim is extracted separately from GroupClaimName<br />and both are mapped to the configured group entity type. When empty, no<br />role extraction is performed. |  | Optional: \{\} <br /> |
| `groupEntityType` _string_ | GroupEntityType is the Cedar entity type name used for principal parent<br />UIDs synthesised from JWT group/role claims. Defaults to "THVGroup" when<br />empty. Must match the entity type used in EntitiesJSON for transitive<br />`in` checks to resolve. Namespaced names (`Foo::Bar`) are not yet supported. |  | Optional: \{\} <br /> |


#### vmcp.config.CircuitBreakerConfig



CircuitBreakerConfig configures circuit breaker behavior.



_Appears in:_
- [vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether circuit breaker is enabled. | false | Optional: \{\} <br /> |
| `failureThreshold` _integer_ | FailureThreshold is the number of failures before opening the circuit.<br />Must be >= 1. | 5 | Minimum: 1 <br />Optional: \{\} <br /> |
| `timeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | Timeout is the duration to wait before attempting to close the circuit.<br />Must be >= 1s to prevent thrashing. | 60s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |


#### vmcp.config.CodeModeConfig



CodeModeConfig configures vMCP code mode (the execute_tool_script virtual tool).
When enabled, agents can submit a Starlark script that calls multiple backend tools
server-side — with loops, conditionals, and parallel() fan-out — and receive a single
aggregated result, collapsing many tool-call round-trips into one.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled turns code mode on. When false (the default), execute_tool_script is not<br />advertised in tools/list and scripts cannot be executed. |  | Optional: \{\} <br /> |
| `stepLimit` _integer_ | StepLimit is the maximum number of Starlark execution steps per script. It bounds<br />runaway loops and computation. Defaults to 100000 if unset or zero. | 100000 | Minimum: 1 <br />Optional: \{\} <br /> |
| `parallelMaxConcurrency` _integer_ | ParallelMaxConcurrency caps the number of goroutines a script's parallel() builtin<br />may run concurrently. Defaults to 10 if unset or zero. | 10 | Minimum: 1 <br />Optional: \{\} <br /> |
| `toolCallTimeout` _[vmcp.config.Duration](#vmcpconfigduration)_ | ToolCallTimeout bounds each individual backend tool call made from within a script.<br />A call exceeding it is cancelled and surfaces a timeout error to the script.<br />Defaults to 30s if unset. | 30s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Type: string <br />Optional: \{\} <br /> |


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
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  | Type: object <br />Optional: \{\} <br /> |
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
| `codeMode` _[vmcp.config.CodeModeConfig](#vmcpconfigcodemodeconfig)_ | CodeMode configures vMCP code mode: server-side execution of Starlark scripts that<br />orchestrate multiple backend tool calls in a single request via the execute_tool_script<br />virtual tool. When enabled, execute_tool_script is advertised alongside the backend<br />tools; a script's inner tool calls are authorized individually, so a script can only<br />reach tools the caller is already permitted to use. Disabled by default. |  | Optional: \{\} <br /> |
| `sessionStorage` _[vmcp.config.SessionStorageConfig](#vmcpconfigsessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When provider is "redis", the operator injects Redis connection parameters<br />(address, db, keyPrefix) here. The Redis password is provided separately via<br />the THV_SESSION_REDIS_PASSWORD environment variable. |  | Optional: \{\} <br /> |
| `rateLimiting` _[ratelimit.types.RateLimitConfig](#ratelimittypesratelimitconfig)_ | RateLimiting defines rate limiting configuration for the Virtual MCP server.<br />Requires Redis session storage to be configured for distributed rate limiting. |  | Optional: \{\} <br /> |
| `passthroughHeaders` _string array_ | PassthroughHeaders is an allowlist of incoming client request header names<br />forwarded verbatim to all backends. Captured at the vMCP incoming edge by<br />headerforward.CaptureMiddleware and consumed once at session creation<br />when the per-session backend client's HeaderForwardConfig is built. Names<br />must not be in the restricted set (Host, hop-by-hop, X-Forwarded-*, etc.). |  | Optional: \{\} <br /> |


#### vmcp.config.ConflictResolutionConfig



ConflictResolutionConfig provides configuration for conflict resolution strategies.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `prefixFormat` _string_ | PrefixFormat defines the prefix format for the "prefix" strategy.<br />Supports placeholders: \{workload\}, \{workload\}_, \{workload\}. | \{workload\}_ | Optional: \{\} <br /> |
| `priorityOrder` _string array_ | PriorityOrder defines the workload priority order for the "priority" strategy. |  | Optional: \{\} <br /> |




#### vmcp.config.Duration

_Underlying type:_ _Duration_

Duration is a wrapper around time.Duration that marshals/unmarshals as a duration string.
This ensures duration values are serialized as "30s", "1m", etc. instead of nanosecond integers.

_Validation:_
- Pattern: `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
- Type: string

_Appears in:_
- [vmcp.config.CircuitBreakerConfig](#vmcpconfigcircuitbreakerconfig)
- [vmcp.config.CodeModeConfig](#vmcpconfigcodemodeconfig)
- [vmcp.config.CompositeToolConfig](#vmcpconfigcompositetoolconfig)
- [vmcp.config.FailureHandlingConfig](#vmcpconfigfailurehandlingconfig)
- [vmcp.config.OptimizerConfig](#vmcpconfigoptimizerconfig)
- [vmcp.config.StepErrorHandling](#vmcpconfigsteperrorhandling)
- [vmcp.config.TimeoutConfig](#vmcpconfigtimeoutconfig)
- [api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)
- [vmcp.config.WorkflowStepConfig](#vmcpconfigworkflowstepconfig)



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
| `caBundlePath` _string_ | CABundlePath is the absolute file path to a PEM-encoded CA certificate bundle<br />used when the OIDC middleware performs HTTPS requests to the issuer<br />(OIDC discovery, JWKS fetch, token introspection). When set, the CA bundle<br />at this path is added to the trust store used for verifying the issuer's<br />TLS certificate. Typically populated by the Kubernetes operator from<br />MCPOIDCConfig.spec.inline.caBundleRef (ConfigMap) or from the in-cluster<br />service-account CA when using Kubernetes service-account auth. |  | Optional: \{\} <br /> |


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
| `embeddingProvider` _string_ | EmbeddingProvider selects the wire protocol used to talk to the embedding<br />service. "tei" speaks the HuggingFace Text Embeddings Inference API;<br />"openai" speaks the OpenAI-compatible /embeddings API, which lets the<br />optimizer use OpenAI, Azure OpenAI, or another OpenAI-compatible gateway.<br />Defaults to "tei" when empty.<br />The "openai" provider reads EmbeddingService directly and cannot be combined<br />with EmbeddingServerRef, which provisions a managed TEI server; the operator<br />rejects that combination at admission. | tei | Enum: [tei openai] <br />Optional: \{\} <br /> |
| `embeddingModel` _string_ | EmbeddingModel is the model name requested from the embedding service<br />(e.g. "text-embedding-3-small"). Required when EmbeddingProvider is<br />"openai". Ignored for the "tei" provider, where the model is fixed by the<br />running TEI container.<br />The API key for an OpenAI-compatible service is not configured here: it is<br />read from the OPENAI_API_KEY environment variable so the secret never<br />lands in a CRD spec or ConfigMap. An empty key omits the Authorization<br />header, which supports keyless in-cluster gateways. |  | Optional: \{\} <br /> |
| `embeddingHeaders` _object (keys:string, values:[vmcp.config.EmbeddingHeaderValue](#vmcpconfigembeddingheadervalue))_ | EmbeddingHeaders holds additional HTTP headers sent with every embedding<br />request. Only supported when EmbeddingProvider is "openai". Values are<br />stored in plain text and must not contain secrets; Authorization<br />(derived from OPENAI_API_KEY) and Content-Type cannot be set. |  | MaxProperties: 32 <br />Optional: \{\} <br /> |
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
| `default` _[pkg.json.Any](#pkgjsonany)_ | Default is the fallback value if template expansion fails.<br />Type coercion is applied to match the declared Type. |  | Schemaless: \{\} <br />Type: object <br />Optional: \{\} <br /> |


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

_Underlying type:_ _struct{Title *string "json:\"title,omitempty\" yaml:\"title,omitempty\""; ReadOnlyHint *bool "json:\"readOnlyHint,omitempty\" yaml:\"readOnlyHint,omitempty\""; DestructiveHint *bool "json:\"destructiveHint,omitempty\" yaml:\"destructiveHint,omitempty\""; IdempotentHint *bool "json:\"idempotentHint,omitempty\" yaml:\"idempotentHint,omitempty\""; OpenWorldHint *bool "json:\"openWorldHint,omitempty\" yaml:\"openWorldHint,omitempty\""}_

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
| `defaultResults` _[pkg.json.Map](#pkgjsonmap)_ | DefaultResults provides fallback output values when this step is skipped<br />(due to condition evaluating to false) or fails (when onError.action is "continue").<br />Each key corresponds to an output field name referenced by downstream steps.<br />Required if the step may be skipped AND downstream steps reference this step's output. |  | Schemaless: \{\} <br />Type: object <br />Optional: \{\} <br /> |
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





## toolhive.stacklok.dev/json


#### pkg.json.Any



Any is a type alias for Data[any], storing arbitrary JSON values.
This is the most flexible type, suitable when the JSON structure is unknown.

_Validation:_
- Type: object







#### pkg.json.Map



Map is a type alias for Data[map[string]any], storing JSON objects.
Use this when you know the data will always be a JSON object (not array, string, etc.).

_Validation:_
- Type: object






## toolhive.stacklok.dev/ratelimit


#### ratelimit.types.RateLimitBucket



RateLimitBucket defines a token bucket configuration with a maximum capacity
and a refill period. Used by both shared and per-user rate limits.



_Appears in:_
- [ratelimit.types.RateLimitConfig](#ratelimittypesratelimitconfig)
- [ratelimit.types.ToolRateLimitConfig](#ratelimittypestoolratelimitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `maxTokens` _integer_ | MaxTokens is the maximum number of tokens (bucket capacity).<br />This is also the burst size: the maximum number of requests that can be served<br />instantaneously before the bucket is depleted. |  | Minimum: 1 <br />Required: \{\} <br /> |
| `refillPeriod` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#duration-v1-meta)_ | RefillPeriod is the duration to fully refill the bucket from zero to maxTokens.<br />The effective refill rate is maxTokens / refillPeriod tokens per second.<br />Format: Go duration string (e.g., "1m0s", "30s", "1h0m0s"). |  | Required: \{\} <br /> |


#### ratelimit.types.RateLimitConfig



RateLimitConfig defines rate limiting configuration for an MCP server.
At least one of shared, perUser, or tools must be configured.



_Appears in:_
- [vmcp.config.Config](#vmcpconfigconfig)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `shared` _[ratelimit.types.RateLimitBucket](#ratelimittypesratelimitbucket)_ | Shared is a token bucket shared across all users for the entire server. |  | Optional: \{\} <br /> |
| `perUser` _[ratelimit.types.RateLimitBucket](#ratelimittypesratelimitbucket)_ | PerUser is a token bucket applied independently to each authenticated user<br />at the server level. Requires authentication to be enabled.<br />Each unique userID creates Redis keys that expire after 2x refillPeriod.<br />Memory formula: unique_users_per_TTL_window * (1 + num_tools_with_per_user_limits) keys. |  | Optional: \{\} <br /> |
| `tools` _[ratelimit.types.ToolRateLimitConfig](#ratelimittypestoolratelimitconfig) array_ | Tools defines per-tool rate limit overrides.<br />Each entry applies additional rate limits to calls targeting a specific tool name.<br />A request must pass both the server-level limit and the per-tool limit. |  | Optional: \{\} <br /> |


#### ratelimit.types.ToolRateLimitConfig



ToolRateLimitConfig defines rate limits for a specific tool.
At least one of shared or perUser must be configured.



_Appears in:_
- [ratelimit.types.RateLimitConfig](#ratelimittypesratelimitconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the MCP tool name this limit applies to. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `shared` _[ratelimit.types.RateLimitBucket](#ratelimittypesratelimitbucket)_ | Shared token bucket for this specific tool. |  | Optional: \{\} <br /> |
| `perUser` _[ratelimit.types.RateLimitBucket](#ratelimittypesratelimitbucket)_ | PerUser token bucket configuration for this tool. |  | Optional: \{\} <br /> |



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
- [api.v1alpha1.MCPAuthzConfig](#apiv1alpha1mcpauthzconfig)
- [api.v1alpha1.MCPAuthzConfigList](#apiv1alpha1mcpauthzconfiglist)
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
- [api.v1alpha1.MCPWebhookConfig](#apiv1alpha1mcpwebhookconfig)
- [api.v1alpha1.MCPWebhookConfigList](#apiv1alpha1mcpwebhookconfiglist)
- [api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition)
- [api.v1alpha1.VirtualMCPCompositeToolDefinitionList](#apiv1alpha1virtualmcpcompositetooldefinitionlist)
- [api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver)
- [api.v1alpha1.VirtualMCPServerList](#apiv1alpha1virtualmcpserverlist)



#### api.v1alpha1.EmbeddingServer



EmbeddingServer is the deprecated v1alpha1 version of the EmbeddingServer resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `EmbeddingServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)_ |  |  |  |
| `status` _[api.v1beta1.EmbeddingServerStatus](#apiv1beta1embeddingserverstatus)_ |  |  |  |


#### api.v1alpha1.EmbeddingServerList



EmbeddingServerList contains a list of EmbeddingServer.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `EmbeddingServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.EmbeddingServer](#apiv1alpha1embeddingserver) array_ |  |  |  |


#### api.v1alpha1.MCPAuthzConfig



MCPAuthzConfig is the deprecated v1alpha1 version of the MCPAuthzConfig resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPAuthzConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPAuthzConfigSpec](#apiv1beta1mcpauthzconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPAuthzConfigStatus](#apiv1beta1mcpauthzconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPAuthzConfigList



MCPAuthzConfigList contains a list of MCPAuthzConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPAuthzConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPAuthzConfig](#apiv1alpha1mcpauthzconfig) array_ |  |  |  |


#### api.v1alpha1.MCPExternalAuthConfig



MCPExternalAuthConfig is the deprecated v1alpha1 version of the MCPExternalAuthConfig resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPExternalAuthConfigStatus](#apiv1beta1mcpexternalauthconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPExternalAuthConfigList



MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPExternalAuthConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPExternalAuthConfig](#apiv1alpha1mcpexternalauthconfig) array_ |  |  |  |


#### api.v1alpha1.MCPGroup



MCPGroup is the deprecated v1alpha1 version of the MCPGroup resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroup` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPGroupSpec](#apiv1beta1mcpgroupspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPGroupStatus](#apiv1beta1mcpgroupstatus)_ |  |  |  |


#### api.v1alpha1.MCPGroupList



MCPGroupList contains a list of MCPGroup.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPGroupList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPGroup](#apiv1alpha1mcpgroup) array_ |  |  |  |


#### api.v1alpha1.MCPOIDCConfig



MCPOIDCConfig is the deprecated v1alpha1 version of the MCPOIDCConfig resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPOIDCConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPOIDCConfigSpec](#apiv1beta1mcpoidcconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPOIDCConfigStatus](#apiv1beta1mcpoidcconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPOIDCConfigList



MCPOIDCConfigList contains a list of MCPOIDCConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPOIDCConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPOIDCConfig](#apiv1alpha1mcpoidcconfig) array_ |  |  |  |


#### api.v1alpha1.MCPRegistry



MCPRegistry is the deprecated v1alpha1 version of the MCPRegistry resource.
The MCPRegistry CRD as a whole is deprecated and will be removed in a future
release; install the ToolHive registry server via the toolhive-registry-server
Helm chart instead: https://github.com/stacklok/toolhive-registry-server





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPRegistrySpec](#apiv1beta1mcpregistryspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPRegistryStatus](#apiv1beta1mcpregistrystatus)_ |  |  |  |


#### api.v1alpha1.MCPRegistryList



MCPRegistryList contains a list of MCPRegistry.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRegistryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPRegistry](#apiv1alpha1mcpregistry) array_ |  |  |  |


#### api.v1alpha1.MCPRemoteProxy



MCPRemoteProxy is the deprecated v1alpha1 version of the MCPRemoteProxy resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxy` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPRemoteProxyStatus](#apiv1beta1mcpremoteproxystatus)_ |  |  |  |


#### api.v1alpha1.MCPRemoteProxyList



MCPRemoteProxyList contains a list of MCPRemoteProxy.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPRemoteProxyList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPRemoteProxy](#apiv1alpha1mcpremoteproxy) array_ |  |  |  |


#### api.v1alpha1.MCPServer



MCPServer is the deprecated v1alpha1 version of the MCPServer resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPServerStatus](#apiv1beta1mcpserverstatus)_ |  |  |  |


#### api.v1alpha1.MCPServerEntry



MCPServerEntry is the deprecated v1alpha1 version of the MCPServerEntry resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServerEntry` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPServerEntrySpec](#apiv1beta1mcpserverentryspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPServerEntryStatus](#apiv1beta1mcpserverentrystatus)_ |  |  |  |


#### api.v1alpha1.MCPServerEntryList



MCPServerEntryList contains a list of MCPServerEntry.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServerEntryList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPServerEntry](#apiv1alpha1mcpserverentry) array_ |  |  |  |


#### api.v1alpha1.MCPServerList



MCPServerList contains a list of MCPServer.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPServer](#apiv1alpha1mcpserver) array_ |  |  |  |


#### api.v1alpha1.MCPTelemetryConfig



MCPTelemetryConfig is the deprecated v1alpha1 version of the MCPTelemetryConfig resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPTelemetryConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPTelemetryConfigSpec](#apiv1beta1mcptelemetryconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPTelemetryConfigStatus](#apiv1beta1mcptelemetryconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPTelemetryConfigList



MCPTelemetryConfigList contains a list of MCPTelemetryConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPTelemetryConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPTelemetryConfig](#apiv1alpha1mcptelemetryconfig) array_ |  |  |  |


#### api.v1alpha1.MCPToolConfig



MCPToolConfig is the deprecated v1alpha1 version of the MCPToolConfig resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPToolConfigSpec](#apiv1beta1mcptoolconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPToolConfigStatus](#apiv1beta1mcptoolconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPToolConfigList



MCPToolConfigList contains a list of MCPToolConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPToolConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPToolConfig](#apiv1alpha1mcptoolconfig) array_ |  |  |  |


#### api.v1alpha1.MCPWebhookConfig



MCPWebhookConfig is the Schema for the mcpwebhookconfigs API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPWebhookConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPWebhookConfigSpec](#apiv1beta1mcpwebhookconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPWebhookConfigStatus](#apiv1beta1mcpwebhookconfigstatus)_ |  |  |  |


#### api.v1alpha1.MCPWebhookConfigList



MCPWebhookConfigList contains a list of MCPWebhookConfig.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `MCPWebhookConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.MCPWebhookConfig](#apiv1alpha1mcpwebhookconfig) array_ |  |  |  |


#### api.v1alpha1.VirtualMCPCompositeToolDefinition



VirtualMCPCompositeToolDefinition is the deprecated v1alpha1 version of the VirtualMCPCompositeToolDefinition resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinition` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.VirtualMCPCompositeToolDefinitionSpec](#apiv1beta1virtualmcpcompositetooldefinitionspec)_ |  |  |  |
| `status` _[api.v1beta1.VirtualMCPCompositeToolDefinitionStatus](#apiv1beta1virtualmcpcompositetooldefinitionstatus)_ |  |  |  |


#### api.v1alpha1.VirtualMCPCompositeToolDefinitionList



VirtualMCPCompositeToolDefinitionList contains a list of VirtualMCPCompositeToolDefinition.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPCompositeToolDefinitionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.VirtualMCPCompositeToolDefinition](#apiv1alpha1virtualmcpcompositetooldefinition) array_ |  |  |  |


#### api.v1alpha1.VirtualMCPServer



VirtualMCPServer is the deprecated v1alpha1 version of the VirtualMCPServer resource.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)_ |  |  |  |
| `status` _[api.v1beta1.VirtualMCPServerStatus](#apiv1beta1virtualmcpserverstatus)_ |  |  |  |


#### api.v1alpha1.VirtualMCPServerList



VirtualMCPServerList contains a list of VirtualMCPServer.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1alpha1` | | |
| `kind` _string_ | `VirtualMCPServerList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1alpha1.VirtualMCPServer](#apiv1alpha1virtualmcpserver) array_ |  |  |  |



## toolhive.stacklok.dev/v1beta1
### Resource Types
- [api.v1beta1.EmbeddingServer](#apiv1beta1embeddingserver)
- [api.v1beta1.EmbeddingServerList](#apiv1beta1embeddingserverlist)
- [api.v1beta1.MCPAuthzConfig](#apiv1beta1mcpauthzconfig)
- [api.v1beta1.MCPAuthzConfigList](#apiv1beta1mcpauthzconfiglist)
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
| `subjectProviderName` _string_ | SubjectProviderName is the name of the upstream provider whose access token<br />is used as the web identity token for STS AssumeRoleWithWebIdentity.<br />This field is used exclusively by VirtualMCPServer, where there is no<br />upstream swap middleware to replace the bearer token before the strategy runs.<br />When left empty and an embedded authorization server is configured on the<br />VirtualMCPServer, the controller automatically populates this field with<br />the first configured upstream provider name. Set it explicitly to override<br />that default or to select a specific provider when multiple upstreams are<br />configured.<br />When no embedded auth server is present, the bearer token from the incoming<br />request's Authorization header is used instead. |  | Optional: \{\} <br /> |


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
| `groupClaimName` _string_ | GroupClaimName is the JWT claim key that contains group membership for the<br />principal. When set, takes priority over the well-known defaults<br />("groups", "roles", "cognito:groups"). Use this for IDPs that place<br />groups under a URI-style claim (e.g. "https://example.com/groups"). When<br />Type is "configMap", a group_claim_name entry in the referenced ConfigMap<br />is overridden by this field if both are set. |  | MaxLength: 253 <br />Optional: \{\} <br /> |
| `roleClaimName` _string_ | RoleClaimName is the JWT claim key that contains role membership for the<br />principal. When set, the claim is extracted separately from GroupClaimName<br />and both are mapped to the configured GroupEntityType. When Type is<br />"configMap", a role_claim_name entry in the referenced ConfigMap is<br />overridden by this field if both are set. |  | MaxLength: 253 <br />Optional: \{\} <br /> |
| `groupEntityType` _string_ | GroupEntityType is the Cedar entity type name used for principal parent<br />UIDs synthesised from JWT group/role claims. Defaults to "THVGroup" when<br />empty. Must match the entity type used in the static entity store for<br />transitive `in` checks (e.g. `ClaimGroup → PlatformRole`) to resolve.<br />Namespaced names (`Foo::Bar`) are not yet supported. When Type is<br />"configMap", a group_entity_type entry in the referenced ConfigMap is<br />overridden by this field if both are set. |  | MaxLength: 63 <br />Pattern: `^[A-Za-z_][A-Za-z0-9_]*$` <br />Optional: \{\} <br /> |


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


#### api.v1beta1.DCRUpstreamConfig



DCRUpstreamConfig configures RFC 7591 Dynamic Client Registration for an
OAuth 2.0 upstream. When present on an OAuth2 upstream, the authserver
performs registration at runtime to obtain client credentials, replacing
the need to pre-provision a ClientID.

Exactly one of DiscoveryURL or RegistrationEndpoint must be set. DiscoveryURL
points at an RFC 8414 / OIDC Discovery document from which the registration
endpoint is resolved; RegistrationEndpoint is used directly when the upstream
does not publish discovery metadata.

The XOR rule uses has() alone (not has() + size() > 0) to keep the rule's
estimated CEL cost under the apiserver's per-rule static budget. With
`omitempty` on both fields, an unset field is absent on the wire and has()
returns false; the explicit-empty-string edge case is rejected at reconcile
time by ValidateOAuth2DCRConfig.



_Appears in:_
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `discoveryUrl` _string_ | DiscoveryURL is the RFC 8414 / OIDC Discovery document URL. The resolver<br />issues a single GET against this URL (no well-known-path fallback) and<br />reads registration_endpoint, authorization_endpoint, token_endpoint,<br />token_endpoint_auth_methods_supported, and scopes_supported from the<br />response.<br />Mutually exclusive with RegistrationEndpoint.<br />HTTPS is required because the registration endpoint resolved from this<br />document carries the initial access token and the issued client_secret<br />(RFC 7591 §3, RFC 8414 §3). MaxLength is a defensive size cap (etcd<br />object budget, regex evaluation cost) and matches the conventional URL<br />length cap. |  | MaxLength: 2048 <br />Pattern: `^https://[^\s?#]+[^/\s?#]$` <br />Optional: \{\} <br /> |
| `registrationEndpoint` _string_ | RegistrationEndpoint is the RFC 7591 registration endpoint URL used<br />directly, bypassing discovery. When using this field, the caller is<br />expected to also supply AuthorizationEndpoint, TokenEndpoint, and an<br />explicit Scopes list on the parent OAuth2UpstreamConfig.<br />Mutually exclusive with DiscoveryURL.<br />HTTPS is required because the registration endpoint carries the initial<br />access token and the issued client_secret (RFC 7591 §3, RFC 8414 §3).<br />MaxLength is a defensive size cap (etcd object budget, regex evaluation<br />cost) and matches the conventional URL length cap. |  | MaxLength: 2048 <br />Pattern: `^https://[^\s?#]+[^/\s?#]$` <br />Optional: \{\} <br /> |
| `initialAccessTokenRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | InitialAccessTokenRef is an optional reference to a Kubernetes Secret<br />carrying an RFC 7591 §3 initial access token. When set, the resolver<br />presents the token value as a Bearer credential on the registration<br />request. Mirrors the ClientSecretRef pattern. |  | Optional: \{\} <br /> |
| `softwareId` _string_ | SoftwareID is the RFC 7591 "software_id" registration metadata value,<br />identifying the client software independent of any particular<br />registration instance. Typically a UUID or short identifier. |  | MaxLength: 255 <br />Optional: \{\} <br /> |
| `softwareStatement` _string_ | SoftwareStatement is the RFC 7591 "software_statement" JWT asserting<br />metadata about the client software, signed by a party the authorization<br />server trusts.<br />Stored inline on the CR. The JWT is signed but not encrypted, so its<br />contents are visible to anyone with get/list/watch on this resource and<br />appear in etcd backups in plaintext. Treat the value as non-confidential<br />(signed attestation, not a secret). Operators that rotate software<br />statements like bearer credentials should keep them at the authorization<br />server side and rely on the registration endpoint's initial access<br />token (see InitialAccessTokenRef) instead of placing them on the CR.<br />Bounded to 16384 characters as a defensive size cap (etcd object<br />budget, regex evaluation cost). Real-world signed statements with<br />embedded x5c certificate chains, JWKS keys, or OIDC-Federation<br />trust-framework metadata routinely exceed 4 KB. |  | MaxLength: 16384 <br />Optional: \{\} <br /> |


#### api.v1beta1.DiscoveredBackend



DiscoveredBackend is an alias to the canonical definition in pkg/vmcp/types.go
This provides a local name for use in the CRD status.







#### api.v1beta1.EmbeddedAuthServerCIMDConfig



EmbeddedAuthServerCIMDConfig configures Client ID Metadata Document (CIMD) support
on the embedded authorization server. When enabled, the AS accepts HTTPS URLs as
client_id values and resolves them via the CIMD protocol, allowing clients such as
VS Code to authenticate without prior Dynamic Client Registration.



_Appears in:_
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled activates CIMD client lookup. When false (the default), the AS only<br />accepts client_id values that were registered via DCR. | false |  |
| `cacheMaxSize` _integer_ | CacheMaxSize is the maximum number of CIMD documents held in the LRU cache.<br />Defaults to 256 when Enabled is true and this field is omitted. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `cacheFallbackTtl` _string_ | CacheFallbackTTL is the fixed TTL applied to every cached CIMD document.<br />Cache-Control header parsing is not yet implemented; all entries use this value.<br />Format: Go duration string (e.g. "5m", "10m", "1h").<br />Defaults to 5 minutes when Enabled is true and this field is omitted. |  | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |


#### api.v1beta1.EmbeddedAuthServerConfig



EmbeddedAuthServerConfig holds configuration for the embedded OAuth2/OIDC authorization server.
This enables running an authorization server that delegates authentication to upstream IDPs.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuer` _string_ | Issuer is the issuer identifier for this authorization server.<br />This will be included in the "iss" claim of issued tokens.<br />Must be a valid HTTPS URL (or HTTP for localhost, or HTTP for trusted in-cluster hosts when<br />insecureAllowHTTP is true) without query, fragment, or trailing slash (per RFC 8414). |  | Pattern: `^https?://[^\s?#]+[^/\s?#]$` <br />Required: \{\} <br /> |
| `authorizationEndpointBaseUrl` _string_ | AuthorizationEndpointBaseURL overrides the base URL used for the authorization_endpoint<br />in the OAuth discovery document. When set, the discovery document will advertise<br />`\{authorizationEndpointBaseUrl\}/oauth/authorize` instead of `\{issuer\}/oauth/authorize`.<br />All other endpoints (token, registration, JWKS) remain derived from the issuer.<br />This is useful when the browser-facing authorization endpoint needs to be on a<br />different host than the issuer used for backend-to-backend calls.<br />Must be a valid HTTPS URL (or HTTP for localhost, or HTTP for trusted in-cluster hosts<br />when insecureAllowHTTP is true) without query, fragment, or trailing slash. |  | Pattern: `^https?://[^\s?#]+[^/\s?#]$` <br />Optional: \{\} <br /> |
| `signingKeySecretRefs` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref) array_ | SigningKeySecretRefs references Kubernetes Secrets containing signing keys for JWT operations.<br />Supports key rotation by allowing multiple keys (oldest keys are used for verification only).<br />If not specified, an ephemeral signing key will be auto-generated (development only -<br />JWTs will be invalid after restart). |  | MaxItems: 5 <br />Optional: \{\} <br /> |
| `hmacSecretRefs` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref) array_ | HMACSecretRefs references Kubernetes Secrets containing symmetric secrets for signing<br />authorization codes and refresh tokens (opaque tokens).<br />Current secret must be at least 32 bytes and cryptographically random.<br />Supports secret rotation via multiple entries (first is current, rest are for verification).<br />If not specified, an ephemeral secret will be auto-generated (development only -<br />auth codes and refresh tokens will be invalid after restart). |  | Optional: \{\} <br /> |
| `tokenLifespans` _[api.v1beta1.TokenLifespanConfig](#apiv1beta1tokenlifespanconfig)_ | TokenLifespans configures the duration that various tokens are valid.<br />If not specified, defaults are applied (access: 1h, refresh: 7d, authCode: 10m). |  | Optional: \{\} <br /> |
| `upstreamProviders` _[api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig) array_ | UpstreamProviders configures connections to upstream Identity Providers.<br />The embedded auth server delegates authentication to these providers.<br />MCPServer and MCPRemoteProxy support a single upstream; VirtualMCPServer supports multiple. |  | MinItems: 1 <br />Required: \{\} <br /> |
| `primaryUpstreamProvider` _string_ | PrimaryUpstreamProvider names the upstream IDP whose access token Cedar<br />should read claims from when authorising a request. Must match the name<br />of one of the entries in UpstreamProviders. When empty, the controller<br />auto-selects the first entry of UpstreamProviders.<br />Only meaningful on VirtualMCPServer, where multiple upstream providers<br />can be configured and Cedar needs to pick which token's claims to<br />evaluate. The VirtualMCPServer controller validates this field against<br />UpstreamProviders at admission and rejects unresolvable values.<br />On MCPServer and MCPRemoteProxy this field is structurally present (the<br />EmbeddedAuthServerConfig struct is shared) but has no runtime effect:<br />those CRDs are restricted to a single upstream so there is no choice to<br />make. Setting it on those CRDs is silently ignored. |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `storage` _[api.v1beta1.AuthServerStorageConfig](#apiv1beta1authserverstorageconfig)_ | Storage configures the storage backend for the embedded auth server.<br />If not specified, defaults to in-memory storage. |  | Optional: \{\} <br /> |
| `disableUpstreamTokenInjection` _boolean_ | DisableUpstreamTokenInjection prevents the embedded auth server from injecting<br />upstream IdP tokens into requests forwarded to the backend MCP server.<br />When true, the embedded auth server still handles OAuth flows for clients,<br />but instead of swapping ToolHive JWTs for upstream tokens the proxy STRIPS<br />the client's credential headers (Authorization, Cookie, Proxy-Authorization)<br />after validating the JWT — the backend receives an unauthenticated request.<br />Use headerForward to attach static credentials (e.g. an API key) if the<br />backend needs them. Cannot be combined with token exchange or AWS STS,<br />which would re-add credentials after the strip.<br />This is useful when the backend MCP server does not require authentication<br />(e.g., public documentation servers) but you still want client authentication. | false | Optional: \{\} <br /> |
| `insecureAllowHTTP` _boolean_ | InsecureAllowHTTP permits an http:// issuer URL for non-localhost hosts.<br />Only set this for in-cluster Kubernetes deployments where traffic between<br />pods traverses a trusted network (e.g. the in-cluster service mesh).<br />Production deployments reachable outside the cluster MUST use https://.<br />On VirtualMCPServer: when false (the default), http:// issuers for non-localhost<br />hosts are rejected at reconcile time with an AuthServerConfigValidated=False condition.<br />On MCPServer and MCPRemoteProxy (via MCPExternalAuthConfig): this field is<br />structurally present but enforcement is deferred to pod startup via Config.Validate();<br />a misconfigured issuer will cause the pod to crash at startup rather than surface<br />as an operator condition. | false | Optional: \{\} <br /> |
| `baselineClientScopes` _string array_ | BaselineClientScopes is a baseline set of OAuth 2.0 scopes guaranteed to be<br />included in every client registration. The embedded auth server unions these<br />scopes into the registered set returned by RFC 7591 Dynamic Client<br />Registration, so a client that narrows the `scope` field at /oauth/register<br />can still request the baseline scopes at /oauth/authorize. All values must<br />be present in the upstream-derived scopesSupported set; the auth server<br />fails to start if any value is missing.<br />Security: every client registered via /oauth/register will gain the<br />ability to request these scopes at /oauth/authorize, regardless of what<br />the client itself requested. Keep the baseline narrow (typically<br />"openid" and "offline_access"). Adding a privileged scope here — e.g.<br />"admin:read" — would grant it to every DCR-registered client, including<br />public clients like Claude Code, Cursor, and VS Code.<br />When cimd.enabled is true, every dynamically resolved CIMD client will<br />also gain the ability to request these scopes, including third-party<br />clients resolved from arbitrary HTTPS URLs. |  | MaxItems: 10 <br />items:MinLength: 1 <br />items:Pattern: `^[\x21\x23-\x5B\x5D-\x7E]+$` <br />Optional: \{\} <br /> |
| `cimd` _[api.v1beta1.EmbeddedAuthServerCIMDConfig](#apiv1beta1embeddedauthservercimdconfig)_ | CIMD configures Client ID Metadata Document support. When omitted, CIMD is disabled. |  | Optional: \{\} <br /> |


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
| `imagePullPolicy` _[PullPolicy](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#pullpolicy-v1-core)_ | ImagePullPolicy defines the pull policy for the container image | IfNotPresent | Enum: [Always Never IfNotPresent] <br />Optional: \{\} <br /> |
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
| `upstreamInject` | ExternalAuthTypeUpstreamInject is the type for upstream token injection<br />This injects an upstream IdP access token as the Authorization: Bearer header<br /> |
| `obo` | ExternalAuthTypeOBO is the type for on-behalf-of (OBO) flows.<br />This type requires a build with an OBO handler registered via<br />controllerutil.RegisterOBOHandler; an upstream-only build surfaces<br />status.conditions[Valid] = False with Reason: EnterpriseRequired<br />when an obo-typed MCPExternalAuthConfig is applied.<br /> |
| `xaa` | ExternalAuthTypeXAA is the type for XAA (Cross-Application Access) auth.<br />XAA performs a two-step token exchange to obtain access tokens for target services:<br />  - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT<br />  - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token<br /> |


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


#### api.v1beta1.IdentityFromTokenConfig



IdentityFromTokenConfig extracts user identity (subject, name, email) directly from the
OAuth2 token-endpoint response body using gjson dot-notation paths. When configured on an
OAuth2UpstreamConfig, the embedded auth server skips the userinfo HTTP call entirely and
resolves identity from the token response.

Paths use gjson dot-notation, where each segment is a JSON object key. For example,
"username" extracts a top-level field, and "authed_user.id" extracts a nested field.

Trust-model warning: Identity claims extracted via this block are not cryptographically
verified — they are trusted only via the TLS connection to the token endpoint. Prefer
OIDC + ID token validation when verifiable claims are required.

Subject uniqueness is scoped to the upstream provider entry. To keep identity namespaces
separate across multiple instances of the same provider (e.g., two Snowflake accounts),
use distinct upstream provider entries.



_Appears in:_
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `subjectPath` _string_ | SubjectPath is the dot-notation path to the subject (user ID) field in the token response.<br />Warning: claims read from the token response are trusted only via TLS, not<br />cryptographically verified; prefer OIDC ID tokens when verifiable claims are required.<br />Example: "authed_user.id" for Slack (top-level token-response field). For providers<br />whose token response embeds the access token as a JWT (e.g. Snowflake), use the<br />"@upstreamjwt" modifier to decode the payload, e.g. "access_token\|@upstreamjwt\|sub".<br />The "@upstreamjwt" modifier performs no signature verification either. |  | MaxLength: 256 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `namePath` _string_ | NamePath is the dot-notation path to the display name field in the token response.<br />If not specified or if the path does not resolve to a string, the display name is omitted.<br />Omit the field entirely rather than setting it to an empty string. |  | MaxLength: 256 <br />MinLength: 1 <br />Optional: \{\} <br /> |
| `emailPath` _string_ | EmailPath is the dot-notation path to the email address field in the token response.<br />If not specified or if the path does not resolve to a string, the email is omitted.<br />Omit the field entirely rather than setting it to an empty string. |  | MaxLength: 256 <br />MinLength: 1 <br />Optional: \{\} <br /> |


#### api.v1beta1.IncomingAuthConfig



IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server



_Appears in:_
- [api.v1beta1.VirtualMCPServerSpec](#apiv1beta1virtualmcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type defines the authentication type: anonymous or oidc<br />When no authentication is required, explicitly set this to "anonymous" |  | Enum: [anonymous oidc] <br />Required: \{\} <br /> |
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this VirtualMCPServer.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration.<br />Reuses MCPServer authz patterns.<br />AuthzConfig and AuthzConfigRef are mutually exclusive. |  | Optional: \{\} <br /> |
| `authzConfigRef` _[api.v1beta1.MCPAuthzConfigReference](#apiv1beta1mcpauthzconfigreference)_ | AuthzConfigRef references a shared MCPAuthzConfig resource for authorization.<br />The referenced MCPAuthzConfig must exist in the same namespace as this VirtualMCPServer.<br />Mutually exclusive with authzConfig.<br />Only cedarv1 MCPAuthzConfig resources are supported for VirtualMCPServer<br />today; referencing a non-Cedar config fails reconciliation with a clear<br />error because the vMCP runtime authz middleware is Cedar-only. |  | Optional: \{\} <br /> |


#### api.v1beta1.InlineAuthzConfig



InlineAuthzConfig contains direct authorization configuration.

Source-agnostic Cedar JWT-claim mapping settings (GroupClaimName,
RoleClaimName, GroupEntityType) live on the parent AuthzConfigRef so they
work the same way for inline and configMap-sourced authz.



_Appears in:_
- [api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policies` _string array_ | Policies is a list of Cedar policy strings |  | MinItems: 1 <br />Required: \{\} <br /> |
| `entitiesJson` _string_ | EntitiesJSON is a JSON string representing Cedar entities. Required when<br />transitive policies (e.g. `ClaimGroup → PlatformRole`) need a static<br />entity store; defaults to "[]". | [] | Optional: \{\} <br /> |
| `primaryUpstreamProvider` _string_ | PrimaryUpstreamProvider names the upstream IDP whose access token's<br />claims Cedar should evaluate.<br />Deprecated: on VirtualMCPServer this field has moved to<br />spec.authServerConfig.primaryUpstreamProvider. The old location is<br />still read for one release for backward compatibility; the<br />VirtualMCPServer controller emits an AuthzPrimaryUpstreamProviderDeprecated<br />Warning event whenever it is consumed, and removal is planned for the<br />release after the deprecation cycle.<br />On MCPServer and MCPRemoteProxy this field has always been a structural<br />no-op (those CRDs do not run an embedded auth server). Setting it<br />continues to surface the AuthzPrimaryUpstreamProviderIgnored advisory<br />condition; the deprecation does not change that behaviour. |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Optional: \{\} <br /> |


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


#### api.v1beta1.MCPAuthzConfig



MCPAuthzConfig is the Schema for the mcpauthzconfigs API.
MCPAuthzConfig resources are namespace-scoped and can only be referenced by
MCPServer, MCPRemoteProxy, or VirtualMCPServer resources within the same namespace.
Cross-namespace references are not supported for security and isolation reasons.



_Appears in:_
- [api.v1beta1.MCPAuthzConfigList](#apiv1beta1mcpauthzconfiglist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPAuthzConfig` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[api.v1beta1.MCPAuthzConfigSpec](#apiv1beta1mcpauthzconfigspec)_ |  |  |  |
| `status` _[api.v1beta1.MCPAuthzConfigStatus](#apiv1beta1mcpauthzconfigstatus)_ |  |  |  |


#### api.v1beta1.MCPAuthzConfigList



MCPAuthzConfigList contains a list of MCPAuthzConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `toolhive.stacklok.dev/v1beta1` | | |
| `kind` _string_ | `MCPAuthzConfigList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[api.v1beta1.MCPAuthzConfig](#apiv1beta1mcpauthzconfig) array_ |  |  |  |


#### api.v1beta1.MCPAuthzConfigReference



MCPAuthzConfigReference references a shared MCPAuthzConfig resource.
The referenced MCPAuthzConfig must be in the same namespace as the referencing workload.



_Appears in:_
- [api.v1beta1.IncomingAuthConfig](#apiv1beta1incomingauthconfig)
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPAuthzConfig resource in the same namespace. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.MCPAuthzConfigSpec



MCPAuthzConfigSpec defines the desired state of MCPAuthzConfig.
MCPAuthzConfig resources are namespace-scoped and can only be referenced by
MCPServer, MCPRemoteProxy, or VirtualMCPServer resources in the same namespace.



_Appears in:_
- [api.v1beta1.MCPAuthzConfig](#apiv1beta1mcpauthzconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type identifies the authorizer backend (e.g., "cedarv1", "httpv1").<br />Must match a registered authorizer type in the factory registry. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `config` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | Config contains the backend-specific authorization configuration.<br />The structure depends on the Type field:<br />  - cedarv1: policies ([]string), entities_json (string), primary_upstream_provider (string), group_claim_name (string)<br />  - httpv1: http (\{url, timeout, insecure_skip_verify\}), context (\{include_args, include_operation\}), claim_mapping (string) |  | Type: object <br /> |


#### api.v1beta1.MCPAuthzConfigStatus



MCPAuthzConfigStatus defines the observed state of MCPAuthzConfig



_Appears in:_
- [api.v1beta1.MCPAuthzConfig](#apiv1beta1mcpauthzconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPAuthzConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPAuthzConfig. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referenceCount` _integer_ | ReferenceCount is the number of workloads referencing this config. |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPAuthzConfig.<br />Each entry identifies the workload by kind and name. The map key is the<br />(kind, name) pair so two workloads of different kinds that share a name<br />(e.g., an MCPServer "foo" and a VirtualMCPServer "foo") are distinct<br />entries rather than colliding under merge-patch semantics. |  | Optional: \{\} <br /> |


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
| `type` _[api.v1beta1.ExternalAuthType](#apiv1beta1externalauthtype)_ | Type is the type of external authentication to configure.<br />When set to "obo", the cluster must run a build that has registered an<br />OBO handler via controllerutil.RegisterOBOHandler; upstream-only builds<br />surface status.conditions[Valid] = False with Reason: EnterpriseRequired<br />for obo-typed configs. |  | Enum: [tokenExchange headerInjection bearerToken unauthenticated embeddedAuthServer awsSts upstreamInject obo xaa] <br />Required: \{\} <br /> |
| `tokenExchange` _[api.v1beta1.TokenExchangeConfig](#apiv1beta1tokenexchangeconfig)_ | TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange<br />Only used when Type is "tokenExchange" |  | Optional: \{\} <br /> |
| `headerInjection` _[api.v1beta1.HeaderInjectionConfig](#apiv1beta1headerinjectionconfig)_ | HeaderInjection configures custom HTTP header injection<br />Only used when Type is "headerInjection" |  | Optional: \{\} <br /> |
| `bearerToken` _[api.v1beta1.BearerTokenConfig](#apiv1beta1bearertokenconfig)_ | BearerToken configures bearer token authentication<br />Only used when Type is "bearerToken" |  | Optional: \{\} <br /> |
| `embeddedAuthServer` _[api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)_ | EmbeddedAuthServer configures an embedded OAuth2/OIDC authorization server<br />Only used when Type is "embeddedAuthServer" |  | Optional: \{\} <br /> |
| `awsSts` _[api.v1beta1.AWSStsConfig](#apiv1beta1awsstsconfig)_ | AWSSts configures AWS STS authentication with SigV4 request signing<br />Only used when Type is "awsSts" |  | Optional: \{\} <br /> |
| `upstreamInject` _[api.v1beta1.UpstreamInjectSpec](#apiv1beta1upstreaminjectspec)_ | UpstreamInject configures upstream token injection for backend requests.<br />Only used when Type is "upstreamInject". |  | Optional: \{\} <br /> |
| `obo` _[api.v1beta1.OBOConfig](#apiv1beta1oboconfig)_ | OBO configures On-Behalf-Of (OBO) authentication.<br />Only used when Type is "obo". Setting this field on an upstream-only build<br />causes the MCPExternalAuthConfig to transition to<br />status.conditions[Valid] = False with Reason: EnterpriseRequired, because<br />no OBO handler is registered. See OBOConfig for the field-to-runtime<br />contract mapping. |  | Optional: \{\} <br /> |
| `xaa` _[api.v1beta1.XAASpec](#apiv1beta1xaaspec)_ | XAA configures XAA (Cross-Application Access) auth for backend requests.<br />Only used when Type is "xaa". |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPExternalAuthConfigStatus



MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfig](#apiv1beta1mcpexternalauthconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations of the MCPExternalAuthConfig's state |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.<br />It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server. |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the current configuration for change detection |  | Optional: \{\} <br /> |
| `referenceCount` _integer_ | ReferenceCount is the number of workloads referencing this config. |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPExternalAuthConfig.<br />Each entry identifies the workload by kind and name. The map key is the<br />(kind, name) pair so two workloads of different kinds that share a name<br />(e.g., an MCPServer "foo" and a VirtualMCPServer "foo") are distinct<br />entries rather than colliding under merge-patch semantics. |  | Optional: \{\} <br /> |


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
| `referenceCount` _integer_ | ReferenceCount is the number of workloads referencing this config. |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPOIDCConfig.<br />Each entry identifies the workload by kind and name. The map key is the<br />(kind, name) pair so two workloads of different kinds that share a name<br />(e.g., an MCPServer "foo" and a VirtualMCPServer "foo") are distinct<br />entries rather than colliding under merge-patch semantics. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPRegistry



MCPRegistry is the Schema for the mcpregistries API.

The MCPRegistry CRD is deprecated and will be removed in a future release.
Install the ToolHive registry server via the toolhive-registry-server Helm chart
instead: https://github.com/stacklok/toolhive-registry-server



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
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#localobjectreference-v1-core) array_ | ImagePullSecrets allows specifying image pull secrets for the registry API workload.<br />These are applied to both the registry-api Deployment's PodSpec.ImagePullSecrets<br />and to the operator-managed ServiceAccount the registry API runs as, so private<br />images are pullable through either path.<br />Use this field for new manifests.<br />Important: this is the ONLY way to attach image-pull credentials to the<br />operator-managed ServiceAccount. The legacy<br />spec.podTemplateSpec.spec.imagePullSecrets path populates the Deployment's pod<br />spec ONLY — it does NOT touch the ServiceAccount. On managed Kubernetes<br />platforms that rely on ServiceAccount-level credential injection (for example<br />GKE Workload Identity, OpenShift's per-SA dockercfg secrets, EKS IRSA), using<br />only the legacy PodTemplateSpec path can fail to pull private images even when<br />the secret exists in the namespace. Always set spec.imagePullSecrets when<br />SA-level credentials matter.<br />Precedence with PodTemplateSpec:<br />  - This field is applied first as the controller-generated default.<br />  - Values set under spec.podTemplateSpec.spec.imagePullSecrets are user overrides<br />    and win on overlap. If the user supplies imagePullSecrets via PodTemplateSpec,<br />    those replace the default list on the Deployment (the list is treated atomically).<br />  - The ServiceAccount is always populated from this field — PodTemplateSpec does not<br />    affect the ServiceAccount.<br />An omitted field and an explicitly empty list are equivalent: both leave the<br />ServiceAccount's existing ImagePullSecrets unchanged. This preserves<br />platform-managed pull secrets (for example OpenShift's per-SA dockercfg<br />entries) when overlays or patches emit an empty list. Truly clearing the<br />ServiceAccount's pull secrets requires recreating the resource. |  | Optional: \{\} <br /> |


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
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this MCPRemoteProxy.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource.<br />SECURITY: if this field is omitted and no other authentication source is configured,<br />the proxy runs UNAUTHENTICATED. It accepts every request that can reach its port and<br />forwards it to the remote MCP server under a synthetic local-user identity, with no<br />token or credential check. Set this field to enforce identity-based access control<br />per request. |  | Optional: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.<br />When specified, the proxy will exchange validated incoming tokens for remote service tokens.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy. |  | Optional: \{\} <br /> |
| `authServerRef` _[api.v1beta1.AuthServerRef](#apiv1beta1authserverref)_ | AuthServerRef optionally references a resource that configures an embedded<br />OAuth 2.0/OIDC authorization server to authenticate MCP clients.<br />Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer). |  | Optional: \{\} <br /> |
| `headerForward` _[api.v1beta1.HeaderForwardConfig](#apiv1beta1headerforwardconfig)_ | HeaderForward configures headers to inject into requests to the remote MCP server.<br />Use this to add custom headers like X-Tenant-ID or correlation IDs. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the proxy.<br />AuthzConfig and AuthzConfigRef are mutually exclusive. |  | Optional: \{\} <br /> |
| `authzConfigRef` _[api.v1beta1.MCPAuthzConfigReference](#apiv1beta1mcpauthzconfigreference)_ | AuthzConfigRef references a shared MCPAuthzConfig resource for authorization.<br />The referenced MCPAuthzConfig must exist in the same namespace as this MCPRemoteProxy.<br />Mutually exclusive with authzConfig. |  | Optional: \{\} <br /> |
| `audit` _[api.v1beta1.AuditConfig](#apiv1beta1auditconfig)_ | Audit defines audit logging configuration for the proxy |  | Optional: \{\} <br /> |
| `toolConfigRef` _[api.v1beta1.ToolConfigRef](#apiv1beta1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons.<br />If specified, this allows filtering and overriding tools from the remote MCP server. |  | Optional: \{\} <br /> |
| `telemetryConfigRef` _[api.v1beta1.MCPTelemetryConfigReference](#apiv1beta1mcptelemetryconfigreference)_ | TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.<br />The referenced MCPTelemetryConfig must exist in the same namespace as this MCPRemoteProxy.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `resources` _[api.v1beta1.ResourceRequirements](#apiv1beta1resourcerequirements)_ | Resources defines the resource requirements for the proxy container |  | Optional: \{\} <br /> |
| `serviceAccount` _string_ | ServiceAccount is the name of an already existing service account to use by the proxy.<br />If not specified, a ServiceAccount will be created automatically and used by the proxy. |  | Optional: \{\} <br /> |
| `podTemplateSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#rawextension-runtime-pkg)_ | PodTemplateSpec defines the pod template to use for the MCPRemoteProxy<br />This allows for customizing the pod configuration beyond what is provided by the other fields.<br />Note that to modify the specific container the remote proxy runs in, you must specify<br />the `toolhive` container name in the PodTemplateSpec.<br />This field accepts a PodTemplateSpec object as JSON/YAML. |  | Type: object <br />Optional: \{\} <br /> |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false | Optional: \{\} <br /> |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  | Optional: \{\} <br /> |
| `resourceOverrides` _[api.v1beta1.ResourceOverrides](#apiv1beta1resourceoverrides)_ | ResourceOverrides allows overriding annotations and labels for resources created by the operator |  | Optional: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup this proxy belongs to.<br />The referenced MCPGroup must be in the same namespace. |  | Optional: \{\} <br /> |
| `sessionAffinity` _string_ | SessionAffinity controls whether the Service routes repeated client connections to the same pod.<br />MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.<br />Set to "None" for stateless servers or when using an external load balancer with its own affinity.<br />Interaction with sessionStorage: when running multiple replicas with<br />sessionStorage.provider "redis", set this to "None" so requests are<br />distributed across replicas and sessions resolve via the shared store.<br />Conversely, "None" without Redis-backed sessionStorage breaks session<br />continuity — any request landing on a different pod fails with<br />"Session not found". | ClientIP | Enum: [ClientIP None] <br />Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the desired number of proxy pod replicas.<br />MCPRemoteProxy creates a single Deployment for the proxy process, so there<br />is only one replicas field (mirrors VirtualMCPServer.spec.replicas).<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller.<br />When set above 1, also configure sessionStorage with the redis provider and<br />sessionAffinity: "None" so sessions resolve across replicas; otherwise a<br />SessionStorageWarning condition is surfaced on the resource status. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `sessionStorage` _[api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When nil, no session storage is configured and the proxy falls back to<br />pod-local in-memory session state — incompatible with multi-replica<br />deployments behind load balancers that don't preserve client-IP affinity<br />(e.g. AWS ALB across multiple AZs).<br />The transparent proxy validates `Mcp-Session-Id` against this store on<br />every non-initialize request (see pkg/transport/proxy/transparent/<br />transparent_proxy.go) and rewrites client-facing session IDs to backend<br />session IDs using session metadata. Both lookups require shared state<br />across replicas.<br />When using the Redis provider, also set sessionAffinity to "None" so the<br />Service routes requests round-robin and all replicas rely on the shared<br />session store rather than pod-local state.<br />Mirrors MCPServer.spec.sessionStorage and VirtualMCPServer.spec.sessionStorage. |  | Optional: \{\} <br /> |


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
| `authzConfigHash` _string_ | AuthzConfigHash is the hash of the referenced MCPAuthzConfig spec for change detection |  | Optional: \{\} <br /> |
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
| `oidcConfigRef` _[api.v1beta1.MCPOIDCConfigReference](#apiv1beta1mcpoidcconfigreference)_ | OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.<br />The referenced MCPOIDCConfig must exist in the same namespace as this MCPServer.<br />Per-server overrides (audience, scopes) are specified here; shared provider config<br />lives in the MCPOIDCConfig resource.<br />SECURITY: if this field is omitted and no other authentication source is configured,<br />the proxy runs UNAUTHENTICATED. It accepts every request that can reach its port and<br />forwards it to the MCP server under a synthetic local-user identity, with no token or<br />credential check. Set this field to enforce identity-based access control per request. |  | Optional: \{\} <br /> |
| `authzConfig` _[api.v1beta1.AuthzConfigRef](#apiv1beta1authzconfigref)_ | AuthzConfig defines authorization policy configuration for the MCP server.<br />AuthzConfig and AuthzConfigRef are mutually exclusive. |  | Optional: \{\} <br /> |
| `authzConfigRef` _[api.v1beta1.MCPAuthzConfigReference](#apiv1beta1mcpauthzconfigreference)_ | AuthzConfigRef references a shared MCPAuthzConfig resource for authorization.<br />The referenced MCPAuthzConfig must exist in the same namespace as this MCPServer.<br />Mutually exclusive with authzConfig. |  | Optional: \{\} <br /> |
| `audit` _[api.v1beta1.AuditConfig](#apiv1beta1auditconfig)_ | Audit defines audit logging configuration for the MCP server |  | Optional: \{\} <br /> |
| `toolConfigRef` _[api.v1beta1.ToolConfigRef](#apiv1beta1toolconfigref)_ | ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.<br />The referenced MCPToolConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `externalAuthConfigRef` _[api.v1beta1.ExternalAuthConfigRef](#apiv1beta1externalauthconfigref)_ | ExternalAuthConfigRef references a MCPExternalAuthConfig resource for external authentication.<br />The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPServer. |  | Optional: \{\} <br /> |
| `webhookConfigRef` _[api.v1beta1.WebhookConfigRef](#apiv1beta1webhookconfigref)_ | WebhookConfigRef references a MCPWebhookConfig resource for webhook middleware configuration.<br />The referenced MCPWebhookConfig must exist in the same namespace as this MCPServer. |  | Optional: \{\} <br /> |
| `authServerRef` _[api.v1beta1.AuthServerRef](#apiv1beta1authserverref)_ | AuthServerRef optionally references a resource that configures an embedded<br />OAuth 2.0/OIDC authorization server to authenticate MCP clients.<br />Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer). |  | Optional: \{\} <br /> |
| `telemetryConfigRef` _[api.v1beta1.MCPTelemetryConfigReference](#apiv1beta1mcptelemetryconfigreference)_ | TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.<br />The referenced MCPTelemetryConfig must exist in the same namespace as this MCPServer.<br />Cross-namespace references are not supported for security and isolation reasons. |  | Optional: \{\} <br /> |
| `trustProxyHeaders` _boolean_ | TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies<br />When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,<br />and X-Forwarded-Prefix headers to construct endpoint URLs | false | Optional: \{\} <br /> |
| `endpointPrefix` _string_ | EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.<br />This is used to handle path-based ingress routing scenarios where the ingress<br />strips a path prefix before forwarding to the backend. |  | Optional: \{\} <br /> |
| `groupRef` _[api.v1beta1.MCPGroupRef](#apiv1beta1mcpgroupref)_ | GroupRef references the MCPGroup this server belongs to.<br />The referenced MCPGroup must be in the same namespace. |  | Optional: \{\} <br /> |
| `sessionAffinity` _string_ | SessionAffinity controls whether the Service routes repeated client connections to the same pod.<br />MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.<br />Set to "None" for stateless servers or when using an external load balancer with its own affinity. | ClientIP | Enum: [ClientIP None] <br />Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the desired number of proxy runner (thv run) pod replicas.<br />MCPServer creates two separate Deployments: one for the proxy runner and one<br />for the MCP server backend. This field controls the proxy runner Deployment.<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `backendReplicas` _integer_ | BackendReplicas is the desired number of MCP server backend pod replicas.<br />This controls the backend Deployment (the MCP server container itself),<br />independent of the proxy runner controlled by Replicas.<br />When nil, the operator does not set Deployment.Spec.Replicas, leaving replica<br />management to an HPA or other external controller. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `sessionStorage` _[api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)_ | SessionStorage configures session storage for stateful horizontal scaling.<br />When nil, no session storage is configured. |  | Optional: \{\} <br /> |
| `rateLimiting` _[ratelimit.types.RateLimitConfig](#ratelimittypesratelimitconfig)_ | RateLimiting defines rate limiting configuration for the MCP server.<br />Requires Redis session storage to be configured for distributed rate limiting. |  | Optional: \{\} <br /> |


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
| `authzConfigHash` _string_ | AuthzConfigHash is the hash of the referenced MCPAuthzConfig spec for change detection |  | Optional: \{\} <br /> |
| `oidcConfigHash` _string_ | OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection |  | Optional: \{\} <br /> |
| `telemetryConfigHash` _string_ | TelemetryConfigHash is the hash of the referenced MCPTelemetryConfig spec for change detection |  | Optional: \{\} <br /> |
| `webhookConfigHash` _string_ | WebhookConfigHash is the hash of the referenced MCPWebhookConfig spec |  | Optional: \{\} <br /> |
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
| `referenceCount` _integer_ | ReferenceCount is the number of workloads referencing this config. |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPToolConfig.<br />Each entry identifies the workload by kind and name. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPWebhookConfigSpec



MCPWebhookConfigSpec defines the desired state of MCPWebhookConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `validating` _[api.v1beta1.WebhookSpec](#apiv1beta1webhookspec) array_ | Validating webhooks are called to approve or deny MCP requests. |  | Optional: \{\} <br /> |
| `mutating` _[api.v1beta1.WebhookSpec](#apiv1beta1webhookspec) array_ | Mutating webhooks are called to transform MCP requests before processing. |  | Optional: \{\} <br /> |


#### api.v1beta1.MCPWebhookConfigStatus



MCPWebhookConfigStatus defines the observed state of MCPWebhookConfig





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#condition-v1-meta) array_ | Conditions represent the latest available observations |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the last observed generation corresponding to the current status |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is a hash of the spec, used for detecting changes |  | Optional: \{\} <br /> |
| `referencingWorkloads` _[api.v1beta1.WorkloadReference](#apiv1beta1workloadreference) array_ | ReferencingWorkloads is a list of workload resources that reference this MCPWebhookConfig.<br />Each entry identifies the workload by kind and name. |  | Optional: \{\} <br /> |


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

Exactly one of ClientID or DCRConfig must be set: ClientID is used when the
client is pre-provisioned out of band, DCRConfig enables RFC 7591 Dynamic
Client Registration at runtime.

ClientSecretRef is mutually exclusive with DCRConfig: when DCRConfig is set,
the client_secret is obtained from the registration response (RFC 7591
§3.2.1) and any static ClientSecretRef would be either dead config or a
competing source of truth. The XValidation rule below rejects the
combination at admission; ValidateOAuth2DCRConfig is the matching
reconcile-time backstop.

Layered XOR behavior: the ClientID/DCRConfig rule treats `clientId: ""` as
absent (size>0) but treats `dcrConfig: {}` as present (has() is true for an
empty object). For input `{ clientId: "", dcrConfig: {} }` the outer rule
passes and the inner DCRUpstreamConfig XOR fires with "exactly one of
discoveryUrl or registrationEndpoint must be set". This is intentional —
adding a non-empty subfield check (e.g.,
`has(self.dcrConfig.discoveryUrl) || has(self.dcrConfig.registrationEndpoint)`)
would inflate CEL cost on an already-budget-bound rule, and the inner
message is still actionable.



_Appears in:_
- [api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `authorizationEndpoint` _string_ | AuthorizationEndpoint is the URL for the OAuth authorization endpoint. |  | Pattern: `^https?://.*$` <br />Required: \{\} <br /> |
| `tokenEndpoint` _string_ | TokenEndpoint is the URL for the OAuth token endpoint. |  | Pattern: `^https?://.*$` <br />Required: \{\} <br /> |
| `userInfo` _[api.v1beta1.UserInfoConfig](#apiv1beta1userinfoconfig)_ | UserInfo contains configuration for fetching user information from the upstream provider.<br />When omitted and IdentityFromToken is also unset, the embedded auth server runs in<br />synthesis mode for this upstream: a non-PII subject derived from the access token, no<br />Name/Email. Use this shape for upstreams with no userinfo surface and no identity in<br />the token response (e.g., MCP authorization servers per the MCP spec). When<br />IdentityFromToken is set instead, identity is resolved from the token response body<br />(e.g., Snowflake's "username" field, Slack's "authed_user.id"); the userinfo HTTP call<br />is skipped entirely. |  | Optional: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.<br />Mutually exclusive with DCRConfig: when DCRConfig is set, ClientID is obtained<br />at runtime via RFC 7591 Dynamic Client Registration and must be left empty. |  | Optional: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.<br />Optional for public clients using PKCE instead of client secret. |  | Optional: \{\} <br /> |
| `redirectUri` _string_ | RedirectURI is the callback URL where the upstream IdP will redirect after authentication.<br />When not specified, defaults to `\{resourceUrl\}/oauth/callback` where `resourceUrl` is the<br />URL associated with the resource (e.g., MCPServer or vMCP) using this config. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the OAuth scopes to request from the upstream IdP. |  | Optional: \{\} <br /> |
| `tokenResponseMapping` _[api.v1beta1.TokenResponseMapping](#apiv1beta1tokenresponsemapping)_ | TokenResponseMapping configures custom field extraction from non-standard token responses.<br />Some OAuth providers (e.g., GovSlack) nest token fields under non-standard paths<br />instead of returning them at the top level. When set, ToolHive performs the token<br />exchange HTTP call directly and extracts fields using the configured dot-notation paths.<br />If nil, standard OAuth 2.0 token response parsing is used.<br />For extracting user identity from the token response, see IdentityFromToken. |  | Optional: \{\} <br /> |
| `identityFromToken` _[api.v1beta1.IdentityFromTokenConfig](#apiv1beta1identityfromtokenconfig)_ | IdentityFromToken extracts user identity (subject, name, email) directly<br />from the OAuth2 token-endpoint response body using gjson dot-notation paths.<br />When set, the embedded auth server skips the userinfo HTTP call entirely<br />and resolves identity from the token response. See IdentityFromTokenConfig<br />for trust-model and uniqueness considerations. |  | Optional: \{\} <br /> |
| `additionalAuthorizationParams` _object (keys:string, values:string)_ | AdditionalAuthorizationParams are extra query parameters to include in<br />authorization requests sent to the upstream provider.<br />This is useful for providers that require custom parameters, such as<br />Google's access_type=offline for obtaining refresh tokens.<br />Framework-managed parameters (response_type, client_id, redirect_uri,<br />scope, state, code_challenge, code_challenge_method, nonce) are not allowed. |  | MaxProperties: 16 <br />Optional: \{\} <br /> |
| `dcrConfig` _[api.v1beta1.DCRUpstreamConfig](#apiv1beta1dcrupstreamconfig)_ | DCRConfig enables RFC 7591 Dynamic Client Registration against the upstream<br />authorization server. When set, the client credentials are obtained at<br />runtime rather than being pre-provisioned, and ClientID must be left empty.<br />Mutually exclusive with ClientID. |  | Optional: \{\} <br /> |


#### api.v1beta1.OBOConfig



OBOConfig holds configuration for the On-Behalf-Of (OBO) external auth type.
Only used when Type is "obo".

This is the user-facing CRD surface for the Microsoft Entra OBO flow. It is
structurally valid in upstream (OSS) builds but inert: an upstream-only build
returns obo.ErrEnterpriseRequired at reconcile (Valid=False, Reason:
EnterpriseRequired) because no OBO handler is registered via
controllerutil.RegisterOBOHandler. A build with the enterprise OBO handler
translates these fields into the runtime wire contract obo.MiddlewareParameters,
so the field names and semantics here track that contract rather than the
upstream TokenExchangeConfig (which uses different names, e.g.
subjectProviderName / externalTokenHeaderName). In particular there is no
externalTokenHeaderName: the OBO subject is sourced from the authenticated
Identity, never from an inbound request header.

Field-to-contract mapping performed by the operator's OBO handler:
  - tenantId (+ optional authority) → tokenUrl
    (https://login.microsoftonline.com/<tenantId>/oauth2/v2.0/token, or the
    configured authority base joined with the tenant for sovereign clouds)
  - clientSecretRef → resolved into a pod env var; only the env var name
    travels in the contract, as clientSecretEnvVar
  - audience / scopes → collapsed to a single exchange target by
    obo.MiddlewareParameters.ExchangeTarget() (space-joined scopes win,
    otherwise audience)
  - cacheSkew → the contract's integer-seconds cacheSkewSeconds

Every field is optional at the CRD level, and the schema deliberately carries
no required field and no cross-field CEL rule. spec.obo shipped as an empty
placeholder ({}) in earlier releases, so adding a required field or an
admission rule that rejects {} would be a backward-incompatible narrowing of
an already-stored, round-trippable object. Presence and combination
requirements — a tenant, a client-auth credential, and at least one of
audience/scopes — are therefore enforced by the registered OBO handler at
reconcile, which reports a violation as Valid=False / Reason=InvalidConfig.
The per-field patterns below still apply, but only to a value that is present.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tenantId` _string_ | TenantID is the Microsoft Entra (Azure AD) directory (tenant) identifier.<br />Optional at the CRD level (see the type doc); the operator enforces its<br />presence, since an OBO confidential-client exchange must target a specific<br />tenant. When set, it must be one of the two forms the Entra v2.0 token<br />endpoint addresses: a directory GUID, or a verified domain name (e.g.<br />contoso.onmicrosoft.com). Well-known aliases such as "common",<br />"organizations", and "consumers" are NOT accepted. The operator<br />interpolates it into the token endpoint<br />(<authority>/<tenantId>/oauth2/v2.0/token), so the value is constrained to<br />the GUID/domain shape (no path metacharacters); the pattern and 253-char<br />cap mirror the enterprise exchanger's validateTenant, so any tenantId<br />admitted here is one the runtime can consume. |  | MaxLength: 253 <br />Pattern: `^([0-9a-fA-F]\{8\}-[0-9a-fA-F]\{4\}-[0-9a-fA-F]\{4\}-[0-9a-fA-F]\{4\}-[0-9a-fA-F]\{12\}\|([a-zA-Z0-9]([a-zA-Z0-9-]\{0,61\}[a-zA-Z0-9])?\.)+[a-zA-Z]\{2,\})$` <br />Optional: \{\} <br /> |
| `authority` _string_ | Authority overrides the default Entra login host<br />(https://login.microsoftonline.com) for sovereign or national clouds, e.g.<br />https://login.microsoftonline.us (US Gov) or<br />https://login.partner.microsoftonline.cn (China). When set, the operator<br />builds the token endpoint by joining <authority>, <tenantId>, and the<br />v2.0 token path. Must be an HTTPS URL with no userinfo, query, fragment,<br />or trailing slash; a path IS permitted and is prefixed before the tenant<br />segment, as some sovereign / B2C / CIAM endpoints require. The OBO exchange<br />POSTs the client secret and the end-user assertion to this host, so it is a<br />credential trust boundary: HTTPS is required and userinfo (user@host) is<br />rejected to prevent host confusion (per RFC 3986 the real host follows the<br />"@", so https://login.microsoftonline.com@attacker.example targets<br />attacker.example). This is intentionally stricter than the downstream<br />exchanger's validateHTTPSURL, which also accepts http for loopback hosts<br />and tolerates a trailing slash — rejecting those at admission is the safe<br />direction. |  | Pattern: `^https://[^\s?#@]+[^/\s?#@]$` <br />Optional: \{\} <br /> |
| `clientId` _string_ | ClientID is the confidential client's application (client) ID registered<br />in Entra. Emitted verbatim as the runtime contract's clientId.<br />Optional at the CRD level so future client-authentication methods (e.g.<br />certificate or workload-identity credentials, planned fast-follows) can be<br />added without a breaking schema change. The operator enforces that clientId<br />and clientSecretRef are both present for the v1 shared-secret flow. |  | Optional: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef references a Kubernetes Secret containing the confidential<br />client's secret. v1 supports a shared client secret only. The operator<br />injects the resolved value into the proxyrunner pod as an environment<br />variable and emits only that variable's name in the runtime contract, as<br />clientSecretEnvVar — the secret value never travels in the contract.<br />Optional at the CRD level for the same forward-compatibility reason as<br />clientId (a certificate/workload-identity flow needs no client secret);<br />the operator enforces presence for the v1 shared-secret flow. |  | Optional: \{\} <br /> |
| `audience` _string_ | Audience is the backend target identifier requested in the exchanged<br />token. Used as the exchange target when Scopes is empty. At least one of<br />audience or scopes must be set; the operator enforces that at reconcile<br />(it is not an admission-time rule — see the type doc). |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the delegated scopes to request for the exchanged token, e.g.<br />["api://<backend>/.default"]. When non-empty they take precedence over<br />Audience. At least one of audience or scopes must be set; the operator<br />enforces that at reconcile. The MaxItems and per-item length caps are<br />defensive bounds on an otherwise unbounded list. |  | MaxItems: 20 <br />items:MaxLength: 256 <br />items:MinLength: 1 <br />Optional: \{\} <br /> |
| `subjectTokenProviderName` _string_ | SubjectTokenProviderName selects the source of the OBO subject (assertion)<br />token from the request's authenticated Identity:<br />  - Omitted: use the inbound end-user token the client presented<br />    (Identity.Token) — the deployment with no embedded auth server, where<br />    the client holds an Entra token directly.<br />  - Set: use the named upstream provider's token<br />    (Identity.UpstreamTokens[<name>]) — the embedded-auth-server<br />    deployment, where the inbound token is the proxy's own session token.<br />    The value must match a configured upstream provider name.<br />The subject is always sourced from the authenticated Identity, never from<br />an inbound request header, so the upstream auth middleware must run first. |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `cacheSkew` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#duration-v1-meta)_ | CacheSkew overrides the OBO token cache's default expiry skew (the margin<br />by which a cached token is treated as expired before its real expiry),<br />e.g. "30s". The operator converts it to the runtime contract's<br />integer-seconds cacheSkewSeconds. Should not be negative, but the schema<br />does not enforce that — metav1.Duration carries no numeric minimum — and<br />upstream builds do not reject it. A negative value is rejected only by an<br />enterprise build's OBO handler once that handler validates the converted<br />parameters; it is not enforced at admission or in upstream-only builds.<br />When omitted, the cache default applies. |  | Optional: \{\} <br /> |


#### api.v1beta1.OIDCUpstreamConfig



OIDCUpstreamConfig contains configuration for OIDC providers.
OIDC providers support automatic endpoint discovery via the issuer URL.



_Appears in:_
- [api.v1beta1.UpstreamProviderConfig](#apiv1beta1upstreamproviderconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuerUrl` _string_ | IssuerURL is the OIDC issuer URL for automatic endpoint discovery.<br />Must be a valid HTTPS URL. |  | Pattern: `^https://.*$` <br />Required: \{\} <br /> |
| `clientId` _string_ | ClientID is the OAuth 2.0 client identifier registered with the upstream IdP. |  | Required: \{\} <br /> |
| `clientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.<br />Optional for public clients using PKCE instead of client secret. |  | Optional: \{\} <br /> |
| `redirectUri` _string_ | RedirectURI is the callback URL where the upstream IdP will redirect after authentication.<br />When not specified, defaults to `\{resourceUrl\}/oauth/callback` where `resourceUrl` is the<br />URL associated with the resource (e.g., MCPServer or vMCP) using this config. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the OAuth scopes to request from the upstream IdP.<br />If not specified, defaults to ["openid", "offline_access"].<br />When using additionalAuthorizationParams with provider-specific refresh token<br />mechanisms (e.g., Google's access_type=offline), set explicit scopes to avoid<br />sending both offline_access and the provider-specific parameter. |  | Optional: \{\} <br /> |
| `userInfoOverride` _[api.v1beta1.UserInfoConfig](#apiv1beta1userinfoconfig)_ | UserInfoOverride allows customizing UserInfo fetching behavior for OIDC providers.<br />By default, the UserInfo endpoint is discovered automatically via OIDC discovery.<br />Use this to override the endpoint URL, HTTP method, or field mappings for providers<br />that return non-standard claim names in their UserInfo response. |  | Optional: \{\} <br /> |
| `additionalAuthorizationParams` _object (keys:string, values:string)_ | AdditionalAuthorizationParams are extra query parameters to include in<br />authorization requests sent to the upstream provider.<br />This is useful for providers that require custom parameters, such as<br />Google's access_type=offline for obtaining refresh tokens.<br />Note: when using access_type=offline, also set explicit scopes to avoid<br />the default offline_access scope being sent alongside it.<br />Framework-managed parameters (response_type, client_id, redirect_uri,<br />scope, state, code_challenge, code_challenge_method, nonce) are not allowed. |  | MaxProperties: 16 <br />Optional: \{\} <br /> |
| `subjectClaim` _string_ | SubjectClaim names the validated ID-token claim to use as the upstream<br />subject. Defaults to "sub" when empty. Set it for IdPs where "sub" isn't<br />stable per user — e.g. Entra/Azure AD, whose "sub" rotates per application<br />and whose stable identifier is "oid".<br />The value is looked up verbatim as a top-level claim name, so it is<br />constrained to a claim-name shape: it must start with a letter or<br />underscore and contain only letters, digits, and underscores. This rejects<br />dotted, colon-namespaced, or whitespace-containing values at admission<br />rather than letting a typo silently miss the claim at login, and keeps the<br />field aligned with the directory service's per-issuer bindingClaim.<br />Changing this on a live deployment re-keys existing users (the value<br />resolves to the internal user ID), so treat it as immutable once users<br />exist.<br />Per-IdP notes:<br />  - Entra/Azure AD: use "oid"; it is only emitted when the upstream scopes<br />    include "profile". "oid" is unique within a single tenant — multi-tenant<br />    apps need oid+tid, which this single-claim field cannot express.<br />  - Okta: the org auth server already puts the stable id in "sub" (default<br />    works). A custom auth server's "sub" is the mutable login/email and the<br />    stable "uid" lives only in the access token, not the ID token — map a<br />    custom ID-token claim and point subjectClaim at it.<br />The pattern matches the claim-name shape and allows empty (defaults to<br />"sub"). Using Pattern rather than a CEL XValidation rule keeps this off the<br />CRD's CEL cost budget — a single-field format check via CEL is rejected by<br />the apiserver as too expensive once multiplied across the upstreams list. |  | MaxLength: 128 <br />Pattern: `^([a-zA-Z_][a-zA-Z0-9_]*)?$` <br />Optional: \{\} <br /> |


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


#### api.v1beta1.PermissionProfileSpec



PermissionProfileSpec defines the permissions for an MCP server





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `read` _string array_ | Read is a list of paths that the MCP server can read from |  | Optional: \{\} <br /> |
| `write` _string array_ | Write is a list of paths that the MCP server can write to |  | Optional: \{\} <br /> |
| `network` _[api.v1beta1.NetworkPermissions](#apiv1beta1networkpermissions)_ | Network defines the network permissions for the MCP server |  | Optional: \{\} <br /> |


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
and a refill period. Used by both shared and per-user rate limits.







#### api.v1beta1.RateLimitConfig



RateLimitConfig defines rate limiting configuration for an MCP server.







#### api.v1beta1.RedisACLUserConfig



RedisACLUserConfig configures Redis ACL user authentication.



_Appears in:_
- [api.v1beta1.RedisStorageConfig](#apiv1beta1redisstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `usernameSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | UsernameSecretRef references a Secret containing the Redis ACL username.<br />When omitted, connections use legacy password-only AUTH. Omit for managed<br />Redis tiers that do not support ACL users (e.g. GCP Memorystore Basic/Standard<br />HA, Azure Cache for Redis). Set for services that support ACL users (e.g. AWS<br />ElastiCache non-cluster with Redis 6+ RBAC). |  | Optional: \{\} <br /> |
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
Exactly one of addr or sentinelConfig must be set. Set clusterMode to true when
addr points to a Redis Cluster discovery endpoint (GCP Memorystore Cluster,
AWS ElastiCache cluster mode enabled).



_Appears in:_
- [api.v1beta1.AuthServerStorageConfig](#apiv1beta1authserverstorageconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `addr` _string_ | Addr is the Redis server address (host:port). Required for standalone and cluster modes.<br />Use for managed Redis services that expose a single endpoint (GCP Memorystore basic tier,<br />AWS ElastiCache without cluster mode, or cluster-mode services when clusterMode is true).<br />Mutually exclusive with sentinelConfig. |  | Optional: \{\} <br /> |
| `clusterMode` _boolean_ | ClusterMode enables the Redis Cluster protocol. Set to true when addr points to a<br />Redis Cluster discovery endpoint (e.g., GCP Memorystore Cluster, AWS ElastiCache<br />cluster mode enabled). Requires addr to be set. |  | Optional: \{\} <br /> |
| `sentinelConfig` _[api.v1beta1.RedisSentinelConfig](#apiv1beta1redissentinelconfig)_ | SentinelConfig holds Redis Sentinel configuration.<br />Use for self-managed Redis with Sentinel-based HA. Mutually exclusive with addr. |  | Optional: \{\} <br /> |
| `aclUserConfig` _[api.v1beta1.RedisACLUserConfig](#apiv1beta1redisacluserconfig)_ | ACLUserConfig configures Redis ACL user authentication. |  | Required: \{\} <br /> |
| `dialTimeout` _string_ | DialTimeout is the timeout for establishing connections.<br />Format: Go duration string (e.g., "5s", "1m"). | 5s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `readTimeout` _string_ | ReadTimeout is the timeout for socket reads.<br />Format: Go duration string (e.g., "3s", "1m"). | 3s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `writeTimeout` _string_ | WriteTimeout is the timeout for socket writes.<br />Format: Go duration string (e.g., "3s", "1m"). | 3s | Pattern: `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$` <br />Optional: \{\} <br /> |
| `tls` _[api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)_ | TLS configures TLS for connections to the Redis/Valkey master or cluster nodes.<br />Presence of this field enables TLS. Omit to use plaintext. |  | Optional: \{\} <br /> |
| `sentinelTls` _[api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)_ | SentinelTLS configures TLS for connections to Sentinel instances.<br />Only applies when sentinelConfig is set. Presence of this field enables TLS. |  | Optional: \{\} <br /> |


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
- [api.v1beta1.DCRUpstreamConfig](#apiv1beta1dcrupstreamconfig)
- [api.v1beta1.EmbeddedAuthServerConfig](#apiv1beta1embeddedauthserverconfig)
- [api.v1beta1.EmbeddingServerSpec](#apiv1beta1embeddingserverspec)
- [api.v1beta1.HeaderFromSecret](#apiv1beta1headerfromsecret)
- [api.v1beta1.HeaderInjectionConfig](#apiv1beta1headerinjectionconfig)
- [api.v1beta1.InlineOIDCSharedConfig](#apiv1beta1inlineoidcsharedconfig)
- [api.v1beta1.OAuth2UpstreamConfig](#apiv1beta1oauth2upstreamconfig)
- [api.v1beta1.OBOConfig](#apiv1beta1oboconfig)
- [api.v1beta1.OIDCUpstreamConfig](#apiv1beta1oidcupstreamconfig)
- [api.v1beta1.RedisACLUserConfig](#apiv1beta1redisacluserconfig)
- [api.v1beta1.RedisTLSConfig](#apiv1beta1redistlsconfig)
- [api.v1beta1.SensitiveHeader](#apiv1beta1sensitiveheader)
- [api.v1beta1.SessionStorageConfig](#apiv1beta1sessionstorageconfig)
- [api.v1beta1.TokenExchangeConfig](#apiv1beta1tokenexchangeconfig)
- [api.v1beta1.WebhookSpec](#apiv1beta1webhookspec)
- [api.v1beta1.WebhookTLSConfig](#apiv1beta1webhooktlsconfig)
- [api.v1beta1.XAASpec](#apiv1beta1xaaspec)

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

_Underlying type:_ _struct{Name string "json:\"name\""; Namespace string "json:\"namespace,omitempty\""; Port int32 "json:\"port,omitempty\""}_

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
- [api.v1beta1.MCPRemoteProxySpec](#apiv1beta1mcpremoteproxyspec)
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
The structure matches the tokenexchange.Config from pkg/oauthproto/tokenexchange/middleware.go



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

For extracting user identity from the token response, see IdentityFromToken.



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







#### api.v1beta1.UpstreamInjectSpec



UpstreamInjectSpec holds configuration for upstream token injection.
This strategy injects an upstream IdP access token obtained by the embedded
authorization server into backend requests as the Authorization: Bearer header.



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `providerName` _string_ | ProviderName is the name of the upstream IdP provider whose access token<br />should be injected as the Authorization: Bearer header. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.UpstreamProviderConfig



UpstreamProviderConfig defines configuration for an upstream Identity Provider.

Exactly one of OIDCConfig or OAuth2Config must be set and must match the
declared Type: oidc-typed providers set OIDCConfig, oauth2-typed providers
set OAuth2Config. The CEL rule below enforces the pairing at admission; the
matching Go-level check in validateUpstreamProvider provides defense-in-depth
for stored objects.

The rule is structured as a chain of equality checks ending in an explicit
`false`, so adding a new UpstreamProviderType value without extending this
rule fails admission instead of silently demanding the OAuth2 shape. When
adding a new type, extend both this rule and validateUpstreamProvider.



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
like GitHub's /user API. For providers that do not expose a usable userinfo endpoint but
include identity in the OAuth2 token response, use IdentityFromToken on OAuth2UpstreamConfig
instead.



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

_Underlying type:_ _struct{SubjectFields []string "json:\"subjectFields,omitempty\""; NameFields []string "json:\"nameFields,omitempty\""; EmailFields []string "json:\"emailFields,omitempty\""}_

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
| `parameters` _[pkg.json.Map](#pkgjsonmap)_ | Parameters defines input parameter schema in JSON Schema format.<br />Should be a JSON Schema object with "type": "object" and "properties".<br />Example:<br />  \{<br />    "type": "object",<br />    "properties": \{<br />      "param1": \{"type": "string", "default": "value"\},<br />      "param2": \{"type": "integer"\}<br />    \},<br />    "required": ["param2"]<br />  \}<br />We use json.Map rather than a typed struct because JSON Schema is highly<br />flexible with many optional fields (default, enum, minimum, maximum, pattern,<br />items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map<br />allows full JSON Schema compatibility without needing to define every possible<br />field, and matches how the MCP SDK handles inputSchema. |  | Type: object <br />Optional: \{\} <br /> |
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
| `passthroughHeaders` _string array_ | PassthroughHeaders is an allowlist of incoming client request header names<br />forwarded verbatim to all backends (e.g. an API key the backend resolves to<br />a user). Takes precedence over config.PassthroughHeaders. Names must not be<br />restricted headers (Host, hop-by-hop, X-Forwarded-*). Forwarded headers are<br />attacker-influenceable unless a trusted upstream sets them. |  | Optional: \{\} <br /> |
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
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#localobjectreference-v1-core) array_ | ImagePullSecrets allows specifying image pull secrets for the vMCP workload.<br />These are applied to both the vMCP Deployment's PodSpec.ImagePullSecrets<br />and to the operator-managed ServiceAccount the vMCP server runs as, so private<br />images are pullable through either path.<br />Merge semantics with PodTemplateSpec:<br />The deployed PodSpec.ImagePullSecrets is the Kubernetes-native strategic-merge<br />union of this field and spec.podTemplateSpec.spec.imagePullSecrets, merged by<br />the patchStrategy:"merge" / patchMergeKey:"name" tags on corev1.PodSpec.<br />  - This field is rendered first as the controller-generated default.<br />  - spec.podTemplateSpec.spec.imagePullSecrets is then strategic-merge-patched<br />    on top, keyed by Name. Distinct names from the two sources are unioned in<br />    the resulting list; entries with the same Name are deduplicated and the<br />    PodTemplateSpec entry wins on overlap (user override).<br />  - Order in the resulting list is not guaranteed and should not be relied on:<br />    strategic merge by name is order-insensitive.<br />  - The operator-managed ServiceAccount's imagePullSecrets list is populated<br />    ONLY from this field. spec.podTemplateSpec.spec.imagePullSecrets does not<br />    reach the ServiceAccount because PodTemplateSpec has no notion of a<br />    ServiceAccount. To make a secret usable via the ServiceAccount path<br />    (e.g. for sidecars or init containers that pull images independently),<br />    list it here rather than under spec.podTemplateSpec.<br />Note on cross-CRD consistency:<br />MCPRegistry currently uses an atomic-replace strategy for its imagePullSecrets<br />(the user-provided value replaces the controller-generated list rather than<br />being merged on top). VirtualMCPServer follows the Kubernetes-native<br />strategic-merge-by-name behavior described above. Aligning the two is tracked<br />as a separate follow-up; until then, manifests that set imagePullSecrets on<br />both CRDs will see different override behavior between them. |  | Optional: \{\} <br /> |


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
| `authzConfigHash` _string_ | AuthzConfigHash is the hash of the referenced MCPAuthzConfig spec for change detection.<br />Only populated when IncomingAuth.AuthzConfigRef is set. |  | Optional: \{\} <br /> |
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


#### api.v1beta1.WebhookConfigRef



WebhookConfigRef defines a reference to a MCPWebhookConfig resource.
The referenced MCPWebhookConfig must be in the same namespace as the MCPServer.



_Appears in:_
- [api.v1beta1.MCPServerSpec](#apiv1beta1mcpserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the MCPWebhookConfig resource |  | Required: \{\} <br /> |


#### api.v1beta1.WebhookFailurePolicy

_Underlying type:_ _string_

WebhookFailurePolicy defines how webhook errors are handled.



_Appears in:_
- [api.v1beta1.WebhookSpec](#apiv1beta1webhookspec)

| Field | Description |
| --- | --- |
| `fail` | WebhookFailurePolicyFail denies the request on webhook error.<br /> |
| `ignore` | WebhookFailurePolicyIgnore allows the request on webhook error.<br /> |


#### api.v1beta1.WebhookSpec



WebhookSpec defines the configuration for a single webhook middleware



_Appears in:_
- [api.v1beta1.MCPWebhookConfigSpec](#apiv1beta1mcpwebhookconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is a unique identifier for this webhook |  | MaxLength: 63 <br />MinLength: 1 <br /> |
| `url` _string_ | URL is the endpoint to call for this webhook. Must be an HTTP/HTTPS URL. |  | Format: uri <br /> |
| `timeout` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#duration-v1-meta)_ | Timeout configures the maximum time to wait for the webhook to respond.<br />Defaults to 10s if not specified. Maximum is 30s. |  | Format: duration <br />Type: string <br />Optional: \{\} <br /> |
| `failurePolicy` _[api.v1beta1.WebhookFailurePolicy](#apiv1beta1webhookfailurepolicy)_ | FailurePolicy defines how to handle errors when communicating with the webhook.<br />Supported values: "fail", "ignore". Defaults to "fail". | fail | Enum: [fail ignore] <br />Optional: \{\} <br /> |
| `tlsConfig` _[api.v1beta1.WebhookTLSConfig](#apiv1beta1webhooktlsconfig)_ | TLSConfig contains optional TLS configuration for the webhook connection. |  | Optional: \{\} <br /> |
| `hmacSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | HMACSecretRef references a Kubernetes Secret containing the HMAC signing key<br />used to sign the webhook payload. If set, the X-Toolhive-Signature header will be injected. |  | Optional: \{\} <br /> |


#### api.v1beta1.WebhookTLSConfig



WebhookTLSConfig contains TLS configuration for secure webhook connections



_Appears in:_
- [api.v1beta1.WebhookSpec](#apiv1beta1webhookspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `caSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | CASecretRef references a Secret containing the CA certificate bundle used to verify the webhook server's certificate.<br />Contains a bundle of PEM-encoded X.509 certificates. |  | Optional: \{\} <br /> |
| `clientCertSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientCertSecretRef references a Secret containing the client certificate for mTLS authentication.<br />The referenced key must contain a PEM-encoded client certificate.<br />Use ClientKeySecretRef to provide the corresponding private key. |  | Optional: \{\} <br /> |
| `clientKeySecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | ClientKeySecretRef references a Secret containing the private key for the client certificate.<br />Required when ClientCertSecretRef is set to enable mTLS. |  | Optional: \{\} <br /> |
| `insecureSkipVerify` _boolean_ | InsecureSkipVerify disables server certificate verification.<br />WARNING: This should only be used for development/testing and not in production environments. |  | Optional: \{\} <br /> |


#### api.v1beta1.WorkloadReference



WorkloadReference identifies a workload that references a shared configuration resource.
Namespace is implicit — cross-namespace references are not supported.



_Appears in:_
- [api.v1beta1.MCPAuthzConfigStatus](#apiv1beta1mcpauthzconfigstatus)
- [api.v1beta1.MCPExternalAuthConfigStatus](#apiv1beta1mcpexternalauthconfigstatus)
- [api.v1beta1.MCPOIDCConfigStatus](#apiv1beta1mcpoidcconfigstatus)
- [api.v1beta1.MCPTelemetryConfigStatus](#apiv1beta1mcptelemetryconfigstatus)
- [api.v1beta1.MCPToolConfigStatus](#apiv1beta1mcptoolconfigstatus)
- [api.v1beta1.MCPWebhookConfigStatus](#apiv1beta1mcpwebhookconfigstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind is the type of workload resource |  | Enum: [MCPServer VirtualMCPServer MCPRemoteProxy] <br />Required: \{\} <br /> |
| `name` _string_ | Name is the name of the workload resource |  | MinLength: 1 <br />Required: \{\} <br /> |


#### api.v1beta1.XAASpec



XAASpec holds configuration for the XAA (Cross-Application Access) auth strategy.
XAA implements draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG) — a
two-step token exchange to obtain access tokens for target services:
  - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT
  - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token



_Appears in:_
- [api.v1beta1.MCPExternalAuthConfigSpec](#apiv1beta1mcpexternalauthconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `idpTokenUrl` _string_ | IDPTokenURL is the IdP token endpoint for IdP exchange (RFC 8693).<br />Must be a valid HTTPS URL. |  | Pattern: `^https://.*$` <br />Required: \{\} <br /> |
| `idpClientId` _string_ | IDPClientID is the OAuth client ID at the IdP for IdP exchange. |  | Optional: \{\} <br /> |
| `idpClientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | IDPClientSecretRef references a Kubernetes Secret containing the IdP client secret. |  | Optional: \{\} <br /> |
| `targetTokenUrl` _string_ | TargetTokenURL is the target AS token endpoint for target grant (RFC 7523). |  | Required: \{\} <br /> |
| `insecureTargetTokenUrl` _boolean_ | InsecureTargetTokenURL allows plain HTTP for TargetTokenURL.<br />WARNING: this is insecure and must only be set for in-cluster or<br />development/testing endpoints — never in production. |  | Optional: \{\} <br /> |
| `targetClientId` _string_ | TargetClientID is the OAuth client ID at the target AS for target grant.<br />ID-JAG draft §9.1 RECOMMENDS confidential clients for target grant; most<br />conformant target authorization servers will reject an unauthenticated<br />JWT-bearer grant per the §4.4.1 client_id continuity requirement. |  | Optional: \{\} <br /> |
| `targetClientSecretRef` _[api.v1beta1.SecretKeyRef](#apiv1beta1secretkeyref)_ | TargetClientSecretRef references a Kubernetes Secret for the target AS client secret. |  | Optional: \{\} <br /> |
| `targetAudience` _string_ | TargetAudience is the resource AS URL for the ID-JAG audience claim. |  | Required: \{\} <br /> |
| `targetResource` _string_ | TargetResource is the RFC 8707 resource indicator sent as the `resource`<br />parameter in IdP exchange (RFC 8693, draft §4.3, OPTIONAL). It<br />identifies the target resource server — not the access-token audience, which<br />is governed by TargetAudience. For MCP backends, set to the MCP server URL.<br />Some authorization servers (e.g. Okta's early ID-JAG implementation) require<br />this parameter in practice despite the draft marking it optional — set it<br />when your IdP needs it. |  | Optional: \{\} <br /> |
| `scopes` _string array_ | Scopes are the requested scopes for the XAA exchange (IdP exchange and target grant). |  | Optional: \{\} <br /> |
| `subjectProviderName` _string_ | SubjectProviderName selects which upstream provider's ID token to use.<br />When left empty and an embedded authorization server is configured,<br />the controller automatically populates this field with the first configured<br />upstream provider name. |  | Optional: \{\} <br /> |
| `subjectTokenType` _string_ | SubjectTokenType is the token-type URN of the upstream subject token<br />used in IdP exchange. Defaults to "urn:ietf:params:oauth:token-type:id_token"<br />when empty. |  | Enum: [urn:ietf:params:oauth:token-type:id_token] <br />Optional: \{\} <br /> |



## toolhive.stacklok.dev/vmcp


























#### pkg.vmcp.ConflictResolutionStrategy

_Underlying type:_ _string_

ConflictResolutionStrategy defines how to handle capability name conflicts.
Placed in vmcp root package to be shared by config and aggregator packages.



_Appears in:_
- [vmcp.config.AggregationConfig](#vmcpconfigaggregationconfig)

| Field | Description |
| --- | --- |
| `prefix` | ConflictStrategyPrefix prefixes all tools with workload identifier.<br /> |
| `priority` | ConflictStrategyPriority uses explicit priority ordering (first wins).<br /> |
| `manual` | ConflictStrategyManual requires explicit overrides for all conflicts.<br /> |








#### pkg.vmcp.DiscoveredBackend



DiscoveredBackend represents a backend server discovered by vMCP runtime.
This type is shared with the Kubernetes operator CRD (VirtualMCPServer.Status.DiscoveredBackends).





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the backend MCPServer |  |  |
| `url` _string_ | URL is the URL of the backend MCPServer |  | Optional: \{\} <br /> |
| `status` _string_ | Status is the current status of the backend (ready, degraded, unavailable, unauthenticated, unknown).<br />Use BackendHealthStatus.ToCRDStatus() to populate this field. |  | Optional: \{\} <br /> |
| `authConfigRef` _string_ | AuthConfigRef is the name of the discovered MCPExternalAuthConfig (if any) |  | Optional: \{\} <br /> |
| `authType` _string_ | AuthType is the type of authentication configured |  | Optional: \{\} <br /> |
| `lastHealthCheck` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | LastHealthCheck is the timestamp of the last health check |  | Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the backend status |  | Optional: \{\} <br /> |
| `circuitBreakerState` _string_ | CircuitBreakerState is the current circuit breaker state (closed, open, half-open).<br />Empty when circuit breaker is disabled or not configured. |  | Enum: [closed open half-open] <br />Optional: \{\} <br /> |
| `circuitLastChanged` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#time-v1-meta)_ | CircuitLastChanged is the timestamp when the circuit breaker state last changed.<br />Empty when circuit breaker is disabled or has never changed state. |  | Optional: \{\} <br /> |
| `consecutiveFailures` _integer_ | ConsecutiveFailures is the current count of consecutive health check failures.<br />Resets to 0 when the backend becomes healthy again. |  | Optional: \{\} <br /> |


























































