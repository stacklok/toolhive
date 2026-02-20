# Upstream Token Refresh Design

## Problem Statement

Upstream refresh tokens are stored and `RefreshTokens()` is fully implemented on both
OAuth2 and OIDC providers, but nobody calls it. The upstream swap middleware
(`pkg/auth/upstreamswap/middleware.go:187-189`) detects expiry but just logs a warning
and passes the expired token through.

**The user is stuck after ~1 hour.** When the upstream AT expires:

1. `GetUpstreamTokens()` returns `ErrExpired` (the `timedEntry.expiresAt` matches AT expiry)
2. The middleware falls through, sending the request without a valid upstream token
3. The backend returns 401
4. The proxy passes the 401 through as-is to the client
5. The client uses its internal refresh token to get a new internal JWT — but the new
   JWT gets the **same `tsid`**, still pointing to the expired upstream tokens
6. Nothing in the code tells the client to re-authenticate from scratch
7. The user is stuck in a loop with no recovery path

This is not just a latency optimization — it's fixing a broken user experience where
sessions silently die with no recovery mechanism.

Tracking issue: https://github.com/stacklok/stacklok-epics/issues/236

## Current Token Flow

```
Client → GET /oauth/authorize → Internal AS → 302 to upstream IdP
                                                    ↓
Client ← redirect with auth code ← Internal AS ← GET /oauth/callback (code)
                                     ↓
                              ExchangeCode → upstream AT, RT, IDT stored (keyed by sessionID)
                              Issue internal JWT (with tsid=sessionID claim)
                              Issue internal opaque refresh token
                                     ↓
Client → proxied request → upstream swap middleware
                              ↓
                         Read upstream tokens by tsid from storage
                         Replace Authorization header with upstream AT
                              ↓
                         Forward to backend
```

### Current Token Lifetimes

| Token | Default | Configurable Range |
|-------|---------|-------------------|
| Internal access token (JWT) | 1h | 1min–24h |
| Internal refresh token (opaque) | 7d | 1h–30d |
| Auth code | 10min | 30s–10min |
| Upstream AT storage TTL | upstream `ExpiresAt` (or 1h fallback) | — |
| Storage cleanup interval | 5min | — |
| Expiry detection buffer | 30s | — |

### Current Gaps

1. **Upstream refresh tokens stored but never used** — `RefreshTokens()` is fully
   implemented (`pkg/authserver/upstream/oauth2.go`, `oidc.go`) but no code calls it
2. **Storage TTL bug** — the upstream token entry expires when the AT expires, which
   garbage-collects the refresh token even though it may be valid for 30+ days
3. **No session revocation on upstream failure** — when upstream tokens die, the internal
   session (7-day RT) keeps the user in a broken loop with no recovery path
4. **Wrong abstraction** — the middleware calls raw `storage.UpstreamTokenStorage` directly,
   which is an implementation detail of the AS, not a proper service boundary

## Design Decision: Service Interface with On-Read Refresh

### Why a Service Interface (Not Direct Storage Access)

The middleware currently calls `storage.UpstreamTokenStorage.GetUpstreamTokens()` directly.
This is the wrong abstraction — storage is an implementation detail of the AS. The middleware
should call a **service-level interface** that represents the business operation: "give me
valid upstream tokens for this session."

The service:
- Transparently refreshes expired tokens on read (the correctness mechanism)
- Revokes the internal session when upstream refresh permanently fails (recovery mechanism)
- Presents a clean API boundary that can go over the network when the AS is extracted

### Why On-Read Refresh (Not Background Loop) for MVP

Three approaches were evaluated: proactive background refresh, lazy on-demand refresh in
the middleware, and on-read refresh behind a service interface.

**On-read refresh behind the service interface wins** because:

- **Architecture boundary**: The middleware calls a service, not raw storage. The service
  owns all IdP interaction. When the AS goes out-of-process, `GetValidTokens()` becomes a
  single RPC. The middleware code doesn't change.
- **Simplicity**: No background goroutines, no `Start`/`Stop` lifecycle, no
  `ListUpstreamTokenSessions()` method. The service is just storage + provider + singleflight.
- **Correctness without optimization**: Every request gets valid tokens or a clear error.
  No edge cases around idle sessions that the background loop missed.
