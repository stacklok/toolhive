# vMCP Auth Server Integration — Design

## Problem Statement

vMCP aggregates multiple backend MCP servers behind a single endpoint. Each backend may require authentication with a different upstream Identity Provider (IDP) — one needs a GitHub token, another needs an Atlassian token, a third needs a static API key.

Today, vMCP has a two-boundary auth model (incoming client auth, outgoing backend auth) with strategies that cover static credentials and token exchange using the incoming JWT. What's missing is the ability to obtain, store, and inject **upstream IDP tokens** — tokens the user acquired by authenticating with external providers through an embedded OAuth authorization server.

This design covers the end-to-end integration of the embedded auth server into vMCP.

## Architectural Invariants

These are non-negotiable constraints that all sub-designs must respect:

1. **Multi-upstream from day one.** vMCP fronts multiple backends, each potentially requiring tokens from different upstream IDPs. A single user session must be able to accumulate tokens from multiple providers. We cannot design for single-provider and retrofit later.

2. **Incoming/outgoing boundary is preserved.** vMCP has a clear dichotomy: incoming auth (client → vMCP) and outgoing auth (vMCP → backends). One upstream provider may serve as the "front door" (incoming auth via the auth server), while others are obtained via step-up. The design must not collapse these boundaries.

3. **Existing auth schemes must not break.** Bearer token passthrough, token exchange (RFC 8693), header injection, and anonymous access all continue to work unchanged. The auth server is additive, not a replacement.

4. **TSID and vMCP session are independent.** Two session concepts coexist in different layers:
   - The **TSID** (token session ID, JWT `tsid` claim) is a durable handle for upstream token lookup, keyed by `(TSID, providerName)`. Its lifecycle is the OAuth refresh chain (hours to days). Created by the auth server (`pkg/authserver/`), consumed solely by the upstream swap middleware (`pkg/auth/upstreamswap/`). Nothing in `pkg/vmcp/` reads the TSID.
   - The **vMCP session** (`Mcp-Session-Id` header) is a transport/routing construct holding the routing table and persistent backend connections. Its lifecycle is per-MCP-client-connection (TTL-based). It lives in `pkg/vmcp/session/`.

   One TSID may serve multiple concurrent vMCP sessions (multiple clients sharing the same JWT). One vMCP session may have zero TSIDs (anonymous mode). The constructs share no storage or state. They coexist in the HTTP request pipeline but do not interact: the upstream swap middleware reads the TSID from `Identity.Claims` to inject upstream tokens; the vMCP session adapter reads the `Mcp-Session-Id` header to resolve routing.

5. **Auth server is optional.** vMCP must function fully without the auth server — all existing flows (OIDC validation, token exchange, header injection, anonymous) work as before. When the auth server is present, new capabilities become available. No silent fallback; explicit errors when auth server features are configured but the auth server is absent.

6. **Upstream tokens are user-bound.** Tokens obtained from upstream IDPs are bound to `(UserID, ClientID)` at storage time. Retrieval must validate these binding fields — a correct TSID with mismatched binding fields must fail with `ErrInvalidBinding`. This prevents cross-session token access even if a TSID is guessed or leaked.

7. **At most one step-up flow per (TSID, provider).** When multiple concurrent requests discover a missing upstream token for the same user and provider, at most one step-up auth flow may be triggered. Concurrent requests must either wait or fail clearly — never trigger duplicate OAuth flows.

8. **No upstream tokens in logs or errors.** Upstream access tokens, refresh tokens, and ID tokens must never appear in log messages, error strings, or HTTP response bodies. The `UpstreamInjectStrategy` handles raw third-party tokens and must treat them with the same care as credentials.

## Key Concepts

**TSID (Token Session ID)**: A random string generated during OAuth callback (`rand.Text()` in `pkg/authserver/server/handlers/callback.go`) and embedded in the JWT `tsid` claim. Serves as the lookup key for upstream IDP tokens in `UpstreamTokenStorage`. Survives JWT refresh (fosite preserves claims across the refresh chain). Created by the auth server; consumed solely by the upstream swap middleware (`pkg/auth/upstreamswap/middleware.go`). Has no knowledge of routing, backends, or capabilities.

