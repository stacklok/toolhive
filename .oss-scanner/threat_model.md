# ToolHive OSS Scanner threat model

## Project and security goals

ToolHive runs and manages MCP (Model Context Protocol) servers. It ships a CLI
and API server (`thv`), a Kubernetes operator (`thv-operator`), a proxy runner
(`thv-proxyrunner`), an aggregating gateway (`vmcp`), and an embedded OAuth
authorization server (`pkg/authserver`, built on ory/fosite) that the proxy
runner and vMCP can host. MCP servers run in containers. Clients reach them through a ToolHive proxy that applies a
middleware chain: authentication, Cedar authorization, token exchange, and
audit.

ToolHive's primary security goals are:

- An MCP server, which runs third-party code, cannot exceed the network,
  filesystem, and host access granted by its permission profile.
- Only authenticated, authorized clients can invoke MCP methods through the
  proxy and vMCP, and authorization covers every method that reaches a backend.
- Each client session sees only its own responses and notifications.
- Secrets, OAuth tokens, and upstream credentials are not disclosed to MCP
  servers, other clients, logs, or other tenants beyond what is configured.
- Attacker-influenced URLs cannot make ToolHive send requests to internal,
  loopback, link-local, or cloud metadata addresses.
- In Kubernetes, a user who can create ToolHive custom resources in a namespace
  cannot use the operator to gain privileges they do not already hold.
- The embedded authorization server binds tokens to the authorized client or
  assertion-derived identity, with audiences in its `AllowedAudiences` and
  permitted scopes. Interactive authorization enforces required consent and
  browser binding; noninteractive grants enforce their configured authentication
  and authorization requirements. Codes and refresh tokens are single-use, and
  token exchange cannot widen the subject token's audience, scope, or remaining
  lifetime.
- Upstream tokens the authorization server stores for one user are never
  released to another user, client, or upstream provider.

## Untrusted inputs

Treat all of the following as adversarial:

- JSON-RPC requests, headers, session IDs, and bearer tokens sent by MCP
  clients to the proxy, the API server, and vMCP over SSE or streamable HTTP.
- Responses, notifications, and errors from MCP servers, including tool names,
  descriptions, schemas, and results. This covers containerized servers,
  remote MCP servers, and vMCP backends.
- Remote MCP server URLs, and the OAuth/OIDC discovery documents, protected
  resource metadata, and token responses they lead to.
- Registry data, container image references, and image metadata from
  registries that are not the configured trusted source.
- Skill and plugin sources (git repositories, archives, manifests) and the
  paths inside them.
- Imported RunConfig and workload export files.
- Custom resources (MCPServer, VirtualMCPServer, MCPRemoteProxy, and related
  kinds) created by namespace users who are not cluster administrators.
- Every request to the embedded authorization server: dynamic client
  registrations, client ID metadata document (CIMD) URLs and the documents they
  point to, client assertions such as SPIFFE JWT-SVIDs, token exchange subject
  and actor tokens, and requests to the upstream callback. Requests may arrive
  from untrusted callers before validation; successful operations may still
  require client authentication, grant authorization, or validated assertions.

The local user running `thv`, cluster administrators, the operator's own
configuration, Cedar policies written by the deployer, the container runtime
itself, and the Redis or Valkey servers that store authorization server state
are trusted.

## In scope

Focus on defects that break the goals above, especially:

- Authentication and Cedar authorization bypass in the proxy middleware and
  vMCP, including MCP methods or transports that skip policy evaluation, and
  claim or identity confusion between token sources.
- Token passthrough, confused-deputy, and audience/issuer validation mistakes
  in OIDC validation and token exchange.
- What ToolHive adds around fosite in the authorization server: storage
  adapters that break single-use codes and refresh tokens (fosite only detects
  reuse when storage returns `ErrInvalidatedAuthorizeCode` or
  `ErrInactiveToken`), `redirect_uri` rewriting and matching, consent and
  browser binding, dynamic registration and CIMD fetching, SPIFFE client
  authentication, and token exchange audience, scope, and actor checks.
- A token the authorization server did not issue, or a delegated token,
  carrying a `tsid` the proxy honors.
