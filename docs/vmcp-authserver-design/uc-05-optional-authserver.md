# UC-05: Optional Auth Server

**Produces**: C-05 (validation rules)
**Consumes**: C-01 (nil contract), C-04 (config types to validate)

## Overview

vMCP operates in two modes: **without an auth server** (existing behavior, "Mode A") and **with an embedded auth server** (new, "Mode B"). UC-05 defines the config model, server wiring, HTTP route mounting, validation rules, and nil-safe behavior for the optional AS. It does not define strategies (UC-02), storage (UC-01), or step-up signaling (UC-06).

**Key constraint — dynamic backends**: In `outgoingAuth.source: discovered` mode, backends and their auth strategies arrive at runtime from Kubernetes (`MCPExternalAuthConfig` CRDs). Startup-time config validation can only validate statically-configured backends. Dynamically-discovered backends require validation at the CRD/operator level and at runtime. UC-05 addresses all three validation points.

---

## 1. Mode Matrix

Every component behaves differently depending on whether the AS is configured:

| Component | Mode A (no AS) | Mode B (with AS) |
|-----------|---------------|-----------------|
| `Config.AuthServer` | nil | `*AuthServerConfig` with `RunConfig` |
| `UpstreamTokenSource` | nil | Pre-wired adapter (UC-01) |
| Strategy registry | 3 strategies: `unauthenticated`, `header_injection`, `token_exchange` | 4 strategies: + `upstream_inject` |
| `token_exchange` subject token | `identity.Token` (incoming JWT) | Front-door provider's upstream token |
| `identity.Token` value | External IDP JWT | TH-JWT (AS-issued) |
| `identity.Claims["tsid"]` | Absent | Present (links to upstream token storage) |
| AS HTTP routes | Not mounted | `/oauth/*`, `/.well-known/{openid-configuration,oauth-authorization-server,jwks.json}` |
| OIDC discovery | External IDP (or none) | Self-referencing loopback to embedded AS |
| Incoming auth middleware | Validates external JWT | Validates TH-JWT via AS's JWKS |

**Invariant**: Mode A behavior is byte-for-byte identical to today. The `AuthServer` field is optional with `omitempty`. When nil, no AS code paths execute.

---

## 2. Config Model

### 2.1 New Field on Config

```go
// pkg/vmcp/config/config.go

type Config struct {
    // ... existing fields unchanged ...

    // AuthServer configures the embedded OAuth authorization server.
    // When nil, vMCP operates without an auth server — only existing
    // auth strategies are available (Mode A).
    // When present, enables upstream token management and upstream_inject
    // strategy (Mode B).
    // +optional
    AuthServer *AuthServerConfig `json:"authServer,omitempty" yaml:"authServer,omitempty"`
}

// AuthServerConfig wraps the auth server's RunConfig for vMCP.
// +kubebuilder:object:generate=true
// +gendoc
type AuthServerConfig struct {
    // RunConfig is the embedded auth server configuration.
    // See pkg/authserver.RunConfig for field documentation.
    RunConfig *authserver.RunConfig `json:"runConfig" yaml:"runConfig"`
}
```

### 2.2 Relationship to IncomingAuthConfig

`AuthServerConfig` and `IncomingAuthConfig` are independent config sections with a semantic link: when the AS is the incoming auth provider, `IncomingAuth.OIDC.Issuer` must match `AuthServer.RunConfig.Issuer` (V-04, Section 5), and `IncomingAuth.OIDC.Audience` must be in `AuthServer.RunConfig.AllowedAudiences` (V-07, Section 5).

The OIDC middleware discovers the AS via standard `/.well-known/openid-configuration` — no special-casing needed. The AS is a standard OIDC provider from the middleware's perspective.

### 2.3 Config Example