**vMCP Session**: A transport/routing construct created when an MCP client sends `initialize` without a `Mcp-Session-Id` header. Identified by a UUID returned in the `Mcp-Session-Id` response header. Contains: routing table (capability name → backend mapping), persistent backend MCP client connections, resolved capability lists. Managed by `session.Manager` with TTL-based cleanup. Has no knowledge of authentication, tokens, or upstream providers. See `pkg/vmcp/session/session.go`.

**Backend Session**: A persistent MCP client connection from vMCP to a single backend MCP server. Each vMCP session may hold multiple backend sessions (one per backend workload). The backend assigns its own session ID. See `pkg/vmcp/session/internal/backend/`.

**Upstream Tokens**: Tokens obtained from an external IDP (GitHub, Atlassian, etc.) stored by the auth server. Currently keyed by `sessionID` (the TSID); target design keys by `(TSID, providerName)`. Retrieved by the upstream swap middleware and injected into outgoing requests. See `pkg/authserver/storage/types.go`.

## Reference Scenario

All sub-designs use this concrete scenario for illustration:

```
Embedded Auth Server (runs in-process in vMCP):
  Upstream Provider: "corporate-idp"  (primary auth — the "front door")
  Upstream Provider: "github"         (step-up — obtained on demand)
  Upstream Provider: "atlassian"      (step-up — obtained on demand)

Backend 1 "github-tools":     upstream_inject  provider="github"        → inject GitHub token directly
Backend 2 "atlassian-tools":  upstream_inject  provider="atlassian"     → inject Atlassian token directly
Backend 3 "corporate-api":    token_exchange   provider="corporate-idp" → exchange corporate token via RFC 8693
Backend 4 "internal-api":     header_injection                          → static API key (no auth server)
```

This scenario exercises:
- Two auth-server-backed backends with different step-up upstream IDPs (Backends 1, 2)
- The front-door provider's token used as RFC 8693 `subject_token` for token exchange (Backend 3 — exercises UC-02 double-duty)
- A backend entirely independent of the auth server (Backend 4)

## Two-Boundary Auth Model

```
                          ┌──────────────────────────────────────────┐
                          │              vMCP Server                  │
                          │                                          │
  Client ──── Boundary 1 ─┤  Incoming Auth                           │
  (TH-JWT)   (incoming)   │  ├─ anonymous                            │
                          │  ├─ oidc (validate TH-JWT)               │
                          │  └─ local                                │
                          │                                          │
                          │  ┌─ Embedded Auth Server (optional) ──┐  │
                          │  │  /oauth/authorize, /callback, /token│  │
                          │  │  /.well-known/openid-configuration  │  │
                          │  │  /.well-known/jwks.json             │  │
                          │  │                                     │  │
                          │  │  Upstream Providers:                │  │
                          │  │  ├─ "corporate-idp" (front door)    │  │
                          │  │  ├─ "github" (step-up)              │  │
                          │  │  └─ "atlassian" (step-up)           │  │
                          │  │                                     │  │
                          │  │  Upstream Token Storage (target):   │  │
                          │  │  (TSID, providerName) → tokens      │  │
                          │  │  (current: TSID only, single-provider)│ │
                          │  └─────────────────────────────────────┘  │
                          │                                          │
                          │  Token Session (TSID — in JWT claim)     │
                          │  └─ upstream token lookup key             │
                          │                                          │
                          │  vMCP Session (Mcp-Session-Id header)    │
                          │  ├─ routing table (tool → backend)       │
                          │  └─ backend connections[]                │
                          │                                          │
             Boundary 2 ──┤  Outgoing Auth                           │
             (outgoing)   │  ├─ unauthenticated                      │
                          │  ├─ header_injection        ── internal-api
                          │  ├─ token_exchange+upstream ── corporate-api
                          │  ├─ upstream_inject (NEW)   ── github-tools
                          │  └─ upstream_inject (NEW)   ── atlassian-tools
                          └──────────────────────────────────────────┘
```

## Established Truths

These are findings from codebase analysis that are confirmed and shape the design.

### Incoming auth works with the AS out of the box

The OIDC middleware validates AS-issued tokens via standard OIDC discovery — the same pattern already used in the proxy runner (`pkg/runner/runner.go`). The AS exposes `/.well-known/openid-configuration` and `/.well-known/jwks.json`; the middleware does lazy discovery with retry on first validation. No bootstrapping problem exists because Go's concurrent HTTP server handles the loopback request. The AS endpoints (`/oauth/*`) are unauthenticated — they are the OAuth flow itself.

