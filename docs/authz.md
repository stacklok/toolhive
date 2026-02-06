# Authorization framework

This document describes the authorization framework for MCP servers managed by
ToolHive. The framework uses a pluggable architecture that allows different
authorization backends to be used based on configuration.

## Overview

ToolHive supports adding authorization to MCP servers it manages through a
pluggable authorizer system. The framework is designed to be extensible,
allowing different authorization engines to be implemented and registered.

### Architecture

The authorization framework consists of the following components:

1. **Authorizer interface**: A common interface (`pkg/authz/authorizers/core.go`)
   that all authorization backends must implement.
2. **AuthorizerFactory interface**: A factory interface for creating and
   validating authorizer instances from configuration.
3. **Registry**: A global registry (`pkg/authz/authorizers/registry.go`) where
   authorizer factories register themselves.
4. **Authorization middleware**: HTTP middleware that extracts information from
   MCP requests and delegates authorization decisions to the configured
   authorizer.
5. **Configuration**: A configuration file (JSON or YAML) that specifies which
   authorizer to use and its settings.

### Available authorizers

ToolHive provides the following authorizer implementations:

| Type | Description                                                                                      |
|------|--------------------------------------------------------------------------------------------------|
| `cedarv1` | Authorization using [Cedar](https://www.cedarpolicy.com/), a policy language developed by Amazon |
| `httpv1` | Authorization using an external HTTP-based Policy Decision Point (PDP) with PORC model           |

The framework is designed to support additional authorizers (e.g., OPA, Casbin,
or custom implementations).

## How it works

When an MCP server is started with authorization enabled, the following process
occurs:

1. The JWT middleware authenticates the client and adds the JWT claims to the
   request context.
2. The authorization middleware extracts information from the MCP request,
   including the feature, operation, and resource ID.
3. The configured authorizer evaluates the request against its policies.
4. If the request is authorized, it is passed to the next handler. Otherwise, a
   403 Forbidden response is returned.

## Configure authorization

To set up authorization for an MCP server managed by ToolHive, follow these
steps:

1. Create an authorization configuration file specifying the authorizer type.
2. Start the MCP server with the `--authz-config` flag pointing to your
   configuration file.

### Configuration file structure

All authorization configuration files share a common structure:

```yaml
version: "1.0"
type: "<authorizer-type>"
# Authorizer-specific configuration follows...
```

The common fields are:

- `version`: The version of the configuration format (currently `"1.0"`).
- `type`: The type of authorizer to use (e.g., `cedarv1`). This determines which
  registered authorizer factory handles the configuration.

### Start an MCP server with authorization

To start an MCP server with authorization, use the `--authz-config` flag:

```bash
thv run --transport sse --name my-mcp-server --proxy-port 8080 --authz-config /path/to/authz-config.yaml my-mcp-server-image:latest -- my-mcp-server-args
```

---

## Cedar authorizer (`cedarv1`)

Cedar is the default authorization backend provided by ToolHive. It uses the
Cedar policy language developed by Amazon to express fine-grained authorization
rules.

### Cedar configuration

Create a configuration file (JSON or YAML) with the following structure:

#### JSON format

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal, action == Action::\"call_tool\", resource == Tool::\"weather\");",
      "permit(principal, action == Action::\"get_prompt\", resource == Prompt::\"greeting\");",
      "permit(principal, action == Action::\"read_resource\", resource == Resource::\"data\");"
    ],
    "entities_json": "[]"
  }
}
```

#### YAML format

```yaml
version: "1.0"
type: cedarv1
cedar:
  policies:
    - 'permit(principal, action == Action::"call_tool", resource == Tool::"weather");'
    - 'permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");'
    - 'permit(principal, action == Action::"read_resource", resource == Resource::"data");'
  entities_json: "[]"