```yaml
# Mode B: vMCP with embedded auth server
authServer:
  runConfig:
    issuer: "http://localhost:4483"
    upstreams:
      - name: corporate-idp
        type: oidc
        oidc_config:
          issuer_url: "https://sso.corp.example.com"
          client_id: "vmcp-prod"
          client_secret_env_var: "CORP_IDP_CLIENT_SECRET"
      - name: github
        type: oauth2
        oauth2_config:
          authorization_endpoint: "https://github.com/login/oauth/authorize"
          token_endpoint: "https://github.com/login/oauth/access_token"
          client_id: "gh-client-id"
          client_secret_env_var: "GITHUB_CLIENT_SECRET"
    allowed_audiences:
      - "http://localhost:4483"
    storage:
      type: memory

incomingAuth:
  type: oidc
  oidc:
    issuer: "http://localhost:4483"       # must match authServer.runConfig.issuer (V-04)
    audience: "http://localhost:4483"      # must be in authServer.runConfig.allowed_audiences (V-07)

outgoingAuth:
  source: inline
  backends:
    github-tools:
      type: upstream_inject               # requires authServer (V-01)
      upstreamInject:
        providerName: github              # must be in authServer upstreams (V-02)
    corporate-api:
      type: token_exchange                # subject token = corporate-idp upstream token (V-03 warning)
      tokenExchange:
        tokenUrl: https://corp-sts.example.com/token
        audience: https://corp-api.example.com
    internal-api:
      type: header_injection              # no auth server involvement
      headerInjection:
        headerName: X-API-Key
        headerValueEnv: INTERNAL_API_KEY
```

---

## 3. Server Wiring Sequence

### 3.1 Startup Flow

```go
// cmd/vmcp/app/commands.go — in runServe()

cfg, err := loadAndValidateConfig(configPath)
// validation includes V-01..V-07 for static backends (Section 5)

// Step 1: Conditional AS creation
var authServer *runner.EmbeddedAuthServer
var upstreamTokenSource authtypes.UpstreamTokenSource
if cfg.AuthServer != nil {
    authServer, err = runner.NewEmbeddedAuthServer(ctx, cfg.AuthServer.RunConfig)
    if err != nil {
        return fmt.Errorf("failed to create embedded auth server: %w", err)
        // Hard failure — no silent fallback (Invariant #5)
    }
    defer authServer.Close()
    upstreamTokenSource = authServer.TokenSource()  // UC-01 adapter
}

// Step 2: Pass token source to backend discovery/factory
backends, backendClient, outgoingRegistry, err :=
    discoverBackends(ctx, cfg, upstreamTokenSource)  // nil when no AS

// Step 3: Pass AS handler to server config
var authServerHandler http.Handler
if authServer != nil {
    authServerHandler = authServer.Handler()
}
serverCfg.AuthServerHandler = authServerHandler
```

**`discoverBackends`** gains a third parameter (`upstreamTokenSource`) which it passes to `factory.NewOutgoingAuthRegistry`. When nil, the factory does not register `upstream_inject` and does not enrich `token_exchange` (UC-02 §4.1, UC-03 §3.1).

### 3.2 EmbeddedAuthServer.TokenSource()

New method on `EmbeddedAuthServer`, delegating to the `Server` interface's `TokenSource()` (defined by UC-01 §2.7). Returns a pre-wired `UpstreamTokenSource` adapter that knows the default (front-door) provider.

```go
// pkg/authserver/runner/embeddedauthserver.go

// TokenSource returns the UpstreamTokenSource adapter for outgoing auth strategies.
// Returns nil if no upstream IDP is configured.
func (e *EmbeddedAuthServer) TokenSource() authtypes.UpstreamTokenSource {
    return e.server.TokenSource()
}
```

### 3.3 Reference Pattern: Proxy Runner

The proxy runner already integrates the embedded AS following this exact pattern:

```go
// pkg/runner/runner.go:257-275 (existing code)
if r.Config.EmbeddedAuthServerConfig != nil {
    r.embeddedAuthServer, err = authserverrunner.NewEmbeddedAuthServer(ctx, r.Config.EmbeddedAuthServerConfig)
    handler := r.embeddedAuthServer.Handler()
    transportConfig.PrefixHandlers = map[string]http.Handler{
        "/oauth/":                                  handler,
        "/.well-known/oauth-authorization-server":  handler,
        "/.well-known/openid-configuration":        handler,
        "/.well-known/jwks.json":                   handler,
    }
}
```

