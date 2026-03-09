# UC-06: Step-Up Auth Signaling

**Produces**: —
**Consumes**: C-01 (upstream chaining extends authorize/callback flow), C-03 (ErrUpstreamTokenNotFound as defense-in-depth sentinel)

## Overview

When a backend requires an upstream token the user hasn't obtained yet, vMCP must either have it available upfront or signal the client to acquire it. This UC defines the v1 mechanism (eager auth) and the rationale for deferring on-demand step-up to a future version.

---

## 1. Why On-Demand Step-Up Is Problematic

### 1.1 The Session Creation Problem

The `authRoundTripper` applies outgoing auth to ALL HTTP requests to a backend — including `Initialize`, `ListTools`, `ListResources`, and `ListPrompts` during session creation. If the upstream token is missing, the backend initialization fails and the backend is **silently excluded** from the session. Its tools never appear in `tools/list`.

```
OnRegisterSession hook fires (AFTER HTTP 200 initialize response sent)
  → factory.MakeSessionWithID()
    → parallel: initOneBackend() × N
      → createMCPClient() — builds transport with authRoundTripper
      → initAndQueryCapabilities()
        → c.Initialize()  ← authRoundTripper.RoundTrip() ← strategy.Authenticate()
          → tokenSource.GetToken(ctx, "github")
            → ErrUpstreamTokenNotFound ← backend excluded from session
```

The user never sees the backend's tools. There is nothing to call, so `tools/call`-level signaling (HTTP 403) never triggers.

### 1.2 SDK Timing Constraints

The mcp-go SDK writes the HTTP 200 `initialize` response **before** the `OnRegisterSession` hook fires. Backend connections happen after the response is committed. The SDK's `beforeInitialize` hook has a void return type — it cannot block initialization or return HTTP 403.

This means there is no interception point where we can both (a) know which backends need step-up and (b) return an HTTP error to the client.

### 1.3 Discovery Without Auth Is Not Practical

Most MCP servers require authentication for `tools/list`, not just `tools/call`. GitHub's MCP server, for example, rejects any unauthenticated request. "Discover capabilities without auth, gate on call" does not work for these backends. Additionally, per-user tool visibility means a service credential's tool list may differ from the user's.

### 1.4 Mechanisms Evaluated

| Mechanism | Why it doesn't work for v1 |
|-----------|---------------------------|
| **HTTP 403 on `tools/call`** | Tools from unauthenticated backends are invisible — nothing to call |
| **HTTP 403 on `initialize`** | SDK commits HTTP 200 before hooks fire; `beforeInitialize` returns void |
| **URL elicitation (-32042)** | Same invisible-tools problem; also requires SDK fix and optional client capability |
| **Response writer interception** | SDK's SSE upgrade logic makes buffered writers fragile |
| **Tool manifests in CRD** | Static declarations drift from reality; tools must be fully dynamic |
| **Cached capabilities from prior sessions** | Cold-start unsolved; per-user visibility mismatch |

---

## 2. V1 Mechanism: Eager Authentication

### 2.1 Design

The auth server requires all upstream scopes during the initial OAuth flow. By the time the client calls `initialize`, every upstream token is already stored under the user's TSID.

When the AS has upstreams configured, the `/oauth/authorize` endpoint automatically includes all `upstream:<name>` scopes — no opt-in flag needed:

```yaml
authServer:
  runConfig:
    upstreams:
      - name: corporate-idp    # front door
      - name: github
      - name: atlassian
```

```
scope=openid upstream:corporate-idp upstream:github upstream:atlassian
```

The client sends a single authorize request. The AS chains through all upstream providers server-side before returning the auth code.

### 2.2 Server-Side Upstream Chaining

UC-01's `selectUpstream()` picks ONE provider per authorize request. To support eager auth, the callback handler chains to subsequent upstreams server-side — the client never sees intermediate redirects.

**Flow** (3 upstreams: corporate-idp, github, atlassian):

