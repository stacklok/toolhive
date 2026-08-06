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
   fallen through to the allowlist.
2. **`ExternalActor`.** Otherwise, consent was already established by
   `resolveAllowedActor`: the claim named by that issuer's `actorClaim`
   (default `azp`; `appid` for Microsoft Entra v1, `cid` for Okta) matched an
   entry in that issuer's `allowedActors`. `allowedActors` holds client IDs in
   the *external* IdP's namespace, not ToolHive client IDs — the likeliest
   misconfiguration. An empty `allowedActors` accepts only `may_act`-bearing
   tokens from that issuer. `checkDelegationConsent` then additionally checks
   the issuer's `allowedDelegateClients`, if configured, against the
   authenticated ToolHive client — see Accepted limitations below.
3. **`client_id` binding.** For a self-issued subject token (not part of the
   external path above), the token's `client_id` must match the authenticated
   client.
4. **No binding.** If none of the above apply, the token carries no
   verifiable client binding and the exchange is rejected.

## Accepted limitations

1. **By default, any allowlisted actor authorizes any ToolHive client.**
   `allowedActors` by itself authorizes "this external client's tokens may be
   exchanged here," not "…by this particular ToolHive client" — the external
   actor claim is never compared against the authenticated ToolHive client
   ID. Every ToolHive confidential client holding the token-exchange grant is
   delegation-equivalent with respect to an allowlisted external actor, so
   compromise of the weakest such client is as good as compromise of all of
   them.

   An operator closes this per issuer by setting `allowedDelegateClients`
   (`TrustedIssuer.AllowedDelegateClients`) on a hand-written
   `authserver.RunConfig`: a list of ToolHive client IDs
   permitted to use that issuer's allowlisted actors. `checkDelegationConsent`
   checks the authenticated client against it whenever it's set — nil (the
   default) keeps today's permissive behavior, so existing configs are
   unaffected. This binding only applies to the `AllowedActors` consent path;
   `may_act` still bypasses `allowedActors` entirely (see limitation 4 below)
   and therefore `allowedDelegateClients` too. Keeping the token-exchange
   client set minimal remains the operator's baseline control regardless.
2. **Subject namespace must be disjoint.** A trusted issuer is trusted to
   assert *any* subject this server accepts for delegation — the delegated
   token carries ToolHive's own `iss` with the external `sub` copied verbatim,
   and downstream authorization decisions key on `sub`. Each trusted issuer's
   subject namespace (and scope names) must be disjoint from every upstream
   provider's and from every other trusted issuer's, or it can mint a
   delegated token indistinguishable from a real user's.
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
- **JWKS caching.** A successful fetch is cached 5 minutes; a failed fetch is
  cached (backed off) for 30 seconds — so a just-corrected `jwksUrl` can keep
  failing for up to 30s before the next retry.
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