**Implication**: The incoming auth configuration just points its `issuer` at the AS. No bypass, no special-casing. The AS is a standard OIDC provider from the middleware's perspective.

### Scopes are the upstream provider selector, not `resource`

RFC 8707 `resource` identifies the **protected resource** (where the token will be sent), not which upstream IDP to authenticate with. Using `resource=https://api.github.com` to mean "use GitHub as the upstream" is a semantic mismatch.

Instead, **scopes** (e.g., `upstream:github`) signal which upstream provider the client should authenticate with. This aligns with the MCP specification's step-up authorization pattern:

```http
HTTP/1.1 403 Forbidden
WWW-Authenticate: Bearer error="insufficient_scope",
                         scope="upstream:github"
```

The auth server maps scope values to upstream provider names and routes the authorization flow accordingly.

### Step-up auth: two spec-blessed mechanisms

When a backend needs an upstream token the user hasn't obtained yet, vMCP must signal the client to perform step-up authentication. The MCP spec provides two mechanisms:

**Mechanism 1: HTTP 403 + `insufficient_scope` (Scope Challenge Handling)**

The MCP spec defines a step-up authorization flow: the server returns HTTP 403 with `WWW-Authenticate: Bearer error="insufficient_scope", scope="upstream:github"`. The client re-authorizes with the expanded scope, the auth server redirects to GitHub, stores the GitHub token under the same TSID, and issues a new JWT (same TSID). The client retries with the new JWT and succeeds. The `Mcp-Session-Id` survives — only HTTP 404 terminates a session.

- Broad client support (part of MCP auth, which all auth-capable clients implement)
- Requires a new JWT to be issued (OAuth mechanics), but TSID is preserved
- Implementation challenge: the token-missing discovery happens inside the MCP SDK's tool handler. Returning HTTP 403 requires either a pre-flight middleware (parse JSON-RPC body before SDK dispatch) or an SDK change to support HTTP-level errors from handlers.

**Mechanism 2: URL mode elicitation (`URLElicitationRequiredError`, code -32042)**

The MCP 2025-11-25 spec introduced URL elicitation specifically for "MCP server needs third-party OAuth tokens." The tool handler returns a -32042 JSON-RPC error with the auth URL. The client opens the URL, user completes OAuth, client retries with the same JWT. No new JWT needed — the server-side state changes (upstream token stored under existing TSID).

- Semantically precise (designed for exactly this scenario)
- Works inside JSON-RPC framing (no pre-flight middleware needed)
- Client support is optional (`elicitation: { url: {} }` is a MAY capability)
- SDK gap: mcp-go's `handleToolCall` wraps all handler errors with -32603 (INTERNAL_ERROR), not checking for `URLElicitationRequiredError`. Requires a ~5-line SDK fix.

