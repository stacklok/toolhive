# External Subject-Token Exchange (Delegation)

This document covers the embedded authorization server's RFC 8693 token-exchange
grant when the `subject_token` comes from an external, trusted OIDC issuer
(`trustedIssuers`). It is unrelated to the *outgoing* token exchange described in
docs [02](02-core-concepts.md), [04](04-secrets-management.md),
[05](05-runconfig-and-permissions.md), [09](09-operator-architecture.md), and
[10](10-virtual-mcp-architecture.md), where `MCPExternalAuthConfig` middleware
exchanges a client's token for an upstream IdP token. Here the server is the
token *issuer*, accepting an externally-minted token as proof of identity and
re-issuing a ToolHive-scoped delegated token.

Normally the grant accepts only the server's own self-issued tokens as
`subject_token`. `trustedIssuers` extends that to tokens minted by an external
IdP (e.g. a corporate IdP), so a client already holding a token from that IdP
can exchange it for a ToolHive delegated token without a separate ToolHive
login.

## Prerequisite: not yet usable end to end

- Using this grant requires a confidential client holding the token-exchange
  grant. No supported deployment path provisions one today: DCR always
  registers public clients restricted to `authorization_code`/`refresh_token`,
  CIMD clients are public-only, and `RunConfig` has no client-seeding field.
- Discovery metadata does not yet advertise the token-exchange grant or any
  secret-based client auth method, so a metadata-driven client will conclude
  the grant is unsupported and never attempt it.
- This applies to the self-issued subject-token path too, not only
  `trustedIssuers`.