```
Client → AS /authorize (scope=upstream:corporate-idp upstream:github upstream:atlassian)
  → authorize handler: parseUpstreamScopes() → ["corporate-idp", "github", "atlassian"]
  → start with "corporate-idp", store remaining=["github", "atlassian"] in PendingAuthorization
  → redirect to corporate-idp /authorize

User authenticates with corporate-idp
  → corporate-idp callback → AS /callback
  → store token at (TSID, "corporate-idp")     ← TSID generated here (first callback)
  → remaining=["github", "atlassian"] → non-empty
  → create new PendingAuthorization{ExistingTSID: tsid, RemainingUpstreams: ["atlassian"]}
  → redirect to github /authorize

User authenticates with GitHub
  → github callback → AS /callback
  → store token at (TSID, "github")             ← same TSID reused
  → remaining=["atlassian"] → non-empty
  → create new PendingAuthorization{ExistingTSID: tsid, RemainingUpstreams: []}
  → redirect to atlassian /authorize

User authenticates with Atlassian
  → atlassian callback → AS /callback
  → store token at (TSID, "atlassian")           ← same TSID reused
  → remaining=[] → empty
  → issue auth code → redirect to client with code
```

**Key invariant**: TSID is generated once on the first callback (`rand.Text()`), then threaded through all subsequent legs via `PendingAuthorization.ExistingTSID`. All upstream tokens share the same TSID.

**`PendingAuthorization` additions** (extends UC-01):

```go
type PendingAuthorization struct {
    // ... existing fields from UC-01 (ExistingTSID, UpstreamProviderName) ...
    RemainingUpstreams []string  // upstream providers still to authenticate
    Subject            string   // user subject from first upstream (front door)
}
```

Note: `ExistingTSID` (from UC-01) serves double duty — it is both the upstream token storage key and the session identity for auth code issuance. No separate `SessionID` field is needed.

**Callback handler logic** (~30 lines added to `handleCallback()`):

```go
// After storing upstream token...
if len(pending.RemainingUpstreams) > 0 {
    next := pending.RemainingUpstreams[0]
    rest := pending.RemainingUpstreams[1:]
    newPending := &PendingAuthorization{
        ExistingTSID:         tsid,
        RemainingUpstreams:   rest,
        Subject:              pending.Subject,
        UpstreamProviderName: next,
    }
    store(newPending)
    redirectToUpstream(w, r, next, newPending.ID)
    return
}
// All upstreams complete → issue auth code to client
issueAuthCode(w, r, tsid)
```

### 2.3 Partial Failure Handling

Not all upstream failures are equal:

| Failure | Behavior | Rationale |
|---------|----------|-----------|
| **Front-door IDP** (first upstream) | Abort entire flow | No user identity established; cannot issue JWT |
| **Non-front-door IDP** | Skip provider, continue chain | User is authenticated; some backends will work without this provider |

The existing `handleUpstreamError()` in `callback.go` currently redirects all errors back to the client. For chaining, it needs to branch:

- If `pending.ExistingTSID == ""` (front-door leg): abort, redirect error to client (current behavior)
- If `pending.ExistingTSID != ""` (non-front-door leg): log warning, skip this provider, chain to next upstream in `pending.RemainingUpstreams` (or issue auth code if none remain)

On partial completion, the AS issues a TH-JWT with the TSID containing only the tokens that succeeded. Backends requiring missing tokens will be excluded from the vMCP session (same behavior as today for missing tokens).

### 2.4 Why This Works

- **No timing problem**: All tokens exist before `initialize`. Backend connections succeed. All tools are visible.
- **No SDK changes**: The MCP session lifecycle is unchanged. No middleware, no hooks, no interception needed.
- **Single client interaction**: The client sends one authorize request and receives one auth code. Server-side chaining is transparent.
- **Boundary preservation**: Auth concerns are fully handled in the AS layer (Boundary 1). vMCP's MCP layer is auth-agnostic.
- **Defense in depth**: The `ErrUpstreamTokenNotFound` sentinel (C-03) still exists. If a token expires or is evicted mid-session, the error propagates through the normal tool call path as a tool result error. The client retries or the user re-authenticates.
- **TSID conveyance**: The callback handler embeds the TSID into the TH-JWT via fosite session claims (`TokenSessionIDClaimKey`). This mechanism is defined in UC-01 (Section on callback handling); UC-06 consumes it.

### 2.5 Trade-Offs

| Concern | Assessment |
|---------|-----------|
| **User friction** | N consent screens at login. For corporate deployments with organizational OAuth consent, these are typically transparent. For personal deployments, 2-3 screens is acceptable. |
| **Unused tokens** | Tokens acquired for providers whose tools the user may never call. Acceptable — tokens are scoped to the upstream provider's grants, not to vMCP capabilities. |
| **Provider availability** | Non-front-door IDP failures are tolerable (partial completion). Front-door failure aborts the flow. |
| **Dynamic backends (K8s)** | New backends added after login still need their tokens. The user must re-authenticate. This is a known limitation for v1. |

