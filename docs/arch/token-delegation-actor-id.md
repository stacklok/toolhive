# Actor identity shape in the `act` claim

## Status

Not implemented. Deferred — this is a cross-cutting identity-model
decision, not a handler-level fix.

## Problem

The token exchange handler sets `act.sub` to the raw OAuth client ID
via `client.GetID()` (`pkg/authserver/server/tokenexchange/handler.go`):

```go
actorID := client.GetID()
...
delegatedSession.JWTClaims.Extra["act"] = map[string]interface{}{
    "sub": actorID,
}
```

This produces values like `"devops-agent"` — a plain string client ID.

However, the Cedar authorization test policies match SPIFFE-style
identities (`pkg/authz/authorizers/cedar/core_test.go`):

```
context.claim_act.sub like "spiffe://toolhive.dev/ns/agents/sa/*"
```

with test fixtures using:

```go
"act": map[string]interface{}{
    "sub": "spiffe://toolhive.dev/ns/agents/sa/devops-agent",
},
```

A token issued by this handler would **not match** a Cedar policy
written against the SPIFFE pattern, because `act.sub` is
`"devops-agent"`, not `"spiffe://toolhive.dev/ns/agents/sa/devops-agent"`.

## Is this a bug?

No. The Cedar tests construct their own claim fixtures with SPIFFE
URIs directly — they are illustrative of *what policies could look
like* with SPIFFE-style identities, not testing this handler's output.
The handler produces a raw `client_id`, which is consistent with what
fosite uses for client identity throughout.

## The design question

Should the handler transform `client.GetID()` into a SPIFFE URI before
placing it in the `act` claim? This depends on:

1. **Agent identity model**: does toolhive's agent identity use SPIFFE
   URIs? The workload identity system does (`pkg/auth/identity.go`
   references SPIFFE), but the OAuth client registry stores plain
   string client IDs.

2. **Downstream consumers**: do Cedar policies, audit logs, and other
   consumers expect SPIFFE URIs or raw client IDs? The Cedar test
   policies suggest SPIFFE; the handler produces raw client IDs.

3. **Cross-cutting concern**: if SPIFFE URIs are the right identifier
   space, the transformation should happen at a shared layer (e.g., a
   client-to-SPIFFE resolver), not hardcoded in the token exchange
   handler. Other handlers that emit client identity (e.g., the
   standard token handler's `client_id` claim) would need the same
   transformation.

## Options

- **Option A (keep raw client_id)**: the handler emits `client.GetID()`
  as-is. Cedar policies must match against plain client IDs. Simplest,
  consistent with fosite, but doesn't align with the SPIFFE-based
  workload identity model.

- **Option B (transform to SPIFFE)**: the handler resolves the client
  ID to a SPIFFE URI before placing it in `act.sub`. Requires a
  client-to-SPIFFE resolver or a convention (e.g.,
  `spiffe://toolhive.dev/ns/agents/sa/<client_id>`). Aligns with the
  Cedar test policies and the workload identity model, but adds a
  transformation step that other handlers would also need.

- **Option C (store SPIFFE URI in client registry)**: the OAuth client
  registration carries a SPIFFE URI alongside the client ID. The
  handler uses the SPIFFE URI if present, falling back to `client_id`.
  Most flexible, but requires changes to client registration.

## Recommendation

Defer until the agent identity model is finalized. The current
behavior (raw `client_id`) is correct for the OAuth layer and doesn't
block any functionality — it's a mismatch between test fixtures and
handler output, not a runtime bug. When the identity model decision is
made, apply it as a cross-cutting concern, not a handler-specific fix.

## References

- `pkg/authserver/server/tokenexchange/handler.go` — `actorID` assignment and `act` claim
- `pkg/authz/authorizers/cedar/core_test.go` — Cedar policies with SPIFFE URIs
- `pkg/authz/authorizers/cedar/entity.go` — Cedar value conversion for `act` claim
- `pkg/auth/identity.go` — SPIFFE references in workload identity
