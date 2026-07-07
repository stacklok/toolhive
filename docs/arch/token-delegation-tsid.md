# Delegated tokens and upstream credential access (`tsid`)

## Background

ToolHive's authorization server issues JWTs that carry a `tsid` claim —
an opaque session identifier linking the token to the user's stored upstream
IdP credentials (e.g., GitHub/Google access + refresh tokens). When the
proxy middleware validates a JWT, it reads `tsid` and loads the upstream
tokens into the request context, making them available to backend auth
strategies like `upstream_inject`, `tokenexchange`, and `aws_sts`.

When an agent obtains a delegated token via RFC 8693 token exchange, the
handler intentionally does **not** carry `tsid` forward into the delegated
JWT (`pkg/authserver/server/tokenexchange/handler.go`). This is a
deliberate security decision, not an oversight.

## The invariant

> **A delegated token (one carrying an `act` claim) MUST NOT carry `tsid`.**

Rationale:
- `tsid` is a handle to the user's **entire** upstream credential store
  across **all** providers, not a scoped credential. Carrying it on a
  delegated token bypasses the scope and audience narrowing the token
  exchange handler enforces.
- RFC 8693 §8 and RFC 9700 §4.8 emphasize least-privilege for issued
  tokens. A session reference that dereferences to all upstream
  credentials is the opposite of least privilege.
- No standard OAuth pattern exists where an issued token carries a
  reference to a stored multi-credential set. OAuth wants the AS to be
  the credential broker; tokens should be capability-bearing but
  store-unaware.

## Consequence

Delegated tokens work with these backend auth strategies:
- `header_injection` (static configured token)
- `xaa` (uses the incoming JWT directly for JWT-bearer grant)
- `unauthenticated`

Delegated tokens do **not** work with:
- `upstream_inject` — returns 401 "upstream authentication required"
- `tokenexchange` — no subject token in `UpstreamTokens` to exchange
- `aws_sts` — no token for STS `AssumeRoleWithWebIdentity`

This is a known limitation. The agent cannot use delegation to access
backends that require the user's raw upstream credentials.

## Future solution: 2-leg RFC 8693 exchange

The long-term fix is a chained token exchange pattern where the AS
remains the credential broker:

1. **Leg 0** (existing): User authenticates via authorization code flow.
   AS issues user JWT with `tsid`. Upstream tokens stored server-side.

2. **Leg 1** (implemented): Agent exchanges user JWT for a delegated JWT
   with `act` claim. No `tsid` on the delegated token.

3. **Leg 2** (future): When the MCP proxy needs an upstream token (e.g.,
   GitHub), it calls the AS `/oauth/token` endpoint again:
   ```
   grant_type=token-exchange
   subject_token=<delegated JWT from leg 1>
   resource=https://api.github.com
   scope=repo:read
   ```
   The AS validates the delegated token, resolves `tsid` **server-side**
   (using the per-user reverse index `GetLatestUpstreamTokensForUser` at
   `pkg/authserver/storage/redis.go:1095`), refreshes the stored GitHub
   token via standard OAuth 2.0 `refresh_token` grant, and returns it as
   the RFC 8693 `access_token` response.

### Properties of the 2-leg approach

- The `tsid` never leaves the AS. The agent and MCP proxy only see
  scoped, audience-bound, short-lived tokens.
- Scope is enforced as AS policy (the AS only returns the token if the
  delegated scope permits). The upstream token itself carries the
  original consent-time scopes — GitHub and most providers don't support
  scope-narrowing at refresh time.
- No cooperation required from the upstream IdP. The AS to GitHub leg is
  standard OAuth 2.0 `refresh_token` grant (RFC 6749 section 6), already
  supported by all major IdPs.
- Full audit trail via `act` claim nesting (RFC 8693 section 4.3).

### Implementation dependencies

- RFC 8693 section 4.3 `act` chain nesting (currently flat-overwrite:
  `handler.go` overwrites `act` instead of nesting)
- New or extended backend strategy that calls back to the AS instead of
  reading `UpstreamTokens` directly
- AS-side support for resolving upstream credentials by user subject
  (the per-user index already exists in Redis storage)

### Comparison with other platforms

| Platform | Approach | Stores upstream creds? | act/may_act? |
|---|---|---|---|
| ToolHive (current) | No tsid on delegated tokens | Yes (server-side, session-indexed) | act yes, may_act yes |
| agentgateway (Solo.io) | Stateless per-request exchange | No: agent brings token every request | Yes (first-class actor_token/may_act) |
| obot | Broker model: per-(user,MCP) store | Yes (per-(user,MCP) DB rows) | No (trusted broker, no act) |

## References

- `pkg/authserver/server/tokenexchange/handler.go` — token exchange handler
- `pkg/authserver/server/session/session.go:116` — `session.New` (sets `tsid`)
- `pkg/auth/token.go:1176-1192` — `loadUpstreamTokens` (reads `tsid`)
- `pkg/auth/upstreamswap/middleware.go:179-183` — upstream swap 401 on missing token
- `pkg/authserver/storage/redis.go:1095-1143` — `GetLatestUpstreamTokensForUser` (per-user index)
- `pkg/authserver/storage/redis.go:815-870` — upstream token storage with TTL
- RFC 8693 section 4.1, 4.3 — `act`/`may_act` claim semantics
- RFC 9700 section 4.8 — token replay prevention