- Tracked in [#6082](https://github.com/stacklok/toolhive/issues/6082).

## Trust model

A trusted issuer plus a matching audience and a valid signature only proves
the token was minted for ToolHive to consume — it authorizes ToolHive as a
*resource*, not any particular *client* as a delegate. Accepting the token on
that basis alone would let any client presenting a validly-signed token
exchange it: a confused-deputy risk (CWE-863). The handler therefore requires
an explicit consent signal before treating the request as an authorized
delegation.

## Consent signals

Two functions cooperate here. `resolveAllowedActor`
(`pkg/authserver/server/tokenexchange/multi_issuer_validator.go`), called from
`validateExternalToken` during signature/claim validation, checks the
external-issuer allowlist and — when it matches — sets
`ValidatedClaims.ExternalActor`. `checkDelegationConsent`
(`pkg/authserver/server/tokenexchange/handler.go`) runs afterwards and decides
whether to grant the exchange, in this order:

1. **`may_act` (RFC 8693 §4.4).** If the subject token carries a well-formed
   `may_act` claim, it is authoritative: the allowlist step above is skipped
   entirely (`validateExternalToken` never calls `resolveAllowedActor` when
   `may_act` is present), and `checkDelegationConsent` enforces `may_act.sub`
   against the authenticated ToolHive client. A malformed `may_act` is
   rejected outright by `validateMayActShape`, not silently ignored or
   fallen through to the allowlist. On the external path, `may_act.iss` is
   mandatory, not merely constrained when present as on the self-issued path
   — `validateMayActShape`'s `requireIss` parameter is `true` there. Without
   this, an external issuer omitting `iss` could authorize any ToolHive
   client by naming its ID in a bare `may_act.sub`, bypassing
   `allowedActors`/`allowedDelegateClients` entirely — the one consent path
   that does.
2. **`ExternalActor`.** Otherwise, consent was already established by
   `resolveAllowedActor`: the claim named by that issuer's `actorClaim`
   (default `azp`; `appid` for Microsoft Entra v1, `cid` for Okta) matched an
   entry in that issuer's `allowedActors`. `allowedActors` holds client IDs in
   the *external* IdP's namespace, not ToolHive client IDs — the likeliest
   misconfiguration. An empty `allowedActors` accepts only `may_act`-bearing
   tokens from that issuer. `checkDelegationConsent` then additionally checks
   the issuer's `allowedDelegateClients` against the authenticated ToolHive
   client — this field is required (see below), so the check always applies
   on this path.
3. **`client_id` binding.** For a self-issued subject token (not part of the
   external path above), the token's `client_id` must match the authenticated
   client.
4. **No binding.** If none of the above apply, the token carries no
   verifiable client binding and the exchange is rejected.

## Accepted limitations

1. **`allowedDelegateClients` binds an allowlisted actor to specific ToolHive
   clients, and is mandatory, not opt-in.** `allowedActors` by itself
   authorizes "this external client's tokens may be exchanged here," not
   "…by this particular ToolHive client" — the external actor claim is never
   compared against the authenticated ToolHive client ID. Without a separate
   binding, every ToolHive confidential client holding the token-exchange
   grant would be delegation-equivalent with respect to an allowlisted
   external actor, so compromise of the weakest such client would be as good
   as compromise of all of them.

   `TrustedIssuer.AllowedDelegateClients` (`allowed_delegate_clients` on a
   hand-written `authserver.RunConfig`) closes this per issuer: a list of
   ToolHive client IDs permitted to use that issuer's allowlisted actors, or
   the wildcard `"*"` to explicitly permit any ToolHive client holding the
   token-exchange grant. `validateTrustedIssuer` rejects the field when it is
   empty or absent — permissiveness must be *declared* with the wildcard, not
   obtained by leaving the field out — and rejects the wildcard combined with
   specific client IDs, since silently ignoring the specific IDs alongside it
   would be worse than rejecting the config outright. `checkDelegationConsent`
   checks the authenticated client against this list on both consent paths:
   `may_act` bypasses `allowedActors` (the external-issuer allowlist) but NOT
   `allowedDelegateClients`, since the validator sets `AllowedDelegateClients`
   for every external token regardless of which path authorized it (see
   limitation 4 below). A self-issued `may_act` (no external issuer involved)
   has no `allowedDelegateClients` equivalent and is unaffected — it remains
   bound by `may_act.sub` alone.

   Nothing can reach this code path in production today: the token-exchange
   grant requires a confidential client, and no supported deployment path
   provisions one (see issue #6082). That makes the fail-closed default free
   right now — it would be a breaking change once a client can actually reach
   this grant.
2. **Subject namespace collisions are closed, not accepted.** A trusted
   issuer is trusted to assert *any* subject this server accepts for
   delegation, and downstream authorization decisions (Cedar) key on `sub`
   alone — not `iss`. Rather than requiring operators to keep every issuer's
   subject namespace disjoint, the delegated token's `sub` is qualified as
   `<issuerURL>#<externalSub>` for every trusted-issuer exchange
   (`tokenexchange.delegatedSubject`), never the external `sub` copied
   verbatim. ToolHive's own native subjects are UUIDs minted by
   `UserResolver.ResolveUser`, which cannot contain `#`, so a qualified
   external subject can never collide with one. Scope names are not
   qualified this way and remain the operator's responsibility to keep
   disjoint across issuers.
3. **Provenance is recorded for every external token.** The RFC 8693 §4.1
   `act` claim records who acted: `act.sub` is always the ToolHive client,
   with the external issuer nested one level in — `ValidatedClaims.ExternalIssuer`
   is set for every token validated by the external-issuer path, whether or
   not it also carries `may_act`. The nested entry additionally carries `sub`
   (the allowlisted actor claim) when the allowlist path resolved one;
   a `may_act`-bearing external token yields `act = {sub: <toolhive-client>,
   act: {iss: <external-issuer>}}` — no client-namespace actor to report, but
   the issuer is still recorded. Either way, Cedar authorizers key on `sub`
   and do not read `act` — it is an audit trail, not an access control. (AWS
   STS role mapping can read arbitrary claims including `act` via its CEL
   matcher, so "authorizers" here means Cedar specifically, not every
   consumer.)
4. **A `may_act`-emitting issuer bypasses the allowlist entirely.** Since it
   takes priority and can name any ToolHive client, that claim must be drawn
   from ToolHive's own client namespace and must not be influenceable by an
   untrusted party.
5. **ID/access-token discrimination on the external path rests on two
   partial layers, neither of which is exhaustive alone.** Nothing inspects
   the JWT header's `typ` claim: RFC 8725 §3.12 recommends `typ: at+jwt` for
   access tokens, but Entra v1/v2, Okta, and Google all emit a bare `JWT`
   `typ` for access tokens too, so requiring `at+jwt` would reject those
   providers' genuine access tokens.

   Layer one: `rejectIDTokenClaims` (`validator.go`) rejects a subject token
   carrying `at_hash` or `c_hash` — OIDC Core §3.3.2.11/§3.3.2.10 define both
   for ID tokens only. This is a sound *positive* detector but not
   exhaustive: both claims are OPTIONAL even on a valid
   authorization-code-flow ID token, so an ID token that omits them is not
   caught here.

   Layer two: `TrustedIssuer.ExpectedAudience` is documented as MUST be a
   resource/API identifier and MUST NOT be a client ID, since an ID token's
   `aud` is the requesting client's own ID while an access token's `aud`
   names the resource. This check is operator-dependent, not
   self-enforcing — nothing validates that the operator actually set a
   resource identifier rather than a client ID.
   `NewMultiIssuerTokenValidator` emits a `slog.Warn` at startup
   (`looksLikeResourceIdentifier`) when `expected_audience` has no URI
   scheme (`https://`, `api://`), naming the issuer and the consequence, but
   does not hard-reject it: Entra v1 legitimately uses a bare app-ID GUID
   (no scheme) as an access token's `aud`, so rejecting every non-URI
   audience would break that real, supported provider.

   A third discriminator — rejecting when a subject token's `aud` equals the
   issuer's configured actor claim, since OIDC Core requires an ID token's
   `aud` to equal its own `azp` — was evaluated and not shipped: an app whose
   API and client share one registration (the same ambiguous
   bare-GUID-audience configuration layer two warns about) legitimately
   issues *access* tokens where `aud` and `azp`/`appid` are the same value
   too, so this would reject genuine access tokens from that setup.

   Taken together: an operator who sets `expected_audience` to a client ID
   (despite the startup warning), talking to a real IdP whose ID tokens omit
   `at_hash`/`c_hash` (both legal per OIDC Core), has no remaining
   discriminator here. The blast radius is bounded: an admitted ID token
   carries no `scope`/`scp` claim, so `grantScopes` (`handler.go`) either
   rejects the exchange with `invalid_scope` or grants a zero-scope
   delegated token — it must also be signed by a configured trusted issuer
   and satisfy one of the consent signals above.

## Operational notes

- **Audience.** The requested audience is bounded by the subject token's own
  `aud` claim (`ensureAudienceSubsetOfSubject`); a request for an audience the
  subject token doesn't carry fails with `invalid_target`. An external IdP's
  `aud` is typically an app-ID GUID or `api://<app-id>`. For an exchange to
  succeed, the external IdP's API identifier must be set as the per-issuer
  `expectedAudience` *and* appear in the server's own `allowedAudiences` (plus
  the calling client's registered audiences) — `expectedAudience` alone is not
  enough, since it only governs whether the subject token validates, not what
  the delegated token is granted. A startup WARN fires when `expectedAudience`
  isn't in `allowedAudiences`.
- **Scopes.** `grantScopes` rejects — it does not intersect — any requested
  scope absent from the subject token's granted scopes, with `invalid_scope`.
  Both the RFC 9068 `"scope"` string claim and the `"scp"` array claim are
  read (`"scope"` wins if both are present) — `"scp"` is not an Entra
  quirk, it is what fosite's own default JWT claims strategy writes for a
  self-issued ToolHive access token, so a genuine subject token minted by
  this server's own token endpoint carries scopes this way already.
- **Delegated token lifetime.** `min(subject token's remaining lifetime,
  configured delegationLifespan)`. A short-lived external token silently
  yields a short delegated token.
- **Delegation chain depth.** Re-exchanging an already-delegated token nests
  `act` further; `maxDelegationDepth` (10) bounds it, past which the exchange
  fails with `invalid_grant` ("delegation chain is too deep").
- **Discovery redirects.** The per-issuer HTTP client only follows same-host
  redirects (`networking.SameHostRedirectPolicy()`), so an issuer whose
  `/.well-known/openid-configuration` redirects cross-host cannot be
  onboarded via discovery — set `jwksUrl` explicitly to skip discovery.
- **JWKS caching.** A successful fetch is cached and refreshed by jwx's
  `jwk.Cache` on a schedule driven by the response's `Cache-Control`/`Expires`
  headers (no fixed TTL). The 30-second figure is `jwksFetchFailureBackoff`,
  which gates only how often a persistently-failing issuer is retried before
  its first successful fetch — so a just-corrected `jwksUrl` can keep failing
  for up to 30s before the next retry; once a fetch has ever succeeded, this
  backoff no longer applies.
- **Diagnostics.** A non-allowlisted actor and an untrusted issuer both
  surface as the same generic `invalid_request`
  ("The subject token is invalid or could not be verified."), per RFC 8693
  §2.2.2. The only discriminator is a `slog.Debug` log line, so diagnosing a
  failed exchange needs debug logging enabled. The two exceptions are startup
  `slog.Warn` lines — an issuer with empty `allowedActors`, and an
  `expectedAudience` missing from `allowedAudiences` — the only non-DEBUG
  diagnostics this feature emits.
- **HTTPS enforcement for a trusted issuer.** `validateTrustedIssuerURL`
  (`pkg/authserver/config.go`) validates `issuer_url` at config time and,
  unlike the server's own issuer, grants no localhost exemption: an
  `http://localhost` trusted issuer is rejected unless that issuer's own
  `insecure_allow_http` is set. There is no separate runtime scheme check for
  `issuer_url` — only `jwks_url` gets one (`ValidateJWKSURL`, on every fetch;
  shared verbatim between the runtime choke point and the config-time check
  so the two can't drift). There is no operator (CRD) surface for this
  feature yet — see [#6082](https://github.com/stacklok/toolhive/issues/6082)
  — so today `trustedIssuers` is reachable only via a hand-written
  `authserver.RunConfig`.
- **`allowPrivateIPs` requires `jwksUrl`.** Enforced at config time by
  `validateTrustedIssuers` (`pkg/authserver/config.go`) for every RunConfig.
  Without a hand-configured `jwksUrl`, OIDC discovery — a document fetched
  from, and thus influenceable by, the external issuer itself — would choose
  the private JWKS dial target, which is exactly what pinning it to
  operator-supplied config prevents.
- **Misconfiguration surfaces as a pod crash**, not an operator condition —
  check pod logs, not `kubectl describe`.

## Implementation

- `pkg/authserver/server/tokenexchange/handler.go` — `checkDelegationConsent`, `grantScopes`, `grantAndBoundAudiences`
- `pkg/authserver/server/tokenexchange/multi_issuer_validator.go` — `MultiIssuerTokenValidator`, `TrustedIssuer`, `resolveAllowedActor`, `ValidateJWKSURL`
- `pkg/authserver/server/tokenexchange/validator.go` — `assignClaim`, `buildValidatedClaims`, `validateMayActShape`, `scpToScopeString`
- `pkg/authserver/config.go` — `validateTrustedIssuerURL`, `validateJWKSEndpointURL`, `validateTrustedIssuers`, `warnTrustedIssuerAudiences`
