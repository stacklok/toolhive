# UC-04: Session Identity Model

**Produces**: —
**Consumes**: C-02 (storage keying model)

## Overview

Three identity/session concepts coexist in vMCP. They operate in different layers, have different lifecycles, and share no storage. This UC formalizes their definitions, cardinality relationships, and interaction boundaries.

This is a conceptual document — it produces no new code. Its purpose is to establish a shared vocabulary and prevent designs that inadvertently couple these constructs.

---

## 1. The Three Axes

### 1.1 User Identity (`Identity`)

**What it is**: The authenticated principal making a request. Represented by `pkg/auth/identity.go:Identity`. Created by authentication middleware (OIDC, local, anonymous) and placed in `context.Context` via `auth.WithIdentity()`.

**Key fields**:
- `Subject` — unique user identifier (from JWT `sub` claim)
- `Token` — the original incoming JWT (redacted in logs/JSON)
- `Claims` — all JWT claims, including `tsid` when AS-issued
- `Groups` — intentionally NOT populated by middleware (claim names vary by provider)

**Lifecycle**: Per-HTTP-request. Created by auth middleware at the start of the request pipeline, consumed by downstream handlers and strategies, garbage-collected when the request completes. Not stored anywhere.

**Scope**: Spans both boundaries. The same `Identity` is available to incoming auth (middleware validates it), outgoing auth (strategies read `Identity.Token` or `Identity.Claims["tsid"]`), and session management (hijack prevention compares token hashes).

### 1.2 Token Session ID (`TSID`)

**What it is**: A durable handle linking a JWT to upstream IDP tokens in storage. A random string generated during the OAuth callback (`rand.Text()`) and embedded as the `tsid` claim in the TH-JWT.

**Where it lives**:
- **Created by**: `pkg/authserver/server/handlers/callback.go` — generated during the OAuth callback, stored via `fosite` session claims
- **Embedded in**: The TH-JWT `tsid` claim (via `pkg/authserver/server/session/session.go:TokenSessionIDClaimKey`)
- **Consumed by**: The `UpstreamTokenSource` adapter (UC-01) — extracts TSID from `Identity.Claims["tsid"]`, calls `storage.GetUpstreamTokens(ctx, tsid, providerName)`

**Lifecycle**: Hours to days. Survives JWT refresh (fosite preserves extra claims across the refresh chain). Ends when upstream tokens expire and are not refreshed, or when `DeleteUpstreamTokens(ctx, tsid)` is called.

**Storage key**: `(TSID, providerName)` — the multi-provider keying from C-02. One TSID can hold tokens from multiple upstream providers (corporate-idp, github, atlassian, etc.).

**Scope**: Auth server layer only. Nothing in `pkg/vmcp/` reads the TSID directly. The `UpstreamTokenSource` interface abstracts it away — strategies call `GetToken(ctx, providerName)` and the adapter internally extracts the TSID from the request context's `Identity.Claims`.

### 1.3 vMCP Session (`Mcp-Session-Id`)

**What it is**: A transport/routing construct for MCP protocol state. Created when an MCP client sends `initialize` without a `Mcp-Session-Id` header. Identified by a UUID returned in the `Mcp-Session-Id` response header.

**What it contains**:
- Routing table (capability name → backend workload mapping)
- Persistent backend MCP client connections (`backend.Session` instances)
- Resolved capability lists (tools, resources, prompts)
- Token binding hash (for hijack prevention)

**Where it lives**: `pkg/vmcp/session/` — two implementations:
- `VMCPSession` (current, being deprecated): wraps `transportsession.StreamableSession`, holds routing table and tools
- `defaultMultiSession` (SessionManagementV2): implements `MultiSession` interface, holds backend connections and full capability lists

**Lifecycle**: Per-MCP-client-connection. TTL-based cleanup by `session.Manager`. Ends when the client disconnects, the TTL expires, or the server returns HTTP 404 (session termination per MCP spec).

