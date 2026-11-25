# ToolHive Auth Server (`pkg/authserver`)

`pkg/authserver` is an embeddable Go OAuth 2.0 authorization server library
built on [Ory Fosite](https://github.com/ory/fosite). It provides a ready-to-use
server (the same one ToolHive's MCP proxy and vMCP run in production) alongside
the handlers, grant factories, upstream clients, and storage backends to
compose your own.

While any Go service that needs to issue OAuth 2.0 tokens, federate sign-in, or
broker delegated tokens can embed it, it is built specifically for services
that sit between AI agents (or MCP clients) and the systems they act on.
Workload identity systems such as SPIFFE answer *who is this agent?*;
`pkg/authserver` answers *what may it do, and for whom?*—accepting workload
identities or signed client assertions, issuing short-lived, resource-bound
tokens that name both the user and the acting agent, keeping upstream
credentials on the server side, and bridging trust domains with Cross-App
Access (ID-JAG).

Although it lives in the ToolHive repository, it has no code dependency on MCP
or ToolHive's proxy runtime. Today it still imports several shared utility
packages inside the ToolHive Go module; making it fully self-contained and
eventually extracting it into its own repository is an active goal.

## Common challenges and how `pkg/authserver` helps

### Keep upstream credentials out of agents
Upstream GitHub, Google, or corporate IdP tokens are broad and long-lived;
placing them in an agent exposes them to memory or log leaks, and MCP forbids
passing client tokens through to downstream APIs. `pkg/authserver` keeps
upstream access, refresh, and ID tokens in server-side storage and issues its
own audience-scoped JWTs (1h default, configurable). Your host service reads,
refreshes, and swaps in the upstream credential at the last hop, and a built-in
refresh guard blocks downstream refreshes when the upstream session is dead.
*Without it:* a per-user token vault, refresh coordination, dead-session
detection, and OAuth client wiring per upstream.

### Narrow tokens to a resource—or a single action
Coarse scopes (`repo:write`) rarely match agent plans that emerge at run time.
Every token issued here is audience-bound (RFC 8707), and RFC 9396 Rich
Authorization Requests (RAR) hooks let you validate structured permissions on
token exchange and carry registered, issuer-signed details from an ID-JAG into
the resulting access token (with per-client lifespans down to 2 seconds, and
re-exchange blocked on tokens carrying details). ToolHive's vMCP builds on
these hooks to enforce single-call, single-use MCP tool tokens.
*Without it:* broad scope strings and long-lived tokens valid for any call.

### Preserve both user and agent identity
When an agent replays a user's token, audit logs and policy see only the user.
`pkg/authserver` implements RFC 8693 token exchange keeping the user in `sub`,
the calling client in `client_id`, and the acting party—including nested hops
and verified SPIFFE IDs—in `act`, with delegation consent checks and scope or
audience narrowing. Autonomous workload tokens (`client_credentials`) put the
workload's SPIFFE ID in `sub` with no `act`, keeping autonomous and delegated
actions distinct, and opt-in RFC 9493 `sub_id` carries the user's verified
upstream identity.
*Without it:* a custom delegation token format, chain tracking, and narrowing
rules.

### Cross trust domains without a consent screen per tool
Per-tool consent does not scale across dozens of tools, and enterprise admins
want their IdP to govern cross-app access. `pkg/authserver` supports both sides
of Cross-App Access / ID-JAG (`draft-ietf-oauth-identity-assertion-authz-grant`):
**issuing** short-lived assertions from opt-in non-rotating exchange grants (so
long-running agents can mint short-lived tokens on demand without re-prompting
the user), and **redeeming** trusted issuers' ID-JAGs via RFC 7523 JWT-bearer
with `client_id` binding, single-use `jti`, resource coverage, and subject
bindings.
*Without it:* an assertion issuer, redeemer, trust allowlist, grant store, and
replay cache.

### Authenticate agents without static secrets
Static client secrets and API keys leak through environment variables and logs.
Alongside `private_key_jwt`, `pkg/authserver` implements the `spiffe_jwt` and
`spiffe_x509` client authentication methods from
`draft-ietf-oauth-spiffe-client-auth` (with documented differences) against a
supplied trust bundle, mapping SPIFFE IDs or `/*` patterns to OAuth clients
with explicit scopes and resources. Workload `client_credentials` tokens carry
the SPIFFE ID as `sub` and never outlive the SVID; delegated exchanges bind
`act.sub` to the verified SPIFFE ID so siblings under a wildcard cannot
exchange each other's tokens.
*Without it:* static secret rotation, per-agent client registration, and custom
workload-to-token binding.

### Put an MCP-ready OAuth server in front of your existing IdP
MCP clients expect RFC 8414 discovery, Client ID Metadata Documents (CIMD) or
Dynamic Client Registration (DCR), PKCE S256, and RFC 8707 resource indicators,
which most corporate IdPs do not expose to unregistered clients.
`pkg/authserver` sits in front of one or more upstream OIDC or OAuth 2.0
providers, handling discovery, CIMD, DCR, PKCE, and consent out of the box.
*Without it:* a discovery endpoint, a CIMD fetcher with SSRF guards,
registration storage, and an OIDC relying party for your IdP.

## Capabilities at a glance

- **Ready-to-use server with composable building blocks.** Out of the box you
  get the OAuth and discovery HTTP endpoints, Fosite grant wiring, upstream
  OAuth/OIDC clients, consent UI (with optional remembered consent and
  consent-exempt clients), and storage backends. Mount the complete server on an
  existing HTTP router, or wire individual subpackages and extension hooks when
  you need custom behavior.
- **No password database to run.** Interactive sign-in delegates to one or more
  upstream OIDC or OAuth 2.0 providers. For non-interactive workloads, the
  server can also run in **token-only** mode with no upstream IdP configured,
  and supports opt-in RFC 8628 device flow for headless clients.
- **Decorator-friendly storage.** In-memory and Redis backends share a single
  storage interface, so applications can wrap them to add encryption at rest,
  multi-tenant isolation, external directory resolution, or custom admission
  rules without modifying OAuth handlers.

## Quick start

```go
import (
	"context"
	"net/http"
	"os"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func mountAuthServer(ctx context.Context, mux *http.ServeMux) (func() error, error) {
	stor := storage.NewMemoryStorage()

	srv, err := authserver.New(ctx, authserver.Config{
		Issuer:           "https://auth.example.com",
		AllowedAudiences: []string{"https://api.example.com"},
		Upstreams: []authserver.UpstreamConfig{
			{
				Name: "corp",
				Type: authserver.UpstreamProviderTypeOIDC,
				OIDCConfig: &upstream.OIDCConfig{
					CommonOAuthConfig: upstream.CommonOAuthConfig{
						ClientID:     os.Getenv("UPSTREAM_CLIENT_ID"),
						ClientSecret: os.Getenv("UPSTREAM_CLIENT_SECRET"),
						RedirectURI:  "https://auth.example.com/oauth/callback",
						Scopes:       []string{"openid", "profile", "email"},
					},
					Issuer: "https://idp.example.com",
				},
			},
		},
	}, stor)
	if err != nil {
		_ = stor.Close()
		return nil, err
	}

	// Mount OAuth and discovery endpoints explicitly so sibling well-known
	// routes (such as /.well-known/oauth-protected-resource) stay reachable.
	h := srv.Handler()
	mux.Handle("/oauth/", h)
	mux.Handle("/.well-known/oauth-authorization-server", h)
	mux.Handle("/.well-known/oauth-authorization-server/", h)
	mux.Handle("/.well-known/openid-configuration", h)
	mux.Handle("/.well-known/openid-configuration/", h)
	mux.Handle("/.well-known/jwks.json", h)

	// Read and refresh stored upstream tokens in your application middleware:
	//   tokens, err := srv.IDPTokenStorage().GetUpstreamTokens(ctx, sessionID, "corp")
	//   refresher := srv.UpstreamTokenRefresher()

	return func() error {
		err := srv.Close()
		_ = stor.Close()
		return err
	}, nil
}
```

For YAML/JSON-driven deployments that resolve secrets from files or environment
variables, use `runner.NewEmbeddedAuthServer` (or
`runner.NewEmbeddedAuthServerWithStorage`) with an `authserver.RunConfig`.

## Architecture and components

### Server entry points and HTTP handlers

Two entry points assemble a complete server:

- [`authserver.New`](server.go) takes a resolved Go [`Config`](config.go) and a
  `storage.Storage`, returning a `Server` whose `Handler()` serves all OAuth
  and discovery routes. Use this when wiring dependencies in Go or plugging in
  Go extension hooks (upstream filtering and factories, custom refresh-grant
  validators, actor-token validators, RAR authorization-details policies, or
  storage decorators).
- [`runner.NewEmbeddedAuthServer`](runner/embeddedauthserver.go) (and
  `NewEmbeddedAuthServerWithStorage`) takes a serializable `RunConfig` whose
  secrets are referenced by file path or environment variable, resolves them at
  startup, and exposes route registration and an optional TLS listener.

If you prefer to assemble the HTTP surface yourself, construct a
`handlers.Handler` (`pkg/authserver/server/handlers`) and register its
`OAuthRoutes` and `WellKnownRoutes` directly on a `chi.Router`.

### Storage and upstream credential custody

All protocol state lives behind `storage.Storage`: authorization codes, access
and refresh token sessions, PKCE challenges, client registrations, pending
authorizations, internal users, remembered consent, exchange grants, and
upstream IdP credentials.

Two backends are included:

- **Memory** (`storage.NewMemoryStorage`): zero-dependency in-process store for
  local development, tests, and single-instance processes.
- **Redis** (`storage.NewRedisStorage`): distributed store for multi-replica
  deployments and restart continuity, supporting **standalone**, **Cluster**,
  and **Sentinel** topologies.

Downstream clients only hold JWTs minted by your authorization server; the host
process retrieves and refreshes the corresponding upstream credentials through
`Server.IDPTokenStorage()` and `Server.UpstreamTokenRefresher()`.

Because `storage.Storage` is an interface, embedders commonly wrap
`MemoryStorage` or `RedisStorage` with decorators for encryption at rest, tenant
scoping, or external directory resolution; decorators must expose
`Unwrap() storage.Storage`, and custom backends must implement the optional
capability interfaces (such as `DCRCredentialStore`) for the features they
enable. See the [storage architecture](../../docs/arch/11-auth-server-storage.md)
for backend and lifecycle details.

### Upstream identity providers

For interactive flows, the server acts as an OAuth 2.0 or OIDC client of one or
more upstream providers:

- Supports both **OIDC** (discovery, ID token and nonce validation) and plain
  **OAuth 2.0** (explicit endpoints, userinfo or token-response identity
  extraction), plus optional upstream Dynamic Client Registration.
- Supports **multi-upstream chains**, walking configured providers in order and
  storing each provider's tokens under the same user session.
- Accepts an optional `UpstreamFilter` so the host application can narrow which
  secondary upstreams a user authorizes once the first login resolves.
- Includes a built-in consent page, with optional remembered consent and
  pre-authorized consent-exempt clients (for pre-registered clients, not
  dynamically registered ones).

### Keys and tokens

Access tokens are asymmetrically signed JWTs whose public keys are published at
`/.well-known/jwks.json`, so resource servers can validate them offline.
Authorization codes and refresh tokens are opaque HMAC-signed tokens.

Ephemeral signing keys and HMAC secrets are generated automatically for local
development. Persistent signing keys and HMAC secrets both support key rotation
so restarts and horizontal scaling do not invalidate issued tokens.

### Delegation and Cross-App Access (ID-JAG)

`pkg/authserver` supports RFC 8693 token exchange, RFC 7523 JWT-bearer grants,
and both sides of **ID-JAG**
([`draft-ietf-oauth-identity-assertion-authz-grant`](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/))
as independent opt-in roles:

- **Issuing:** Configured delegate clients exchange a login access token for an
  exchange grant, then mint short-lived `oauth-id-jag+jwt` assertions for
  allowlisted target issuers and resources. Go embedders can opt into verified
  upstream subject claims (`sub_id`) and register an `ActorTokenValidator` hook
  to validate an `actor_token` and populate `act`.
- **Redeeming:** Trusted issuers' `oauth-id-jag+jwt` assertions are redeemed at
  `/oauth/token` via RFC 7523 JWT-bearer into local resource-scoped access
  tokens (with per-client lifespans down to 2s and optional registered
  `authorization_details`), enforcing `client_id` matching, single-use `jti`,
  `resource` coverage, and configured subject bindings while preserving `act`.

ID-JAG support is a partial draft implementation: sender-constrained ID-JAGs
(DPoP/`cnf`) are not supported and are rejected at both issuance and redemption.
See [external subject-token exchange & delegation](../../docs/arch/17-token-exchange-delegation.md)
for details.

### SPIFFE workload authentication

Workloads can authenticate without static OAuth secrets using **JWT-SVIDs**
(`spiffe_jwt`) or mTLS **X.509-SVIDs** (`spiffe_x509`) verified against a
supplied trust bundle. Trust-domain associations map exact SPIFFE IDs or `/*`
descendant patterns to explicit OAuth client IDs with per-association scopes,
RFC 8707 resources, and grant types (`client_credentials`, RFC 8693 token
exchange, or both). See
[SPIFFE association declarations](../../docs/arch/18-spiffe-association-declarations.md)
for details.

### Package layout

Supporting packages (`storage`, `upstream`, `upstreamfilter`,
`authorizationdetails`) can be imported independently. The grant packages under
`server/` are Fosite grant factories wired through `pkg/authserver/server`, so
you can compose a custom subset of grants on top of Fosite, though they are not
framework-agnostic standalone handlers.

| Package | Role |
| --- | --- |
| `pkg/authserver` | Ready-to-use server (`authserver.New`), `Config`, and `RunConfig`. |
| `pkg/authserver/runner` | File/env-driven `EmbeddedAuthServer`, secret resolution, and optional TLS listener. |
| `pkg/authserver/storage` | `Storage` interface, `MemoryStorage`, `RedisStorage`, and built-in decorators (CIMD, SPIFFE, client admission, user-keyed upstream tokens). |
| `pkg/authserver/upstream` | OIDC and OAuth 2.0 upstream provider clients and token refresh. |
| `pkg/authserver/upstreamfilter` | `UpstreamFilter` interface and group-based multi-upstream chain filter. |
| `pkg/authserver/authorizationdetails` | RFC 9396 `authorization_details` parser and policy hook registry. |
| `pkg/authserver/server` | Fosite provider wiring, JWT signing (`server/keys`, `server/crypto`), client registration (`server/registration`), and HTTP handlers (`server/handlers`). |
| `pkg/authserver/server/tokenexchange` | Fosite grant factories for RFC 8693 token exchange, RFC 7523 JWT-bearer, and ID-JAG issuance/redemption. |
| `pkg/authserver/server/clientcredentials` | Fosite grant factory for SPIFFE `client_credentials`. |
| `pkg/authserver/server/deviceflow` | Fosite grant factory for RFC 8628 device authorization. |

## How applications use it

- **ToolHive proxy & vMCP:** Embeds the authorization server alongside the MCP
  proxy to issue short-lived JWTs to clients and swap in stored upstream IdP
  credentials when forwarding requests to backends.
- **Multi-tenant connector gateway (Stacklok Enterprise Platform):** Embeds
  `pkg/authserver` over decorated Redis storage to handle multi-tenant sign-in
  and scope each user's authorization flow to their enabled connectors.
- **Agent identity broker (mecatl):** Embeds `pkg/authserver` both to issue
  delegated tokens and ID-JAGs for agents and to custody upstream OAuth
  credentials for protected downstream tools.

## Supported flows and standards

The tables below summarize what the ready-to-use server (`authserver.New`)
implements. References identify the implemented features rather than claiming
full conformance with each specification.

### OAuth 2.0 grants and flows

| Flow | Spec | Status in `pkg/authserver` |
| --- | --- | --- |
| Authorization Code + PKCE (S256) | [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html), [RFC 7636](https://www.rfc-editor.org/rfc/rfc7636.html) | Enabled in interactive mode (public and confidential clients; PKCE S256 enforced). |
| Refresh Token | [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html) | Enabled in interactive mode; login refresh tokens rotate. |
| Device Authorization | [RFC 8628](https://www.rfc-editor.org/rfc/rfc8628.html) | Opt-in; requires a single upstream (multi-upstream deployments fail at verification). |
| Client Credentials | [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html) | Opt-in for SPIFFE-authenticated workloads only (bound to the workload's SPIFFE ID and SVID lifetime). |
| Token Exchange | [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693.html) | Delegation for authenticated clients (delegate clients, opt-in `private_key_jwt` DCR clients, or SPIFFE workloads), using self-issued or trusted external subject tokens, plus opt-in non-rotating exchange-grant refresh tokens. |
| JWT-Bearer Authorization Grant | [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html) | Opt-in per trusted issuer; supports plain assertions and bound ID-JAG redemption. |
| Identity Assertion JWT Authorization Grant (ID-JAG / Cross-App Access) | [Internet-Draft](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/) | Opt-in, partial draft implementation: issuing from exchange grants and redeeming via JWT-bearer. Sender-constrained ID-JAGs (`cnf`/DPoP) are not supported; see [delegation docs](../../docs/arch/17-token-exchange-delegation.md#bound-id-jag-assertions). |

Implicit and Resource Owner Password grants are not registered by the
ready-to-use server. Public token introspection, revocation, and userinfo HTTP
endpoints are not exposed.

### Onboarding, metadata, tokens, and client auth

| Capability | Spec | Notes |
| --- | --- | --- |
| Client ID Metadata Documents (CIMD) | [Internet-Draft](https://datatracker.ietf.org/doc/draft-ietf-oauth-client-id-metadata-document/) | Opt-in, partial draft support for URL-based public clients (favored by MCP). |
| Dynamic Client Registration (DCR) | [RFC 7591](https://www.rfc-editor.org/rfc/rfc7591.html) | Public clients by default; confidential and `private_key_jwt` opt-in. Unauthenticated and rate-limited. |
| Authorization Server Metadata & JWKS | [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414.html), [RFC 7517](https://www.rfc-editor.org/rfc/rfc7517.html) | Also serves `/.well-known/openid-configuration` for client compatibility; does not issue OIDC ID tokens. |
| Resource Indicators | [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707.html) | Audience binding against configured `AllowedAudiences` (or ID-JAG target allowlists). |
| JWT Access Tokens | [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519.html) ([RFC 9068](https://www.rfc-editor.org/rfc/rfc9068.html) `at+jwt` opt-in) | Asymmetric JWTs (`typ: JWT` by default; Go embedders may opt into `typ: at+jwt`). |
| Rich Authorization Requests (RAR) | [RFC 9396](https://www.rfc-editor.org/rfc/rfc9396.html) | Go policy hooks for `authorization_details` on token exchange, carrying registered details from ID-JAGs into redeemed access tokens, plus built-in exchange-grant details. |
| Subject Identifiers | [RFC 9493](https://www.rfc-editor.org/rfc/rfc9493.html) | Opt-in `sub_id` claim carrying verified upstream OIDC identity on login-derived tokens. |
| `private_key_jwt` Client Auth | [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html) | Supported for configured delegate clients and opt-in token-exchange DCR clients. |
| SPIFFE Workload Client Auth | [Internet-Draft](https://datatracker.ietf.org/doc/draft-ietf-oauth-spiffe-client-auth/) (JWT-SVID / X.509-SVID) | Opt-in `spiffe_jwt` and `spiffe_x509` methods (with documented differences) for `client_credentials` and token exchange. |
| Upstream OAuth 2.0 / OIDC Client | [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html), [OIDC Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) | Server acts as the OAuth/OIDC client when federating login to upstream IdPs. |

The optional ID-JAG actor validation hook is inspired by the individual draft
[`draft-mcguinness-oauth-actor-profile`](https://datatracker.ietf.org/doc/draft-mcguinness-oauth-actor-profile/).
All Internet-Drafts listed above are works in progress, not RFCs.

## HTTP endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /oauth/authorize` | Start an interactive authorization request. |
| `POST /oauth/consent` | Submit the user consent decision. |
| `GET /oauth/callback` | Handle the upstream IdP callback. |
| `POST /oauth/token` | Issue tokens for enabled grants. |
| `POST /oauth/register` | Dynamic client registration. |
| `POST /oauth/device_authorization`, `GET/POST /oauth/device`, `POST /oauth/device/confirm` | Device authorization and browser verification (when enabled). |
| `GET /.well-known/oauth-authorization-server` | OAuth authorization-server metadata (plus path-based issuer variants). |
| `GET /.well-known/openid-configuration` | Discovery metadata alias (plus path-based issuer variants). |
| `GET /.well-known/jwks.json` | Public JWT signing keys. |

## Further reading

- [Auth server storage architecture](../../docs/arch/11-auth-server-storage.md)
- [External subject-token exchange & delegation](../../docs/arch/17-token-exchange-delegation.md)
- [SPIFFE association declarations](../../docs/arch/18-spiffe-association-declarations.md)