```

The Cedar-specific configuration fields are:

- `cedar`: The Cedar-specific configuration.
  - `policies`: An array of Cedar policy strings.
  - `entities_json`: A JSON string representing Cedar entities.

### Writing Cedar policies

Cedar is a powerful policy language that allows you to express complex
authorization rules. Here's a guide to writing Cedar policies for MCP servers.

#### Policy structure

A Cedar policy has the following structure:

```plain
permit|forbid(principal, action, resource) when { conditions };
```

- `permit` or `forbid`: Whether to allow or deny the operation.
- `principal`: The entity making the request.
- `action`: The operation being performed.
- `resource`: The object being accessed.
- `conditions`: Optional conditions that must be satisfied for the policy to
  apply.

#### MCP entities

In the context of MCP servers, the following entities are used:

- **Principal**: The client making the request, identified by the `sub` claim in
  the JWT token.

  - Format: `Client::<client_id>`
  - Example: `Client::user123`

- **Action**: The operation being performed on an MCP feature.

  - Format: `Action::<operation>`
  - Examples:
    - `Action::"call_tool"`: Call a tool
    - `Action::"get_prompt"`: Get a prompt
    - `Action::"read_resource"`: Read a resource

  Note: List operations (`tools/list`, `prompts/list`, `resources/list`) are always
  allowed but the response is filtered based on the corresponding call/get/read policies.
  Define policies for the specific operations (call_tool, get_prompt, read_resource)
  and the list responses will automatically show only the items the user is authorized to access.

- **Resource**: The object being accessed.
  - Format: `<type>::<id>`
  - Examples:
    - `Tool::"weather"`: The weather tool
    - `Prompt::"greeting"`: The greeting prompt
    - `Resource::"data"`: The data resource
    - `FeatureType::"tool"`: The tool feature type (used for list operations)

#### Example policies

Here are some example policies for common scenarios:

##### Allow a specific tool

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather");
```

This policy allows any client to call the weather tool.

##### Allow a specific prompt

```plain
permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");
```

This policy allows any client to get the greeting prompt.

##### Allow a specific resource

```plain
permit(principal, action == Action::"read_resource", resource == Resource::"data");
```

This policy allows any client to read the data resource.

##### List operations

List operations (`tools/list`, `prompts/list`, `resources/list`) do not require explicit policies.
They are always allowed but the response is automatically filtered based on the user's permissions
for the corresponding operations:

- `tools/list` shows only tools the user can call (based on `call_tool` policies)
- `prompts/list` shows only prompts the user can get (based on `get_prompt` policies)
- `resources/list` shows only resources the user can read (based on `read_resource` policies)

For example, if you have this policy:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather");
```

Then `tools/list` will only show the "weather" tool for that user.

##### Allow a specific client to call any tool

```plain
permit(principal == Client::"user123", action == Action::"call_tool", resource);
```

This policy allows the client with ID `user123` to call any tool.

##### Allow clients with a specific role to call any tool

```plain
permit(principal, action == Action::"call_tool", resource) when { principal.claim_roles.contains("admin") };
```

This policy allows any client with the `admin` role to call any tool. The
`claim_roles` attribute is extracted from the JWT claims and added to the principal entity.

##### Allow clients to call tools based on arguments

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"calculator") when {
  resource.arg_operation == "add" || resource.arg_operation == "subtract"
};
```

This policy allows any client to call the calculator tool, but only for the
"add" and "subtract" operations. The `arg_operation` attribute is extracted from
the tool arguments and added to the resource entity.

#### Using JWT claims in policies

The authorization middleware automatically extracts JWT claims from the request
context and adds them with a `claim_` prefix. For example, the `sub` claim becomes
`claim_sub`, and the `name` claim becomes `claim_name`.

These claims are available in two ways in your policies:

1. On the principal entity:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather") when {
  principal.claim_name == "John Doe"
};
```

2. In the context:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather") when {
  context.claim_name == "John Doe"
};
```

Both approaches work and can be used to make authorization decisions based on
the client's identity. This policy allows only clients with the name "John Doe"
to call the weather tool.

#### Using tool arguments in policies

The authorization middleware also extracts tool arguments from the request and
adds them with an `arg_` prefix. For example, the `location` argument becomes
`arg_location`.

These arguments are available in two ways in your policies:

1. On the resource entity:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather") when {
  resource.arg_location == "New York" || resource.arg_location == "London"
};
```

2. In the context:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather") when {
  context.arg_location == "New York" || context.arg_location == "London"
};
```

Both approaches work and can be used to make authorization decisions based on
the specific parameters of the request. This policy allows any client to call the
weather tool, but only for the locations "New York" and "London".

#### Combining JWT claims and tool arguments

