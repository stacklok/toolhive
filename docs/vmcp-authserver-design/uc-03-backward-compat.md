# UC-03: Backward Compatibility

**Produces**: —
**Consumes**: C-04 (must not change existing config shapes), C-05 (must not reject existing valid configs)

## Overview

The auth server integration introduces changes across three layers: factory signatures, strategy constructors, and storage interfaces. This UC catalogs every change, classifies its backward-compatibility impact, and defines the invariants that guarantee existing deployments continue working without configuration changes.

The core principle: **when `UpstreamTokenSource` is nil (no auth server), every code path must be identical to today's behavior.** This is the backward-compat litmus test for every change.

---

## 1. Change Classification

Every change falls into one of three categories:

| Category | Definition | Risk |
|----------|-----------|------|
| **Compile-time break** | Function/method signature changes that prevent compilation. All callers must be updated. | Mechanical — add nil/default arg. |
| **Behavioral preservation** | Code compiles but internal logic changes. Must verify equivalence on the no-AS path. | Requires invariant proof. |
| **Additive-only** | New types, constants, files, or struct fields with `omitempty`. Zero impact on existing code. | None. |

---

## 2. Compile-Time Breaks

### 2.1 `NewOutgoingAuthRegistry` Signature

**File**: `pkg/vmcp/auth/factory/outgoing.go`

| | Signature |
|---|---|
| **Current** | `NewOutgoingAuthRegistry(_ context.Context, envReader env.Reader) (OutgoingAuthRegistry, error)` |
| **Proposed** | `NewOutgoingAuthRegistry(_ context.Context, envReader env.Reader, upstreamTokenSource UpstreamTokenSource) (OutgoingAuthRegistry, error)` |

Adding a required positional parameter is a hard compile-time break. Every caller must add a third argument.

**Caller migration table**:

| Caller | File | Line | Fix | Type |
|--------|------|------|-----|------|
| Production | `cmd/vmcp/app/commands.go` | 241 | Pass `nil` (UC-05 wires the real source later) | Mechanical |
| Integration helper | `test/integration/vmcp/helpers/vmcp_server.go` | 147 | Pass `nil` | Mechanical |
| Unit test (6 calls) | `pkg/vmcp/auth/factory/outgoing_test.go` | 25, 49, 65, 107, 126, 152 | Pass `nil` | Mechanical |
| Integration test (3 calls) | `pkg/vmcp/auth/factory/integration_test.go` | 40, 124, 236 | Pass `nil` | Mechanical |

**Total: 11 call sites, all mechanical (add `nil`).**

### 2.2 `UpstreamTokenStorage.GetUpstreamTokens` Interface

**File**: `pkg/authserver/storage/types.go`

| | Signature |
|---|---|
| **Current** | `GetUpstreamTokens(ctx context.Context, sessionID string) (*UpstreamTokens, error)` |
| **Proposed** (UC-01) | `GetUpstreamTokens(ctx context.Context, sessionID, providerName string) (*UpstreamTokens, error)` |

This is an interface break affecting implementations, mocks, and all callers.

**Impact matrix**:

| Component | File | Fix | Type |
|-----------|------|-----|------|
| MemoryStorage impl | `pkg/authserver/storage/memory.go` | Add param, change internal map keying | Behavioral |
| RedisStorage impl | `pkg/authserver/storage/redis.go` | Add param, change Redis key pattern | Behavioral |
| Generated mocks (2) | `pkg/authserver/storage/mocks/mock_storage.go` | Regenerate via `task gen` | Mechanical |
| upstreamswap middleware | `pkg/auth/upstreamswap/middleware.go:192` | Pass provider name (see §2.3) | Behavioral |
| Memory storage tests (~10 calls) | `pkg/authserver/storage/memory_test.go` | Add provider name to store+get calls | Mechanical |
| Redis storage tests (~9 calls) | `pkg/authserver/storage/redis_test.go` | Add provider name to store+get calls | Mechanical |
| Redis integration tests (~6 calls) | `pkg/authserver/storage/redis_integration_test.go` | Add provider name | Mechanical |
| Middleware tests (~7 expectations) | `pkg/auth/upstreamswap/middleware_test.go` | Add provider name to mock expectations | Mechanical |

**Note**: `StoreUpstreamTokens` gains the same `providerName` parameter, affecting `pkg/authserver/server/handlers/callback.go:127` and its test helper at `pkg/authserver/server/handlers/helpers_test.go:193`.

