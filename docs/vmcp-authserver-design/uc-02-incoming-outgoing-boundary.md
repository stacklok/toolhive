# UC-02: Incoming/Outgoing Auth Boundary

**Produces**: C-04 (config shape), C-01 (`UpstreamTokenSource` interface definition), C-03 (`ErrUpstreamTokenNotFound` sentinel)
**Consumes**: C-01 (strategies use `UpstreamTokenSource` adapter from UC-01)

## Overview

The front-door upstream provider serves double duty: it drives the incoming auth flow (user authenticates with corporate IDP → gets TH-JWT) and its tokens may also be needed for outgoing auth (exchange corporate token for backend access). This UC defines the `UpstreamTokenSource` interface, the new `upstream_inject` strategy, and the enhanced `token_exchange` strategy.

Reference scenario from the top-level design:

```
Backend 1 "github-tools":     upstream_inject  provider="github"        → inject GitHub token directly
Backend 2 "atlassian-tools":  upstream_inject  provider="atlassian"     → inject Atlassian token directly
Backend 3 "corporate-api":    token_exchange                            → exchange front-door token via RFC 8693
Backend 4 "internal-api":     header_injection                          → static API key (no auth server)
```

Backends 1-2 require explicit provider names (`upstream_inject` always targets a specific upstream). Backend 3 uses `token_exchange` which, when the AS is configured, automatically uses the front-door provider's upstream token — no provider name needed. Backend 4 is unchanged.

---

## 1. Interface and Error Definitions (C-01, C-03)

### 1.1 UpstreamTokenSource Interface

Defined in `pkg/vmcp/auth/types/types.go` (the leaf package with no vmcp dependencies):

```go
// UpstreamTokenSource resolves upstream IDP tokens for outgoing auth strategies.
// The implementation (provided by the auth server adapter, UC-01) extracts the
// TSID from Identity.Claims in the request context and calls UpstreamTokenStorage.
//
// Strategies depend on this interface, never on auth server internals.
// nil when no auth server is configured — the factory gates strategy registration on non-nil.
type UpstreamTokenSource interface {
    GetToken(ctx context.Context, providerName string) (string, error)
}
```

**Default provider convention**: When `providerName` is empty (`""`), the adapter resolves it to the AS's front-door provider. This is used by `token_exchange` which does not name a specific provider — it always exchanges the token from the user's initial authentication.

**Dependency flow** (all arrows point toward the leaf package):

```
strategies/upstream_inject.go  ──imports──> types.UpstreamTokenSource
strategies/tokenexchange.go    ──imports──> types.UpstreamTokenSource
factory/outgoing.go            ──imports──> types.UpstreamTokenSource
authserver/tokensource/        ──implements──> types.UpstreamTokenSource
```

No cycles. The interface depends only on `context.Context`.

### 1.2 ErrUpstreamTokenNotFound Sentinel

Co-located with the interface in `pkg/vmcp/auth/types/types.go`:

```go
// ErrUpstreamTokenNotFound indicates that no upstream token exists for the
// requested provider. This typically means the user has not yet authenticated
// with that upstream provider and step-up authentication is required.
// Callers should check for this error using errors.Is().
var ErrUpstreamTokenNotFound = errors.New("upstream token not found")
```

The adapter (UC-01) wraps this sentinel:

```go
// In the adapter's GetToken, when storage returns ErrNotFound:
return "", fmt.Errorf("provider %q: %w", providerName, authtypes.ErrUpstreamTokenNotFound)
```

Strategies propagate it with additional context via `%w`:

```go
// In upstream_inject:
return fmt.Errorf("upstream_inject: %w", err)
```

UC-06 intercepts it with `errors.Is(err, authtypes.ErrUpstreamTokenNotFound)`.

**Why a sentinel, not a typed error**: A sentinel with `errors.Is()` matching is sufficient for the current use case (binary "is this a step-up trigger or not?"). If UC-06 later needs to extract the provider name from the error, it can promote to a typed error at that point.