### 2.6 Session Behavior

With eager auth, the vMCP session lifecycle is unchanged from today:

1. Client sends `initialize` with TH-JWT
2. `OnRegisterSession` fires → `MakeSessionWithID` → all backends connect successfully (tokens available)
3. All tools from all backends appear in `tools/list`
4. `tools/call` works normally — `authRoundTripper` injects the correct upstream token per backend

No session rebinding, no augmentation, no `tools/list_changed` notifications needed.

---

## 3. Future Work: URL Elicitation for Lazy Auth

The most promising post-v1 direction is **URL elicitation** (MCP spec code -32042), designed specifically for "MCP server needs third-party OAuth tokens."

### 3.1 Why It's the Right Long-Term Mechanism

- **Semantically precise**: The server says "visit this URL so I can acquire a token" — not "your scope is insufficient" (which is strained when the client's TH-JWT is valid)
- **Composes with session augmentation**: After the user completes the URL flow, the AS stores the token, vMCP connects the backend, and sends `notifications/tools/list_changed`
- **Eliminates the trigger problem**: The server tells the client exactly what URL to visit at exactly the moment it matters
- **No new JWT needed**: Server-side state changes (token stored under existing TSID); the client retries with the same JWT

### 3.2 Current Blockers

1. **mcp-go SDK**: `handleToolCall` wraps all handler errors as `-32603 INTERNAL_ERROR` without type-checking for `URLElicitationRequiredError`. Requires a ~10-line SDK fix.
2. **Client adoption**: `elicitation: { url: {} }` is a MAY capability. Clients that don't declare it won't handle -32042.

### 3.3 Implementation Sketch

1. During `initialize`, record whether the client declared URL elicitation capability
2. On `tools/call` for an unauthenticated backend: if elicitation is supported, return -32042 with the AS authorize URL; otherwise, return tool result error
3. After step-up completes: connect the backend, register tools via `AddSessionTools`, send `notifications/tools/list_changed`

### 3.4 Lazy Discovery Summary

| Approach | Feasibility | Recommendation |
|----------|-------------|----------------|
| Accept limitation (tools invisible) | High | v1 implicit fallback |
| Cached capabilities from prior sessions | Medium | Optional enhancement for homogeneous backends |
| URL elicitation + `tools/list_changed` | High (once blockers resolve) | Primary post-v1 investment |
| Two-phase transport (service credentials) | Low | Not recommended |
| Background polling | Low | Not recommended |

---

## 4. Invariant Compliance

| Invariant | How UC-06 complies |
|-----------|--------------------|
| #1 Multi-upstream | Eager auth obtains tokens for all configured providers via server-side chaining. |
| #2 Boundary preserved | Eager auth is AS-level (Boundary 1). vMCP's MCP layer is auth-agnostic. |
| #3 Existing schemes | Eager auth only activates for `upstream_inject` backends. Mode A (no AS) is unchanged. |
| #4 TSID/session independent | TSID keys upstream tokens. Session is transport/routing. No coupling. |
| #5 AS is optional | No eager auth without AS. Non-AS modes are unchanged. |
| #6 Tokens are user-bound | `UpstreamTokenSource.GetToken()` validates `(UserID, ClientID)` binding. |
| #7 At most one step-up | Eager auth eliminates step-up entirely. All tokens obtained at login. |
| #8 No tokens in logs | Chaining logs provider names only. TSID is a random string, not a token. |

---

## 5. File Changes

| File | Change |
|------|--------|
| `pkg/authserver/server/handlers/authorize.go` | Add `parseUpstreamScopes()` to extract all `upstream:*` scopes; validate all provider names upfront; store `RemainingUpstreams` in `PendingAuthorization` |
| `pkg/authserver/server/handlers/callback.go` | Add chaining logic: after storing token, check `RemainingUpstreams`; if non-empty, redirect to next upstream; if empty, issue auth code. Modify `handleUpstreamError()` to branch on front-door vs non-front-door failure. |
| `pkg/authserver/storage/types.go` | Add `RemainingUpstreams`, `Subject` fields to `PendingAuthorization` |
| `pkg/authserver/storage/memory.go` | Update defensive copy in `StorePendingAuthorization`/`LoadPendingAuthorization` to clone `RemainingUpstreams` slice |