- **Minimal interface**: One method (`GetValidTokens`) returning an opaque `UpstreamCredential`. Clean for in-process and network use.

**The latency cost is acceptable**: On-read refresh adds ~200ms (one IdP round-trip) to
the first request after AT expiry — once per hour per session. MCP tool calls typically
take 500ms–2s. The 200ms is imperceptible.

**Background refresh loop deferred to Phase 2**: Can be added as an internal optimization
of the concrete implementation without changing the `Service` interface. The
`EmbeddedAuthServer` can call `Start()`/`Stop()` on the concrete type via type assertion
or a separate `Lifecycle` interface.

## Service Interface

**Package**: `pkg/authserver/upstreamtoken/`

```go
package upstreamtoken

type Service interface {
    // GetValidTokens returns a valid upstream credential for the given session.
    // If stored tokens are expired and a refresh token is available, the
    // implementation transparently refreshes them before returning.
    //
    // If refresh permanently fails (e.g. refresh token revoked), the
    // implementation revokes the internal session to force re-authentication.
    GetValidTokens(ctx context.Context, sessionID string) (UpstreamCredential, error)
}

// UpstreamCredential is an opaque type representing a valid upstream token.
// The middleware does not need to know internal structure — it only needs
// a bearer token string to inject into upstream requests.
type UpstreamCredential struct {
    accessToken string
}

// NewUpstreamCredential creates an UpstreamCredential. Only the service
// package should call this.
func NewUpstreamCredential(accessToken string) UpstreamCredential {
    return UpstreamCredential{accessToken: accessToken}
}

// BearerToken returns the access token string for injection into HTTP headers.
func (c UpstreamCredential) BearerToken() string {
    return c.accessToken
}

var (
    ErrSessionNotFound = errors.New("upstreamtoken: session not found")
    ErrRefreshFailed   = errors.New("upstreamtoken: refresh failed")
    ErrNoRefreshToken  = errors.New("upstreamtoken: no refresh token")
)
```

## In-Process Implementation

```go
type inProcessService struct {
    storage  storage.UpstreamTokenStorage
    provider upstream.OAuth2Provider
    revoker  SessionRevoker
    group    singleflight.Group
}
```

Three dependencies:
- `storage.UpstreamTokenStorage` — read/write upstream token entries
- `upstream.OAuth2Provider` — call IdP to refresh tokens
- `SessionRevoker` — revoke internal session when upstream refresh permanently fails

### GetValidTokens Flow

```
1. storage.GetUpstreamTokens(ctx, sessionID)
   → not found: return ErrSessionNotFound

2. AT not expired?
   → return UpstreamCredential(AccessToken)

3. AT expired, no RT?
   → return ErrNoRefreshToken

4. AT expired, has RT → singleflight.Do("refresh:"+sessionID, func() {
       // Double-check: re-read from storage (another goroutine may have refreshed)
       tokens := storage.GetUpstreamTokens(ctx, sessionID)
       if !tokens.IsExpired() → return tokens  // already refreshed

       // Call upstream IdP
       newTokens, err := provider.RefreshTokens(ctx, tokens.RefreshToken, tokens.UpstreamSubject)

       if err != nil:
           if isInvalidGrant(err):
               // Permanent failure — upstream RT is dead
               // Revoke the internal session so the client is forced to re-authenticate
               revoker.RevokeSessionByTSID(ctx, sessionID)
               storage.DeleteUpstreamTokens(ctx, sessionID)
               return ErrRefreshFailed  // maps to 401
           else:
               // Transient failure — IdP down, network error
               return ErrRefreshFailed wrapping original  // maps to 502

       // Store refreshed tokens (preserves binding fields, handles RT rotation)
       // UpdateUpstreamTokens resets the sliding 2h TTL automatically.
       updated := &storage.UpstreamTokens{
           ProviderID:      tokens.ProviderID,
           AccessToken:     newTokens.AccessToken,
           RefreshToken:    coalesce(newTokens.RefreshToken, tokens.RefreshToken),
           IDToken:         newTokens.IDToken,
           ExpiresAt:       newTokens.ExpiresAt,
           UserID:          tokens.UserID,
           UpstreamSubject: tokens.UpstreamSubject,
           ClientID:        tokens.ClientID,
       }
       storage.UpdateUpstreamTokens(ctx, sessionID, updated)
       return updated
   })

5. Convert to UpstreamCredential, return.
```