**Scope**: Transport/routing layer only. Has no knowledge of authentication, tokens, or upstream providers. The session holds a token hash for hijack prevention (`HijackPreventionDecorator`), but this is a one-way binding — the session never reads or modifies the token, it only validates that subsequent requests come from the same identity that created the session.

---

## 2. Cardinality Relationships

```
                    1          0..*
           User ──────────────── TSID
         (Subject)            (one per auth flow)
            │
            │ 1
            │
            │         0..*
            ├──────────────── vMCP Session
            │              (one per MCP client connection)
            │
            │
           TSID ─────────── vMCP Session
                   0..*         0..*
              (independent — no direct link)
```

### 2.1 User → TSID (1:N)

One user may have multiple TSIDs (from different browser sessions, devices, or OAuth flows). Each TSID is an independent token session with its own upstream tokens. TSIDs for the same user share nothing — each maintains its own `(TSID, providerName)` token entries.

**Edge case**: A single OAuth flow produces exactly one TSID. Step-up auth for a new provider reuses the existing TSID (from `PendingAuthorization.ExistingTSID`), adding tokens under the same TSID with a different provider name. A fresh login creates a new TSID.

### 2.2 User → vMCP Session (1:N)

One user may have multiple concurrent vMCP sessions (multiple MCP clients, multiple tabs, different tools). Each session is independently created, has its own routing table, and is bound to the user via token hashing (`HijackPreventionDecorator`).

### 2.3 TSID → vMCP Session (M:N, independent)

A TSID and a vMCP session share no direct link. They coexist in the same HTTP request but are resolved independently:

1. Auth middleware validates the JWT → produces `Identity` (with `Claims["tsid"]`)
2. Session adapter reads `Mcp-Session-Id` header → resolves `VMCPSession` or `MultiSession`
3. Hijack prevention compares `Identity.Token` hash against the session's bound token hash
4. Outgoing auth strategies may call `UpstreamTokenSource.GetToken()`, which reads `Identity.Claims["tsid"]` internally

Steps 2 and 4 are independent — neither references the other. The only shared artifact is the `Identity` in the request context, which both layers read but neither modifies.

**Concrete cardinalities**:
- One TSID may serve multiple vMCP sessions (same JWT used by multiple MCP clients)
- One vMCP session may have zero TSIDs (anonymous mode, or OIDC without AS)
- One vMCP session always has at most one TSID per request (one JWT per HTTP request)

---

## 3. Request Pipeline

This shows how the three axes flow through a single HTTP request in the AS-enabled case:

```
HTTP Request arrives
  │
  ▼
┌─────────────────────────────────┐
│ 1. Auth Middleware               │  Validates JWT, produces Identity
│    (pkg/auth/token.go)           │  Identity.Claims["tsid"] = "abc123"
│    → auth.WithIdentity(ctx, id)  │  Identity.Token = TH-JWT
└──────────────┬──────────────────┘
               │
               ▼
┌─────────────────────────────────┐
│ 2. Session Adapter               │  Reads Mcp-Session-Id header
│    (pkg/vmcp/server/)            │  Resolves VMCPSession from manager
│    → routing table, tools        │  Token hash validated (hijack prevention)
└──────────────┬──────────────────┘
               │
               ▼
┌─────────────────────────────────┐
│ 3. MCP SDK Handler               │  JSON-RPC dispatch (tools/call, etc.)
│    (mcp-go SDK)                  │  Routes via session's routing table
└──────────────┬──────────────────┘
               │
               ▼
┌─────────────────────────────────┐
│ 4. Outgoing Auth Strategy        │  Per-backend auth before forwarding
│    (pkg/vmcp/auth/strategies/)   │
│                                  │
│    upstream_inject:              │
│      → tokenSource.GetToken(     │  Adapter reads tsid from Identity.Claims
│          ctx, "github")          │  Calls storage.GetUpstreamTokens(tsid, "github")
│      → req.Header.Set(Bearer)   │  Injects upstream token
│                                  │
│    token_exchange:               │
│      → tokenSource.GetToken(     │  Adapter reads tsid, gets front-door token
│          ctx, "")                │  Uses as RFC 8693 subject_token
│      → exchange → req.Header.Set │
│                                  │
│    header_injection:             │
│      → req.Header.Set(static)   │  No involvement of Identity or TSID
└──────────────┬──────────────────┘
               │
               ▼
┌─────────────────────────────────┐
│ 5. Backend MCP Server            │  Receives request with injected auth
└─────────────────────────────────┘
```