**Trade-offs**: 403 has broader client support but slightly strained semantics (the client's token is valid — it's server-side state that's missing). -32042 is semantically exact but optional for clients. Both converge in one retry. The decision between them (or using both with capability-based selection) is deferred to UC-06.

### Enabling the AS changes token semantics for outgoing auth

When the AS is disabled, `identity.Token` holds whatever JWT the client sent (e.g., a corporate IDP token). The existing `token_exchange` strategy uses `identity.Token` directly as the RFC 8693 `subject_token`.

When the AS is enabled, `identity.Token` is always a **TH-JWT** (issued by the embedded AS). The original upstream IDP token is stored in upstream token storage under `(TSID, providerName)`. This is not a problem — it's the architecture:

- **`upstream_inject`** fetches the upstream token by explicit provider name via `UpstreamTokenSource` and injects it directly. No involvement of `identity.Token`.
- **`token_exchange`** automatically uses the front-door provider's upstream token as `subject_token` when the AS is configured (i.e., `UpstreamTokenSource` is non-nil). No per-backend provider name needed — there is exactly one internal IDP (the AS), and its front-door upstream token is the right subject. When the AS is not configured, it uses `identity.Token` as today.

The discriminator is the presence of `UpstreamTokenSource` on the strategy (non-nil means AS is configured), not a per-backend config field. See `docs/design/vmcp-embedded-auth-strategy.md` for the `UpstreamTokenSource` interface and prior art.

**Config validation**: If the AS is the incoming auth provider, `token_exchange` will use the front-door upstream token as subject — ensure the backend's STS trusts that upstream IDP (not the TH AS).

### The architecture is partially ready for multi-upstream

| Component | Status | Detail |
|-----------|--------|--------|
| Per-backend outgoing auth config | Ready | `config.OutgoingAuth.Backends` map with `ResolveForBackend()` |
| `UpstreamRunConfig.Name` field | Ready | Exists, designed for multi-upstream routing |
| Auth server multi-upstream validation | Blocked | `len(Upstreams) > 1` explicitly rejected in `pkg/authserver/config.go` |
| Token storage keying | Not ready | Single key `sessionID`, needs `(sessionID, providerName)` |
| `PendingAuthorization` | Not ready | No field tracking which upstream was selected |
| Provider ID in callback | Wrong | Uses upstream `Type()` ("oauth2") not `Name()` — GitHub and Atlassian would collide |
| Handler upstream routing | Not ready | Single `h.upstream` field, no selection logic |
| Step-up auth flow | Not implemented | No mechanism exists |

## Contracts

Contracts define the interfaces, error types, and config shapes that connect sub-designs. Each contract has a **producer** (the sub-design that defines and implements it) and **consumers** (sub-designs that depend on it). When all contracts are fulfilled, the feature is complete.

### C-01: UpstreamTokenSource Interface

The sole bridge between the auth server and vMCP outgoing auth strategies. Strategies call `GetToken(ctx, providerName)` and receive a token string. The implementation (provided by the auth server adapter) internally extracts the TSID from `Identity.Claims` and calls `UpstreamTokenStorage`. Strategies never see TSIDs, storage, or session internals.

```go
type UpstreamTokenSource interface {
    GetToken(ctx context.Context, providerName string) (string, error)
}
```

- **Producer**: UC-02 (interface definition in `pkg/vmcp/auth/types/`), UC-01 (adapter implementation in `pkg/authserver/tokensource/`)
- **Consumers**: UC-02 (strategies use it), UC-05 (nil when no AS), UC-06 (returns C-03 error for missing tokens)
- **Default provider convention**: When `providerName` is empty (`""`), the adapter resolves it to the AS's front-door provider. This is used by `token_exchange` which does not name a specific provider — it always exchanges the token from the user's initial authentication.
- **Nil contract**: When no AS is configured, `UpstreamTokenSource` is nil. Strategies that receive nil must not be registered (UC-05). The factory gates registration on non-nil.
- **Binding enforcement** (Invariant #6): The adapter implementation MUST extract `Identity` from the request context and validate `(UserID, ClientID)` binding fields against the stored `UpstreamTokens` before returning. A valid TSID with mismatched binding fields must return `ErrInvalidBinding`, not the token.
- **Token refresh**: The adapter MUST handle expired upstream access tokens transparently — refresh using the stored refresh token before returning, or return an error if refresh fails. Callers never manage token lifecycle.

### C-02: UpstreamTokenStorage (Multi-Provider)

The auth server's storage interface gains a `providerName` dimension. Storage key becomes `(sessionID, providerName)`.

```go
StoreUpstreamTokens(ctx context.Context, sessionID string, providerName string, tokens *UpstreamTokens) error
GetUpstreamTokens(ctx context.Context, sessionID string, providerName string) (*UpstreamTokens, error)
DeleteUpstreamTokens(ctx context.Context, sessionID string) error  // wipe entire session
```

- **Producer**: UC-01 (storage schema change, memory + Redis implementations)
- **Consumers**: C-01 adapter (calls `GetUpstreamTokens`), UC-04 (session identity keying)
- **Binding fields** (Invariant #6): The `(UserID, ClientID)` binding validation is enforced by the `UpstreamTokens` struct fields at retrieval time — the storage implementation checks stored binding fields against the caller's identity. The binding fields are part of the `UpstreamTokens` struct, not the storage method signature.
- **Front-door guarantee**: The front-door provider's upstream token MUST be stored during the initial OAuth callback (not just during step-up). This ensures backends like Backend 3 (token_exchange with corporate-idp) can retrieve the front-door token immediately after initial authentication.
- **Migration**: The existing single-provider callers (proxy runner's `upstreamswap` middleware) must be updated to pass a provider name. The upstreamswap middleware currently has no provider concept — it retrieves "the" upstream token by TSID alone. Migration options: (a) configure the middleware with a default provider name, or (b) add a `GetDefaultUpstreamTokens` convenience method. UC-01 must address this.

### C-03: Step-Up Error Sentinel

A sentinel error returned by `UpstreamTokenSource.GetToken` when the requested upstream token has not been obtained yet. Distinct from storage failures or misconfigurations. Callers use `errors.Is()` to match.

```go
// ErrUpstreamTokenNotFound indicates the user has not authenticated with the
// requested upstream provider. The caller should trigger step-up auth.
// Callers should check for this error using errors.Is().
var ErrUpstreamTokenNotFound = errors.New("upstream token not found")
```

The adapter wraps it with provider context: `fmt.Errorf("provider %q: %w", name, ErrUpstreamTokenNotFound)`. Strategies wrap further: `fmt.Errorf("upstream_inject: %w", err)`. UC-06 matches with `errors.Is(err, authtypes.ErrUpstreamTokenNotFound)`.

- **Defined in**: `pkg/vmcp/auth/types/types.go` (co-located with `UpstreamTokenSource` interface, leaf package). No dependency on any UC.
- **Producer**: UC-02 (defines the sentinel in the types package)
- **Consumers**: UC-01 (adapter returns it wrapped when storage returns `ErrNotFound`), UC-02 (strategies propagate it), UC-06 (translates to client-facing signals)
- **Invariant**: This error must NOT contain tokens or session IDs (Invariant #8). Provider names are configuration values, not secrets.

### C-04: Backend Auth Config Shape

The per-backend config types for `upstream_inject` and the enhanced `token_exchange`. `TokenExchangeConfig` is **not modified** — the discriminator for subject token source is the presence of `UpstreamTokenSource` on the strategy (non-nil means AS is configured), not a per-backend config field. When the AS is configured, `token_exchange` automatically uses the front-door provider's upstream token.

```go
// New strategy config
type UpstreamInjectConfig struct {
    ProviderName string `json:"providerName" yaml:"providerName"`
}

// TokenExchangeConfig: unchanged. When UpstreamTokenSource is present on the
// strategy, it resolves the front-door upstream token automatically.

// Extended — added UpstreamInject field
type BackendAuthStrategy struct {
    // ... existing fields unchanged ...
    UpstreamInject *UpstreamInjectConfig `json:"upstreamInject,omitempty" yaml:"upstreamInject,omitempty"`
}
```

- **Producer**: UC-02 (defines the config types and strategy behavior)
- **Consumers**: UC-03 (must not change existing config shapes), UC-05 (validation rules reference these types)

### C-05: Validation Rules

Startup-time validation that catches misconfigurations before requests flow. These rules connect the AS presence, provider names, and strategy configs.

| Rule | Severity | Rationale |
|------|----------|-----------|
| `upstream_inject` configured but no AS | Error | Strategy cannot function without `UpstreamTokenSource` |
| `upstream_inject` provider name not in AS upstream config | Error | Typo or config drift |
| `token_exchange` with AS as incoming auth | Warning | Subject token changes to upstream front-door token; ensure backend STS trusts the upstream IDP |

- **Producer**: UC-05 (defines and implements validation)
- **Consumers**: UC-03 (validation must not reject existing valid configs)

### C-06: Auth Server Multi-Upstream Routing

The auth server's handler layer supports multiple upstream providers. The authorize endpoint selects the upstream provider based on requested scopes (`upstream:<providerName>`). The callback handler identifies which upstream responded and stores tokens under the correct `(TSID, providerName)` key.

- **Producer**: UC-01 (handler changes: multi-upstream map, scope-based routing, provider name in `PendingAuthorization`)
- **Consumers**: UC-06 (step-up flow reuses the same routing with TSID preservation)
- **Current gap**: Handler uses single `h.upstream` field; callback uses `upstream.Type()` not `Name()`.
- **Deduplication** (Invariant #7): The authorize endpoint MUST check for an existing pending or completed flow for the same `(TSID, providerName)` before initiating a new upstream redirect. Concurrent requests for the same step-up must either join the pending flow or fail with a clear error — never trigger duplicate OAuth flows.

### Contract Dependency Graph

```
C-02 (Storage)
  │
  ▼
C-01 (UpstreamTokenSource) ◄── C-03 (Step-up Error)
  │
  ├──► C-04 (Config Shape) ──► C-05 (Validation)
  │
  └──► C-06 (AS Routing)
```

## Use Cases

Each UC produces or consumes the contracts above. Sub-designs are independently implementable — when all contracts are fulfilled, the feature is complete.

### [UC-01: Multi-Upstream Provider Support](uc-01-multi-upstream-providers.md)

A single vMCP deployment supports multiple upstream IDPs. One provider handles primary authentication (the "front door"), while additional providers are acquired via step-up auth.

**Produces**: C-02 (multi-provider storage), C-06 (handler routing), C-01 (adapter implementation)
**Consumes**: C-03 (adapter returns `ErrUpstreamTokenNotFound` when provider token is missing)

### [UC-02: Incoming/Outgoing Auth Boundary](uc-02-incoming-outgoing-boundary.md)

The front-door upstream provider serves double duty: it drives the incoming auth flow (user authenticates with corporate IDP → gets TH-JWT) and its tokens may also be needed for outgoing auth (exchange corporate token for backend access). Defines the new `upstream_inject` strategy and enhanced `token_exchange`.

**Produces**: C-01 (interface definition), C-03 (error sentinel), C-04 (config shape)
**Consumes**: C-01 (strategies use `UpstreamTokenSource` adapter from UC-01)

### [UC-03: Backward Compatibility](uc-03-backward-compat.md)

Existing deployments using OIDC validation, bearer passthrough, token exchange, header injection, or anonymous access must continue working without configuration changes. Defines the compatibility contract and migration path.

**Produces**: —
**Consumes**: C-04 (must not change existing fields), C-05 (must not reject existing valid configs)

### [UC-04: Session Identity Model](uc-04-session-identity-model.md)

Defines the three-axis identity model (User, TSID, vMCP Session) and their cardinality relationships. Covers how TSID and vMCP session coexist without coupling, and how multi-provider storage is keyed.

**Produces**: —
**Consumes**: C-02 (storage keying model)

### [UC-05: Optional Auth Server](uc-05-optional-authserver.md)

vMCP operates in two modes: with auth server (full upstream token management) and without (existing strategies only). Defines configuration validation and error behavior.

**Produces**: C-05 (validation rules)
**Consumes**: C-01 (nil contract), C-04 (config types to validate)

### [UC-06: Step-Up Auth Signaling](uc-06-step-up-signaling.md)

When a tool call targets a backend whose upstream token hasn't been obtained yet, vMCP must signal the client to perform step-up authentication with a specific provider. Covers the signaling mechanism, timing, and client-side re-authorization flow.

**Produces**: —
**Consumes**: C-01 (returns C-03 error), C-03 (translates to client signals), C-06 (step-up reuses AS routing with TSID preservation)

## Prior Art

- [`docs/design/vmcp-embedded-auth-strategy.md`](../design/vmcp-embedded-auth-strategy.md) — Earlier design sketch covering outgoing auth strategies (`upstream_inject`, enhanced `token_exchange`), the `UpstreamTokenSource` interface, and storage changes. Useful as reference but may be incomplete or outdated relative to this design.

## Key Interfaces (Current)

These are the interfaces as they exist today. Sub-designs will propose changes.

| Type | Location | Purpose |
|------|----------|---------|
| `UpstreamTokenStorage` (interface) | `pkg/authserver/storage/types.go` | Store/retrieve upstream IDP tokens (currently single-provider per session) |
| `OutgoingAuthRegistry` (interface) | `pkg/vmcp/auth/auth.go` | Registry of outgoing auth strategies, resolved per-backend |
| `Strategy` (interface) | `pkg/vmcp/auth/auth.go` | Single outgoing auth strategy (`Authenticate` method) |
| `Identity` (struct) | `pkg/auth/identity.go` | Authenticated user identity with claims (including `tsid`) |
| `EmbeddedAuthServer` (struct) | `pkg/authserver/runner/embeddedauthserver.go` | In-process auth server lifecycle |
| `BackendAuthStrategy` (struct) | `pkg/vmcp/auth/types/types.go` | Per-backend auth strategy config |
| `OutgoingAuthConfig` (struct) | `pkg/vmcp/config/config.go` | Outgoing auth with `ResolveForBackend()` |

## Status

| Use Case | Status |
|----------|--------|
| UC-01: Multi-Upstream Providers | Complete |
| UC-02: Incoming/Outgoing Boundary | Complete |
| UC-03: Backward Compatibility | Complete |
| UC-04: Session Identity Model | Complete |
| UC-05: Optional Auth Server | Complete |
| UC-06: Step-Up Auth Signaling | Complete |