### 2.3 upstreamswap Middleware Migration

The `upstreamswap` middleware is the proxy runner's existing mechanism for injecting upstream tokens. It currently calls `GetUpstreamTokens(ctx, tsid)` with no provider concept.

**Important distinction**: The C-01 "default provider convention" (empty `providerName` → resolve to front-door provider) applies to `UpstreamTokenSource.GetToken()` in the vMCP strategy layer. The raw `UpstreamTokenStorage.GetUpstreamTokens()` interface in the auth server layer validates that `providerName` is non-empty (UC-01 §1.2). These are different abstraction layers with different contracts.

**Migration strategy** (per UC-01 §1.6): Add a `ProviderName` field to `upstreamswap.Config`. The middleware passes `cfg.ProviderName` to storage — never an empty string.

```go
// Updated Config:
type Config struct {
    HeaderStrategy   string `json:"header_strategy,omitempty" yaml:"header_strategy,omitempty"`
    CustomHeaderName string `json:"custom_header_name,omitempty" yaml:"custom_header_name,omitempty"`
    ProviderName     string `json:"provider_name" yaml:"provider_name"`
}

// Updated middleware call:
tokens, err := stor.GetUpstreamTokens(r.Context(), tsid, cfg.ProviderName)
```

**Defaulting for single-upstream**: The proxy runner's `addUpstreamSwapMiddleware` (in `pkg/runner/middleware.go`) populates `ProviderName` from the auth server's upstream config. For single-upstream deployments where the upstream name defaults to `"default"` (the existing `UpstreamRunConfig.Name` default), the middleware config also gets `"default"`. This is transparent — existing single-upstream deployments work without configuration changes because the provider name was already defaulted at config validation time.

---

## 3. Behavioral Preservation (No-AS Path)

These are the invariants that must hold when `UpstreamTokenSource` is nil (no auth server configured). Each invariant has a verification strategy.

### 3.1 Factory: `NewOutgoingAuthRegistry(ctx, envReader, nil)` ≡ today

**Invariant**: When `upstreamTokenSource` is nil, the registry contains exactly three strategies: `unauthenticated`, `header_injection`, `token_exchange`. The `upstream_inject` strategy is NOT registered.

**Why it holds**: The proposed factory code gates `upstream_inject` registration on `upstreamTokenSource != nil`:

```go
if upstreamTokenSource != nil {
    registry.RegisterStrategy("upstream_inject", ...)
}
```

When nil, the conditional is skipped. The three always-registered strategies use the same constructors as today.

**Verification**: The existing `outgoing_test.go` test "creates registry with all strategies registered" asserts exactly three strategies by name. After adding `nil` as the third arg, this test passes unchanged — proving the registry is identical. If `upstream_inject` were accidentally registered, `GetStrategy("upstream_inject")` would succeed, but the test doesn't check for it, so no false positive. Adding a negative assertion (`upstream_inject` NOT registered when nil) is recommended.

### 3.2 Constructor: `NewTokenExchangeStrategy(envReader)` ≡ today

**Invariant**: The variadic `opts ...TokenExchangeOption` accepts zero arguments. The `upstreamTokenSource` field remains nil (Go zero value for interface). Zero options means zero functional option callbacks are executed.

**Why it holds**: Go variadic parameters are source-compatible when adding to a function that had no variadic. `opts` receives a nil `[]TokenExchangeOption` slice. The `for _, opt := range opts` loop body executes zero times. The struct is identical to the current constructor's output.

**Verification**: All 7 existing test call sites in `tokenexchange_test.go` compile and pass without modification. The `CurrentTokenUsed` test (line 541) explicitly asserts that `identity.Token` is used as the RFC 8693 subject token — this serves as the backward-compatibility sentinel.

### 3.3 Authenticate: `identity.Token` as subject when no AS

**Invariant**: When `upstreamTokenSource` is nil, the `token_exchange` strategy uses `identity.Token` as the subject token — identical to the current code path.

**Why it holds**: The proposed code inserts a conditional before `createUserConfig`:

```go
subjectToken := identity.Token
if s.upstreamTokenSource != nil {
    // ...fetch upstream token...
    subjectToken = upstream
}
exchangeConfig := s.createUserConfig(config, subjectToken)
```