You can combine JWT claims and tool arguments in your policies to create more sophisticated authorization rules:

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"sensitive_data") when {
  principal.claim_roles.contains("data_analyst") &&
  resource.arg_data_level <= principal.claim_clearance_level
};
```

This policy allows clients with the "data_analyst" role to access the sensitive_data tool, but only if their clearance level (from JWT claims) is sufficient for the requested data level (from tool arguments).

### Advanced Cedar topics

#### Entity attributes

Cedar entities can have attributes that can be used in policy conditions. The
authorization middleware automatically adds JWT claims and tool arguments as
attributes to the principal entity.

You can also define custom entities with attributes in the `entities_json` field
of the configuration file:

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal, action == Action::\"call_tool\", resource) when { resource.owner == principal.claim_sub };"
    ],
    "entities_json": "[
      {
        \"uid\": \"Tool::weather\",
        \"attrs\": {
          \"owner\": \"user123\"
        }
      }
    ]"
  }
}
```

This configuration defines a custom entity for the weather tool with an `owner`
attribute set to `user123`. The policy allows clients to call tools only if they
own them.

#### Policy evaluation

Cedar policies are evaluated in the following order:

1. If any `forbid` policy matches, the request is denied.
2. If any `permit` policy matches, the request is authorized.
3. If no policy matches, the request is denied (default deny).

This means that `forbid` policies take precedence over `permit` policies.

---

## HTTP PDP authorizer (`httpv1`)

The HTTP PDP authorizer provides authorization using an external HTTP-based Policy
Decision Point (PDP). This is a general-purpose authorizer that can work with
any PDP server that implements the PORC (Principal-Operation-Resource-Context)
decision endpoint.

### HTTP PDP configuration

The authorizer connects to a remote PDP server via HTTP. This allows you to
share a single PDP across multiple services or run the PDP as a sidecar service.

#### YAML format

```yaml
version: "1.0"
type: httpv1
pdp:
  http:
    url: "http://localhost:9000"
    timeout: 30  # Optional, timeout in seconds (default: 30)
    insecure_skip_verify: false  # Optional, skip TLS verification (default: false)
  claim_mapping: "mpe"  # Required: claim mapper type (options: "mpe", "standard")
```

#### JSON format

```json
{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "http://localhost:9000",
      "timeout": 30,
      "insecure_skip_verify": false
    },
    "claim_mapping": "mpe"
  }
}
```

The configuration fields are:

- `pdp.http.url`: The base URL of the PDP server (required)
- `pdp.http.timeout`: HTTP request timeout in seconds (default: 30)
- `pdp.http.insecure_skip_verify`: Skip TLS certificate verification (default: false)
- `pdp.claim_mapping`: Claim mapper type (required)
  - `"mpe"`: Maps to m-prefixed claims (mroles, mgroups, mclearance, mannotations) - compatible with Manetu PolicyEngine and similar systems
  - `"standard"`: Uses standard OIDC claim names (roles, groups) - compatible with PDPs expecting standard OIDC conventions

> **⚠️ SECURITY WARNING: `insecure_skip_verify`**
>
> The `insecure_skip_verify` option disables TLS certificate validation, making the connection vulnerable to man-in-the-middle attacks. An attacker could intercept and modify authorization decisions, potentially granting unauthorized access to your MCP servers.
>
> **NEVER use `insecure_skip_verify: true` in production environments.**
>
> This option is provided ONLY for local development and testing scenarios where you may be using self-signed certificates. In production, always use valid TLS certificates and keep this option set to `false` (the default).

### Context configuration

The context configuration controls what MCP-specific information is included in
the PORC `context` object. By default, no MCP context is included. You can enable
specific context fields based on your policy requirements.

```yaml
version: "1.0"
type: httpv1
pdp:
  http:
    url: "http://localhost:9000"
  context:
    include_args: true       # Include tool/prompt arguments in context.mcp.args
    include_operation: true  # Include feature, operation, and resource_id in context.mcp
```

The context configuration fields are:

- `pdp.context.include_args`: When `true`, includes tool/prompt arguments in
  `context.mcp.args`. Default is `false`.
- `pdp.context.include_operation`: When `true`, includes MCP operation metadata
  (`feature`, `operation`, `resource_id`) in `context.mcp`. Default is `false`.

#### Important notes about context configuration

**Policy requirements**: Enable context options based on what your PDP policies require.
If your policies reference `context.mcp.*` fields (such as `context.mcp.resource_id`
or `context.mcp.operation`), you must enable the corresponding context option.
Otherwise, those fields will not be present in the PORC, which may cause:

- Policy evaluation failures
- Authorization denials
- Unexpected behavior