### Session Revocation on Permanent Failure

When upstream refresh fails with `invalid_grant` (RT revoked, expired, or invalidated by
the IdP), the service must break the internal session to prevent the user from being stuck
in a loop. Without this, the client would:

1. Get 401 from the middleware
2. Use its internal refresh token to get a new internal JWT
3. New JWT has the same `tsid` (fosite preserves session claims on refresh)
4. Next request hits the same dead upstream tokens
5. Infinite loop — user is stuck

The fix: `RevokeSessionByTSID(tsid)` deletes all internal access tokens and refresh tokens
where `session.UpstreamSessionID == tsid`. The next time the client tries to use its
internal refresh token, fosite returns `invalid_grant`, which is the standard signal that
forces the client into a full re-authentication (new OAuth flow → new upstream tokens →
new `tsid`).

## Session Revocation Interface

```go
// SessionRevoker revokes all internal tokens associated with a session.
// The UpstreamTokenService calls this when upstream refresh permanently fails,
// to force the client into re-authentication.
type SessionRevoker interface {
    RevokeSessionByTSID(ctx context.Context, tsid string) error
}
```

**Implementation** on `MemoryStorage`:

```go
func (s *MemoryStorage) RevokeSessionByTSID(ctx context.Context, tsid string) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Delete all refresh tokens for this session
    for sig, entry := range s.refreshTokens {
        session, ok := entry.value.GetSession().(*session.Session)
        if ok && session.UpstreamSessionID == tsid {
            delete(s.refreshTokens, sig)
        }
    }

    // Delete all access tokens for this session
    for sig, entry := range s.accessTokens {
        session, ok := entry.value.GetSession().(*session.Session)
        if ok && session.UpstreamSessionID == tsid {
            delete(s.accessTokens, sig)
        }
    }

    return nil
}
```

This follows the existing pattern — `RevokeRefreshToken(requestID)` and
`RevokeAccessToken(requestID)` already do O(n) scans over the same maps. The service
never touches fosite internals (signatures, request IDs). It only knows about `tsid`.

### How Fosite Refresh Token Storage Works (Context)

- Refresh tokens are stored in `refreshTokens map[string]*timedEntry[fosite.Requester]`,
  keyed by token signature (crypto hash)
- Each entry's `fosite.Requester` contains a `session.Session` with `UpstreamSessionID`
  (the `tsid`)
- All tokens within the same authorization grant share the same `tsid`
- When a deleted refresh token is used, fosite returns `invalid_grant` to the client,
  which is the standard signal to re-authenticate

## Storage Layer Changes

### New Method on `UpstreamTokenStorage`

```go
// UpdateUpstreamTokens atomically updates token values for an existing session
// and resets the sliding-window TTL (2h inactivity timeout). Used by refresh logic.
// Since there is no background refresh loop, every call to UpdateUpstreamTokens
// is triggered by a user request, so resetting the TTL here is correct.
UpdateUpstreamTokens(ctx context.Context, sessionID string, tokens *UpstreamTokens) error
```

### New Interface: `SessionRevoker`

```go
// RevokeSessionByTSID revokes all internal access tokens and refresh tokens
// associated with the given upstream session ID.
RevokeSessionByTSID(ctx context.Context, tsid string) error
```

Implemented on `MemoryStorage`. The `UpstreamTokenService` depends on this interface
separately from `UpstreamTokenStorage`.

### Behavioral Change to `StoreUpstreamTokens`

This is a change to an existing method's behavior — not just a new method.

```
BEFORE: expiresAt = tokens.ExpiresAt (AT expiry, ~1h)

AFTER:  if tokens.RefreshToken != "" → expiresAt = now + DefaultUpstreamInactivityTimeout (2h)
        if tokens.RefreshToken == "" → expiresAt = tokens.ExpiresAt (unchanged)
```

New constant: `DefaultUpstreamInactivityTimeout = 2 * time.Hour`

### Why 2 Hours?

- Long enough that a briefly idle user (lunch, meeting) doesn't lose their session
- Short enough that abandoned sessions are reaped quickly (not 7 days)
- TTL is reset by `StoreUpstreamTokens` (initial auth) and `UpdateUpstreamTokens`
  (refresh). Since there is no background loop, every write is user-triggered,
  so resetting TTL on write correctly tracks user activity.
