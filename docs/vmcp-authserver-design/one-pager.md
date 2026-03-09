# vMCP Auth Server Integration — End-to-End Walkthrough

This document walks through the complete flow from user login to authenticated tool call, using the reference scenario: corporate-idp (front door), GitHub, and Atlassian upstreams.

---

## The Problem

vMCP aggregates multiple backend MCP servers. Each backend may need a token from a different upstream IDP — GitHub tools need a GitHub token, Atlassian tools need an Atlassian token, corporate tools need a token exchanged from the corporate IDP. Today, vMCP can pass through or exchange the incoming JWT, but it cannot obtain and store per-user tokens from multiple upstream providers.

## The Solution in One Paragraph

An embedded OAuth authorization server runs in-process inside vMCP. During login, it chains the user through all configured upstream IDPs (corporate-idp, then GitHub, then Atlassian) in a single OAuth flow, storing each provider's tokens under a shared Token Session ID (TSID). The TSID is embedded in the JWT. When a tool call reaches a backend, the outgoing auth strategy retrieves the correct upstream token by `(TSID, providerName)` and injects it into the request. The MCP layer never touches auth — it just routes.

---

## End-to-End Flow

### Phase 1: Configuration

```yaml
authServer:
  runConfig:
    issuer: https://vmcp.example.com
    upstreams:
      - name: corporate-idp    # front door — user identity comes from here
        issuerURL: https://idp.corp.example.com
        clientID: vmcp-client
      - name: github
        issuerURL: https://github.com/login/oauth
        clientID: vmcp-github
      - name: atlassian
        issuerURL: https://auth.atlassian.com
        clientID: vmcp-atlassian

incomingAuth:
  type: oidc
  oidc:
    issuer: https://vmcp.example.com   # same as AS — AS is the incoming auth provider

outgoingAuth:
  backends:
    github-tools:
      type: upstream_inject
      upstreamInject:
        providerName: github           # inject GitHub token directly
    atlassian-tools:
      type: upstream_inject
      upstreamInject:
        providerName: atlassian        # inject Atlassian token directly
    corporate-api:
      type: token_exchange             # exchange front-door token via RFC 8693
    internal-api:
      type: header_injection           # static API key, no AS involvement
      headerInjection:
        headerName: X-API-Key
        headerValueEnv: INTERNAL_API_KEY
```

At startup, vMCP validates: every `upstream_inject` provider name exists in the AS config; the AS issuer matches the incoming OIDC issuer; the OIDC audience is in the AS's allowed audiences. If the AS is absent, `upstream_inject` strategies are not registered — misconfiguration fails loudly, never silently.

### Phase 2: Login (Eager Auth with Server-Side Chaining)