When `upstreamTokenSource` is nil, the `if` block is skipped, `subjectToken` remains `identity.Token`, and `createUserConfig(config, identity.Token)` is called — exactly as today's line 121.

**No downstream references**: After line 121, `identity.Token` is never used again in `Authenticate()`. The exchanged token (`token.AccessToken`) is injected into the request. The intermediate variable introduces no behavioral difference.

### 3.4 Caching: Server config cache is unaffected

**Invariant**: The strategy's `ExchangeConfig` cache (keyed by `buildCacheKey`) is per-backend-server, not per-user. The cache key does not include the subject token value or any reference to `upstreamTokenSource`.

**Why it holds**: `buildCacheKey` uses `TokenURL`, `ClientID`, `Audience`, `Scopes`, and `SubjectTokenType` — all from the backend config, not from the token source. `createUserConfig` always creates a fresh per-user copy with the token provider closure. Whether the token string came from `identity.Token` or `GetToken()` is irrelevant to caching.

### 3.5 SubjectTokenType: Override mechanism works for both paths

**Invariant**: The `SubjectTokenType` field on `TokenExchangeConfig` (default: `access_token`) is parsed from per-backend config, orthogonal to the token source. Existing deployments that override it (e.g., `subjectTokenType: "id_token"`) continue working.

**Why it holds**: `parseTokenExchangeConfig` reads `SubjectTokenType` from the backend config struct — this parsing happens before and independently of the subject token resolution. No change to `parseTokenExchangeConfig`, `getOrCreateServerConfig`, or `buildCacheKey`.

### 3.6 Error paths: No new errors on the no-AS path

**Invariant**: When `upstreamTokenSource` is nil, the error paths in `token_exchange.Authenticate()` remain exactly:
1. `"no identity found in context"`
2. `"identity has no token"`
3. `"invalid strategy configuration: ..."`
4. `"token exchange failed: ..."`

The new `"token_exchange: ..."` error wrapping `ErrUpstreamTokenNotFound` only fires when `upstreamTokenSource` is non-nil — a new deployment mode that didn't exist before.

### 3.7 Health check bypass: Unchanged

The health check `return nil` is the first statement in `Authenticate()`, before any of the new code. Both paths (AS and no-AS) skip authentication for health checks identically.

---

## 4. Additive-Only Changes (Zero Risk)

These changes have no backward-compatibility impact.

| Change | File | Why zero risk |
|--------|------|---------------|
| `UpstreamTokenSource` interface | `pkg/vmcp/auth/types/types.go` | New type, no existing implementors |
| `ErrUpstreamTokenNotFound` sentinel | `pkg/vmcp/auth/types/types.go` | New variable, not checked by existing code |
| `StrategyTypeUpstreamInject` constant | `pkg/vmcp/auth/types/types.go` | New constant, not referenced by existing code |
| `UpstreamInjectConfig` struct | `pkg/vmcp/auth/types/types.go` | New type |
| `UpstreamInject` field on `BackendAuthStrategy` | `pkg/vmcp/auth/types/types.go` | Pointer + `omitempty` — nil when absent from YAML/JSON |
| `UpstreamInjectStrategy` | `pkg/vmcp/auth/strategies/upstream_inject.go` | New file |
| `UpstreamInjectConverter` | `pkg/vmcp/auth/converters/upstream_inject.go` | New file |
| Converter registration | `pkg/vmcp/auth/converters/interface.go` | One `r.Register(...)` call added to `NewRegistry()` — purely additive |
| `TokenExchangeOption` type | `pkg/vmcp/auth/strategies/tokenexchange.go` | New type |
| `WithUpstreamTokenSource` function | `pkg/vmcp/auth/strategies/tokenexchange.go` | New function |
| `upstreamTokenSource` field on `TokenExchangeStrategy` | `pkg/vmcp/auth/strategies/tokenexchange.go` | Unexported, zero value is nil |
| `ExternalAuthTypeUpstreamInject` constant | `mcpexternalauthconfig_types.go` | New CRD constant |
| `UpstreamInjectSpec` struct | `mcpexternalauthconfig_types.go` | New CRD type |
| Regenerated `zz_generated.deepcopy.go` | `pkg/vmcp/auth/types/` | Existing field deep-copy is unaffected |

---

## 5. Config Deserialization Compatibility

**Question**: Can existing YAML/JSON configs deserialize into the new `BackendAuthStrategy` struct?