- Without a background refresh loop, there are zero unnecessary IdP refreshes for
  abandoned sessions — the entry simply expires and gets reaped

### No `LastAccessedAt` Field Needed

The `timedEntry.expiresAt` sliding window (reset by `UpdateUpstreamTokens`) already
tracks user activity. Adding a separate field would add complexity for zero benefit.

### No `RefreshFailed` Flag Needed

The service handles errors transparently and returns them to the caller. When upstream
refresh permanently fails, it revokes the internal session — there's no need for a
persistent failure flag.

## Token Lifecycle Scenarios

### A. Active user (continuous requests)

```
T+0:00  Auth. AT stored (exp T+1:00). Entry TTL = T+2:00.
T+0:30  Request. GetValidTokens → AT valid. TTL unchanged (T+2:00).
T+1:00  Request. AT expired. GetValidTokens refreshes via IdP (~200ms).
        UpdateUpstreamTokens stores new AT (exp T+2:00), resets TTL → T+3:00.
T+1:30  Request. AT valid. TTL unchanged (T+3:00).
T+2:00  Request. AT expired. Refresh again. TTL resets → T+4:00.
... continues indefinitely — each refresh resets TTL.
```

### B. User idle 30 min, returns

```
T+0:00  Last request. TTL = T+2:00.
T+0:30  User returns. AT still valid (expires T+1:00). TTL unchanged (T+2:00).
```
Result: Seamless. No refresh needed.

### C. User idle 90 min, returns

```
T+0:00  Last request. TTL = T+2:00.
T+1:30  User returns. AT expired (expired at T+1:00). TTL still alive (T+2:00).
        GetValidTokens refreshes via IdP (~200ms). UpdateUpstreamTokens resets TTL → T+3:30.
```
Result: 200ms latency on first request. Transparent to user.

### D. User idle 2+ hours, returns

```
T+0:00  Last request. TTL = T+2:00.
T+2:00  Cleanup loop reaps entry.
T+2:15  User returns. GetValidTokens → ErrSessionNotFound → 401. Re-authenticate.
```
Result: Must re-authenticate. Expected for 2h+ idle.

### E. Abandoned session

```
T+0:00  Last request. TTL = T+2:00.
T+2:00  Entry reaped. Zero unnecessary IdP calls.
```

### F. RT revoked by IdP

```
T+1:00  User request. AT expired. GetValidTokens attempts refresh.
        IdP returns invalid_grant.
        Service calls revoker.RevokeSessionByTSID(tsid) → deletes internal AT + RT.
        Service calls storage.DeleteUpstreamTokens(tsid) → deletes upstream entry.
        Returns ErrRefreshFailed → middleware returns 401.
T+1:01  Client tries internal refresh token → fosite returns invalid_grant.
        Client forced into full re-authentication.
        New OAuth flow → new upstream tokens → new tsid → user is recovered.
```
Result: Clean recovery. User re-authenticates and gets a fresh session.

### G. IdP temporarily down

```
T+1:00  User request. AT expired. GetValidTokens attempts refresh.
        IdP returns connection error.
        Return ErrRefreshFailed wrapping transient error → middleware returns 502.
        Internal session NOT revoked (transient, not permanent).
        Client retries.
T+1:01  Client retries. GetValidTokens attempts refresh again.
        IdP is back → refresh succeeds. New AT stored. Request proceeds.
```
Result: Brief 502, then recovery on retry.

### H. Concurrent requests for same expired session

```
T+1:00  5 requests arrive. All call GetValidTokens(sessionID).
        singleflight deduplicates: 1 actual refresh call to IdP.
        All 5 callers block, all get same result.
```

### I. IdP rotates refresh token

```
T+1:00  Refresh returns new AT + new RT. UpdateUpstreamTokens stores both.
        Old RT replaced atomically. coalesce() keeps old RT if IdP doesn't rotate.
```

### J. Re-authentication while session exists

```
T+0:30  User re-authenticates (new OAuth flow). Callback handler calls
        StoreUpstreamTokens with the same or new sessionID.
        If same sessionID: entry overwritten with fresh tokens (natural upsert).
        If new sessionID: new entry created; old one expires on TTL.
```

