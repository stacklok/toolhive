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

Currently, ToolHive provides the following authorizer implementation:

| Type | Description |
|------|-------------|
| `cedarv1` | Authorization using [Cedar](https://www.cedarpolicy.com/), a policy language developed by Amazon |

The framework is designed to support additional authorizers in the future (e.g.,
OPA, Casbin, or custom implementations).

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
