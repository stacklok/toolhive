# UC-01: Multi-Upstream Provider Support

**Produces**: C-02 (multi-provider storage), C-06 (handler routing), C-01 (adapter implementation)
**Consumes**: C-03 (`ErrUpstreamTokenNotFound` for missing provider tokens)

## Overview

A single auth server deployment supports multiple upstream IDPs. The storage, handler, and adapter layers all gain a `providerName` dimension.

Reference scenario (vMCP with front door + step-up):

```
Embedded Auth Server:
  Upstream Provider: "corporate-idp"  (front door)
  Upstream Provider: "github"         (step-up)
  Upstream Provider: "atlassian"      (step-up)
```

Standalone scenario (proxy runner, single backend):

```
Embedded Auth Server:
  Upstream Provider: "github"         (sole provider — authenticates the user AND
                                       provides the upstream token for the backend)
```

Both scenarios are first-class. The design does NOT assume a separate front-door provider. When there is only one upstream, it serves as both the authentication mechanism and the upstream token source. The `providerName` parameter is always required at the storage and adapter levels; for single-upstream deployments, the provider name defaults to `"default"` (matching the existing `UpstreamRunConfig.Name` default).

---

## 1. Storage Changes (C-02)

### 1.1 Interface Signature Changes

The `UpstreamTokenStorage` interface gains a `providerName` parameter on `Store` and `Get`. `Delete` remains session-scoped (wipes all providers for a TSID).

```go
// pkg/authserver/storage/types.go

type UpstreamTokenStorage interface {
    StoreUpstreamTokens(ctx context.Context, sessionID, providerName string, tokens *UpstreamTokens) error
    GetUpstreamTokens(ctx context.Context, sessionID, providerName string) (*UpstreamTokens, error)
    DeleteUpstreamTokens(ctx context.Context, sessionID string) error  // wipe entire session
}
```

The `UpstreamTokens` struct is unchanged. Its existing `ProviderID` field records which provider issued the tokens (same value as `providerName`).

### 1.2 Memory Storage Changes

The map changes from flat to nested:

```go
// Before:
upstreamTokens map[string]*timedEntry[*UpstreamTokens]

// After: sessionID -> providerName -> entry
upstreamTokens map[string]map[string]*timedEntry[*UpstreamTokens]
```

A nested map is chosen over a composite key (`sessionID + ":" + providerName`) because `DeleteUpstreamTokens` must wipe all providers for a session. A nested map makes this `O(1)` lookup + `O(providers)` delete, vs `O(total entries)` scan with composite keys.

**`StoreUpstreamTokens`**: Validates both `sessionID` and `providerName` are non-empty. Creates the inner map on first write for a session. Otherwise follows the existing pattern (defensive copy, TTL from `ExpiresAt`).

**`GetUpstreamTokens`**: Double lookup: outer map by `sessionID`, inner map by `providerName`. Returns `ErrNotFound` if either is missing.

**`DeleteUpstreamTokens`**: Unchanged behavior -- deletes the outer map entry, removing all providers for the session.

**Cleanup loop**: Iterates both levels. When all providers within a session are expired, the session entry itself is removed.

**`DeleteUser`**: Iterates all sessions and all providers within each, deleting entries where `value.UserID == id`.

### 1.3 Redis Storage Key Pattern

```
Before: {prefix}upstream:{sessionID}
After:  {prefix}upstream:{sessionID}:{providerName}
```

A new helper:

```go
func redisUpstreamKey(prefix, sessionID, providerName string) string {
    return fmt.Sprintf("%s%s:%s:%s", prefix, KeyTypeUpstream, sessionID, providerName)
}
```

**Session index for `DeleteUpstreamTokens`**: A Redis SET at `{prefix}upstream:idx:{sessionID}` tracks all provider names for a session. `StoreUpstreamTokens` adds to the set; `DeleteUpstreamTokens` reads the set, deletes all referenced keys, then deletes the set (Lua script for atomicity).