## Wiring Chain

### `EmbeddedAuthServer` (constructs Server and Service)

The `EmbeddedAuthServer` constructs the `upstream.OAuth2Provider` directly (it already
builds the upstream config), keeps a reference, and passes it to both the Server and the
Service. The `authserver.Server` interface does NOT change.

```go
// In NewEmbeddedAuthServer:
provider := upstream.NewOAuth2Provider(upstreamConfig)
server := authserver.New(provider, ...)

tokenService := upstreamtoken.NewInProcessService(
    server.IDPTokenStorage(),   // UpstreamTokenStorage
    provider,                   // OAuth2Provider for refresh calls
    server.IDPTokenStorage(),   // SessionRevoker (MemoryStorage implements both)
)
```

### `MiddlewareRunner` Interface

```go
// BEFORE:
GetUpstreamTokenStorage() func() storage.UpstreamTokenStorage

// AFTER:
GetUpstreamTokenService() func() upstreamtoken.Service
```

### Middleware

```go
// BEFORE (middleware.go):
stor := storageGetter()
tokens, err := stor.GetUpstreamTokens(ctx, tsid)
if tokens.IsExpired(time.Now()) {
    logger.Warn("upstreamswap: upstream tokens expired")
    // Continue with expired token
}
injectToken(r, tokens.AccessToken)

// AFTER:
svc := serviceGetter()
cred, err := svc.GetValidTokens(r.Context(), tsid)
if err != nil {
    // map error to 401 or 502
    return
}
injectToken(r, cred.BearerToken())
```

## Error Mapping in Middleware

| Service Error | HTTP | When |
|---|---|---|
| `ErrSessionNotFound` | 401 | No upstream tokens for session |
| `ErrNoRefreshToken` | 401 | AT expired, no RT to refresh with |
| `ErrRefreshFailed` + `invalid_grant` | 401 | RT permanently dead (internal session also revoked) |
| `ErrRefreshFailed` + transient | 502 | IdP temporarily down |

Parse `invalid_grant` via `networking.OAuth2Error` struct.

## Future: Out-of-Process AS

When the AS moves to a separate service:

**HTTP endpoint** (on the AS):
```
POST /internal/upstream-tokens/{sessionID}

200: { "access_token": "..." }
404: { "error": "session_not_found" }
401: { "error": "refresh_failed" }
422: { "error": "no_refresh_token" }
```

**Network client**: Implements `upstreamtoken.Service` by making HTTP calls to the AS.
`GetValidTokens()` becomes a single HTTP request. Error types are serializable.

**Middleware code doesn't change** — same interface, different implementation behind it.

**Background refresh loop** (Phase 2 optimization): When added, it lives inside the AS
process as an internal optimization of the concrete `Service` implementation. It can be
added without changing the `Service` interface — the `EmbeddedAuthServer` (or future AS
service) starts/stops it via the concrete type or a separate `Lifecycle` interface.

## Implementation Phases

### Phase 1: Storage Layer Extensions

**Files**:
- `pkg/authserver/storage/types.go` — Add `UpdateUpstreamTokens`
  to `UpstreamTokenStorage` interface. Add `SessionRevoker` interface.
- `pkg/authserver/storage/memory.go` — Implement new methods including
  `RevokeSessionByTSID`. Change `StoreUpstreamTokens` TTL logic (RT present → 2h sliding
  window). This is a behavioral change to an existing method.
- `pkg/authserver/storage/config.go` — Add `DefaultUpstreamInactivityTimeout = 2h`
- `pkg/authserver/storage/mocks/` — Regenerate

**Tests**: New tests for `UpdateUpstreamTokens` (including TTL reset behavior),
`RevokeSessionByTSID`, and the `StoreUpstreamTokens` TTL behavior change.

**Verification**: `task test`, `task gen`, `task lint`

### Phase 2: Service Package

**Files**:
- `pkg/authserver/upstreamtoken/service.go` — `Service` interface, `UpstreamCredential`,
  sentinel errors
- `pkg/authserver/upstreamtoken/inprocess.go` — `inProcessService` with
  `GetValidTokens` (on-read refresh, singleflight, session revocation on permanent failure)