```
MCP Client                    vMCP (AS)               corporate-idp    GitHub    Atlassian
    │                            │                         │              │           │
    ├─ GET /oauth/authorize ────►│                         │              │           │
    │  scope=openid              │                         │              │           │
    │  upstream:corporate-idp    │                         │              │           │
    │  upstream:github           │                         │              │           │
    │  upstream:atlassian        │                         │              │           │
    │                            │                         │              │           │
    │                            ├─ parseUpstreamScopes()  │              │           │
    │                            │  → ["corporate-idp",    │              │           │
    │                            │     "github",           │              │           │
    │                            │     "atlassian"]        │              │           │
    │                            │                         │              │           │
    │                            │  Store pending:         │              │           │
    │                            │    remaining=           │              │           │
    │                            │    ["github","atlassian"]│              │           │
    │                            │                         │              │           │
    │◄─ 302 to corporate-idp ───┤                         │              │           │
    │                            │                         │              │           │
    ├─ User authenticates ──────────────────────────────►  │              │           │
    │                            │                         │              │           │
    │                      ◄─────── callback ──────────────┤              │           │
    │                            │                         │              │           │
    │                            ├─ Generate TSID          │              │           │
    │                            ├─ Store token at         │              │           │
    │                            │  (TSID, "corporate-idp")│              │           │
    │                            ├─ remaining=             │              │           │
    │                            │  ["github","atlassian"] │              │           │
    │                            │  → non-empty            │              │           │
    │                            │                         │              │           │
    │◄─ 302 to GitHub ──────────┤                         │              │           │
    │                            │                         │              │           │
    ├─ User authenticates ─────────────────────────────────────────────►  │           │
    │                            │                         │              │           │
    │                      ◄────── callback ──────────────────────────────┤           │
    │                            │                         │              │           │
    │                            ├─ Store token at         │              │           │
    │                            │  (TSID, "github")       │              │           │
    │                            ├─ remaining=["atlassian"]│              │           │
    │                            │  → non-empty            │              │           │
    │                            │                         │              │           │
    │◄─ 302 to Atlassian ───────┤                         │              │           │
    │                            │                         │              │           │
    ├─ User authenticates ──────────────────────────────────────────────────────────► │
    │                            │                         │              │           │
    │                      ◄───── callback ───────────────────────────────────────────┤
    │                            │                         │              │           │
    │                            ├─ Store token at         │              │           │
    │                            │  (TSID, "atlassian")    │              │           │
    │                            ├─ remaining=[]           │              │           │
    │                            │  → empty                │              │           │
    │                            │                         │              │           │
    │◄─ 302 with auth code ─────┤                         │              │           │
    │                            │                         │              │           │
    ├─ POST /oauth/token ───────►│                         │              │           │
    │                            │                         │              │           │
    │◄─ TH-JWT {tsid: "abc123"} ┤                         │              │           │
```

The client sends one authorize request. The AS chains through all three upstream IDPs server-side. The TSID is generated on the first callback and reused for all subsequent legs. The client receives a single TH-JWT containing the `tsid` claim. All three upstream tokens are stored under `(TSID, providerName)` before any MCP interaction begins.

**Partial failure**: If GitHub is down, the AS skips it and continues to Atlassian. The TH-JWT is still issued — backends needing GitHub tokens will be excluded from the session. Front-door (corporate-idp) failure aborts the entire flow.

### Phase 3: MCP Session Creation

```
MCP Client                    vMCP                    github-tools   atlassian-tools   corporate-api
    │                            │                         │              │               │
    ├─ POST /mcp                 │                         │              │               │
    │  initialize                │                         │              │               │
    │  Authorization: TH-JWT     │                         │              │               │
    │                            │                         │              │               │
    │                  ┌─────────┤                         │              │               │
    │                  │ OIDC    │                         │              │               │
    │                  │ middle- │ Validates TH-JWT via    │              │               │
    │                  │ ware    │ standard OIDC discovery │              │               │
    │                  │         │ (AS = OIDC provider)    │              │               │
    │                  └─────────┤                         │              │               │
    │                            │                         │              │               │
    │◄─ HTTP 200 ────────────────┤  (SDK writes response)  │              │               │
    │   Mcp-Session-Id: sess-1   │                         │              │               │
    │                            │                         │              │               │
    │              OnRegisterSession hook fires             │              │               │
    │                            │                         │              │               │
    │                  ┌─────────┤                         │              │               │
    │                  │ Session │                         │              │               │
    │                  │ factory │                         │              │               │
    │                  │         ├─ initOneBackend() ─────►│              │               │
    │                  │         │  Initialize + ListTools  │              │               │
    │                  │         │  (authRoundTripper       │              │               │
    │                  │         │   injects GitHub token)  │              │               │
    │                  │         │                         │              │               │
    │                  │         ├─ initOneBackend() ──────────────────►  │               │
    │                  │         │  (injects Atlassian token)             │               │
    │                  │         │                         │              │               │
    │                  │         ├─ initOneBackend() ─────────────────────────────────►  │
    │                  │         │  (exchanges corporate token via STS)   │               │
    │                  │         │                         │              │               │
    │                  └─────────┤  All backends connected  │              │               │
    │                            │  Routing table built     │              │               │
```