**User reverse index**: The existing `user:upstream` set tracks `{sessionID}:{providerName}` pairs instead of bare `{sessionID}`.

**Data migration**: The key pattern change means tokens stored under the old pattern (`{prefix}upstream:{sessionID}`) become invisible to lookups using the new pattern (`{prefix}upstream:{sessionID}:{providerName}`). Two options:

1. **Fallback read** (if existing deployments exist): On `GetUpstreamTokens`, if the new key returns `ErrNotFound`, attempt a read from the old key pattern. If found, migrate the entry in-place by writing it to the new key (with the configured default provider name) and deleting the old key. This is transparent and self-healing — each token is migrated on first access.

2. **No migration** (if no production Redis-backed AS deployments exist yet): Document the key pattern change as a breaking change. Existing sessions will require re-authentication after upgrade. Since the embedded AS is new and may not have production deployments with Redis storage, this is likely sufficient.

The implementation should start with option 2 (documentation-only) and add the fallback read if production deployments are identified before the change ships.

### 1.4 Front-Door Guarantee

During the initial OAuth callback, the handler stores the provider's upstream tokens immediately using the provider name from `PendingAuthorization.UpstreamProviderName`. This is not new behavior -- the current callback already calls `StoreUpstreamTokens`. The only change is adding the `providerName` argument.

For single-upstream deployments (standalone proxy runner), the sole provider's tokens are stored under its name (e.g., `"github"` or `"default"`). There is no separate "front door" concept -- the same provider serves both roles.

### 1.5 Binding Field Validation

