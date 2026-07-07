# Refresh tokens for delegated tokens

## Status

Not implemented. This is a design note for a follow-up PR atop the
token exchange handler.

## Problem

Delegated tokens (RFC 8693 token exchange) are short-lived — capped at
`min(subject_remaining, delegationLifespan)`, typically <= 1 hour. The
handler does not issue refresh tokens. When a delegated token expires
mid-session, the MCP client gets a 401 with no recovery path short of
re-running token exchange (which requires the original subject token,
which may also be expired).

## Proposed approach

Issue a refresh token alongside the delegated access token. The agent
uses the standard `refresh_token` grant to obtain a new delegated
access token without re-presenting the subject token.

### Prerequisites

- The `offline_access` scope must be requested and granted (already in
  the default scopes, `pkg/authserver/config.go:71`)
- The client must have the `refresh_token` grant type registered
- The handler needs `RefreshTokenStrategy` and `RefreshTokenStorage`
  wired in (currently only `AccessTokenStrategy`/`AccessTokenStorage`
  via `HandleHelper`)

### Implementation sketch (3 parts)

1. **Wire refresh token dependencies** into `Handler` and `Factory`:
   - Add `refreshTokenStrategy oauth2.RefreshTokenStrategy` and
     `refreshTokenStorage oauth2.RefreshTokenStorage` to `Handler`
   - Wire them in `Factory` from the `strategy`/`storage` params
     (same comma-ok pattern as access token strategy/storage)

2. **Issue refresh token** in `PopulateTokenEndpointResponse`:
   ```go
   if requester.GetGrantedScopes().HasOneOf("offline_access") &&
       client.GetGrantTypes().Has("refresh_token") {
       refresh, refreshSig, err := h.refreshTokenStrategy.GenerateRefreshToken(ctx, requester)
       if err != nil { return err }
       if err := h.refreshTokenStorage.CreateRefreshTokenSession(ctx, refreshSig, accessSig, requester.Sanitize([]string{})); err != nil {
           return err
       }
       responder.SetExtra("refresh_token", refresh)
   }
   ```

3. **Bound the refresh token's lifetime** to the subject token's expiry:
   In `HandleTokenEndpointRequest`, after computing `lifetime`:
   ```go
   delegatedSession.SetExpiresAt(fosite.RefreshToken, validatedClaims.Expiry)
   ```
   This ensures the refresh token expires when the subject token expires,
   preventing indefinite delegation.

### Fosite constraint (the known limitation)

Fosite's `RefreshTokenGrantHandler` (`flow_refresh.go:107-111`)
**unconditionally overwrites** both the access token and refresh token
`ExpiresAt` on refresh:

```go
request.GetSession().SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(atLifespan))
request.GetSession().SetExpiresAt(fosite.RefreshToken, time.Now().UTC().Add(rtLifespan))
```

This means:
- The refreshed **access token** gets the default `AccessTokenLifespan`
  (e.g., 1 hour), NOT the `min(subject_remaining, delegationLifespan)` cap.
- The refreshed **refresh token** gets the default `RefreshTokenLifespan`
  (e.g., 7 days), NOT the subject token's expiry.

The `act` claim IS preserved across refresh — fosite clones the original
session (`flow_refresh.go:87`: `request.SetSession(originalRequest.GetSession().Clone())`),
so `JWTClaims.Extra["act"]` survives.

### Workaround for the constraint

Two options:

**Option A (pragmatic, recommended for first iteration)**:
Set the refresh token's session `ExpiresAt` to `subjectExpiry` at leg-1
time. Fosite's refresh handler will overwrite the access token expiry to
the default lifespan, but the **storage layer** (Redis TTL) will evict
the refresh token session at `subjectExpiry`. After that, refresh fails
with `invalid_grant` (refresh token not found in storage).

Tradeoff: individual access tokens from refresh may outlive the subject
token by up to `AccessTokenLifespan`, but the delegation window is
bounded by the refresh token's storage TTL.

**Option B (correct, future work)**:
Write a custom `TokenEndpointHandler` for the `refresh_token` grant that
wraps fosite's and re-applies the `min(subject_remaining, delegationLifespan)`
cap after fosite's handler sets the session. This requires either:
- Replacing fosite's `OAuth2RefreshTokenGrantFactory` in the provider
  chain, or
- Adding a post-refresh hook that runs after fosite's handler

This is the right long-term solution but is an architectural change to
the fosite handler chain.

### What to test

- Refresh token issued when `offline_access` scope granted
- Refresh token NOT issued when `offline_access` not granted
- Refresh token NOT issued when client lacks `refresh_token` grant
- Refreshed access token has `act` claim preserved
- Refresh fails after subject token expiry (storage TTL eviction)
- Refresh fails for wrong client (fosite's `client_id` check at
  `flow_refresh.go:83`)

## References

- `pkg/authserver/server/tokenexchange/handler.go` — token exchange handler
- `pkg/authserver/server/tokenexchange/factory.go` — factory wiring
- fosite `flow_refresh.go:87,107-111` — refresh handler (session clone + expiry overwrite)
- fosite `flow_authorize_code_token.go:106-113,148-172` — refresh token issuance pattern
- `pkg/authserver/storage/redis.go:457` — `RefreshTokenStorage` implementation
- `pkg/authserver/server_impl.go:336` — `OAuth2RefreshTokenGrantFactory` already wired