All backend connections succeed because all upstream tokens are available. The `authRoundTripper` wrapping each backend's HTTP client calls `UpstreamTokenSource.GetToken(ctx, providerName)`, which extracts the TSID from `Identity.Claims["tsid"]` and retrieves the correct token from storage.

### Phase 4: Tool Call

```
MCP Client                    vMCP                    github-tools
    │                            │                         │
    ├─ POST /mcp                 │                         │
    │  Mcp-Session-Id: sess-1    │                         │
    │  Authorization: TH-JWT     │                         │
    │                            │                         │
    │  {"method":"tools/call",   │                         │
    │   "params":{"name":        │                         │
    │     "github-tools/list_repos"}}                      │
    │                            │                         │
    │                  ┌─────────┤                         │
    │                  │ 1. OIDC middleware validates JWT   │
    │                  │ 2. Session adapter resolves sess-1 │
    │                  │ 3. Router: list_repos → github-tools
    │                  │ 4. authRoundTripper:               │
    │                  │    tokenSource.GetToken(ctx,       │
    │                  │      "github")                     │
    │                  │    → storage.Get(TSID, "github")   │
    │                  │    → GitHub access token           │
    │                  │    → req.Header.Set(Authorization, │
    │                  │        "Bearer <github-token>")    │
    │                  └─────────┤                         │
    │                            │                         │
    │                            ├─ tools/call list_repos ─►│
    │                            │  (with GitHub token)     │
    │                            │                         │
    │                            │◄─ tool result ──────────┤
    │                            │                         │
    │◄─ tool result ─────────────┤                         │
```

The MCP layer (routing, session management) has no knowledge of auth. The outgoing auth strategy fetches the correct upstream token per backend, injects it, and the request flows through. Steps 2 (session) and 4 (outgoing auth) are independent — the session uses the `Mcp-Session-Id` header, outgoing auth uses `Identity.Claims["tsid"]`.

---

## Three Identity Axes

| Axis | Purpose | Lifecycle | Storage |
|------|---------|-----------|---------|
| **Identity** | Authenticated principal (`Subject`, `Token`, `Claims`) | Per-HTTP-request | `context.Context` |
| **TSID** | Upstream token lookup key (`tsid` JWT claim) | Hours-days (OAuth refresh chain) | `UpstreamTokenStorage` keyed by `(TSID, providerName)` |
| **vMCP Session** | Routing table + backend connections | Minutes-hours (TTL-based) | In-process maps + `transportsession.Storage` |

These are independent. One TSID may serve multiple vMCP sessions (same JWT, multiple MCP clients). One vMCP session may have zero TSIDs (anonymous mode). They share no storage — the only link is the JWT, which carries the `tsid` claim and is presented to both layers via `Identity`.

## Deployment Modes

| Mode | Identity | TSID | Upstream Tokens | What works |
|------|----------|------|-----------------|------------|
| **No AS** | From external IDP | None | None | header_injection, token_exchange (with incoming JWT), unauthenticated |
| **With AS** | From TH-JWT | Present | All providers from login | All of the above + upstream_inject, token_exchange with front-door token |
| **Anonymous** | `anonymous` | None | None | unauthenticated, header_injection |

The AS is purely additive. Removing it returns vMCP to today's behavior — no silent fallback, no degradation.

---

## Implementation Plan

The design is implemented in three milestones. Each milestone delivers end-to-end user value — a user can configure and use the feature after each milestone ships. UC-03 (backward compatibility) and UC-04 (session identity model) are verification documents with no code changes; their guidance is folded into each PR.

### Milestone 1: Single upstream provider with `upstream_inject`

A user configures one upstream IDP and one backend with `upstream_inject`. The backend's tools are visible and tool calls work with the injected upstream token.

