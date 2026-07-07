# Delegation chain nesting (`act` claim)

## Status

Not implemented. Deferred — this is a prerequisite for the 2-leg
exchange and `actor_token` features, not for the current handler's
correctness.

## Problem

The token exchange handler unconditionally sets the `act` claim as a
flat object, overwriting any existing `act` claim from the subject
token (`pkg/authserver/server/tokenexchange/handler.go`):

```go
delegatedSession.JWTClaims.Extra["act"] = map[string]interface{}{
    "sub": actorID,
}
```

RFC 8693 section 4.3 specifies that for chained delegation, the `act`
claim MUST nest: `act: {sub: newActor, act: {sub: oldActor, ...}}`.
The current flat overwrite would erase the original actor from the
chain on re-exchange, breaking provenance for multi-hop delegation.

## Why it's safe today

The handler requires `aud == iss == this server` for subject tokens
(`validator.go:108-110`). Delegated tokens get `aud` from the requested
audience/resource, not from the issuer. So a delegated token presented
as a `subject_token` would fail the audience check — re-exchange is
blocked incidentally.

This protection is *incidental* on the audience config. If any client
is ever registered with the issuer as an allowed audience, a delegated
token could be re-exchanged and the original actor would be lost.

## When to implement

This fix is a dependency of:

1. **2-leg RFC 8693 exchange** (see `token-delegation-tsid.md`): leg 2
   re-exchanges a delegated token. The leg-2 token must carry
   `act: {sub: mcp-proxy, act: {sub: agent}}` to preserve the chain.

2. **`actor_token` support** (in the `token-delegation` branch): when
   an `actor_token` is presented, the handler must validate the
   incoming actor's `act` chain and extend it per RFC 8693 section 4.3
   ("the new act claim is added to the front of the chain").

## Implementation sketch

When the subject token already carries an `act` claim, nest it instead
of overwriting:

```go
// Extract existing act from subject token, if present.
var existingAct map[string]interface{}
if raw, ok := validatedClaims.Extra["act"]; ok {
    if m, ok := raw.(map[string]interface{}); ok {
        existingAct = m
    }
}

newAct := map[string]interface{}{"sub": actorID}
if existingAct != nil {
    newAct["act"] = existingAct
}
delegatedSession.JWTClaims.Extra["act"] = newAct
```

Note: `validatedClaims.Extra` currently routes `act` to the `default`
case in `buildValidatedClaims` (`validator.go`). The handler would need
to extract it from `Extra` or a new `ValidatedClaims.Act` field.

The `oauthproto.Act` type (attempted and reverted in the initial
review) would be the right vehicle for this when it lands — a typed
struct with recursive `Act *Act` field, `NewAct` for single-level,
and `ExtendAct` for chain construction. It was reverted because it
leaked a typed value into the Cedar converter; when re-introduced as
part of this feature, the Cedar converter should handle it via the
existing `map[string]interface{}` case (after JSON round-trip, the
type is a plain map anyway).

## References

- `pkg/authserver/server/tokenexchange/handler.go` — `act` claim construction
- `pkg/authserver/server/tokenexchange/validator.go` — `buildValidatedClaims` (Extra routing)
- RFC 8693 section 4.1, 4.3 — `act` claim semantics and chain nesting
- `docs/arch/token-delegation-tsid.md` — 2-leg exchange design (depends on this)