Binding validation (Invariant #6) is NOT performed at the storage layer. It is enforced by the `UpstreamTokenSource` adapter (Section 3), which has access to the caller's `Identity` context. Storage remains pure CRUD.

### 1.6 Upstreamswap Middleware Migration

The proxy runner's `upstreamswap` middleware (`pkg/auth/upstreamswap/middleware.go`) currently calls `stor.GetUpstreamTokens(ctx, tsid)` with no provider name.

**Change**: Add `ProviderName` to `upstreamswap.Config`:

```go
type Config struct {
    HeaderStrategy   string `json:"header_strategy,omitempty"`
    CustomHeaderName string `json:"custom_header_name,omitempty"`
    ProviderName     string `json:"provider_name" yaml:"provider_name"`
}
```

The middleware passes `cfg.ProviderName` to storage. Validation rejects empty `ProviderName`.

**Defaulting for single-upstream**: The proxy runner's `addUpstreamSwapMiddleware` populates `ProviderName` from the auth server's upstream config. For single-upstream configs where the name defaults to `"default"`, the middleware config also gets `"default"`. This is transparent -- existing single-upstream deployments work without configuration changes, because the provider name was already defaulted to `"default"` at config validation time.

**What changes in `pkg/runner/middleware.go`**:

The `addUpstreamSwapMiddleware` function must read the auth server config's upstream name and set it on the `upstreamswap.Config`. If the `RunConfig.UpstreamSwapConfig` already has a `ProviderName`, it takes precedence.

---

## 2. Handler Changes (C-06)

### 2.1 Handler: Single Upstream to Multi-Upstream

The `Handler` struct replaces its single `upstream` field with a map and a default:

```go
// pkg/authserver/server/handlers/handler.go

type Handler struct {
    provider        fosite.OAuth2Provider
    config          *server.AuthorizationServerConfig
    storage         storage.Storage
    upstreams       map[string]upstream.OAuth2Provider  // keyed by provider name
    defaultUpstream string                              // name of the first provider
    userResolver    *UserResolver
}
```

`defaultUpstream` is the first upstream in config order. For single-upstream deployments, this is the only provider.

`NewHandler` changes signature to accept the map and default name.

### 2.2 OAuth2Provider Interface: No Changes

The `OAuth2Provider` interface is NOT modified. Providers do not need to know their own names — names are configuration-assigned and the handler owns the `map[string]upstream.OAuth2Provider` mapping. The callback uses `pending.UpstreamProviderName` (from `PendingAuthorization`) as the authoritative provider identity, not a method on the provider itself. This avoids adding a `Name()` method that would need to be set at construction time and kept in sync with the map key.

### 2.3 PendingAuthorization: Track Selected Upstream

Two new fields on `PendingAuthorization`:

```go
type PendingAuthorization struct {
    // ... existing fields ...

    // UpstreamProviderName identifies which upstream was selected for this flow.
    UpstreamProviderName string

    // ExistingTSID is set during step-up flows. When non-empty, the callback
    // stores tokens under this TSID instead of generating a new one.
    ExistingTSID string
}
```

Both must be included in defensive copies in `StorePendingAuthorization` and `LoadPendingAuthorization` (memory and Redis).

### 2.4 Authorize Endpoint: Scope-Based Provider Selection

The authorize handler selects the upstream by looking for an `upstream:<name>` scope in the request. If none is found, it falls back to `defaultUpstream`.

```go
const upstreamScopePrefix = "upstream:"

func (h *Handler) selectUpstream(scopes fosite.Arguments) string {
    for _, scope := range scopes {
        if name, ok := strings.CutPrefix(scope, upstreamScopePrefix); ok {
            return name
        }
    }
    return h.defaultUpstream
}
```

After selection, the handler validates the name exists in `h.upstreams`. If not, it returns `fosite.ErrInvalidScope`.

The selected provider name is stored in `PendingAuthorization.UpstreamProviderName`.

**Scope passthrough**: The `upstream:<name>` scope is consumed by the auth server for routing. It is NOT forwarded to the upstream IDP. It IS granted back to the client so the client can see which upstream it authenticated with.

**Scope validation — custom ScopeStrategy**: The `upstream:<name>` scopes must be allowed for all clients without requiring per-client registration. Since upstream providers can be added/removed dynamically, hardcoding them on each client's allowed scopes is impractical.

Solution: a custom `fosite.ScopeStrategy` that auto-allows `upstream:` prefixed scopes and delegates everything else to `ExactScopeStrategy`:

```go
const UpstreamScopePrefix = "upstream:"

func UpstreamAwareScopeStrategy(haystack []string, needle string) bool {
    if strings.HasPrefix(needle, UpstreamScopePrefix) {
        return true
    }
    return fosite.ExactScopeStrategy(haystack, needle)
}
```

Three changes required:
1. `pkg/authserver/server/provider.go`: Set `ScopeStrategy: UpstreamAwareScopeStrategy` (replaces `fosite.ExactScopeStrategy`)
2. `pkg/authserver/server/handlers/callback.go` line 203: Replace hardcoded `fosite.ExactScopeStrategy` with the same custom strategy
3. `pkg/authserver/server/registration/dcr.go` `ValidateScopes()`: Accept `upstream:` prefixed scopes without requiring them in `ScopesSupported`

The authorize handler then performs a **secondary routing validation** — after fosite accepts the scope, `selectUpstream` checks the named provider exists in `h.upstreams` and returns `fosite.ErrInvalidScope` if not. This separates scope validation (fosite concern) from routing validation (handler concern).

**Step-up TSID preservation**: For step-up flows (detailed in UC-06), the handler extracts the existing TSID and stores it in `PendingAuthorization.ExistingTSID`. The exact conveyance mechanism (e.g., `tsid_hint` query parameter) is deferred to UC-06.

### 2.5 Callback: Route to Correct Upstream

The callback handler uses `PendingAuthorization.UpstreamProviderName` to look up the correct upstream from `h.upstreams`.

**Bug fix**: The current code uses `string(h.upstream.Type())` (returns `"oauth2"`) as the `providerID` for user resolution and token storage. This is changed to `pending.UpstreamProviderName`, which is the configured name (e.g., `"github"`). Without this fix, multiple OAuth2-type providers would collide on the same provider ID. Note: `Name()` is NOT added to the `OAuth2Provider` interface — the handler owns the name-to-provider mapping, and `pending.UpstreamProviderName` is the authoritative source.

**TSID generation**: If `pending.ExistingTSID` is non-empty (step-up), use it. Otherwise generate a new TSID via `rand.Text()` (same as today).

**Token storage call**: Changes from `StoreUpstreamTokens(ctx, sessionID, tokens)` to `StoreUpstreamTokens(ctx, sessionID, providerName, tokens)`.

### 2.6 Deduplication (Invariant #7)

For step-up flows where `ExistingTSID` is set, the authorize handler checks storage before redirecting to the upstream:

```go
if pending.ExistingTSID != "" {
    _, err := h.storage.GetUpstreamTokens(ctx, pending.ExistingTSID, selectedName)
    if err == nil {
        // Tokens already exist -- issue auth code without re-authenticating.
        // A concurrent request may have completed the step-up.
        // ... writeAuthorizationResponse with existing TSID ...
        return
    }
}
```

This is a best-effort check. If two concurrent requests both pass the check, the second callback overwrites the first's tokens (idempotent -- no data corruption, just an extra browser redirect). Full distributed locking is deferred.

### 2.7 Server Construction Changes

`server_impl.go` changes:

- `server` struct: replaces single `upstreamIDP` with `upstreams map[string]upstream.OAuth2Provider`
- `newServer`: iterates `cfg.Upstreams`, creates each provider via the factory, stores in map. First entry becomes `defaultUpstream`.
- `NewHandler` called with the map and default name instead of a single provider.

The `Server` interface gains a method to expose upstream providers (needed by the adapter):

```go
type Server interface {
    Handler() http.Handler
    IDPTokenStorage() storage.UpstreamTokenStorage
    UpstreamProviders() map[string]upstream.OAuth2Provider  // NEW
    Close() error
}
```

### 2.8 Shared Callback URL

All upstream providers redirect to the same `/oauth/callback` endpoint. The AS disambiguates using its internal `state` parameter — the `InternalState` stored in `PendingAuthorization` maps back to `UpstreamProviderName`, so the callback knows which provider responded. Upstream providers (GitHub, Atlassian, etc.) simply echo the state back; they have no awareness of multi-upstream.

Each upstream provider's OAuth app must be registered with callback URL `{issuer}/oauth/callback`. This is standard single-callback-endpoint setup.

---

## 3. UpstreamTokenSource Adapter (C-01)

### 3.1 Package Location

```
pkg/authserver/tokensource/
    tokensource.go
    tokensource_test.go
```

Lives under `pkg/authserver/` because it depends on auth server storage and upstream provider types. Implements the `UpstreamTokenSource` interface defined in `pkg/vmcp/auth/types/types.go` (owned by UC-02).

### 3.2 Adapter Structure

```go
type Adapter struct {
    storage   storage.UpstreamTokenStorage
    upstreams map[string]upstream.OAuth2Provider  // for token refresh
}

func NewAdapter(
    stor storage.UpstreamTokenStorage,
    upstreams map[string]upstream.OAuth2Provider,
) *Adapter
```

### 3.3 GetToken Contract

`GetToken(ctx context.Context, providerName string) (string, error)`:

1. Extract `Identity` from context via `auth.IdentityFromContext`. If absent, return `ErrNoIdentity`.
2. Extract TSID from `Identity.Claims[session.TokenSessionIDClaimKey]`. If missing, return `ErrNoTSID`.
3. Call `storage.GetUpstreamTokens(ctx, tsid, providerName)`.
4. On `ErrNotFound` → return `&autherrors.ErrUpstreamTokenNotFound{ProviderName: providerName}`.
5. Validate binding fields (Section 3.4). Binding is checked BEFORE refresh to prevent an unauthorized caller from triggering a refresh of someone else's token.
6. If expired → attempt refresh using the upstream provider's `RefreshTokens` method, update storage, return the fresh access token. If refresh fails, return `ErrUpstreamTokenNotFound`.
7. Return `tokens.AccessToken`.

### 3.4 Binding Field Validation (Invariant #6)

After retrieving tokens, the adapter validates:

- `tokens.UserID` matches `identity.Subject` (if both non-empty)
- `tokens.ClientID` matches `identity.Claims["client_id"]` (if both non-empty)

Mismatch returns `storage.ErrInvalidBinding`. Empty binding fields on stored tokens are treated as "not bound" (permissive -- supports tokens stored before binding was implemented).

### 3.5 Error Handling

- `ErrNoIdentity`: returned when `Identity` is not in the request context (auth middleware did not run or anonymous mode)
- `ErrNoTSID`: returned when `Identity` exists but has no `tsid` claim (JWT was not issued by the AS)
- `ErrUpstreamTokenNotFound`: returned when storage returns `ErrNotFound`, or when refresh fails
- `storage.ErrInvalidBinding`: returned when binding validation fails (correct TSID, wrong UserID/ClientID)
- Other storage errors: wrapped generically without exposing token values or session IDs

`ErrNoIdentity` and `ErrNoTSID` are distinct from `ErrUpstreamTokenNotFound` — they indicate a configuration or pipeline error, not a missing step-up. Callers should NOT trigger step-up auth for these.

### 3.6 Invariant #8 Compliance (No Tokens in Logs)

The adapter never logs token values (access, refresh, ID tokens). Error messages include only provider names (configuration values, not secrets). The `slog` calls use structural metadata only.

---

## 4. Config Changes

### 4.1 Removing the Multi-Upstream Rejection

In `pkg/authserver/config.go`, `validateUpstreams`, remove:

```go
if len(c.Upstreams) > 1 {
    return fmt.Errorf("multiple upstreams not yet supported (found %d)", len(c.Upstreams))
}
```

The rest of `validateUpstreams` (name uniqueness, per-upstream validation) is already multi-upstream-ready.

### 4.2 Provider Name Validation

For multi-upstream configs, names MUST be explicitly set (no relying on the `"default"` fallback):

```go
if len(c.Upstreams) > 1 {
    for _, up := range c.Upstreams {
        if up.Name == "" || up.Name == "default" {
            return fmt.Errorf("upstream names must be explicitly set when multiple upstreams are configured")
        }
    }
}
```

For single-upstream, the existing default-to-`"default"` behavior is preserved.

### 4.3 ScopesSupported Auto-Population

When multiple upstreams are configured, automatically add `upstream:<name>` scopes to `ScopesSupported` in `applyDefaults`. This advertises them in the OIDC discovery document for client discovery.

### 4.4 Deprecate `GetUpstream()`

`Config.GetUpstream()` returns only the first upstream. With multi-upstream, callers should iterate `cfg.Upstreams` directly. Mark `GetUpstream()` as deprecated; new code in `server_impl.go` uses the loop.

---

## 5. Test Impact

### 5.1 Unit Tests Requiring Changes

| Test file | What changes |
|-----------|-------------|
| `pkg/authserver/storage/memory_test.go` | All `UpstreamTokens` tests gain `providerName` arg; add multi-provider test cases (store two providers under same session, retrieve each, delete session wipes both) |
| `pkg/authserver/storage/redis_test.go` | Same as memory; verify new key pattern |
| `pkg/authserver/storage/redis_integration_test.go` | Same as redis_test with real Redis |
| `pkg/authserver/storage/mocks/mock_storage.go` | Regenerate via `mockgen` (interface changed) |
| `pkg/authserver/config_test.go` | Test that `len > 1` no longer rejected; test name uniqueness; test explicit name required for multi |
| `pkg/authserver/server/handlers/helpers_test.go` | `testStorageState.upstreamTokens` changes to nested map; mock expectations updated for new `StoreUpstreamTokens` signature |
| `pkg/authserver/server/handlers/authorize_test.go` | Test scope-based provider selection; test fallback to default; test `UpstreamAwareScopeStrategy` |
| `pkg/authserver/server/handlers/callback_test.go` | Test correct upstream selected from pending; test `pending.UpstreamProviderName` used as provider ID; test `ExistingTSID` reuse |
| `pkg/authserver/server/handlers/handlers_test.go` | `NewHandler` call updated to pass map + default |
| `pkg/authserver/server_test.go` | Multi-upstream server creation |
| `pkg/auth/upstreamswap/middleware_test.go` (if exists) | Test `ProviderName` config; storage call includes provider name |
| `pkg/runner/middleware_test.go` | `addUpstreamSwapMiddleware` passes provider name; mock expectations updated |
| `pkg/runner/runner_test.go` | `GetUpstreamTokenStorage` tests may need signature updates |

### 5.2 Integration Tests Requiring Changes

| Test file | What changes |
|-----------|-------------|
| `pkg/authserver/integration_test.go` | `setupTestServer` must create upstream providers with `Name()` support; `StoreUpstreamTokens` calls updated; add test case for multi-upstream flow (authorize with `upstream:github` scope, callback stores under `"github"`, token retrieval succeeds) |
| `pkg/authserver/storage/redis_integration_test.go` | Test multi-provider store/get/delete against real Redis |

### 5.3 New Tests

| Package | Test |
|---------|------|
| `pkg/authserver/tokensource/` | Adapter: binding validation, token-not-found, missing TSID, missing identity, expired tokens |

---

## 6. Summary of File Changes

| File | Change |
|------|--------|
| `pkg/authserver/storage/types.go` | Add `providerName` to `StoreUpstreamTokens` and `GetUpstreamTokens`; add `UpstreamProviderName` and `ExistingTSID` to `PendingAuthorization` |
| `pkg/authserver/storage/memory.go` | Nested map for upstream tokens; update all upstream token methods + cleanup + user deletion |
| `pkg/authserver/storage/redis.go` | New key pattern; session index set; updated store/get/delete + Lua scripts |
| `pkg/authserver/storage/redis_keys.go` | Add `redisUpstreamKey` helper |
| `pkg/authserver/storage/mocks/mock_storage.go` | Regenerate |
| `pkg/authserver/config.go` | Remove `len > 1` rejection; require explicit names for multi-upstream; auto-populate `upstream:<name>` scopes |
| `pkg/authserver/server.go` | Add `UpstreamProviders()` to `Server` interface |
| `pkg/authserver/server_impl.go` | Create all providers in loop; store as map; pass to handler; expose via `UpstreamProviders()` |
| `pkg/authserver/upstream/oauth2.go` | No interface changes (handler owns name mapping) |
| `pkg/authserver/server/provider.go` | Replace `fosite.ExactScopeStrategy` with `UpstreamAwareScopeStrategy` |
| `pkg/authserver/server/handlers/handler.go` | Replace `upstream` with `upstreams` map + `defaultUpstream`; update `NewHandler` signature |
| `pkg/authserver/server/handlers/authorize.go` | Scope-based provider selection; populate `UpstreamProviderName` on pending; secondary routing validation |
| `pkg/authserver/server/handlers/callback.go` | Look up upstream by name from pending; use `pending.UpstreamProviderName` not `Type()`; support `ExistingTSID`; pass `providerName` to `StoreUpstreamTokens`; use custom scope strategy |
| `pkg/authserver/server/registration/dcr.go` | `ValidateScopes()` accepts `upstream:` prefixed scopes without requiring them in `ScopesSupported` |
| `pkg/authserver/tokensource/tokensource.go` | **NEW** -- `UpstreamTokenSource` adapter |
| `pkg/auth/upstreamswap/middleware.go` | Add `ProviderName` to config; pass to storage call |
| `pkg/runner/middleware.go` | `addUpstreamSwapMiddleware` populates `ProviderName` from auth server config |