- Clients gaining unauthorized access to token exchange, including bypasses of
  registration gates, client authentication, or grant eligibility. Explicitly
  enabled `private_key_jwt` dynamic registration for token exchange is intended
  behavior.
- Session confusion: responses, notifications, or SSE events delivered to the
  wrong client, and session ID guessing or fixation.
- Container isolation: permission profile, mount, network isolation, and
  egress proxy bypasses across Docker, Podman, and Kubernetes runtimes.
- SSRF through remote URLs, registry and skill fetchers, OAuth discovery, and
  redirects, including private-address checks that miss IPv6 or encoded forms.
- Secret storage and handling: encryption, key derivation, and leakage through
  logs, errors, environment variables, or API responses.
- Operator reconciliation that turns custom resource fields into privileged
  pod specs, RBAC, or cross-namespace references.
- Path traversal and archive extraction issues in skills, plugins, and
  configuration import.
- Panics, unbounded memory or goroutine growth, and other denial of service
  reachable by an unauthenticated client or a malicious MCP server.

## Out of scope

- What an MCP server does within the permissions it was granted, such as
  exfiltrating data over network access the user allowed.
- Prompt injection carried in tool descriptions or results, unless ToolHive
  itself acts on that content in a way that breaks a goal above.
- Attacks that require control of the host, the container runtime socket, the
  user's ToolHive config directory, or cluster-admin rights.
- Behavior the user explicitly opted into through documented insecure flags or
  settings, and documented, intended behavior.
- Availability or compromise of an external identity provider, registry, or
  remote MCP server, when ToolHive validates its responses as documented.
- Bugs purely inside fosite, unless ToolHive's configuration or storage
  adapter triggers them.
- In the authorization server: unencrypted state in Redis, which the deployer
  protects, and SHA-256 hashing of client secrets the server generates.
- Replay of otherwise valid SPIFFE JWT-SVID client assertions during their
  acceptance window. Authentication limits remaining validity to six minutes;
  validation additionally permits one minute of expiration leeway.
- Operator-configured delegates exchanging otherwise valid self-issued subject
  tokens issued to another client. Bypasses of actor validation, explicit
  delegation restrictions, external-issuer policy, or audience, scope, and
  lifetime limits remain in scope.

## How to exercise it

The image has `thv`, `thv-operator`, `thv-proxyrunner`, and `vmcp` in
`/usr/local/bin`, and the source in `/src`. There is no container runtime or
Kubernetes cluster, so prefer Go tests that drive the code directly. The
proxy, middleware, vMCP, and authorization server packages under
`pkg/transport`, `pkg/auth`, `pkg/authz`, `pkg/vmcp`, and `pkg/authserver` have `httptest`-based tests that are a good model
for reproducers. The unit suite passes in this image: tests that need a
keyring, or that make a file unwritable to force an error (which root writes
through), skip themselves with the reason in their `SKIP` line.

## Severity

- **Critical**: an unauthenticated remote client gains code execution on the
  host, escapes an MCP server's container, invokes backend MCP methods with
  no authentication at all, or obtains another user's upstream tokens.
- **High**: an authenticated client bypasses Cedar authorization, reads
  another session's or tenant's responses, obtains secrets or tokens it was
  not granted, reuses an authorization code or refresh token, gets a code
  through a `redirect_uri` bypass, widens a token through token exchange, or
  reaches internal or cloud metadata addresses through ToolHive; a namespace
  user escalates privileges through the operator.
- **Medium**: the same impacts when they need a non-default but supported
  configuration (enabling the authorization server does not count), a
  malicious MCP server, or user interaction; unauthenticated denial of service
  that takes down the proxy, vMCP, or authorization server, or starves a
  shared rate limiter.
- **Low**: information leaks without credentials or user data, hardening gaps
  with no demonstrated exploit, denial of service that needs authentication,
  and making the authorization server send requests to public hosts.

## Report guidance

Report issues with a minimal, self-contained reproducer, preferably a Go test,
that names the affected ToolHive version or commit, the component (`thv`,
proxy, vMCP, operator, authorization server), and the security impact. Report one root cause per
finding, even when several entry points reach it. Proposed patches should be
minimal and follow the repository's existing patterns. Do not publicly disclose
candidate vulnerabilities.