**Key observation**: Steps 2 (session) and 4 (outgoing auth) are independent. Step 2 uses the `Mcp-Session-Id` header. Step 4 uses `Identity.Claims["tsid"]` via `UpstreamTokenSource`. They never reference each other.

---

## 4. Storage Isolation

Each construct has its own storage, with no cross-references:

| Construct | Storage | Key | Location |
|-----------|---------|-----|----------|
| Identity | `context.Context` | `IdentityContextKey{}` | Per-request, not persisted |
| TSID → Upstream Tokens | `UpstreamTokenStorage` | `(TSID, providerName)` | `pkg/authserver/storage/` (memory or Redis) |
| vMCP Session | `transportsession.Storage` + in-process maps | `Mcp-Session-Id` (UUID) | `pkg/vmcp/session/` + transport layer |
| Session token binding | `HijackPreventionDecorator` fields + transport session metadata (`vmcp.token.hash`, `vmcp.token.salt`) | Embedded in decorator (runtime) and transport session metadata (persistence) | In-process + `transportsession.Storage` |

**No foreign keys**: The TSID storage does not reference vMCP session IDs. The vMCP session storage does not reference TSIDs. The only link is the JWT itself — it contains the `tsid` claim and is presented to both layers via the `Identity` in the request context.

---

## 5. Binding and Security

### 5.1 TSID Binding (Invariant #6)

Upstream tokens are bound to `(UserID, ClientID)` at storage time. The `UpstreamTokenSource` adapter validates these binding fields before returning a token. A correct TSID with mismatched `UserID` or `ClientID` returns `ErrInvalidBinding`.

This prevents:
- **Cross-user access**: User A cannot use User B's TSID to retrieve User B's upstream tokens
- **Cross-client access**: A token issued to Client X cannot retrieve tokens stored by Client Y

The binding fields come from the `Identity` in the request context — the adapter compares `identity.Subject` against `tokens.UserID` and `identity.Claims["client_id"]` against `tokens.ClientID`.

### 5.2 vMCP Session Binding (Hijack Prevention)

vMCP sessions are bound to the creator's token via HMAC-SHA256 hashing (`HijackPreventionDecorator`). On session creation, the factory computes `HashToken(identity.Token, hmacSecret, salt)` and stores the hash. On every subsequent request, the decorator recomputes the hash from the current `identity.Token` and compares using constant-time comparison.

This prevents:
- **Session hijacking**: An attacker who guesses a `Mcp-Session-Id` cannot use it with a different token
- **Session upgrade**: An anonymous session cannot be claimed by presenting a token

**Note**: The session binds to `identity.Token` (the full JWT string), not to the `sub` claim or the TSID. This means:
- A refreshed JWT (different string, same TSID) creates a **new** vMCP session — the old session's token hash won't match the new JWT
- This is intentional: MCP sessions are lightweight and expected to be recreated on token refresh
- The TSID survives across JWT refreshes (fosite preserves claims), so upstream token continuity is maintained even when the vMCP session is recreated

### 5.3 The Two Bindings Are Independent

| Property | TSID Binding | Session Binding |
|----------|-------------|-----------------|
| What is bound | Upstream tokens → (UserID, ClientID) | Session → JWT string |
| When validated | On `GetToken()` call | On every session method call |
| Survives JWT refresh | Yes (TSID claim preserved) | No (new JWT string ≠ old hash) |
| Survives session recreation | N/A (TSID is not session-scoped) | N/A (new session = new binding) |
| Error on mismatch | `ErrInvalidBinding` | `ErrUnauthorizedCaller` |