**Answer**: Yes. The new field is:

```go
UpstreamInject *UpstreamInjectConfig `json:"upstreamInject,omitempty" yaml:"upstreamInject,omitempty"`
```

When deserializing existing configs:
- The `upstreamInject` key is absent from input.
- Go JSON/YAML decoders leave the pointer field as `nil`.
- The `Type` field remains one of the existing values.
- The struct is byte-for-byte equivalent to what current code produces.

No deserialization error. No validation triggered (the field is only validated when `Type == "upstream_inject"`).

---

## 6. Validation Compatibility

### 6.1 Config Validator (`pkg/vmcp/config/validator.go`)

**Current allowlist** (line 187-193):

```go
validTypes := []string{
    authtypes.StrategyTypeUnauthenticated,
    authtypes.StrategyTypeHeaderInjection,
    authtypes.StrategyTypeTokenExchange,
}
```

**Change**: Add `authtypes.StrategyTypeUpstreamInject` to the allowlist.

**Backward-compat impact**: None. Adding to an allowlist never invalidates currently-valid values. Existing configs with `unauthenticated`, `header_injection`, or `token_exchange` remain valid. The error message string changes cosmetically (includes the new type), but this is informational.

**New validation case**: A `case authtypes.StrategyTypeUpstreamInject:` block is added to the switch statement (lines 199-220). This only fires when `Type == "upstream_inject"`. Existing types bypass it entirely.

### 6.2 CRD Validation (`mcpexternalauthconfig_types.go`)

**Equivalence check pattern** (lines 681-697): A new check is added:

```go
if (r.Spec.UpstreamInject == nil) == (r.Spec.Type == ExternalAuthTypeUpstreamInject) {
    return fmt.Errorf("upstreamInject must be set when and only when type is upstreamInject")
}
```

**Backward-compat for existing types**: For any existing CRD object where `Type != "upstreamInject"` and `UpstreamInject == nil`:
- `(nil == true)` → `true`
- `(Type == upstreamInject)` → `false`
- `true == false` → `false` → no error

Existing objects pass the new check.

**Unauthenticated type check** (lines 700-706): Must add `r.Spec.UpstreamInject != nil` to the "no config when unauthenticated" guard. Existing unauthenticated CRDs have `UpstreamInject == nil`, so the extended check still passes.

### 6.3 Cross-Cutting Validation (UC-05)

UC-05 adds rules like "reject `upstream_inject` when no AS configured" and "reject provider name not in AS upstream list." These are new rules for a new strategy type. They cannot fire for existing configs because existing configs don't use `upstream_inject`. Zero backward-compat risk.

---

## 7. Test Impact Summary

### 7.1 Mechanical Changes (Add nil/default arg)

| Test file | Calls to update | Change |
|-----------|----------------|--------|
| `pkg/vmcp/auth/factory/outgoing_test.go` | 6 | Add `nil` third arg to `NewOutgoingAuthRegistry` |
| `pkg/vmcp/auth/factory/integration_test.go` | 3 | Add `nil` third arg to `NewOutgoingAuthRegistry` |
| `test/integration/vmcp/helpers/vmcp_server.go` | 1 | Add `nil` third arg to `NewOutgoingAuthRegistry` |
| `pkg/auth/upstreamswap/middleware_test.go` | ~7 | Add provider name to `GetUpstreamTokens` mock expectations |
| `pkg/authserver/storage/memory_test.go` | ~10 | Add provider name to `Store`/`GetUpstreamTokens` calls |
| `pkg/authserver/storage/redis_test.go` | ~9 | Add provider name to `Store`/`GetUpstreamTokens` calls |
| `pkg/authserver/storage/redis_integration_test.go` | ~6 | Add provider name to `Store`/`GetUpstreamTokens` calls |
| `pkg/authserver/server/handlers/helpers_test.go` | 1 | Add `gomock.Any()` to `StoreUpstreamTokens` expectation + update `DoAndReturn` closure signature to accept `providerName string` |
| `pkg/authserver/storage/mocks/mock_storage.go` | — | Regenerate via `task gen` |

### 7.2 No Changes Required

| Test file | Why |
|-----------|-----|
| `pkg/vmcp/auth/strategies/tokenexchange_test.go` | Variadic constructor is backward-compatible. All 7 call sites work unchanged. The `CurrentTokenUsed` test is the backward-compat sentinel. |