vMCP follows the same lifecycle (create → mount → close) but uses `http.NewServeMux` for route registration instead of `PrefixHandlers`.

---

## 4. HTTP Route Mounting

### 4.1 Constraints

1. AS endpoints must be **unauthenticated** — they are the auth flow itself. A client calling `/oauth/authorize` does not have a token yet.
2. AS endpoints must be served at the **same origin** as the OIDC issuer URL. The OIDC middleware resolves `{issuer}/.well-known/openid-configuration` — if the issuer is `http://localhost:4483`, the AS's discovery endpoint must be reachable at that origin.
3. AS endpoints must be **non-overlapping** with MCP endpoints and the existing RFC 9728 Protected Resource Metadata endpoint.

### 4.2 Route Table

```
Unauthenticated (mounted on mux directly, no auth middleware):
  /health, /ping, /readyz, /status, /api/backends/health   (existing)
  /metrics                                                  (existing, if telemetry)
  /.well-known/oauth-protected-resource                     (existing, RFC 9728)
  /.well-known/openid-configuration                         (NEW, AS only)
  /.well-known/oauth-authorization-server                   (NEW, AS only)
  /.well-known/jwks.json                                    (NEW, AS only)
  /oauth/                                                   (NEW, AS only)

Authenticated (auth middleware wraps the handler):
  / (catch-all)                                             (MCP endpoint)
```

### 4.3 Implementation in server.go

```go
// pkg/vmcp/server/server.go — in Handler()

// Auth server endpoints (conditional, unauthenticated)
if s.config.AuthServerHandler != nil {
    mux.Handle("/oauth/", s.config.AuthServerHandler)
    mux.Handle("/.well-known/openid-configuration", s.config.AuthServerHandler)
    mux.Handle("/.well-known/oauth-authorization-server", s.config.AuthServerHandler)
    mux.Handle("/.well-known/jwks.json", s.config.AuthServerHandler)
}

// RFC 9728 Protected Resource Metadata — explicit paths (not catch-all)
if wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler); wellKnownHandler != nil {
    mux.Handle("/.well-known/oauth-protected-resource", wellKnownHandler)
    mux.Handle("/.well-known/oauth-protected-resource/", wellKnownHandler)
}
```

**Breaking change from current code**: The existing `mux.Handle("/.well-known/", wellKnownHandler)` at `server.go:493` is a catch-all that would swallow the AS's discovery endpoints. It must be replaced with explicit path registrations for `/.well-known/oauth-protected-resource`. This is a non-breaking behavioral change — the only `/.well-known/` path the current handler serves is `oauth-protected-resource`; all others return 404.

The server `Config` struct gains:

```go
// AuthServerHandler is the HTTP handler for the embedded auth server's
// OAuth/OIDC endpoints. nil when no auth server is configured.
AuthServerHandler http.Handler
```

### 4.4 Self-Referencing OIDC Discovery

When the AS is embedded, the OIDC middleware validates TH-JWTs by discovering the JWKS at `{issuer}/.well-known/openid-configuration`. This is a loopback request — the middleware makes an HTTP call to itself.

**Why this works**: Go's HTTP server processes requests concurrently. The middleware's outbound discovery request is served by the same server process. The `TokenValidator` in `pkg/auth/token.go` uses lazy discovery (deferred to first validation request) with exponential backoff retry. By the time a client sends a request with a TH-JWT, the AS's discovery endpoint is already serving.

**Startup timing**: `TokenValidator` is created during middleware setup (before `http.ListenAndServe`). Discovery is deferred to the first real token validation. The retry logic (500ms initial, 2s max interval) handles the window between server start and listener readiness.

---

## 5. Validation Rules (C-05)

### 5.1 The Dynamic Backend Problem

| Mode | Backend auth source | When known | Validation point |
|------|-------------------|------------|-----------------|
| **inline** | YAML config (`OutgoingAuth.Backends`) | Startup | Config validator |
| **discovered** | `MCPExternalAuthConfig` CRDs | Runtime | Operator reconciler + runtime |