| PR | What ships | UCs | Contracts |
|----|-----------|-----|-----------|
| **1. Multi-provider storage** | Storage keyed by `(TSID, providerName)`. Memory + Redis implementations. `upstreamswap` middleware passes provider name. | UC-01 (storage) | C-02 |
| **2. UpstreamTokenSource adapter** | Interface definition, error sentinel, adapter implementation with binding validation and token refresh. | UC-01 (adapter), UC-02 (interface + sentinel) | C-01, C-03 |
| **3. `upstream_inject` strategy + config** | Strategy implementation, `UpstreamInjectConfig` type, CRD type + converter, factory registration gated on non-nil tokenSource. | UC-02 (strategy), UC-03 (backward compat) | C-04 |
| **4. AS wiring into vMCP** | `AuthServer` config field, `NewEmbeddedAuthServer` at startup, AS routes on HTTP mux, tokenSource passed to outgoing auth factory, validation rules (V-01 through V-07). | UC-05 | C-05 |

After Milestone 1: a user with one upstream provider and one `upstream_inject` backend has a working end-to-end flow. The AS handles login, stores the upstream token, and the strategy injects it.

### Milestone 2: Multi-upstream with eager auth

A user configures multiple upstream IDPs. All tokens are obtained at login via server-side chaining. All backends are visible in the MCP session.

| PR | What ships | UCs | Contracts |
|----|-----------|-----|-----------|
| **5. Multi-upstream handler routing** | `upstreams` map on handler, scope-based routing in authorize, provider name in `PendingAuthorization`, remove `len(Upstreams) > 1` rejection. | UC-01 (handlers) | C-06 |
| **6. Server-side chaining** | `parseUpstreamScopes()`, callback chaining via `RemainingUpstreams`, partial failure handling (`handleUpstreamError` branching), upfront scope validation. | UC-06 | - |

After Milestone 2: the reference scenario works end-to-end — corporate-idp + GitHub + Atlassian, all tokens obtained at login, all backends connected, all tools visible.

### Milestone 3: Enhanced token exchange

The front-door upstream token is used as the RFC 8693 `subject_token` for `token_exchange` backends when the AS is configured.

| PR | What ships | UCs | Contracts |
|----|-----------|-----|-----------|
| **7. token_exchange with front-door token** | `TokenExchangeOption` for `upstreamTokenSource`, `Authenticate` uses front-door token as subject when AS is present, falls back to `identity.Token` when absent. | UC-02 (token_exchange enhancement) | - |

After Milestone 3: Backend 3 (`corporate-api` with token exchange) works — the corporate IDP token stored during login is exchanged via RFC 8693 for a backend-specific token.

### Dependency Graph

```
PR 1 (storage) ──► PR 2 (adapter) ──► PR 3 (strategy) ──► PR 4 (wiring)
                                                               │
                                          ┌────────────────────┤
                                          ▼                    ▼
                                    PR 5 (multi-upstream) ► PR 6 (chaining)
                                                               │
                                                               ▼
                                                         PR 7 (token_exchange)
```

PRs 1-4 are sequential (each depends on the previous). PRs 5-6 build on PR 4. PR 7 can land any time after PR 4.

---

## Design Documents

| Document | What it covers |
|----------|---------------|
| [README](README.md) | Invariants, contracts, reference scenario, key interfaces |
| [UC-01](uc-01-multi-upstream-providers.md) | Multi-provider storage, handler routing, UpstreamTokenSource adapter |
| [UC-02](uc-02-incoming-outgoing-boundary.md) | `upstream_inject` strategy, enhanced `token_exchange`, error sentinels |
| [UC-03](uc-03-backward-compat.md) | Migration paths, behavioral preservation proofs |
| [UC-04](uc-04-session-identity-model.md) | Identity/TSID/Session independence, cardinality, binding |
| [UC-05](uc-05-optional-authserver.md) | AS optionality, config validation rules, startup wiring |
| [UC-06](uc-06-step-up-signaling.md) | Eager auth, server-side chaining, why on-demand step-up is deferred |