Each PDP implementation handles missing context fields differently. Consult your
PDP's documentation to understand how it treats missing fields in authorization
decisions.

**Recommendation**: Start with both options disabled (the default) and only enable
them when your policies explicitly require those fields. This minimizes the data
sent to the PDP and reduces the risk of misconfiguration.

### Claim mapping

The HTTP PDP authorizer supports different claim mapping conventions through the
`claim_mapping` configuration option. This allows you to use the authorizer with
PDPs that expect different claim naming conventions.

#### MPE claim mapping (`claim_mapping: "mpe"`)

The MPE claim mapper uses m-prefixed claims, designed for compatibility with
Manetu PolicyEngine and similar systems. It accepts both standard OIDC claims
and m-prefixed claims as input:

| JWT Claim (input) | Principal Field (output) | Notes |
|-------------------|-------------------------|-------|
| `sub` | `sub` | Subject identifier |
| `roles` or `mroles` | `mroles` | Roles (accepts both, outputs `mroles`) |
| `groups` or `mgroups` | `mgroups` | Groups (accepts both, outputs `mgroups`) |
| `scope` or `scopes` | `scopes` | Access scopes (normalized to `scopes`) |
| `clearance` or `mclearance` | `mclearance` | Clearance level (accepts both, outputs `mclearance`) |
| `annotations` or `mannotations` | `mannotations` | Additional annotations (accepts both, outputs `mannotations`) |

#### Standard OIDC claim mapping (`claim_mapping: "standard"`)

The standard claim mapper uses standard OIDC claim names without modification:

| JWT Claim (input) | Principal Field (output) | Notes |
|-------------------|-------------------------|-------|
| `sub` | `sub` | Subject identifier |
| `roles` | `roles` | Roles (standard name) |
| `groups` | `groups` | Groups (standard name) |
| `scope` or `scopes` | `scopes` | Access scopes (normalized to `scopes`) |

### PORC mapping

The HTTP PDP authorizer uses the PORC (Principal-Operation-Resource-Context)
model for authorization decisions. ToolHive automatically maps MCP requests to
PORC:

| MCP Concept | PORC Field | Format |
|-------------|------------|--------|
| Client identity | `principal.sub` | From JWT `sub` claim |
| Roles | `principal.mroles` (MPE) or `principal.roles` (standard) | From JWT `roles` or `mroles` claim (depends on `claim_mapping`) |
| Groups | `principal.mgroups` (MPE) or `principal.groups` (standard) | From JWT `groups` or `mgroups` claim (depends on `claim_mapping`) |
| Scopes | `principal.scopes` | From JWT `scope` or `scopes` claim |
| MCP operation | `operation` | `mcp:<feature>:<operation>` (e.g., `mcp:tool:call`) |
| MCP resource | `resource` | `mrn:mcp:<server>:<feature>:<id>` (e.g., `mrn:mcp:myserver:tool:weather`) |
| MCP feature | `context.mcp.feature` | The MCP feature type - requires `include_operation: true` |
| MCP operation type | `context.mcp.operation` | The MCP operation - requires `include_operation: true` |
| MCP resource ID | `context.mcp.resource_id` | The resource identifier - requires `include_operation: true` |
| Tool arguments | `context.mcp.args` | Tool/prompt arguments - requires `include_args: true` |

### Example PORC expressions

#### With MPE claim mapping

When a client calls the `weather` tool with `location: "New York"`, using MPE
claim mapping (`claim_mapping: "mpe"`), and both `include_operation`
and `include_args` are enabled, the resulting PORC expression looks like:

```json
{
  "principal": {
    "sub": "user@example.com",
    "mroles": ["developer"],
    "mgroups": ["engineering"],
    "scopes": ["read", "write"],
    "mannotations": {}
  },
  "operation": "mcp:tool:call",
  "resource": "mrn:mcp:myserver:tool:weather",
  "context": {
    "mcp": {
      "feature": "tool",
      "operation": "call",
      "resource_id": "weather",
      "args": { "location": "New York" }
    }
  }
}
```

If no context options are enabled (the default), the `context` object will be empty.

#### With standard OIDC claim mapping

When using standard OIDC claim mapping (`claim_mapping: "standard"`), the same
request would produce:

```json
{
  "principal": {
    "sub": "user@example.com",
    "roles": ["developer"],
    "groups": ["engineering"],
    "scopes": ["read", "write"]
  },
  "operation": "mcp:tool:call",
  "resource": "mrn:mcp:myserver:tool:weather",
  "context": {
    "mcp": {
      "feature": "tool",
      "operation": "call",
      "resource_id": "weather",
      "args": { "location": "New York" }
    }
  }
}
```

Note that the principal uses standard claim names (`roles`, `groups`) instead of
m-prefixed names (`mroles`, `mgroups`), and MPE-specific fields like `mclearance`
and `mannotations` are not included.

### PDP API contract

The HTTP PDP authorizer expects the PDP server to implement the following endpoint:

**POST /decision**

Request body: A JSON PORC object (see example above)

Response body:
```json
{
  "allow": true
}
```

The `allow` field should be `true` to permit the request, or `false` to deny it.

### Compatible PDP servers

The HTTP PDP authorizer is designed to work with any PDP server that implements
the PORC-based decision endpoint described above. Examples include:

- [Manetu PolicyEngine (MPE)](https://manetu.github.io/policyengine) - A policy
  engine built on OPA with multi-phase evaluation (use `claim_mapping: "mpe"`)
- Custom PDP implementations that follow the PORC API contract
- Other policy engines adapted to accept PORC-formatted requests

When integrating with a specific PDP, configure the `claim_mapping` option to match
your PDP's expected claim naming conventions.

---

## Implementing a custom authorizer

The authorization framework is designed to be extensible. You can implement your
own authorizer by following these steps:

### 1. Implement the Authorizer interface

Create a type that implements the `Authorizer` interface defined in
`pkg/authz/authorizers/core.go`:

```go
type Authorizer interface {
    AuthorizeWithJWTClaims(
        ctx context.Context,
        feature MCPFeature,
        operation MCPOperation,
        resourceID string,
        arguments map[string]interface{},
    ) (bool, error)
}
```

### 2. Implement the AuthorizerFactory interface

Create a factory that implements the `AuthorizerFactory` interface defined in
`pkg/authz/authorizers/registry.go`:

```go
type AuthorizerFactory interface {
    // ValidateConfig validates the authorizer-specific configuration.
    ValidateConfig(rawConfig json.RawMessage) error

    // CreateAuthorizer creates an Authorizer instance from the configuration.
    CreateAuthorizer(rawConfig json.RawMessage) (Authorizer, error)
}
```

### 3. Register the factory

Register your factory in an `init()` function so it's available when the package
is imported:

```go
package myauthorizer

import "github.com/stacklok/toolhive/pkg/authz/authorizers"

const ConfigType = "myauthv1"

func init() {
    authorizers.Register(ConfigType, &Factory{})
}

type Factory struct{}

func (*Factory) ValidateConfig(rawConfig json.RawMessage) error {
    // Validate your configuration
    return nil
}

func (*Factory) CreateAuthorizer(rawConfig json.RawMessage) (authorizers.Authorizer, error) {
    // Parse config and create your authorizer
    return &MyAuthorizer{}, nil
}
```

### 4. Import the package

Ensure your authorizer package is imported (typically via a blank import) so that
the `init()` function runs and registers the factory:

```go
import _ "github.com/stacklok/toolhive/pkg/authz/authorizers/myauthorizer"
```

---

## Troubleshooting

If you're having issues with authorization, here are some common problems and
solutions:

### Request is denied unexpectedly

- Check that your policies are correctly formatted.
- Check that the principal, action, and resource in your policies match the
  actual values in the request.
- Check that any conditions in your policies are satisfied by the request.
- Remember that most authorizers use a default deny policy, so if no policy
  explicitly permits the request, it will be denied.

### JWT claims are not available in policies

- Make sure that the JWT middleware is configured correctly and is running
  before the authorization middleware.
- Check that the JWT token contains the expected claims.
- Remember that JWT claims are added with a `claim_` prefix (e.g., `claim_sub`,
  `claim_roles`).

### Tool arguments are not available in policies

- Check that the tool arguments are correctly specified in the request.
- Remember that tool arguments are added with an `arg_` prefix (e.g.,
  `arg_location`).

### Unknown authorizer type

- Ensure the authorizer package is imported (see "Implementing a custom
  authorizer" above).
- Check that the `type` field in your configuration matches a registered
  authorizer type exactly.
- Use `authorizers.RegisteredTypes()` to see which authorizer types are
  available.