Startup-time validation covers inline-mode backends and `OutgoingAuth.Default`. Dynamically-discovered backends are validated by the operator's deployment controller during reconciliation and by the runtime strategy registry as a safety net.

### 5.2 Rule Table

| ID | Rule | Severity | Validation points | Rationale |
|----|------|----------|-------------------|-----------|
| V-01 | `upstream_inject` configured but no AS | **Error** | S, K, R | Strategy requires `UpstreamTokenSource` which is nil without AS. Runtime: `"strategy not registered"`. |
| V-02 | `upstream_inject` provider name not in AS upstream config | **Error** | S, K | Typo or config drift. Would cause `ErrUpstreamTokenNotFound` on every request. |
| V-03 | `token_exchange` with AS as incoming auth provider | **Warning** | S | Subject token changes from `identity.Token` (TH-JWT) to upstream front-door token. Backend STS must trust the upstream IDP, not the TH-AS. |
| V-04 | AS `issuer` ≠ `incomingAuth.oidc.issuer` | **Error** | S | OIDC middleware validates the JWT `iss` claim against the configured issuer. If they differ, every AS-issued token is rejected. |
| V-05 | AS `RunConfig` fails validation | **Error** | S | Delegates to `RunConfig.Validate()` (existing). Catches missing upstreams, invalid issuer URL, etc. |
| V-06 | `upstream_inject` provider name is empty | **Error** | S, K | Structural validation in `validateBackendAuthStrategy`. |
| V-07 | `incomingAuth.oidc.audience` not in AS `allowedAudiences` | **Error** | S | AS issues tokens with audience from `AllowedAudiences`. If the incoming OIDC audience isn't in that list, the AS either can't issue tokens with the right audience, or the middleware rejects tokens with the wrong audience. |

Legend: **S** = startup config validation, **K** = Kubernetes operator reconciler, **R** = runtime safety net.

### 5.3 Startup-Time Implementation

The config validator gains two additions:

**1. Structural** (in `validateBackendAuthStrategy`):

Add `upstream_inject` to the `validTypes` allowlist. Add a switch case:

```go
case authtypes.StrategyTypeUpstreamInject:
    if strategy.UpstreamInject == nil {
        return fmt.Errorf("upstreamInject requires UpstreamInject configuration")
    }
    if strategy.UpstreamInject.ProviderName == "" {
        return fmt.Errorf("upstreamInject requires providerName field")
    }
```

**2. Cross-cutting** (new `validateAuthServerIntegration` called from `Validate`):

