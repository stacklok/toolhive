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

MCP parsing validates every POST in an MCP middleware chain, on any path and
regardless of Content-Type. Missing or non-JSON media types do not bypass
request-level controls. Malformed, trailing, batched, ambiguous, or mixed
JSON-RPC envelopes are rejected before dispatch (HTTP 400; body-size failures
remain 413). Valid client responses do not populate `ParsedMCPRequest`; their
handling remains transport-specific. Authorization middleware still refuses
POSTs without a parsed request, including client responses.

### Virtual MCP parity

Virtual MCP (`vmcp`) enforces the same Cedar policies through its core admission
seam rather than HTTP middleware, but the client-facing behavior matches a
single-server `thv run`: a denied `tools/call`, `resources/read`, or `prompts/get`
is rejected with **HTTP 403** and a JSON-RPC error whose code is `403`, and the
denied request is audited with outcome `denied`. The denial message is kind-only
(for example, `call denied by authorization policy`) and never reveals the
capability name or whether it exists, so it cannot be used to enumerate the
server.

Virtual MCP applies the same resource URI rules (see [MCP entities](#mcp-entities)) to
`resources/read` and to `completion/complete` with a `ref/resource`: a read of a URI
that is not in canonical form is denied, and a completion ref is refused only when it
is empty or contains a space or control character. For `completion/complete` the denial
is a JSON-RPC error, not an HTTP 403.

One ordering differs from the direct `thv run` proxies, where authorization runs
first: Legacy vMCP checks session ownership before the authorization gate. A request that
carries an `Mcp-Session-Id` the caller does not own, or that does not exist, gets
**HTTP 404** before any Cedar 403. A denied call on the caller's own session still gets
403. Two checks run before session ownership: the loopback `Host` guard, and call
binding. A request with an access token bound to one MCP call that the token does not
permit gets its 401 or 403 from call binding whatever session it names.

Call binding is not a Cedar policy and runs whether or not Cedar is configured. An
access token that carries an `authorization_details` entry of type
`https://toolhive.dev/mcp_tool_call` may start only the one `tools/call` its entry
names, once. A retried call, such as the retry after a URL elicitation, is a second
call and needs a fresh token. An entry that names a list call (a backend and a list
method) instead lets the token send that list method once, answered
with only that backend's items, all in one response without `nextCursor`; it cannot
start a `tools/call`. Before that list call, a `tools/call` or another list method gets
**HTTP 403** `method_mismatch`, and a list request that carries a `cursor` gets 403
`invalid_params`. A backend the caller is not admitted to anything of, including one
that does not exist, gets 403 `server_not_found`; one the caller can see but that is
not served now for its health status gets **HTTP 503**. None of these uses up the
token. Before its `exp`, such a token may also send, to the MCP endpoint:

- the handshake (`initialize`, `notifications/initialized`), the Modern
  `server/discover` probe and `ping`; `initialize` only until the token has started
  its call;
- `tools/list`, with a tool-call entry, filtered by Cedar as usual; it does not use
  up the token;
- GET and DELETE on sessions its principal owns, and once the token has started its
  call, only on that call's session;
- on the Legacy session of its tool call, while the call is open (its request is
  open, or its dispatch still runs, as after the client closes the call's POST): a
  `notifications/cancelled` for the call and any JSON-RPC response, such as an
  elicitation answer.

Call binding refuses everything else on the MCP endpoint. A refusal that describes the
token gets **HTTP 401** with `WWW-Authenticate: Bearer error="invalid_token"` (with the
same `realm` and `resource_metadata` as the token validator's challenges) and an RFC
6750 error body: the token has expired (`token_expired`), has already started its call
(`token_reused`), or its `authorization_details` or claims are unusable
(`malformed_entry`, `single_entry_required`, `claims_required`). The client needs a
new token. Any other refusal is about the request and gets **HTTP 403** with a JSON-RPC
error and no `WWW-Authenticate`, like a Cedar denial. In both cases the message ends
with the reason, and the request is audited as `denied` with
`metadata.extra.denial_reason`.

Two consequences are easy to miss. A token whose `authorization_details` claim is
malformed is refused on every request, whatever its client, even one that never
asked for call binding. And `exp` limits only when a call can start: a tool call that
outlives its token keeps, on its session, its cancellation, elicitation answers, and
GET and DELETE, until the call's request ends or the call has run for the maximum
call duration (30 minutes by default). Anything else after `exp` gets 401. This needs
the token validator to share the vMCP's call binding, as the `vmcp` command wires it.
See
[Call-bound access tokens](arch/10-virtual-mcp-architecture.md#call-bound-access-tokens).

For Legacy vMCP and stateful proxy requests, a delegated session is bound to the
issuer and user, the delegated route, the OAuth client (`client_id`), and the
immediate actor when present. Interactive and delegated tokens cannot reuse each
other's sessions, even for the same user, when the validator identifies the
delegated route. A token on that route with a missing or invalid `client_id`, a
malformed or incomplete `act`, or an explicit `act: null` cannot open or reuse a session.
On Legacy vMCP, a request that tries to reuse a session without a matching
binding gets the same plain-text 404 `Session terminated` as an unknown session.
See [vMCP session ownership](arch/10-virtual-mcp-architecture.md#session-ownership)
and [proxy session ownership](arch/03-transport-architecture.md#session-ownership)
for the binding rules and upgrade requirements.

See the [Virtual MCP architecture](arch/10-virtual-mcp-architecture.md#authorization-enforcement-core-admission-seam--pre-dispatch-gate)
for details.

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
      "permit(principal, action == Action::\"read_resource\", resource == Resource::\"data\");",
      "permit(principal, action == Action::\"get_skill\", resource == Skill::\"mcp://example/skill\");"
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
    - 'permit(principal, action == Action::"get_skill", resource == Skill::"mcp://example/skill");'
  entities_json: "[]"
```

The Cedar-specific configuration fields are:

- `cedar`: The Cedar-specific configuration.
  - `policies`: An array of Cedar policy strings. Each entry must hold a single
    policy statement.
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

Each entry in a `policies` list must hold exactly one `permit` or `forbid`
statement. Entries that contain more than one statement are rejected; place
each statement in its own list entry.

#### Policy limits

Inline policies on an `MCPRemoteProxy` are limited to 8 KiB (8192 bytes) each.
The operator checks this limit together with the policy syntax. A policy over
the limit, or an entry containing more than one statement, is rejected the same
way as one with a syntax error: the
`ConfigurationValid` condition is set to `False` with reason
`AuthzPolicySyntaxInvalid`, the proxy phase is `Failed`, and the operator emits
a Warning event that names the policy index and the limit, for example
`cedar policy at index 2 rejected: policy is too large (9120 bytes, max 8192)`.
The `Ready` and `AuthConfigured` conditions also become `False`, and the
operator retries with backoff until the policy is fixed. The existing
Deployment, Service and authorization ConfigMap keep their last valid
configuration in the meantime.

Inline policies on `MCPServer` and `VirtualMCPServer`, and policies in a
referenced ConfigMap, are not size-checked.

For large allowlists, use group or claim membership, or `entitiesJson`,
instead of listing every value in a policy. Do not split a `forbid ... unless`
allowlist across several list entries: each entry is evaluated on its own, so
splitting changes what the policy means.

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
    - `Action::"get_skill"`: Get a skill

  Note: List operations (`tools/list`, `prompts/list`, `resources/list`, `skills/list`) are always
  allowed but the response is filtered based on the corresponding call/get/read policies.
  Define policies for the specific operations (call_tool, get_prompt, read_resource, get_skill)
  and the list responses will automatically show only the items the user is authorized to access.

- **Resource**: The object being accessed.
  - Format: `<type>::<id>`
  - Examples:
    - `Tool::"weather"`: The weather tool
    - `Prompt::"greeting"`: The greeting prompt
    - `Resource::"data"`: A short resource name
    - `Resource::"file:///etc/passwd"`: An MCP resource URI (exact URI is the Cedar entity ID)
    - `Skill::"mcp://example/skill"`: An MCP skill URI (exact URI is the Cedar entity ID)
    - `FeatureType::"tool"`: The tool feature type (used for list operations)

  For `read_resource`, the Cedar entity ID is the **exact resource URI** (for example
  `Resource::"file:///ok"` or `Resource::"mcp://srv/config:admin"`). Do not rewrite
  characters such as `/`, `:`, or `?` into underscores; policies must name the URI as
  the client and server see it. Skill URI values follow these same Cedar string-literal
  escaping rules. In Cedar source the ID is a double-quoted string literal, so almost
  every URI character is ordinary, but `"` and `\` must be escaped (for example
  `Resource::"file://C:\\share\\data"`).

**Write policies with the canonical URI.** The proxy compares the URI exactly as the
client sent it, and it accepts a resource URI only when it is already in canonical
form. A URI in any other form is rejected with a 403 before any policy is evaluated.
This applies to `resources/read`, `resources/subscribe`, `resources/unsubscribe` and
each URI in a `subscriptions/listen` request.

The reason is that some MCP servers look a resource up by a cleaned-up form of the
URI. We verified this for the TypeScript SDK and for the Python SDK, whose URL type
(pydantic `AnyUrl`) cleans up URIs the same way the TypeScript SDK does. The people who
reported the problem showed that the C# SDK cleans up URIs too. A `forbid` on one
spelling would not cover the other spellings that reach the same resource. Because
only one spelling per resource gets through, one exact comparison is the whole
decision.

A URI is canonical when WHATWG URL serialization (the browser standard) leaves it
unchanged and it also passes a few RFC 3986 normalization rules. In practice:

- **The scheme is lower case.** `HARNESS://host/x` is refused; use `harness://host/x`.
  A scheme is required.
- **The host is lower case for `http`, `https`, `ws`, `wss`, `ftp` and `file`.**
  `http://EXAMPLE.com/x` is refused; use `http://example.com/x`. Other schemes keep the
  case of the host (`repo://Stacklok/toolhive/README.md` is fine). A host holds only
  letters, digits, `-` and `_`, in dot-separated labels with no empty label and no
  trailing dot.
- **Percent-encoding uses upper-case hex.** `%3a` is refused; use `%3A`.
- **Letters, digits and `-._~` are not percent-encoded.** `%41` and `%7e` are refused;
  use `A` and `~`.
- **There are no `.` or `..` segments**, written plainly or as `%2e`. The refused
  `harness://host/a/../b` and `harness://host/%2e%2e/b` are both written
  `harness://host/b`.
- **User information is plain.** A user name, and an optional password after one `:`,
  are allowed when each is not empty and uses only letters, digits, `-._~!$&'()*+,` and
  upper-case `%XX`. `postgres://user@host:5432/db/table/schema` and
  `harness://u:p@host/x` are fine. These are refused: an empty user or password
  (`harness://@host/x`, `harness://:@host/x`, `harness://u:@host/x`), a second `:` or an
  `@` in it, and `;` or `=` (`harness://u;x@host/x`), which parsers encode. User
  information is never allowed in a `file` URI, and it needs a host.
- **A port is plain.** It has a host, no leading zero, is not `0`, is at most 65535
  and is not the default port for the scheme. `http://example.com:80/x` is refused; use
  `http://example.com/x`. So are `harness://host:/x` (empty port) and
  `harness://host:080/x`.
- **`http`, `https`, `ws`, `wss` and `ftp` have a host and a path.**
  `http://example.com` is refused; use `http://example.com/`. A numeric host must be a
  full dotted quad: `http://127.1/` is refused, `http://127.0.0.1/` is fine.
- **`xn--` (punycode) labels are not allowed in the host** of these schemes and
  `file`.
- **A query does not contain `'`** for those schemes and `file`. Write it as `%27`.
- **`file` URIs are written `file:///path`.** The forms `file:/path`,
  `file://localhost/path` and `file://host:8080/path` are refused, and the path must
  not start with `//`. A drive letter is allowed only with an empty host:
  `file:///C:/x` is fine, `file://host/C:/x` is refused.
- **Only these bytes appear:** letters, digits, and `- . _ ~ ! $ & ' ( ) * + , ; = : @ / ? %`.
  A space, a control character (`0x00` to `0x1F`, `0x7F`), a non-ASCII character and
  any of `"`, `<`, `>`, `\`, `^`, a backtick, `{`, `|`, `}`, `[` and `]` are refused.
  Percent-encode them: a space is `%20` and `café` is `caf%C3%A9`.
- **There is no fragment** (`#...`) and **no IPv6 literal host** (`[::1]`). Both are
  refused.

For example, `Resource::"harness://static/a%20b"` and `Resource::"file:///C:/x"` match
the URIs a client sends, and the canonical spelling is the only one that can match.

A server that lists a resource under a URI that is not in canonical form (for example
`file:///My Docs/x`) can still show it in `resources/list`, but a read of it is
refused. The server should list the canonical URI.

The reference in `completion/complete` with a `ref/resource` is a resource template
such as `file:///{name}`. A template is not a URI, and SDKs match it by exact string.
So the proxy refuses it only when it is empty or contains a space or control
character, and otherwise authorizes it exactly as sent.

Requests that identify their target only by `params.name` or `params.uri` (`tools/call`,
`prompts/get`, `resources/read`, `resources/subscribe`, `resources/unsubscribe`, and
`skills/get`) are rejected with a 403 when that value is missing, empty, or not a string. The proxy denies these before any policy is evaluated or
the backend is called, so a permit that does not name the target cannot match an empty
identifier.

#### Skills (SEP-2640 direct proxy)

Direct proxies authorize `skills/get` with `Action::"get_skill"` on an exact
`Skill::"<params.uri>"` entity. The URI is passed through verbatim: it is not
canonicalized and no scheme or suffix is validated. Requests with duplicate
immediate `params.uri` members are denied so the proxy and backend cannot
interpret an ambiguous URI differently.

`skills/list` itself has no separate list policy. It is forwarded and each entry is
shown only when its exact string `uri` is permitted by `get_skill`; all other entries,
including their manifests, are removed. Skill permission is independent of
`read_resource` permission.

This support is only for the direct proxy. Skill capability negotiation, including
initialize extension maps, is passed through unchanged; the proxy neither fabricates
nor rewrites capabilities. Directory reads and all other SEP-2640 operations are not
supported by this authorization layer.

#### Example policies

Here are some example policies for common scenarios:

##### Allow a specific tool

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather");
```

This policy allows any client to call the weather tool.

##### Allow tools from a specific vMCP backend

For tools advertised by a Virtual MCP Server, Cedar receives the logical
originating backend as a second resource parent:

```text
Tool::"search"
  -> MCP::"main-vmcp"
  -> Backend::"github-mcp"
```

This allows every tool from one backend without relying on the advertised tool
name or its conflict-resolution prefix:

```plain
permit(
  principal,
  action == Action::"call_tool",
  resource in Backend::"github-mcp"
);
```

The Backend entity ID is the tool's logical vMCP `BackendID`, not a network
address. ToolHive obtains it from the aggregated capability and uses the same
value for list filtering and call authorization. A direct Backend policy does
not require an entry in `entities_json`: Cedar evaluates it from the Backend UID
in the Tool entity's parent set. Define the Backend in `entities_json` only when
it needs attributes or parents for a transitive hierarchy.

Composite tools have no single originating backend, so they have no Backend
parent. Consequently, neither a backend-scoped `permit` nor a backend-scoped
`forbid` matches a composite tool. If another policy permits the composite, a
backend-scoped `forbid` does not prevent its workflow steps from reaching that
backend because workflow steps are not authorized as separate top-level tool
calls. Express backend restrictions as conditions on `permit` policies and
authorize composite tools separately by their `Tool` entity.

##### Allow a specific prompt

```plain
permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");
```

This policy allows any client to get the greeting prompt.

##### Allow a specific resource

```plain
permit(principal, action == Action::"read_resource", resource == Resource::"data");
```

This policy allows any client to read the resource whose entity ID is `data`.

For URI-shaped MCP resources, name the exact URI:

```plain
permit(
  principal,
  action == Action::"read_resource",
  resource == Resource::"file:///etc/passwd"
);
```

##### Forbid a specific resource

A `forbid` on a resource URI, next to a broader `permit`, holds against servers built
on the TypeScript SDK and the Python SDK. Those SDKs parse the URI and look the
resource up by the cleaned-up form (lower-case scheme, no `.` or `..` segments, and so
on). The proxy refuses every URI that is not in canonical form before the policy runs,
so a client cannot reach the resource through a different spelling:

```plain
permit(principal, action == Action::"read_resource", resource);
forbid(
  principal,
  action == Action::"read_resource",
  resource == Resource::"harness://host/secret"
);
```

With this policy, `HARNESS://host/secret`, `harness://host/./secret` and
`harness://host/%2e%2e/secret` are all refused with a 403, and the exact URI
`harness://host/secret` is denied by the `forbid`.

Some cases the proxy does not close:

- **The C# SDK.** The people who reported the problem showed that it cleans up URIs,
  for example `harness://STATIC/text` reaches `harness://static/text`. The canonical
  check closes what it shares with the browser standard (a lower-case scheme and
  host for `http` and `https`, dot segments, and so on), but not these:
  - the case of the host for other schemes, which the proxy keeps as sent;
  - user information, which the proxy accepts as plain text (we did not verify whether
    C# ignores or changes it);
  - possibly decoded `!'()*` escapes, and the default ports of other schemes.

  If a backend uses the C# SDK, use an allow-list.
- **A resource template handler that changes the URI it receives.** For example, a
  handler that decodes `%2F` to `/` can reach a resource the `forbid` names under a
  different spelling. So can a handler that rebuilds the resource from the host or path
  of the parsed URL, because it ignores the user information and the port:
  `harness://u@static:99/secret` can reach what a `forbid` on
  `harness://static/secret` names. Check what your handlers do with the URI, or use an
  allow-list.
- **A server's own URI handling** beyond these SDKs.

The safest policy is an allow-list. Permit only the URIs that a client may read, and
rely on default deny for everything else:

```plain
permit(
  principal,
  action == Action::"read_resource",
  resource == Resource::"harness://host/public"
);
```

Any other URI matches no `permit` and is denied.

Do not rely on a pattern in a condition to block part of a URI space. A guard such as
`when { resource.uri like "harness://host/public/*" }`, a `contains` check or a test for
`..` matches the text of the URI, not the resource the server picks. For example,
`%2e%2e` is a `..` segment that a text check for `..` does not see, and a server may
treat `\` as `/`. Name each URI exactly.

##### List operations

List operations (`tools/list`, `prompts/list`, `resources/list`, `skills/list`) do not require explicit policies.
They are always allowed but the response is automatically filtered based on the user's permissions
for the corresponding operations:

- `tools/list` shows only tools the user can call (based on `call_tool` policies); a result in which a tool repeats `name`, `annotations`, or the annotation `title` or a hint such as `readOnlyHint`, or spells one with a case-insensitive alias, fails closed with a generic internal JSON-RPC error, so policies evaluate exactly the hints the client receives.
- `prompts/list` shows only prompts the user can get (based on `get_prompt` policies)
- `resources/list` shows only resources the user can read (based on `read_resource` policies)
- `skills/list` shows only skill entries the user can get (based on `get_skill` policies); entries
  without exactly one non-empty string `uri` fail closed with a generic internal JSON-RPC error.

Filtering means reading the response body, so a compressed body would be opaque
to the filter. For the methods listed above (and for `find_tool`), ToolHive drops
the client's `Accept-Encoding` before forwarding the request and lets Go's
transport negotiate `gzip` on its own, which it decodes before the filter runs.
Those responses therefore reach the client uncompressed, while every other
method — `features/list` included — keeps end-to-end compression. If a backend
compresses one of them without being asked, the filter cannot read it and will
not forward it unfiltered: the client receives HTTP 500 with a generic JSON-RPC
internal error, and ToolHive logs a `WARN` naming the encoding and the backend's
status. An encoded non-2xx response is exempt, since it cannot carry a list, and
is forwarded with its status and body intact.

For example, if you have this policy:
```plain
permit(principal, action == Action::"call_tool", resource == Tool::"weather");
```

Then `tools/list` will only show the "weather" tool for that user.

A filtered list depends on the caller's permissions, so `tools/list`, `prompts/list`, and `resources/list` responses carry the HTTP header `Cache-Control: private, no-store`, and a `cacheScope` or `ttlMs` member the backend sent in the result is replaced with `"private"` or `0`. The proxy does not add either member when the backend omitted it, so a list from a backend on an MCP revision that predates these members reaches the client without them. A result that repeats `cacheScope` or `ttlMs`, or spells one with a case-insensitive alias, fails closed with a generic internal JSON-RPC error. `resources/templates/list` responses carry the same header, and their result always includes `cacheScope: "private"` and `ttlMs: 0`.

The SSE transport (`--transport sse`) and the GET stream of streamable HTTP filter list answers on the event stream. The middleware wraps every `GET`, so this applies to each GET it sees. Each event is filtered as it arrives, with the filter chosen from the result's shape:

- A list answer (tools, prompts, resources, resource templates, skills) is filtered by policy.
- A `find_tool` answer is filtered.
- A `tools/call` answer from any other tool that names a tool under a `tools` key, at any depth, fails closed with a JSON-RPC error, because the stream cannot see which tool was called. It is stricter than the POST path, which filters only the answer to a call to `find_tool`. A `tools/call` answer that is a valid `find_tool` answer, but carries another `tools` member naming a tool (under `_meta` or beside `tools`), also fails closed.
- A tool name under a member with another key, such as `related`, is not checked.
- A result that is not a clean JSON-RPC response, carries more than one list, or is ambiguous fails closed with a JSON-RPC error that carries the request id.
- Other results pass through unchanged.

Permissions are taken from the request that opens the stream, and each event is checked against the authorizer as it arrives. A change to the caller's role or token takes effect when the client reconnects, not mid-stream.

Other limits:

- A 2xx response that is not `text/event-stream` is held until the handler returns, then read as one JSON-RPC message or as SSE events.
- A non-2xx response passes through unchanged.
- A body larger than 16 MiB ends the stream with one error.
- A filtered stream with a `Content-Encoding` other than `identity` gets `502 Bad Gateway`. The filter removes `Accept-Encoding` from every GET so the backend sends an identity body.
- A frame that fails closed but has no readable id is dropped, because an error without an id ends the client's session. Frames that carry no result, such as notifications, pass through.
- Every GET behind authz is wrapped, including non-MCP GETs on the transparent proxy. A filtered 2xx GET response carries `Cache-Control: private, no-store`.
- A request to upgrade the connection (Hijack) is refused on a filtered GET.

##### Methods covered by a prompt or resource policy

Some MCP methods do not name a capability directly but still act on one. They are
authorized as the capability their request body references:

| Method | Authorized as |
| --- | --- |
| `prompts/get` | `get_prompt` on the prompt name |
| `completion/complete` with a `ref/prompt` | `get_prompt` on the referenced prompt name |
| `resources/read` | `read_resource` on the URI |
| `resources/subscribe`, `resources/unsubscribe` | `read_resource` on the URI |
| `completion/complete` with a `ref/resource` | `read_resource` on the referenced URI or URI template |
| `subscriptions/listen` | `read_resource` on **every** URI in `notifications.resourceSubscriptions` |

For prompts the name is the same string in both methods, so one rule covers both.
This policy:

```plain
permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");
```

lets a client both retrieve the `greeting` prompt and request argument
completions for it, and nothing else. A client denied `greeting` cannot use
completion to enumerate its argument values.

###### Resource templates need their own rule

A `ref/resource` completion references a **URI template** such as
`secrets://tenant/{name}`, not a concrete URI. That template string is its own
Cedar entity ID, exactly as it is for `resources/templates/list`. A policy naming
only concrete URIs does not cover it:

```plain
# Allows reading the resource, but NOT completions on the template.
permit(principal, action == Action::"read_resource", resource == Resource::"secrets://tenant/admin");

# Required as well, to allow completing the template's {name} variable.
permit(principal, action == Action::"read_resource", resource == Resource::"secrets://tenant/{name}");
```

Two consequences to weigh when writing these rules:

- If you grant nothing for the template string, template completions are denied
  for everyone. That is fail-closed, but it is a change from earlier releases
  where `completion/complete` was allowed unconditionally.
- A wildcard rule such as `when { resource.uri like "secrets://tenant/*" }`
  matches the template **and** every concrete URI under it. Granting the template
  that way lets a client enumerate candidate values for the whole namespace, even
  where a narrower `forbid` covers an individual resource. Name the template
  explicitly rather than relying on a wildcard.

###### Other behaviors worth knowing

- **Subscriptions are all-or-nothing.** If a `subscriptions/listen` request names
  any URI the policy denies, the whole request is rejected with 403 rather than
  being silently narrowed to the permitted subset. At most 50 URIs may be named in
  one request; beyond that the request is rejected. Repeated URIs are evaluated
  once, but still count individually toward that limit. All of a request's
  decisions share a 30-second budget, so a slow or unresponsive external
  authorizer cannot hold a request open for one timeout per URI; a request that
  exhausts the budget is denied.
- **Unresolvable references are denied.** A `completion/complete` whose `ref` is
  missing, malformed, of an unknown type, or carrying both a `name` and a `uri`
  is rejected, because no single capability can be established for it. It is
  never authorized against an empty resource ID. This includes the legacy
  bare-string form `"ref": "prompt-name"`, which does not say whether it names a
  prompt or a resource; clients must send the `{"type": ..., "name"|"uri": ...}`
  object form.
- **Unknown subscription fields are rejected.** A `subscriptions/listen` whose
  `notifications` object carries a member outside `toolsListChanged`,
  `promptsListChanged`, `resourcesListChanged` and `resourceSubscriptions` is
  refused, so a field this version does not understand cannot carry an
  unauthorized resource reference past the policy.
- **A JSON `null` where the schema expects an object or array is rejected.**
  `"notifications": null` and `"resourceSubscriptions": null` are both refused,
  because this proxy and the backend would each have to guess the same meaning
  for them. An omitted member and an empty array are not rejected — both
  unambiguously name no resource, and both are allowed through with no policy
  check.
- **Policies conditioned on `arg_*` do not match these methods.** Argument
  conditions describe the arguments the authorized operation runs with, and a
  completion or subscription request does not supply them. A rule like
  `when { context.arg_env == "dev" }` therefore denies these methods rather than
  matching them — grant them with a rule that does not test arguments.

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

#### Knowing where a claim came from

Claims do not always come from the same place. When `primaryUpstreamProvider` is
set, policies are normally evaluated against claims asserted by that upstream
IdP — but several paths fall back to the claims in the request's own token, and
until you check, `claim_email` looks identical either way.

Every request therefore carries a `thv_claim_source` attribute, on both the
principal and the context, naming the trust root behind the claims:

| Value | Meaning |
| --- | --- |
| `request` | No `primaryUpstreamProvider` is configured. Claims come from the request's token, and no provenance was ever demanded. |
| `upstream:<provider>` | Claims come from that upstream's access token, with any claim it omits filled from the same upstream's id_token (see [Claims from the upstream id_token](#claims-from-the-upstream-id_token)). |
| `request:no-upstream-session` | The validated embedded-auth-server token was proven to have been minted with no upstream login, so no upstream credential can exist for it. |
| `request:upstream-opaque` | An upstream credential exists but is an opaque OAuth 2.0 access token whose claims cannot be read, so evaluation degraded to the request token's claims. |

Where a fallback value comes from is covered under [What the fallback claims
actually are](#what-the-fallback-claims-actually-are) below.

A policy that must only act on what an upstream actually asserted can say so:

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"deploy") when {
  principal.thv_claim_source == "upstream:github" &&
  principal has claim_email &&
  principal.claim_email like "*@example.com"
};
```

The attribute is written after claim prefixing and its name does not start with
`claim_`, so a token carrying a claim named `thv_claim_source` becomes
`claim_thv_claim_source` and cannot spoof its own provenance.

##### Claims from the upstream id_token

On the `upstream:<provider>` path, any claim the upstream's access token omits is
filled from that same upstream's stored id_token. This includes group and role
claims under any name, `email_verified`, `acr`, `amr` and custom claims. The
ToolHive-issued token is never read on this path. This applies to every workload
that uses the embedded authorization server (`MCPServer`, `MCPRemoteProxy` and
`VirtualMCPServer`), and it cannot be turned off. The primary upstream is the
first configured one; only a `VirtualMCPServer` can name another with
`primaryUpstreamProvider`.

**ToolHive's client registration is authoritative for what the access token
omits.** Identity providers compute group and role claims per client or per
audience: Entra ID builds each token from its app registration's group and
app-role configuration, Okta configures group claims per authorization server
and app, Auth0 Actions set each token's claims separately, and Keycloak resolves
`realm_access`/`resource_access` through the requesting client's role scope
mappings. A group or role claim filled from the id_token is therefore the answer
for ToolHive's own client registration at the IdP, not the resource API's. When
the access token carries no group or role claim, configure group filters, app
roles and client scopes on that registration.

- **Fill-only**: a claim the access token asserts is used as-is, never replaced
  or unioned with the id_token's value.
- **Never filled**: claims that describe the id_token itself (`iss`, `aud`,
  `exp`, `nbf`, `iat`, `jti`, `nonce`, `at_hash`, `c_hash`, `s_hash`, `azp`,
  `sid`, `client_id`, `typ`, `token_use`, `uti`, `rh`, `idtyp`, `ver`, `cnf`,
  `appid`, `appidacr`, `azpacr`, `cid`, `gty`, `allowed-origins`,
  `session_state`, `xms_cc`, `acrs`), the delegated grant (`scope`, `scp`,
  `act`, `may_act`, `authorization_details`), and `sub`.
- **Group and role claims come from one token**: if the access token carries
  a list under any claim group extraction reads (`group_claim_name`,
  `role_claim_name` — `groupClaimName` and `roleClaimName` on the operator CRDs
  — or one of the defaults `groups`, `roles` and `cognito:groups`), the
  id_token fills none of them; a null or non-list value does not count. One
  decision therefore never combines the resource API's `groups` with the client
  registration's `roles`. A name that is not filled is also absent as a
  `claim_<name>` attribute.
- **Empty audience filters**: a provider that omits the group claim from the
  access token because a per-audience filter matched nothing will have the
  client registration's groups filled in. Apply the same filter to ToolHive's
  client registration, emit an empty claim, or keep groups out of the id_token.
- **Entra ID app roles**: the id_token's `roles` are the user's roles on
  ToolHive's auth-server client, and Entra omits `roles` from the access token
  when the user has none on the resource API. Assign app roles on the client
  registration accordingly, or use one registration for both.
- **Freshness**: the id_token is used even after it expires. An expired one
  triggers an upstream refresh, and Entra ID, Okta, Auth0 and Keycloak return a
  new id_token on refresh when `openid` was requested, so filled groups track
  the provider at about the id_token's lifetime. A provider that does not rotate
  it, or a refresh that fails, leaves the old one in place for as long as the
  session is kept alive; the refresh-token lifespan is renewed on every refresh,
  so it bounds only idle sessions.
- **Revoking a user's last group**: Keycloak (and apparently Entra ID) omit
  `groups` when the user has none instead of sending an empty list, so after
  the last group is revoked the access token carries no group claim and the
  id_token's list is consulted. Both return a new id_token on the same refresh,
  which omits `groups` too, so no stale list is filled. A stale list survives
  only when such a provider does not rotate the id_token or the refresh fails;
  keeping groups out of the id_token closes that case. Microsoft also advises against relying on an id_token returned by a
  refresh for authorization, so on Entra ID prefer group and role claims in the
  access token.

> **Upgrading: claims from the id_token**
>
> Releases v0.41.0 through v0.69.1 fill only `name` and `email` from the
> id_token. Later releases fill every claim not listed above, so decisions can
> change in both directions. Review `permit` and `forbid` rules before
> upgrading:
>
> - A `permit` or `forbid` keyed on a group or claim the id_token now supplies
>   can start matching.
> - A condition such as `!(principal has claim_X)` or an `unless` clause can
>   stop matching.
> - A claim that arrives with an unexpected type can make a policy error, which
>   denies the request.
> - With `group_claim_name` set and no group or role claim in the access token,
>   the id_token's list becomes the group set.

##### What the fallback claims actually are

Both labels mean evaluation fell back to the claims in the ToolHive-issued token
the client presented, but the two paths put different things in that token.

On `request:upstream-opaque` the token is an ordinary login-issued access token.
Its `name` and `email` are mirrored from the identity resolved at the **first**
upstream in the authorization chain, which need not be the provider you pinned.

On `request:no-upstream-session` the values depend on the grant. An RFC 8693
delegated token copies `name` and `email` from its subject token — which is the
first upstream's mirror when the subject token was ToolHive-issued, but is the
external IdP's own assertion when it came from a trusted external issuer. An RFC
7523 JWT-bearer token carries neither claim; see [What these tokens can and
cannot express](#what-these-tokens-can-and-cannot-express).

Where the mirror does apply, it matters only when the pinned provider is not the
first upstream. With a single upstream — or when `primaryUpstreamProvider`
resolves to the first one, which is the default — the mirror and the pinned
provider's own assertion are the same value, and only the provenance label
differs. They diverge in a multi-upstream chain pinned to a later upstream: a
policy keyed on `claim_email` alone may then match on an email the pinned
provider never asserted. Gate on `thv_claim_source` wherever that distinction
matters.

Pinning to a later upstream is only reachable through the operator, via
`spec.authServerConfig.primaryUpstreamProvider`. On the CLI path the provider is
always resolved to the first upstream, so the mirror and the pinned provider's
assertion cannot diverge there.

#### Tokens with no upstream login

Two grants mint a token that is not tied to any upstream IdP login, so no
upstream credential can ever exist for it:

- **RFC 8693 delegation** — the token carries an `act` claim naming the actor
  that obtained it on the subject's behalf.
- **RFC 7523 JWT-bearer** — the subject is a machine identity asserted by a
  separate trust root. ToolHive's authorization server stamps
  `https://toolhive.dev/no_upstream_session: true` on these tokens, because the
  grant has no standard marker of its own.

Under a pinned `primaryUpstreamProvider`, both are evaluated against their own
claims and labelled `request:no-upstream-session` only after the incoming token
has been validated for the configured embedded authorization-server issuer.

##### What these tokens can and cannot express

A session-less token carries at most `sub`, `act`, `client_id` and `scope`, plus
`name` and `email` when the grant supplies them (RFC 8693 delegation copies them
from the subject token; RFC 7523 JWT-bearer has no end user and carries neither).
A token redeemed from an ID-JAG that carries `authorization_details` also
carries the entries the authorization server granted: whole entries the
ID-JAG's issuer signed, possibly fewer of them. When ToolHive's authorization
server issues the ID-JAG, it signs the entries its client requested (see
[On ID-JAG minting](arch/17-token-exchange-delegation.md#on-id-jag-minting)).
An ID-JAG whose types are not registered is refused rather than redeemed (see
[Authorization details](arch/17-token-exchange-delegation.md#on-id-jag-redemption)).
Nothing else crosses. In particular **no upstream attribute is carried** — no
`groups`, no `roles`, no `department`, nothing the pinned IdP asserted about the
subject.

That bounds which policies work under delegation. Policies keyed on the identity
of the actor, the subject, or the delegated scope work:

```plain
permit(principal, action == Action::"call_tool", resource) when {
  principal.thv_claim_source == "request:no-upstream-session" &&
  principal.claim_act.sub == "my-delegate" &&
  principal.claim_scope like "*mcp:tools*"
};
```

For a SPIFFE client, `act.sub` is the workload's SPIFFE ID, including when the client exchanges a token without an `actor_token`. A policy that means "this client" should match `principal.claim_client_id` instead, because every workload that shares the client ID has the same `client_id` claim. See [Workloads that share a client ID](arch/18-spiffe-association-declarations.md).

Policies keyed on upstream attributes do not. A rule guarded on
`principal has claim_department` simply stops matching, so the request is denied
— the same outcome as before this fallback existed, reached at policy evaluation
instead of at claim resolution. If your policies depend on upstream attributes,
delegated tokens are not yet usable under a pinned provider. Carrying those
attributes across the exchange (claims transcription,
[draft-ietf-oauth-identity-chaining](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-chaining/)
§2.5) is tracked in
[#6511](https://github.com/stacklok/toolhive/issues/6511).

Two consequences worth planning around:

- **`forbid` rules over upstream attributes do not survive delegation.** Their
  `has` guard goes false when the attribute is absent, so the rule never fires
  and the restriction lifts. Express upstream restrictions as conditions on a
  `permit` rather than as a `forbid`, or require
  `principal.thv_claim_source == "upstream:<provider>"` in the `forbid` so it is
  clear the rule only governs directly-authenticated requests.
- **With more than one upstream provider, `claim_email` and `claim_name` on a
  delegated token may come from a provider other than the pinned one.** They are
  copied from the subject token, whose profile claims mirror the first configured
  upstream. This is not specific to delegation — it applies to every fallback
  path; see [What the fallback claims actually
  are](#what-the-fallback-claims-actually-are). Check `thv_claim_source` before
  treating them as an assertion by the pinned provider.

Everything else stays closed. A token that simply arrives without upstream
credentials and says nothing about why — an anonymous or local identity, or a
bearer token from an IdP other than the pinned one — is denied, as is a session
that holds credentials for other providers but none for the pinned one. A session
whose stored upstream credentials have all expired or been deleted never reaches
the authorizer: the authentication middleware answers it with 401 and a
`WWW-Authenticate` challenge so the client signs in again (see
[Middleware Architecture](middleware.md)).

**Upgrading:** JWT-bearer tokens minted by an authorization server running a
version before this marker existed carry no marker, so a pinned deployment
denies them until those tokens turn over. Delegated tokens are unaffected — the
`act` claim they already carry needs no new plumbing. Roll the authorization
server before pinning `primaryUpstreamProvider` on a deployment that uses the
JWT-bearer grant.

Deployments that scrape the authorizer's DEBUG logs should note that the
`source` field now uses the same vocabulary as `thv_claim_source`: the former
`token`, `token-fallback` and `upstream` values are now `request`,
`request:upstream-opaque` and `upstream:<provider>`, and the session-less path
that previously denied now logs `request:no-upstream-session`.

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

JSON numbers become Cedar `decimal` values, so compare them with the decimal
methods (`greaterThan`, `lessThanOrEqual`, and so on) and `decimal("...")`
literals. The `<`, `<=`, `>` and `>=` operators do not accept decimals and raise
an error on one. ToolHive reads every JSON number as a 64-bit floating-point
value first, so a number with more than about 15 significant digits may be
rounded, and the rules below apply to the rounded value.

A decimal holds at most four fractional digits and values up to about
+/-922337203685477.5. A number within that range and with at most four
fractional digits is converted exactly (the rounded value, see above). Any other
number is passed as a marker record instead of being left out. Prompt arguments
(`prompts/get`) on the proxy path get the same `arg_` treatment as tool
arguments. vMCP does not expose prompt arguments to policies.

A number marker equals nothing, not even another marker for the same number, so
`==` against it is false and `!=` is true. Compare numeric IDs as strings, not
as numbers. A too-deep or unsupported claim marker equals only a marker for an
identical value. `context has arg_n` is true for an out-of-range number, because the
argument is present. Typed operations on a marker are an evaluation error,
including decimal methods, `<`, `<=`, `>`, `>=`, `like`, boolean operators, set
methods called on the marker, `in`, `is`, and reading any attribute other than
the marker's own. `==`, `!=`, `has`, `set.contains(marker)` and reading the
marker's own attributes evaluate without error. ToolHive always denies a
request when a policy evaluation returns an error. A policy that never reads the
argument is unaffected, and a rule that does a typed operation on a marker
denies every request that the policy's scope matches and that sends it, including
requests that another policy would permit.

Cedar cannot test whether a value is a marker without raising an error on other
types: `context.arg_x has thv_unrepresentable` is a type error on a string, bool
or decimal. Treat a marker as a value that equals nothing and errors on every
typed operation, and write rules as positive permits. The shape of a marker (its
attribute names and contents) is not a stable interface.

An argument that is an array, an object or `null` is not available as
`arg_<name>`. Only `arg_<name>_present` is set, to `true`. A scalar argument
does not set `_present` (except one literally named `<name>_present`), so
`context has arg_x_present` normally means the call sent a non-scalar value.
Use it only as a deny signal, never as a permit condition.

A `forbid` guarded by `context has arg_x` is skipped for such a call, because
`context has arg_x` is false. Do not use a `forbid` on an argument value as your
only control. If a backend might coerce an array into a scalar (for example
`["rm"]` into `"rm"` or `[5000]` into 5000), add a rule that rejects non-scalar
values:

```plain
forbid(principal, action, resource) when { context has arg_x_present };
```

Prefer a `permit` that states the allowed values positively, for example
`permit(principal, action == Action::"call_tool", resource == Tool::"x") when { context has arg_x && context.arg_x == "safe" }`.
A non-scalar call does not match it, so it is denied.

A `forbid` that works only because an evaluation error denies the request looks
like a no-op in other Cedar tools (the CLI, policy analysis), which skip a policy
that errors. Guard with `has` and state the allowed values positively.

Numeric JWT claims follow the same rules for numbers. Maps and arrays in a claim may nest
11 levels deep, counting the claim value itself. A deeper map or array is passed
as a marker, and a top-level `null` claim is treated as absent. Claim arrays and
objects stay real Cedar sets and records. `null` elements in a claim array and
`null` values inside a claim object are dropped. `has` on a marker is false for any attribute other than the
marker's own, so a `has`-guarded rule on a claim path deeper than the nesting limit is still
skipped.

**Upgrading:** numeric values reach policies differently from earlier versions.
Check these:

- Any `permit` whose only condition on a numeric argument or claim is `has` or
  `!=`. An out-of-range number now matches it.
- Policies that compare arguments which legitimately carry more than four
  fractional digits (coordinates, prices). Rules that use decimal methods on
  them now deny those calls, and `==` rules stop matching (a `==` permit no
  longer allows, and a `==` forbid no longer fires). Remove the comparison or
  have the client round.
- Policies that use `arg_` and `claim_` attributes with decimal methods.
- Boundary values such as 0.57. Numbers with up to four fractional digits are now
  exact, where some were previously off by 0.0001 (0.57 arrived as 0.5699), so
  results at an exact boundary may flip in either direction.
- Claim values past the nesting limit, which are now markers instead of being
  dropped.
- Out-of-range or long-fraction numbers inside a claim array. They used to be
  dropped from the set (`[1e20, 5]` became `{5}`, and `[1e20]` became `{}`).
  They are now markers in the set, so `containsAll`, `isEmpty()` and `==` on that
  claim set can change result.

Array, object and `null` arguments, and `null` claim elements, are unchanged.

#### Combining JWT claims and tool arguments

You can combine JWT claims and tool arguments in your policies to create more sophisticated authorization rules:

```plain
permit(principal, action == Action::"call_tool", resource == Tool::"sensitive_data") when {
  principal.claim_roles.contains("data_analyst") &&
  resource.arg_data_level.lessThanOrEqual(principal.claim_clearance_level)
};
```

This policy allows clients with the "data_analyst" role to access the sensitive_data tool, but only if their clearance level (from JWT claims) is sufficient for the requested data level (from tool arguments). Both values must be JSON numbers with at most four fractional digits and within about +/-922337203685477.5, or the request is denied.

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
