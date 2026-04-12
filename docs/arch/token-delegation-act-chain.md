# Delegation chain nesting (`act` claim)

## Status

Implemented. Token exchange preserves a prior RFC 8693 `act` claim by nesting
it under the newly resolved actor. The handler rejects malformed chains and
limits the resulting chain to ten levels.

## Behavior

When a delegated token is re-exchanged, the new actor is prepended to the
existing chain:

```json
{
  "act": {
    "sub": "new-actor",
    "act": {
      "sub": "prior-actor"
    }
  }
}
```

For an externally issued subject token, the trusted issuer provenance is also
nested before any existing chain. The handler parses the prior chain using the
shared audit parser, rejects malformed content, and caps the final depth to
avoid issuing unbounded tokens.

## References

- `pkg/authserver/server/tokenexchange/handler.go` — `buildActClaim`
- `pkg/authserver/server/tokenexchange/handler_test.go` — re-exchange and depth
  limit coverage
- RFC 8693 section 4.1 — `act` claim semantics