```go
func (v *DefaultValidator) validateAuthServerIntegration(cfg *Config) error {
    asConfigured := cfg.AuthServer != nil && cfg.AuthServer.RunConfig != nil
    asIsIncomingAuth := asConfigured &&
        cfg.IncomingAuth != nil &&
        cfg.IncomingAuth.Type == IncomingAuthTypeOIDC &&
        cfg.IncomingAuth.OIDC != nil &&
        cfg.IncomingAuth.OIDC.Issuer == cfg.AuthServer.RunConfig.Issuer

    // Validate AS RunConfig if present (V-05)
    if asConfigured {
        if err := cfg.AuthServer.RunConfig.Validate(); err != nil {
            return fmt.Errorf("authServer: %w", err)
        }
    }

    // V-04: Issuer consistency
    if asConfigured && cfg.IncomingAuth != nil &&
        cfg.IncomingAuth.Type == IncomingAuthTypeOIDC &&
        cfg.IncomingAuth.OIDC != nil {
        // If the AS issuer looks like it's intended to be the incoming auth
        // issuer but doesn't match exactly, it's a misconfiguration.
        // Note: we only error if both are OIDC and both have issuers set.
        // If incomingAuth uses a different issuer, the AS is not the incoming
        // auth provider — that's valid (AS only provides outgoing tokens).
    }

    // V-07: Audience consistency (only when AS is the incoming auth provider)
    if asIsIncomingAuth {
        audience := cfg.IncomingAuth.OIDC.Audience
        if !containsString(cfg.AuthServer.RunConfig.AllowedAudiences, audience) {
            return fmt.Errorf("incomingAuth.oidc.audience %q is not in authServer.runConfig.allowedAudiences %v; "+
                "the AS cannot issue tokens with the expected audience", audience, cfg.AuthServer.RunConfig.AllowedAudiences)
        }
    }

    // Collect statically-configured backend strategies
    strategies := collectAllBackendStrategies(cfg.OutgoingAuth)

    for backendName, strategy := range strategies {
        switch strategy.Type {
        case authtypes.StrategyTypeUpstreamInject:
            // V-01: upstream_inject requires AS
            if !asConfigured {
                return fmt.Errorf("outgoingAuth backend %q uses upstream_inject but no authServer is configured", backendName)
            }
            // V-02: provider name must exist in AS upstreams
            if !hasUpstreamProvider(cfg.AuthServer.RunConfig, strategy.UpstreamInject.ProviderName) {
                return fmt.Errorf("outgoingAuth backend %q references upstream provider %q which is not in authServer.runConfig.upstreams",
                    backendName, strategy.UpstreamInject.ProviderName)
            }
        case authtypes.StrategyTypeTokenExchange:
            // V-03: warn about subject token semantics change
            if asIsIncomingAuth {
                slog.Warn("token_exchange with embedded AS: subject token will be the upstream front-door token, not the TH-JWT; "+
                    "ensure the backend STS trusts the upstream IDP",
                    "backend", backendName,
                )
            }
        }
    }

    return nil
}
```

`collectAllBackendStrategies` returns only statically-configured strategies (the `OutgoingAuth.Default` and `OutgoingAuth.Backends` map). In discovered mode, this is a subset — dynamic backends are the operator's responsibility.

### 5.4 Operator Reconciler Validation

The deployment controller performs cross-resource validation during reconciliation:

1. Resolves the `VirtualMCPServer`'s auth server config.
2. For each backend in the `MCPGroup` with an `externalAuthConfigRef`, resolves the `MCPExternalAuthConfig`.
3. Applies V-01 and V-02 against the resolved strategies.
4. Invalid configurations are surfaced as status conditions on the `VirtualMCPServer`, preventing the vMCP pod from starting with an invalid config.

---

## 6. Nil-Safe Behavior

What each component does when `Config.AuthServer` is nil (Mode A):

| Component | Behavior when AS absent | Code path |
|-----------|------------------------|-----------|
| `EmbeddedAuthServer` | Not created | `if cfg.AuthServer != nil` guard in `runServe()` |
| `UpstreamTokenSource` | nil | Declared as `var`, zero value |
| `NewOutgoingAuthRegistry(ctx, env, nil)` | Registers 3 strategies. Does NOT register `upstream_inject`. `token_exchange` has nil `upstreamTokenSource` field → uses `identity.Token` as subject. | UC-02 §4.1: `if upstreamTokenSource != nil` guard |
| `upstream_inject` strategy lookup | Returns `"strategy not registered"` error | `OutgoingAuthRegistry.GetStrategy()` |
| `token_exchange.Authenticate()` | Uses `identity.Token` directly | UC-02 §3.2: `if s.upstreamTokenSource != nil` guard |
| AS HTTP routes | Not mounted | `if s.config.AuthServerHandler != nil` guard in `Handler()` |
| `/.well-known/` paths | Only `oauth-protected-resource` served (RFC 9728) | Explicit path registration |
| V-01..V-07 validation | V-01/V-02 only fire if `upstream_inject` is in config. V-03/V-04/V-07 only fire if AS is configured. No false positives for Mode A. | Conditional checks in `validateAuthServerIntegration` |
| `authServer.Close()` | Not called | `defer` only inside `if cfg.AuthServer != nil` block |

**No nil pointer risk**: Every AS-dependent code path is guarded by a nil check on either `cfg.AuthServer`, `upstreamTokenSource`, or `s.config.AuthServerHandler`. The factory's conditional registration ensures that `upstream_inject` cannot be invoked when the source is nil.

---

## 7. Error Behavior