---

## 2. Config Type Changes (C-04)

### 2.1 New Strategy Type Constant

```go
// pkg/vmcp/auth/types/types.go

const (
    // ... existing constants ...

    // StrategyTypeUpstreamInject identifies the upstream inject strategy.
    // This strategy injects an upstream IDP token (obtained via the auth server)
    // directly as a Bearer token in outgoing requests.
    StrategyTypeUpstreamInject = "upstream_inject"
)
```

### 2.2 New UpstreamInjectConfig

```go
// UpstreamInjectConfig configures the upstream inject auth strategy.
// This strategy resolves an upstream IDP token by provider name and injects
// it directly as an Authorization: Bearer header.
// +kubebuilder:object:generate=true
// +gendoc
type UpstreamInjectConfig struct {
    // ProviderName identifies which upstream provider's token to inject.
    // Must match a provider name configured in the embedded auth server.
    ProviderName string `json:"providerName" yaml:"providerName"`
}
```

### 2.3 TokenExchangeConfig: No Changes

`TokenExchangeConfig` is **not modified**. There is no `UpstreamProviderName` field.

When the AS is configured, the `token_exchange` strategy automatically uses the front-door provider's upstream token as the subject. The discriminator is the presence of `UpstreamTokenSource` on the strategy (non-nil means AS is configured), not a per-backend config field.

Rationale: when the AS is the incoming auth provider, there is exactly one internal IDP. The front-door upstream token is always the right subject for token exchange. Naming the provider explicitly would be redundant — it's always the default.

### 2.4 Extended BackendAuthStrategy

Add the `UpstreamInject` discriminated union variant:

```go
type BackendAuthStrategy struct {
    // Type is the auth strategy: "unauthenticated", "header_injection",
    // "token_exchange", "upstream_inject"
    Type string `json:"type" yaml:"type"`

    // HeaderInjection contains configuration for header injection auth strategy.
    HeaderInjection *HeaderInjectionConfig `json:"headerInjection,omitempty" yaml:"headerInjection,omitempty"`

    // TokenExchange contains configuration for token exchange auth strategy.
    TokenExchange *TokenExchangeConfig `json:"tokenExchange,omitempty" yaml:"tokenExchange,omitempty"`

    // UpstreamInject contains configuration for upstream inject auth strategy.
    UpstreamInject *UpstreamInjectConfig `json:"upstreamInject,omitempty" yaml:"upstreamInject,omitempty"`
}
```

### 2.5 Config Example

```yaml
outgoingAuth:
  source: inline
  backends:
    github-tools:
      type: upstream_inject
      upstreamInject:
        providerName: github
    atlassian-tools:
      type: upstream_inject
      upstreamInject:
        providerName: atlassian
    corporate-api:
      type: token_exchange
      tokenExchange:
        tokenUrl: https://corp-sts.example.com/token
        audience: https://corp-api.example.com
    internal-api:
      type: header_injection
      headerInjection:
        headerName: X-API-Key
        headerValueEnv: INTERNAL_API_KEY
```

Note: `corporate-api` uses `token_exchange` with no provider name. When the AS is configured, the strategy automatically fetches the front-door provider's upstream token as the subject. When the AS is not configured, it uses `identity.Token` as today.

---

## 3. Strategy Implementations

### 3.1 New: UpstreamInjectStrategy

**File**: `pkg/vmcp/auth/strategies/upstream_inject.go`

```go
type UpstreamInjectStrategy struct {
    tokenSource authtypes.UpstreamTokenSource  // required, never nil
}

func NewUpstreamInjectStrategy(tokenSource authtypes.UpstreamTokenSource) *UpstreamInjectStrategy {
    return &UpstreamInjectStrategy{tokenSource: tokenSource}
}

func (*UpstreamInjectStrategy) Name() string {
    return authtypes.StrategyTypeUpstreamInject
}
```

**`Authenticate()`**:

```go
func (s *UpstreamInjectStrategy) Authenticate(
    ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
    if health.IsHealthCheck(ctx) {
        return nil
    }

    if strategy == nil || strategy.UpstreamInject == nil {
        return fmt.Errorf("upstream_inject configuration is required")
    }

    token, err := s.tokenSource.GetToken(ctx, strategy.UpstreamInject.ProviderName)
    if err != nil {
        return fmt.Errorf("upstream_inject: %w", err)
    }

    req.Header.Set("Authorization", "Bearer "+token)
    return nil
}
```

Key properties:
- Always injects as `Authorization: Bearer`. Custom headers are covered by `header_injection`.
- Propagates `ErrUpstreamTokenNotFound` via `%w` for UC-06 to intercept.
- Never logs the token value (Invariant #8).
- Health check bypass consistent with other strategies.

**`Validate()`**:

```go
func (s *UpstreamInjectStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
    if strategy == nil || strategy.UpstreamInject == nil {
        return fmt.Errorf("UpstreamInject configuration is required")
    }
    if strategy.UpstreamInject.ProviderName == "" {
        return fmt.Errorf("ProviderName is required in upstream_inject configuration")
    }
    return nil
}
```

Validates structural completeness only. Cross-cutting validation (provider name exists in AS config) belongs in UC-05.

### 3.2 Enhanced: TokenExchangeStrategy

**Constructor change** — functional options:

```go
type TokenExchangeOption func(*TokenExchangeStrategy)

func WithUpstreamTokenSource(src authtypes.UpstreamTokenSource) TokenExchangeOption {
    return func(s *TokenExchangeStrategy) {
        s.upstreamTokenSource = src
    }
}

type TokenExchangeStrategy struct {
    exchangeConfigs     map[string]*tokenexchange.ExchangeConfig
    mu                  sync.RWMutex
    envReader           env.Reader
    upstreamTokenSource authtypes.UpstreamTokenSource  // NEW, nil when no AS
}

func NewTokenExchangeStrategy(envReader env.Reader, opts ...TokenExchangeOption) *TokenExchangeStrategy {
    s := &TokenExchangeStrategy{
        exchangeConfigs: make(map[string]*tokenexchange.ExchangeConfig),
        envReader:       envReader,
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}
```

This is a backwards-compatible change — existing call sites pass zero options.

**`Authenticate()` change**:

```go
func (s *TokenExchangeStrategy) Authenticate(
    ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
    if health.IsHealthCheck(ctx) {
        return nil
    }

    identity, ok := auth.IdentityFromContext(ctx)
    if !ok {
        return fmt.Errorf("no identity found in context")
    }

    // Guard: identity.Token is required as the fallback subject token (no-AS path)
    // and as the TH-JWT for potential actor_token use (AS path). When AS is
    // configured, this is always a TH-JWT and never empty for authenticated users.
    if identity.Token == "" {
        return fmt.Errorf("identity has no token")
    }

    config, err := s.parseTokenExchangeConfig(strategy)
    if err != nil {
        return fmt.Errorf("invalid strategy configuration: %w", err)
    }

    // Resolve subject token: upstream (when AS configured) or identity.Token
    subjectToken := identity.Token
    if s.upstreamTokenSource != nil {
        // AS is configured — use the front-door provider's upstream token.
        // Empty provider name → adapter resolves to the default (front-door) provider.
        // Note: ErrUpstreamTokenNotFound here means the front-door token is missing
        // from storage (data loss or eviction), NOT a step-up scenario. UC-06 should
        // treat this differently from a missing step-up provider token.
        upstream, err := s.upstreamTokenSource.GetToken(ctx, "")
        if err != nil {
            return fmt.Errorf("token_exchange: %w", err)
        }
        subjectToken = upstream
    }

    exchangeConfig := s.createUserConfig(config, subjectToken)
    tokenSource := exchangeConfig.TokenSource(ctx)

    token, err := tokenSource.Token()
    if err != nil {
        return fmt.Errorf("token exchange failed: %w", err)
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
    return nil
}
```

The change is minimal: before `createUserConfig`, check if `upstreamTokenSource` is non-nil. If yes, call `GetToken(ctx, "")` to get the front-door upstream token. If no, use `identity.Token` as before.

**`SubjectTokenType` note**: When the upstream token is used, it's an OAuth 2.0 access token. The existing default of `access_token` (`urn:ietf:params:oauth:token-type:access_token`) is correct. The existing `SubjectTokenType` field allows override for edge cases.

**No changes to**: `parseTokenExchangeConfig`, `Validate`, `createUserConfig`, `getOrCreateServerConfig`, `buildCacheKey`. The internal config parsing remains identical — the subject token resolution is handled entirely in `Authenticate()`.

---

## 4. Factory Wiring (C-04)

### 4.1 Updated Factory Signature

```go
// pkg/vmcp/auth/factory/outgoing.go

func NewOutgoingAuthRegistry(
    _ context.Context,
    envReader env.Reader,
    upstreamTokenSource authtypes.UpstreamTokenSource, // nil when no AS
) (auth.OutgoingAuthRegistry, error) {
    registry := auth.NewDefaultOutgoingAuthRegistry()

    // Always register strategies that don't need the auth server
    if err := registry.RegisterStrategy(
        authtypes.StrategyTypeUnauthenticated,
        strategies.NewUnauthenticatedStrategy(),
    ); err != nil {
        return nil, err
    }
    if err := registry.RegisterStrategy(
        authtypes.StrategyTypeHeaderInjection,
        strategies.NewHeaderInjectionStrategy(),
    ); err != nil {
        return nil, err
    }

    // Token exchange: always registered, optionally enriched with upstream source
    teOpts := []strategies.TokenExchangeOption{}
    if upstreamTokenSource != nil {
        teOpts = append(teOpts, strategies.WithUpstreamTokenSource(upstreamTokenSource))
    }
    if err := registry.RegisterStrategy(
        authtypes.StrategyTypeTokenExchange,
        strategies.NewTokenExchangeStrategy(envReader, teOpts...),
    ); err != nil {
        return nil, err
    }

    // Upstream inject: only registered when auth server provides a token source
    if upstreamTokenSource != nil {
        if err := registry.RegisterStrategy(
            authtypes.StrategyTypeUpstreamInject,
            strategies.NewUpstreamInjectStrategy(upstreamTokenSource),
        ); err != nil {
            return nil, err
        }
    }

    return registry, nil
}
```

Key properties:
- `token_exchange` is always registered. When `upstreamTokenSource` is nil, it uses `identity.Token`. When non-nil, it fetches the front-door upstream token.
- `upstream_inject` is only registered when `upstreamTokenSource` is non-nil. A backend config referencing `upstream_inject` without an AS will get "strategy not found" — UC-05 catches this at validation time.

### 4.2 Caller Wiring

The factory is called from the vMCP server setup. The caller obtains the `UpstreamTokenSource` from the auth server:

```go
// Pseudocode — exact location depends on server wiring (UC-05)
var tokenSource authtypes.UpstreamTokenSource
if authServer != nil {
    tokenSource = authServer.TokenSource()  // pre-wired adapter, knows the default provider
}

registry, err := factory.NewOutgoingAuthRegistry(ctx, envReader, tokenSource)
```

---

## 5. CRD and Converter Changes

### 5.1 CRD Type: ExternalAuthType for upstream_inject

Add to `cmd/thv-operator/api/v1alpha1/mcpexternalauthconfig_types.go`:

```go
const (
    // ... existing constants ...

    // ExternalAuthTypeUpstreamInject is the type for upstream token injection
    // using the embedded auth server's upstream token storage.
    ExternalAuthTypeUpstreamInject ExternalAuthType = "upstreamInject"
)
```

Add `UpstreamInject` spec field to `MCPExternalAuthConfigSpec`:

```go
type MCPExternalAuthConfigSpec struct {
    // ... existing fields ...

    // UpstreamInject specifies configuration for the upstream inject auth type.
    // Required when type is "upstreamInject".
    UpstreamInject *UpstreamInjectSpec `json:"upstreamInject,omitempty"`
}

type UpstreamInjectSpec struct {
    // ProviderName identifies which upstream provider's token to inject.
    // Must match a provider name configured in the embedded auth server.
    ProviderName string `json:"providerName"`
}
```

### 5.2 New Converter: UpstreamInjectConverter

```go
// pkg/vmcp/auth/converters/upstream_inject.go

type UpstreamInjectConverter struct{}

func (*UpstreamInjectConverter) StrategyType() string {
    return authtypes.StrategyTypeUpstreamInject
}

func (*UpstreamInjectConverter) ConvertToStrategy(
    externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
    if externalAuth.Spec.UpstreamInject == nil {
        return nil, fmt.Errorf("upstreamInject spec is required for type upstreamInject")
    }
    return &authtypes.BackendAuthStrategy{
        Type: authtypes.StrategyTypeUpstreamInject,
        UpstreamInject: &authtypes.UpstreamInjectConfig{
            ProviderName: externalAuth.Spec.UpstreamInject.ProviderName,
        },
    }, nil
}

func (*UpstreamInjectConverter) ResolveSecrets(
    _ context.Context,
    _ *mcpv1alpha1.MCPExternalAuthConfig,
    _ client.Client,
    _ string,
    strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
    // No secrets to resolve — upstream tokens come from the auth server at runtime
    return strategy, nil
}
```

Register in `NewRegistry()`:

```go
r.Register(mcpv1alpha1.ExternalAuthTypeUpstreamInject, &UpstreamInjectConverter{})
```

### 5.3 CRD Validation

Add to `MCPExternalAuthConfig` validation using the existing equivalence check pattern (lines 683-695):

```go
// Add to the existing spec/type equivalence checks:
if (r.Spec.UpstreamInject == nil) == (r.Spec.Type == ExternalAuthTypeUpstreamInject) {
    return fmt.Errorf("upstreamInject must be set when and only when type is upstreamInject")
}

// Add to the per-type validation switch:
case ExternalAuthTypeUpstreamInject:
    if r.Spec.UpstreamInject.ProviderName == "" {
        return fmt.Errorf("upstreamInject.providerName is required")
    }
```

---

## 6. Invariant Compliance

| Invariant | How UC-02 complies |
|-----------|--------------------|
| #1 Multi-upstream | `upstream_inject` targets specific providers by name; `token_exchange` uses the front-door implicitly |
| #2 Boundary preserved | Strategies operate on Boundary 2 only. They call `UpstreamTokenSource` which abstracts Boundary 1 |
| #3 Existing schemes | `token_exchange` without AS works identically (`upstreamTokenSource` is nil → uses `identity.Token`). `header_injection` and `unauthenticated` untouched |
| #4 TSID/session independence | N/A — strategies do not interact with TSID or vMCP session constructs. Token resolution is fully abstracted by `UpstreamTokenSource` |
| #5 AS is optional | `UpstreamTokenSource` is nil when no AS. `upstream_inject` not registered. `token_exchange` falls back to `identity.Token` |
| #6 Tokens are user-bound | Enforced by the adapter (UC-01), not by strategies |
| #8 No tokens in logs | Strategies never log token strings. Error wrapping via `%w` uses provider names only |

---

## 7. Test Impact

### 7.1 Unit Tests Requiring Changes

| Test file | What changes |
|-----------|-------------|
| `pkg/vmcp/auth/strategies/tokenexchange_test.go` | `NewTokenExchangeStrategy` call updated (zero options for backward compat); add tests for upstream path: mock `UpstreamTokenSource`, verify upstream token used as subject; test `ErrUpstreamTokenNotFound` propagation |
| `pkg/vmcp/auth/factory/outgoing_test.go` | Add `upstreamTokenSource` param; test with nil (upstream_inject not registered) and non-nil (registered); verify `token_exchange` always registered |

### 7.2 New Tests

| Package | Test |
|---------|------|
| `pkg/vmcp/auth/strategies/upstream_inject_test.go` | Happy path: mock returns token, verify `Authorization: Bearer <token>` set. Error path: mock returns `ErrUpstreamTokenNotFound`, verify propagated. Health check: verify no-op. Validate: nil config → error, empty provider → error |
| `pkg/vmcp/auth/converters/upstream_inject_test.go` | ConvertToStrategy: valid spec → correct `BackendAuthStrategy`. Missing spec → error. Empty provider → error. ResolveSecrets: pass-through |

### 7.3 Mock Requirements

Generate a mock for `UpstreamTokenSource`:

```go
//go:generate mockgen -destination=mocks/mock_upstream_token_source.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth/types UpstreamTokenSource
```

---

## 8. Summary of File Changes

| File | Change |
|------|--------|
| `pkg/vmcp/auth/types/types.go` | Add `UpstreamTokenSource` interface, `ErrUpstreamTokenNotFound` sentinel, `StrategyTypeUpstreamInject` constant, `UpstreamInjectConfig` struct, `UpstreamInject` field on `BackendAuthStrategy` |
| `pkg/vmcp/auth/types/zz_generated.deepcopy.go` | Regenerate (new struct with kubebuilder markers) |
| `pkg/vmcp/auth/strategies/upstream_inject.go` | **NEW** — `UpstreamInjectStrategy` |
| `pkg/vmcp/auth/strategies/tokenexchange.go` | Add `upstreamTokenSource` field, `TokenExchangeOption` type, `WithUpstreamTokenSource`, update constructor to accept options, update `Authenticate` to resolve upstream token when source is non-nil |
| `pkg/vmcp/auth/factory/outgoing.go` | Add `upstreamTokenSource` parameter, conditional `upstream_inject` registration, pass option to `token_exchange` |
| `cmd/vmcp/app/commands.go` | Update `NewOutgoingAuthRegistry` call to pass `tokenSource` (nil when no AS) |
| `test/integration/vmcp/helpers/vmcp_server.go` | Update `NewOutgoingAuthRegistry` call to pass nil |
| `pkg/vmcp/auth/converters/upstream_inject.go` | **NEW** — `UpstreamInjectConverter` |
| `pkg/vmcp/auth/converters/interface.go` | Register `UpstreamInjectConverter` in `NewRegistry()` |
| `cmd/thv-operator/api/v1alpha1/mcpexternalauthconfig_types.go` | Add `ExternalAuthTypeUpstreamInject`, `UpstreamInjectSpec`, CRD validation |

---

## 9. Known Limitations

### 9.1 Actor Token (Deferred)

RFC 8693 delegation scenarios (sending TH-JWT as `actor_token` alongside the upstream `subject_token`) are not supported in this UC. The underlying `exchangeRequest` struct supports it, but `ExchangeConfig` does not expose `ActorToken`/`ActorTokenType` fields. This requires a focused change to `pkg/auth/tokenexchange/exchange.go` shared with the proxy runner.

When implemented, the config and strategy can be extended with an `ActorTokenMode` field. The implementation is ~10 lines in `Authenticate()`.

### 9.2 upstream_inject Is Bearer-Only

The `upstream_inject` strategy always injects as `Authorization: Bearer <token>`. APIs that require non-Bearer formats should use `header_injection` with a static credential. This covers every major OAuth2-protected API.

### 9.3 token_exchange Always Uses the Front-Door Provider

When the AS is configured, `token_exchange` always exchanges the front-door provider's token. There is no mechanism to exchange a step-up provider's token via RFC 8693. This is intentional — step-up provider tokens (e.g., GitHub) are meant for direct injection via `upstream_inject`, not for exchange. If a future use case requires exchanging a step-up token, `TokenExchangeConfig` can be extended with an optional `UpstreamProviderName` field at that point.