- `pkg/authserver/upstreamtoken/inprocess_test.go` — Unit tests
- `pkg/authserver/upstreamtoken/mocks/` — Generated mock for `Service`

**Tests**:
- Happy path: valid tokens returned as UpstreamCredential
- Expired AT with RT: successful refresh, tokens updated
- Expired AT without RT: `ErrNoRefreshToken`
- Refresh failure (invalid_grant): `ErrRefreshFailed`, internal session revoked,
  upstream tokens deleted
- Refresh failure (transient): `ErrRefreshFailed`, internal session NOT revoked
- Singleflight dedup: concurrent calls, only one refresh
- RT rotation: new RT stored when IdP rotates

**Verification**: `task test`, `task lint`

### Phase 3: Wiring Chain

**Files**:
- `pkg/authserver/runner/embeddedauthserver.go` — Construct provider + service, add
  `UpstreamTokenService()` accessor
- `pkg/transport/types/transport.go` — `GetUpstreamTokenStorage()` →
  `GetUpstreamTokenService()`
- `pkg/runner/runner.go` — Updated implementation
- `pkg/transport/types/mocks/` — Regenerate

**Verification**: Compiles, mocks regen cleanly

### Phase 4: Middleware Rewrite

**Files**:
- `pkg/auth/upstreamswap/middleware.go` — Replace `StorageGetter` with `ServiceGetter`,
  simplify to `GetValidTokens()` + error mapping
- `pkg/auth/upstreamswap/middleware_test.go` — Rewrite tests with mock
  `upstreamtoken.Service`

**Verification**: `task test`, `task lint`

### Phase 5: Cleanup and Verification

- Remove dead imports and unused code
- `task test-all` (unit + e2e)
- `task lint`, `task gen`, `task build`

Each phase leaves the codebase compilable. Phases 3+4 should ship together if in separate PRs.

### Future Phase: Background Refresh Loop (Optimization)

Can be added to the concrete `inProcessService` without changing the `Service` interface:
- Add `Start(ctx context.Context)` / `Stop()` to the concrete struct
- Add `ListUpstreamTokenSessions()` to `UpstreamTokenStorage` interface
- `EmbeddedAuthServer` calls `Start`/`Stop` via type assertion or `Lifecycle` interface
- Background loop refreshes AT-bearing sessions 5min before expiry
- Background refresh calls `UpdateUpstreamTokens` which resets TTL — if this is
  undesirable (keeping idle sessions alive), a separate `TouchUpstreamTokens` method
  can be introduced at that point to separate TTL-resetting writes from non-resetting writes
- Uses the same `singleflight.Group` as `GetValidTokens` for dedup

This adds IdP outage resilience (grace period with pre-refreshed tokens) and eliminates
the ~200ms latency on the first post-expiry request.

## Key Code References

| File | Current Role | Change |
|---|---|---|
| `pkg/authserver/storage/types.go` | Storage interface + UpstreamTokens struct | Add UpdateUpstreamTokens + SessionRevoker interface |
| `pkg/authserver/storage/memory.go` | In-memory storage impl | Implement new methods, fix TTL, add RevokeSessionByTSID |
| `pkg/authserver/storage/config.go` | TTL constants | Add DefaultUpstreamInactivityTimeout |
| `pkg/authserver/upstream/oauth2.go` | `RefreshTokens()` impl | No change (already implemented) |
| `pkg/authserver/upstream/oidc.go` | OIDC `RefreshTokens()` impl | No change |
| `pkg/authserver/upstream/tokens.go` | 30s expiry buffer | No change |
| `pkg/authserver/server.go` | Server interface | No change |
| `pkg/authserver/server_impl.go` | Server impl (has upstreamIDP) | No change |
| `pkg/authserver/server/session/session.go` | Session with UpstreamSessionID | No change (used by RevokeSessionByTSID) |
| `pkg/authserver/runner/embeddedauthserver.go` | Wraps Server | Construct service |
| `pkg/transport/types/transport.go` | MiddlewareRunner interface | Change method |
| `pkg/runner/runner.go` | Runner impl | Update method |
| `pkg/auth/upstreamswap/middleware.go` | Upstream swap middleware | Simplify to use service |
| `pkg/authserver/upstreamtoken/` | **NEW** | Service interface + implementation |
