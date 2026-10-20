# ToolHive OSS Scanner threat model

## Project and security goals

ToolHive runs and manages MCP (Model Context Protocol) servers. It ships a CLI
and API server (`thv`), a Kubernetes operator (`thv-operator`), a proxy runner
(`thv-proxyrunner`), and an aggregating gateway (`vmcp`). MCP servers run in
containers. Clients reach them through a ToolHive proxy that applies a
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

The local user running `thv`, cluster administrators, the operator's own
configuration, Cedar policies written by the deployer, and the container
runtime itself are trusted.

## In scope

Focus on defects that break the goals above, especially:

- Authentication and Cedar authorization bypass in the proxy middleware and
  vMCP, including MCP methods or transports that skip policy evaluation, and
  claim or identity confusion between token sources.
- Token passthrough, confused-deputy, and audience/issuer validation mistakes
  in OIDC validation, token exchange, and the embedded authorization server.
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

## How to exercise it

The image has `thv`, `thv-operator`, `thv-proxyrunner`, and `vmcp` in
`/usr/local/bin`, and the source in `/src`. There is no container runtime or
Kubernetes cluster, so prefer Go tests that drive the code directly. The
proxy, middleware, and vMCP packages under `pkg/transport`, `pkg/auth`,
`pkg/authz`, and `pkg/vmcp` have `httptest`-based tests that are a good model
for reproducers. Unit tests in `pkg/api`, `pkg/client`, `pkg/secrets/keyring`,
`pkg/plugins/pluginsvc`, `pkg/skills/skillsvc`, and `cmd/thv/app` fail in this
image because there is no container runtime or keyring, and because the
image runs as root, so tests that make a file unwritable to force an error
see the write succeed. Those failures are expected.

## Severity

- **Critical**: an unauthenticated remote client gains code execution on the
  host, escapes an MCP server's container, or invokes backend MCP methods with
  no authentication at all.
- **High**: an authenticated client bypasses Cedar authorization, reads
  another session's or tenant's responses, obtains secrets or tokens it was
  not granted, or reaches internal or cloud metadata addresses through
  ToolHive; a namespace user escalates privileges through the operator.
- **Medium**: the same impacts when they need a non-default but supported
  configuration, a malicious MCP server, or user interaction; unauthenticated
  denial of service that takes down the proxy or vMCP.
- **Low**: information leaks without credentials or user data, hardening gaps
  with no demonstrated exploit, and denial of service that needs
  authentication.

## Report guidance

Report issues with a minimal, self-contained reproducer, preferably a Go test,
that names the affected ToolHive version or commit, the component (`thv`,
proxy, vMCP, operator), and the security impact. Report one root cause per
finding, even when several entry points reach it. Proposed patches should be
minimal and follow the repository's existing patterns. Do not publicly disclose
candidate vulnerabilities.