| Scenario | Behavior | User-facing message |
|----------|----------|-------------------|
| AS configured but fails to start | Hard startup failure. Process exits. No fallback to Mode A (Invariant #5). | `"failed to create embedded auth server: ..."` |
| `upstream_inject` without AS (inline, V-01) | Startup failure | `"backend X uses upstream_inject but no authServer is configured"` |
| `upstream_inject` without AS (discovered, runtime) | Request-time error | `"strategy not registered: upstream_inject"` |
| Provider name typo (inline, V-02) | Startup failure | `"backend X references upstream provider Y which is not in authServer.runConfig.upstreams"` |
| Provider name typo (discovered, runtime) | Request-time error | `"upstream_inject: provider Y: upstream token not found"` (every request) |
| Issuer mismatch (V-04) | Startup failure (when detectable) | `"incomingAuth.oidc.issuer X does not match authServer.runConfig.issuer Y"` |
| Audience mismatch (V-07) | Startup failure | `"incomingAuth.oidc.audience X is not in authServer.runConfig.allowedAudiences"` |
| AS config removed while pod running | Dynamic backends with `upstream_inject` fail at request time | Acceptable: operator triggers reconciliation → pod restart |

---

## 8. Invariant Compliance

| Invariant | How UC-05 complies |
|-----------|--------------------|
| #1 Multi-upstream | Config model accepts `RunConfig.Upstreams` with multiple providers. V-02 validates provider names against the full upstream list. |
| #2 Boundary preserved | AS wired to outgoing auth via `UpstreamTokenSource` (Boundary 2). Incoming auth uses standard OIDC middleware. AS routes are unauthenticated — separate from the MCP auth boundary. |
| #3 Existing schemes | Nil `AuthServer` = identical behavior to today (Section 6). `AuthServer` field is optional with `omitempty`. V-01/V-02 only fire for `upstream_inject`. |
| #4 TSID/session independence | N/A — UC-05 does not touch TSID or vMCP session constructs. The wiring passes `UpstreamTokenSource` to the factory, which abstracts TSID handling. |
| #5 AS is optional | Core guarantee of this UC. Nil config → nil token source → `upstream_inject` not registered → no fallback. Sections 1, 6, 7 prove this throughout. |
| #6 Tokens are user-bound | Enforced by the UC-01 adapter, not by UC-05 wiring. UC-05 passes the adapter through; it does not bypass binding checks. |
| #7 At most one step-up | N/A — step-up deduplication is UC-06's concern. The AS validation (V-05) ensures the AS config is valid, which includes upstream provider uniqueness. |
| #8 No tokens in logs | Validation error messages contain only config values (strategy types, provider names, issuer URLs). AS startup errors from `RunConfig` are also config-level. |

---

## 9. File Changes

| File | Change |
|------|--------|
| `pkg/vmcp/config/config.go` | Add `AuthServer *AuthServerConfig` to `Config`; add `AuthServerConfig` struct |
| `pkg/vmcp/config/validator.go` | Add `validateAuthServerIntegration`; extend `validateBackendAuthStrategy` for `upstream_inject`; add `collectAllBackendStrategies`, `hasUpstreamProvider` helpers |
| `pkg/vmcp/config/zz_generated.deepcopy.go` | Regenerate |
| `cmd/vmcp/app/commands.go` | Conditional AS creation; pass `upstreamTokenSource` to `discoverBackends`; pass `AuthServerHandler` to server config |
| `pkg/vmcp/server/server.go` | Add `AuthServerHandler` to server `Config`; mount AS routes conditionally; replace `/.well-known/` catch-all with explicit path registrations |
| `pkg/authserver/runner/embeddedauthserver.go` | Add `TokenSource() UpstreamTokenSource` method |
| `pkg/authserver/server.go` | Add `TokenSource()` to `Server` interface (UC-01 dependency) |
| `cmd/thv-operator/api/v1alpha1/virtualmcpserver_types.go` | Add `AuthServer *AuthServerSpec` to `VirtualMCPServerSpec`; add `AuthServerSpec`, `AuthServerConfigRef` types |
| `cmd/thv-operator/pkg/vmcpconfig/converter.go` | Resolve `AuthServerSpec` → `AuthServerConfig` during CRD conversion |

---

## 10. FAQ

### Why is AuthServer a top-level config field, not part of IncomingAuth?

The AS serves both incoming auth (OIDC issuer) and outgoing auth (upstream token source). Nesting it under `IncomingAuth` would imply it's only about incoming auth and would create a confusing dependency where outgoing auth strategies reference a field inside the incoming auth config.

The AS is an independent server component that happens to be used by both auth boundaries. The semantic links (issuer consistency, audience consistency) are enforced by validation rules V-04 and V-07, not by config nesting.

### Can the AS be configured without being the incoming auth provider?

Yes. The AS could theoretically be configured only for outgoing auth (upstream token management) while incoming auth uses a different OIDC provider. In this case, V-04 does not fire (the issuers are intentionally different) and `identity.Token` is the external IDP's JWT, not a TH-JWT.

However, this mode has limited utility: the `tsid` claim is only present in TH-JWTs, so the `UpstreamTokenSource` adapter cannot extract the TSID from external JWTs. This mode is only useful if the upstream token lookup uses a different identity mapping (future work). For the current design, the expected configuration is AS-as-incoming-auth.

### What about the proxy runner's AS integration — is the vMCP pattern different?

The lifecycle is identical: create → mount → use → close. The differences are:
- Proxy runner uses `PrefixHandlers` map on `TransportConfig`. vMCP uses explicit `mux.Handle()` calls.
- Proxy runner provides `IDPTokenStorage()` for the `upstreamswap` middleware. vMCP provides `TokenSource()` which wraps storage with the UC-01 adapter (adds binding validation, default provider resolution, token refresh).
- Proxy runner's upstream swap operates at the HTTP middleware level (before the MCP handler). vMCP's outgoing auth strategies operate inside the MCP handler (per-backend, per-request).

### Why is V-04 an Error and not a Warning?

If the AS issuer and the incoming OIDC issuer differ, the OIDC middleware will reject every token the AS issues because the `iss` claim doesn't match. This is not a "might cause problems" situation — it's a guaranteed hard failure. Catching it at startup with a clear error message is far better than debugging "invalid token" errors at request time.

### What happens to the self-referencing OIDC discovery on the very first request?

The `TokenValidator` uses lazy discovery with retry (exponential backoff, 500ms initial, 2s max). On the first authenticated request, the middleware calls `{issuer}/.well-known/openid-configuration` which is a loopback to the same server. Go's `net/http` server handles this concurrently — the discovery request is processed by a different goroutine. The retry handles the edge case where the first request arrives before the listener is fully ready. Typical latency: 0-500ms on the very first request, zero thereafter (cached).

---

## 11. Known Limitations

### 11.1 V-04 Cannot Detect All Issuer Mismatches

V-04 compares `incomingAuth.oidc.issuer` with `authServer.runConfig.issuer` as strings. If they use different representations of the same URL (e.g., `http://localhost:4483` vs `http://127.0.0.1:4483`), the string comparison fails but the configuration may be functionally valid. Normalizing URLs is error-prone (port normalization, trailing slashes, scheme). The current approach errs on the side of strictness — users must use the exact same string in both places.

### 11.2 Dynamic Backend Validation Is Eventually Consistent

In discovered mode, a newly-created `MCPExternalAuthConfig` with `upstream_inject` is validated by the operator reconciler, not by the vMCP process. There is a window between CRD creation and reconciliation where the vMCP pod may attempt to use an unvalidated strategy. The runtime safety net (`"strategy not registered"`) catches this, but the error message is less helpful than the startup-time validation message.

### 11.3 No Hot-Reload of AS Configuration

If the AS configuration changes (e.g., new upstream provider added), the vMCP process must be restarted. The `EmbeddedAuthServer` is created once at startup and the `UpstreamTokenSource` adapter is wired once. In Kubernetes, this is handled by the operator triggering a rolling update when the `VirtualMCPServer` spec changes.