---

## 6. Mode Matrix

How the three axes behave in each deployment mode:

| Mode | Identity | TSID | vMCP Session |
|------|----------|------|--------------|
| **Anonymous** | `Identity{Subject: "anonymous"}`, no token | None (no `tsid` claim) | Created, bound to empty token hash |
| **OIDC (no AS)** | From external IDP JWT | None (external JWTs have no `tsid`) | Created, bound to JWT hash |
| **OIDC (with AS)** | From TH-JWT (AS-issued) | Present (`tsid` claim in TH-JWT) | Created, bound to TH-JWT hash |
| **Local auth** | `Identity{Subject: username}`, bearer token | None | Created, bound to bearer token hash |

**Only the "OIDC with AS" mode** activates the TSID axis. In all other modes, TSID is absent and `UpstreamTokenSource` is nil — outgoing auth strategies use `identity.Token` directly (UC-03 backward compat).

---

## 7. Invariant Compliance

| Invariant | How UC-04 complies |
|-----------|--------------------|
| #1 Multi-upstream | TSID keys by `(TSID, providerName)` — one TSID holds tokens from multiple providers |
| #2 Boundary preserved | Identity spans both boundaries but is read-only. TSID is auth-server-only. Session is transport-only. |
| #3 Existing schemes | All modes except "OIDC with AS" have no TSID. Behavior is identical to today. |
| #4 TSID/session independence | Core guarantee of this UC. Sections 2.3, 3, and 4 prove independence. |
| #5 AS is optional | Section 6 mode matrix shows TSID is only present when AS is configured. |
| #6 Tokens are user-bound | Section 5.1 describes the `(UserID, ClientID)` binding on upstream token retrieval. |
| #7 At most one step-up | N/A — step-up deduplication is UC-06's concern. The TSID model supports it (same TSID for step-up). |
| #8 No tokens in logs | `Identity.Token` is redacted in `String()` and `MarshalJSON()`. TSID is a random string, not a token. |

---

## 8. FAQ

### Why doesn't the vMCP session store the TSID?

Because they serve different purposes and have different lifecycles. The TSID is for upstream token lookup (hours/days). The vMCP session is for routing (minutes/hours). Coupling them would mean:
- Session recreation (on JWT refresh) would require re-linking to the TSID
- TSID expiry would need to invalidate vMCP sessions
- Anonymous sessions would need a TSID placeholder

Keeping them independent means each can evolve without affecting the other.

### Can two vMCP sessions share upstream tokens?

Yes, indirectly. If two MCP clients use the same JWT (same TSID), their outgoing auth strategies will call `UpstreamTokenSource.GetToken()` with the same TSID and get the same upstream tokens. The sessions don't know about each other — the sharing is implicit through the TSID in the JWT.

### What happens when a JWT is refreshed?

1. The new JWT has the same `tsid` claim (fosite preserves it)
2. The new JWT has a different string value (new signature, new `exp`, etc.)
3. The `UpstreamTokenSource` adapter extracts the same TSID → upstream tokens are still available
4. The `HijackPreventionDecorator` sees a different token hash → existing vMCP session rejects the new JWT
5. The MCP client must re-`initialize` to create a new vMCP session with the new JWT
6. The new session gets a new routing table, new backend connections, new token binding — but upstream tokens are preserved via the shared TSID

This is the correct behavior: transport state (routing, connections) is ephemeral; auth state (upstream tokens) is durable.

### Why does session binding use the full JWT string, not the `sub` claim?

Using the `sub` claim would allow a different JWT (e.g., from a different client, or a stolen token from the same user) to hijack the session. The full JWT string provides the strongest possible binding — only the exact same token can access the session.

The trade-off is that JWT refresh requires session recreation. This is acceptable because:
- MCP sessions are cheap to create (routing table + backend connections)
- JWT refresh is infrequent (typically every 5-60 minutes)
- The MCP spec already handles session recreation gracefully (client re-initializes)