### 7.3 Recommended New Tests (Additive)

| Test | Purpose |
|------|---------|
| Factory with nil: `upstream_inject` NOT registered | Negative assertion that `GetStrategy("upstream_inject")` returns error when `upstreamTokenSource` is nil |
| Factory with mock: `upstream_inject` IS registered | Positive assertion when `upstreamTokenSource` is non-nil |
| `token_exchange` with mock source: upstream token used as subject | Verify the AS-configured path uses `GetToken(ctx, "")` result |
| `token_exchange` with mock source returning error: propagation | Verify `ErrUpstreamTokenNotFound` propagates as `"token_exchange: ..."` |

---

## 8. Migration Ordering

The changes can be applied in two independent streams (matching the UC-01 / UC-02 split):

### Stream A: vMCP Side (UC-02 changes)

1. Add additive types to `pkg/vmcp/auth/types/types.go` (interface, sentinel, config, constant)
2. Add `upstream_inject` strategy and converter (new files), register converter in `converters/interface.go`
3. Add variadic options to `TokenExchangeStrategy` constructor
4. Add upstream token resolution to `TokenExchangeStrategy.Authenticate()`
5. Update factory signature, add conditional `upstream_inject` registration
6. Update all callers with `nil` — **all existing tests pass at this point**
7. Update validator with new type and case
8. Regenerate `zz_generated.deepcopy.go`

### Stream B: Auth Server Side (UC-01 changes)

1. Update `UpstreamTokenStorage` interface (add `providerName`)
2. Update MemoryStorage and RedisStorage implementations
3. Update `upstreamswap` middleware to pass `cfg.ProviderName`
4. Update `callback.go` to pass provider name from `PendingAuthorization`
5. Update all storage tests and mock expectations
6. Regenerate mocks via `task gen`

**Streams A and B are independent at compile time.** Stream A introduces the `UpstreamTokenSource` interface (consumed by strategies). Stream B changes the storage interface (consumed by the adapter that implements `UpstreamTokenSource`). The adapter itself (UC-01) bridges both — it is implemented after both streams.

---

## 9. Invariant Compliance

| Invariant | How UC-03 complies |
|-----------|--------------------|
| #1 Multi-upstream | N/A — UC-03 verifies existing single-upstream behavior is preserved |
| #2 Boundary preserved | No changes to boundary model. Incoming auth is untouched. |
| #3 Existing schemes | Core guarantee of this UC. Every change is verified to preserve existing behavior when AS is absent. |
| #4 TSID/session independence | N/A — UC-03 does not touch TSID or vMCP session constructs |
| #5 AS is optional | `nil` token source → identical behavior to today. No silent fallback. |
| #6 Tokens are user-bound | N/A — binding enforcement is in the adapter (UC-01), not in the code UC-03 verifies |
| #7 At most one step-up | N/A — step-up flows are a new feature (UC-06), not an existing behavior to preserve |
| #8 No tokens in logs | No new log statements in changed code paths. |

---

## 10. Known Limitations

### 10.1 Storage Key Migration (Redis)

When the `GetUpstreamTokens` key pattern changes from `{prefix}upstream:{sessionID}` to `{prefix}upstream:{sessionID}:{providerName}`, existing Redis entries under the old key format will not be found by the new code. Since upstream access tokens are short-lived (typically 1 hour), this is acceptable — tokens expire naturally during a rolling upgrade. No data migration is needed.

For in-memory storage, the issue is moot — the store is ephemeral and cleared on restart.

### 10.2 Error Message Drift

The config validator's error message for invalid strategy types changes from listing three types to listing four. Any code or test that matches the exact error string (e.g., `assert.Contains(t, err.Error(), "unauthenticated, header_injection, token_exchange")`) will break. The current tests use `assert.Contains(t, err.Error(), "type must be one of")` which is stable — but downstream consumers (if any) matching the full list will need updating.

### 10.3 CRD Schema Regeneration

Adding the `UpstreamInject` field to `MCPExternalAuthConfigSpec` and `BackendAuthStrategy` requires CRD schema regeneration:

```bash
task operator-generate
task operator-manifests
cd cmd/thv-operator && task crdref-gen
```

The regenerated CRD YAML will include the new field as optional. Existing CRD objects in Kubernetes that lack the field will default to `nil` — no migration needed.
