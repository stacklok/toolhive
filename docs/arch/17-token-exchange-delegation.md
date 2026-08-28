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

This document also describes how a DCR-registered client can authenticate to the
exchange without receiving a secret. That is RFC 7523 §2.2 `private_key_jwt`
**client authentication**: the client registers an inline public JWKS and signs
a `client_assertion` with its private key. It is not the RFC 7523 §2.1
JWT-bearer grant, which uses a JWT as the grant assertion itself and does not
authenticate a registered client. The two mechanisms are independent.

ToolHive requires every registered private-key JWT JWK to be public, have
`use: "sig"`, and identify a signing algorithm compatible with the registered
`token_endpoint_auth_signing_alg`. ToolHive requires this explicit `use` value
even though the JWK specification makes the member optional.

## Deployment and configuration

RFC 8693 is reachable through a pre-provisioned **delegate client**. A delegate
client is a confidential client registered when the embedded authorization server
starts. It is limited to the token-exchange grant and authenticates at
`/oauth/token` with `client_secret_basic` or `client_secret_post`.

### RunConfig

`RunConfig.delegate_clients` is the portable runtime configuration surface. Each
client must name a unique `client_id`, a secret **reference**, one or more scopes,
and one or more audiences. The grant type is fixed internally to
`urn:ietf:params:oauth:grant-type:token-exchange` and is not configurable.
Scopes must be a subset of `scopes_supported` (or the default supported OIDC
scopes when that field is omitted), and audiences must be a subset of
`allowed_audiences`. These required narrowing lists prevent a delegate client
from inheriting every server scope or resource.

The following is a relevant `RunConfig` excerpt. `client_secret_env_var` may be
replaced by `client_secret_file`; an inline secret is not supported. Generate
the secret with a CSPRNG, e.g. `openssl rand -base64 32`.

```yaml
issuer: https://auth.example.com
scopes_supported: [openid, profile]
allowed_audiences: [https://mcp.example.com]
delegate_clients:
  - client_id: reporting-delegate
    client_secret_env_var: REPORTING_DELEGATE_CLIENT_SECRET
    scopes: [openid]
    audiences: [https://mcp.example.com]
trusted_issuers:
  - issuer_url: https://login.example-idp.com
    expected_audience: https://mcp.example.com
    allowed_actors: [external-reporting-client]
    allowed_delegate_clients: [reporting-delegate]
```

A `RunConfig` is needed only when subject tokens originate at an external
issuer; a configured delegate client can also exchange a self-issued subject
token. See [Trust model](#trust-model) for the required external-issuer
binding. The Kubernetes operator also exposes `trusted_issuers` as
`EmbeddedAuthServerConfig.trustedIssuers` — see
[Kubernetes operator](#kubernetes-operator) below.

### Kubernetes operator

The operator exposes delegate clients on the shared
`EmbeddedAuthServerConfig`, which has two supported consumers:

- `MCPExternalAuthConfig.spec.embeddedAuthServer.delegateClients`, for an
  embedded authorization server referenced by an `MCPServer` or
  `MCPRemoteProxy`.
- `VirtualMCPServer.spec.authServerConfig.delegateClients`, for a vMCP's inline
  embedded authorization server.

The following `MCPExternalAuthConfig` excerpt declares the same client. The
operator converts the Secret reference into a pod environment-variable reference
and always supplies the token-exchange grant; it does not copy the secret into
the generated ConfigMap or `RunConfig`.

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPExternalAuthConfig
metadata:
  name: embedded-auth
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
    # Other required embedded-auth-server fields, including upstreamProviders,
    # are omitted here.
    delegateClients:
      - clientId: reporting-delegate
        clientSecretRef:
          name: reporting-delegate-secret
          key: client-secret
        scopes: [openid]
        audiences: [https://mcp.example.com]
```

For a `VirtualMCPServer`, place the list under `spec.authServerConfig`:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: VirtualMCPServer
metadata:
  name: reporting-gateway
spec:
  # Other required VirtualMCPServer fields are omitted here.
  authServerConfig:
    issuer: https://auth.example.com
    # Other required embedded-auth-server fields, including upstreamProviders,
    # are omitted here.
    delegateClients:
      - clientId: reporting-delegate
        clientSecretRef:
          name: reporting-delegate-secret
          key: client-secret
        scopes: [openid]
        audiences: [https://mcp.example.com]
```

The CRD accepts Secret references only: no plaintext secret, redirect URI, or
arbitrary grant selection is available. A non-empty `clientSecretRef.name` and
`.key`, at least one scope, and at least one audience are required. Delegate
clients require an HTTPS issuer at admission.

`trustedIssuers` is exposed the same way, on the same shared
`EmbeddedAuthServerConfig`, and converts directly to `[]tokenexchange.TrustedIssuer`
(no secret is referenced, so there is no `SecretKeyRef` indirection to apply):

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPExternalAuthConfig
metadata:
  name: embedded-auth
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
    # Other required embedded-auth-server fields, including upstreamProviders,
    # are omitted here.
    trustedIssuers:
      - issuerUrl: https://login.example-idp.com
        expectedAudience: https://mcp.example.com
        allowedActors: [external-reporting-client]
        # actorMatcher is additive with allowedActors, not a replacement —
        # either signal is sufficient. Omit it to rely on allowedActors alone.
        actorMatcher: "has(claims.roles) && 'trusted-delegator' in claims.roles"
        allowedDelegateClients: [reporting-delegate]
        allowMayAct: false
        # Optional: trust a private CA for this issuer's discovery/JWKS fetch.
        caBundleRef:
          configMapRef:
            name: login-example-idp-ca
            key: ca.crt
```

Static delegate clients and confidential Dynamic Client Registration (DCR) are
independent. Declaring `delegateClients` neither enables nor requires
`allowConfidentialClientRegistration`; the latter governs unauthenticated
`/oauth/register` requests. If a persisted DCR client has the same ID as a
static delegate client, the static client is registered at server startup and
replaces the DCR registration, including its secret and permissions. Do not
reuse IDs between the two mechanisms.

### Discovery and secret rotation

Both `/.well-known/oauth-authorization-server` and
`/.well-known/openid-configuration` advertise the token-exchange grant in
`grant_types_supported`. `token_endpoint_auth_methods_supported` always
includes `none`; it also includes `client_secret_basic` and
`client_secret_post` when confidential DCR is enabled or a static delegate
client is configured, and includes `private_key_jwt` when
`allowPrivateKeyJWTRegistration` is enabled. In the latter case,
`token_endpoint_auth_signing_alg_values_supported` lists the implemented
algorithms. The RFC 7523 §2.1 JWT-bearer grant is advertised only when a trusted
issuer opts into it. A private-key JWT DCR client is limited to token exchange;
it is not an authorization-code client.

On Kubernetes, a delegate-client Secret is injected as a pod environment
variable and is resolved when the authorization server starts. Updating that
Secret does not change the environment of an already running pod. The operator
has no delegate-client Secret watch or automatic rollout for this feature, so
restart or otherwise roll out the workload after rotating the secret.

## Secretless DCR delegate flow

Set `allowPrivateKeyJWTRegistration: true` without enabling
`allowConfidentialClientRegistration` when the authorization server should
accept key-based registrations but must not mint secret-based confidential
clients. Registration is still unauthenticated, so protect the endpoint through
network policy and enable it only for callers that are trusted to register.

The request uses an inline public JWK. This example intentionally uses a
placeholder key and contains no private key or secret; the client keeps the
corresponding private key locally and never sends it to ToolHive:

```http
POST /oauth/register HTTP/1.1
Host: auth.example.com
Content-Type: application/json

{
  "redirect_uris": ["https://client.example/callback"],
  "token_endpoint_auth_method": "private_key_jwt",
  "token_endpoint_auth_signing_alg": "RS256",
  "grant_types": ["urn:ietf:params:oauth:grant-type:token-exchange"],
  "jwks": {"keys": [{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "client-key", "n": "<base64url-public-modulus>", "e": "AQAB"}]}
}
```

The successful response returns a `client_id`, the registered metadata, and no
`client_secret`. The client then signs a short-lived `client_assertion` with
its private key and sends it to `/oauth/token` together with the token-exchange
request:

```text
grant_type=urn:ietf:params:oauth:grant-type:token-exchange&
subject_token=<subject-token>&
subject_token_type=urn:ietf:params:oauth:token-type:access_token&
client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer&
client_assertion=<signed-assertion>
```

The assertion identifies the registered `client_id` in `iss` and `sub`, targets
the token endpoint in `aud`, and includes a unique `jti` and an `exp`. The
server verifies it against the stored inline JWKS and rejects replay while the
assertion is valid. No `client_secret` is included in registration or token
exchange.

Private-key JWT registration accepts only inline `jwks`; `jwks_uri`, SPIFFE/SVID
authentication, AWS STS `act` mapping, and ID-JAG chaining are deferred and out
of scope. `token_endpoint_auth_signing_alg` is required and the supported
values are `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`,
`ES384`, and `ES512` (not `EdDSA` — the pinned fosite version's
client-assertion verification doesn't handle it). Unlike confidential-client
registration, this has no transport restriction of its own: the DCR response
for a `private_key_jwt` client never contains a `client_secret` or any other
secret, so there is nothing for cleartext HTTP to expose. It is governed only
by the issuer's general transport policy (`insecureAllowHTTP`), the same as a
public (`none`) client.

## Trust model

A trusted issuer plus a matching audience and a valid signature only proves
the token was minted for ToolHive to consume — it authorizes ToolHive as a
*resource*, not any particular *client* as a delegate. Accepting the token on
that basis alone would let any client presenting a validly-signed token
exchange it: a confused-deputy risk (CWE-863). The handler therefore requires
an explicit consent signal before treating the request as an authorized
delegation.

## Consent signals

Two functions cooperate here. `resolveActorAuthorization`
(`pkg/authserver/server/tokenexchange/multi_issuer_validator.go`), called from
`validateExternalToken` during signature/claim validation, checks the
external-issuer authorization and — when it matches — sets
`ValidatedClaims.ExternalActorAuthorized` (and `ValidatedClaims.ExternalActor`
when the allowlist path resolved an actor claim). `checkDelegationConsent`
(`pkg/authserver/server/tokenexchange/handler.go`) runs afterwards and decides
whether to grant the exchange, in this order:

1. **`may_act` (RFC 8693 §4.4).** A trusted issuer must explicitly set
   `allow_may_act: true` before its well-formed `may_act` claim is considered.
   It is otherwise rejected outright, not silently ignored or fallen through
   to external-issuer authorization. When enabled, `may_act` is authoritative:
   external-issuer authorization below is skipped entirely
   (`validateExternalToken` never calls `resolveActorAuthorization` when
   `may_act` is present), and `checkDelegationConsent` enforces `may_act.sub`
   against the authenticated ToolHive client. A malformed `may_act` is
   rejected outright by `validateMayActShape`, not silently ignored. On the
   external path, `may_act.iss` is mandatory, not merely constrained when
   present as on the self-issued path — `validateMayActShape`'s `requireIss`
   parameter is `true` there. Without this, an external issuer omitting `iss`
   could authorize any ToolHive client by naming its ID in a bare
   `may_act.sub`, bypassing `allowedActors`/`actorMatcher` external
   authorization entirely. The handler still enforces `allowedDelegateClients`
   for every external token.
2. **External actor authorization.** Otherwise, consent was already
   established by `resolveActorAuthorization`: the claim named by that
   issuer's `actorClaim` (default `azp`; `appid` for Microsoft Entra v1,
   `cid` for Okta) matched an entry in that issuer's `allowedActors`, **or**
   its `actorMatcher` CEL expression evaluated to `true`. `allowedActors`
   holds client IDs in the *external* IdP's namespace, not ToolHive client
   IDs — the likeliest misconfiguration. `actorMatcher` is an admin-authored
   CEL expression whose only variable is `claims`, a `map[string]any`
   containing the complete signature-verified JWT claims map, including
   registered claims such as `iss`, `sub`, `aud`, `exp`, `iat`, `nbf`, and
   `jti`. It is compiled during config validation and validator construction;
   a syntax or type error fails startup, but an expression that compiles yet
   never returns bool does NOT — it is only caught the first time a real
   token evaluates it, denying that token (and every subsequent one, since it
   can never return bool). Runtime CEL errors, including that one, deny the
   token (they are never treated as a match). Access to optional claims must be
   guarded with `has()` (for example, `has(claims.roles) && 'trusted-delegator'
   in claims.roles`), because a missing claim produces a CEL evaluation error
   and denies the token. An empty `allowedActors` and
   `actorMatcher` accepts only permitted `may_act`-bearing tokens from that
   issuer; without `allowMayAct`, it accepts no tokens at all.
   `checkDelegationConsent` then additionally checks the issuer's
   `allowedDelegateClients` against the authenticated ToolHive client — this
   field is required (see below), so the check always applies on this path.

   **`actorMatcher` misconfiguration risk, analogous to `allowedActors`
   above:** write the predicate against a claim the external issuer alone
   controls and that does not vary with a user- or self-service-editable
   attribute — an actor/client identifier such as `azp`, `appid`, or `cid`,
   not a profile or business attribute such as a department or team name
   (which may be HR-synced or editable by the user themselves). A matcher
   written against the wrong kind of claim can grant delegation on a basis
   the operator never intended.
3. **`client_id` binding.** For a self-issued subject token (not part of the
   external path above), the token's `client_id` must match the authenticated
   client — **unless** the authenticated client is a configured delegate
   client (`Handler.configuredDelegateClients`, sourced from
   `Config.DelegateClients`), in which case the binding is skipped and any
   self-issued subject token is accepted regardless of which client it was
   originally issued to. This exception applies only on the self-issued path
   (`ExternalIssuer == ""`); a delegate client presenting an externally-issued
   token with a mismatched `client_id` is still rejected here.
4. **No binding.** If none of the above apply, the token carries no
   verifiable client binding and the exchange is rejected.

## `actor_token` and `id_token`

RFC 8693 also defines an optional `actor_token`/`actor_token_type` pair,
which `resolveActorIdentity` (`handler.go`) supports for self-issued actor
tokens only — an `actor_token` validated against any other issuer is
rejected. Two claims on the `actor_token` matter, and they play different
roles:

- **`client_id`** is the authenticated-client binding: it must equal the
  authenticated OAuth client ID (the client fosite already authenticated for
  this request). This is the same role a `client_id` claim plays on a normal
  issued token — it says which client the token was minted for.
- **`sub`** is the actor identity that flows into the delegated token's
  `act.sub`, in the same slot the authenticated client ID would otherwise
  occupy. It is not required to equal the client ID: this is what lets a
  single OAuth client present a more specific actor identity (e.g. a
  particular agent instance or delegate persona) than its own `client_id`.

Presenting a distinct `sub` this way does not itself grant any extra
privilege — the resulting actor identity still has to clear
`checkDelegationConsent` above like any other actor identity would (via
`may_act`, `ExternalActor`, the `client_id` binding, or the configured-
delegate exemption).

`actor_token_type` only accepts `access_token` or `jwt`; `id_token` is
rejected outright. Similarly, `subject_token_type` accepts `access_token` or
`jwt`, but not `id_token`: this embedded authorization-server endpoint does
not implement the XAA/ID-JAG profile and applies exactly one validation profile
to a subject or actor token (the self-issued/access-token one — see
`rejectIDTokenClaims`'s doc comment in `validator.go`), so accepting a
declared `subject_token_type`/`actor_token_type` of `id_token` without a
distinct validation profile behind it would silently ignore the semantic
differences between an ID token and an access token (e.g. an ID token's
`aud` names the relying-party client, not a resource). Rather than validate
an ID token as if it were an access token, the server declines the type
until a real ID-token validation profile is implemented.

### Delegate clients and self-issued token exchange

A configured delegate client can convert *any* self-issued ToolHive access
token it can obtain — for any user, regardless of which client originally
obtained that token — into a delegated token asserting that user's `sub`.
This is an intentional blanket-trust model, not an oversight: delegate
clients are declared by the operator at server startup (`RunConfig`/CRD),
not obtained through self-service registration, so the same trust already
placed in them by granting the token-exchange grant extends to any
self-issued subject token rather than only ones issued to that specific
client.

The relaxation is bounded the same way every other exchange is: `grantScopes`
and `grantAndBoundAudiences`'s `ensureAudienceSubsetOfSubject` still narrow
the delegated token to what both the delegate client and the subject token
are authorized for, and the delegated token's lifetime is still capped by
`min(subject token's remaining lifetime, configured delegationLifespan)`.
It is **not** bounded by per-`jti` single-use enforcement — there is none in
this codebase today, for any token-exchange path, self-issued or external
(see the `Handler` doc comment in `handler.go` for why: replay is bounded by
lifetime, not single-use tracking, pending the broader M2M/sender-constrained-
token effort).

Revoking a delegate client (removing it from config) revokes this specific
relaxation for that client on the next server restart — it does **not**
retroactively delete the client's already-registered row, its hashed secret,
or its token-exchange grant from storage. No `DeleteClient` path exists in
this codebase today; that is a separate, pre-existing gap, unrelated to this
relaxation, and not something this change addresses.

RFC 8693 §2.1 permits this: it defines the subject token only as a token
that represents "the identity of the party on behalf of whom the request is
being made," with no requirement that the token be validated against the
identity of the client presenting it as `subject_token`. Applying a
client-authentication-based policy on top of that — as this exception does —
is within the authorization server's discretion as the token-exchange STS.

### Worked example

This is the scenario the relaxation exists for (issue #5194): an interactive
client obtains a self-issued token for a user, and a separate, unrelated
delegate client later exchanges that same token to act as the user.

`RunConfig` declares the delegate client (`chat-ui`, the client the user
authenticated through, needs no entry here — only the delegate does):

```yaml
delegate_clients:
  - client_id: coding-agent
    client_secret_env_var: CODING_AGENT_CLIENT_SECRET
    scopes: [openid, mcp:tools]
    audiences: [https://mcp.example.com]
```

1. The user signs in through `chat-ui` and ToolHive mints a self-issued
   access token whose `client_id` is `chat-ui` — not `coding-agent`:

   ```json
   {
     "iss": "https://auth.example.com",
     "sub": "3f9c2eab-1a4e-4b8f-9c11-3a0d2e6f9d21",
     "client_id": "chat-ui",
     "aud": "https://mcp.example.com",
     "scope": "openid mcp:tools",
     "exp": 1755100000
   }
   ```

2. `coding-agent` — a client the user never signed in through — presents
   that access token as `subject_token`, authenticating itself with its own
   client credentials (`client_secret_basic`, i.e. an `Authorization: Basic`
   header):

   ```http
   POST /oauth/token HTTP/1.1
   Host: auth.example.com
   Authorization: Basic Y29kaW5nLWFnZW50OjxzZWNyZXQ+
   Content-Type: application/x-www-form-urlencoded

   grant_type=urn:ietf:params:oauth:grant-type:token-exchange&
   subject_token=<the access token from step 1>&
   subject_token_type=urn:ietf:params:oauth:token-type:access_token&
   audience=https://mcp.example.com&
   scope=openid
   ```

   Because `coding-agent` is a configured delegate client, `checkDelegationConsent`
   skips the `client_id` binding described in step 3 above — `chat-ui` on the
   subject token is not compared against `coding-agent`, the authenticated
   client — and the exchange proceeds.

3. The delegated token names the same user as `sub`, and records
   `coding-agent` — the client that performed the exchange, not `chat-ui` —
   as the acting party in `act.sub`:

   ```json
   {
     "iss": "https://auth.example.com",
     "sub": "3f9c2eab-1a4e-4b8f-9c11-3a0d2e6f9d21",
     "act": { "iss": "https://auth.example.com", "sub": "coding-agent" },
     "aud": "https://mcp.example.com",
     "scope": "openid",
     "exp": 1755099400
   }
   ```

   `scope` is narrowed from the requested `openid` to what both `coding-agent`
   and the subject token already carried (`grantScopes` never grows it); had
   the request asked for `mcp:tools` too, the same subset rule would have
   granted it, since the subject token carried it. `aud` passes
   `ensureAudienceSubsetOfSubject` because it's both in the subject token's
   own `aud` and in `coding-agent`'s configured `audiences`. `exp` is capped
   to `min(subject token's remaining lifetime, delegationLifespan)`, which
   here yields an expiry earlier than the subject token's own.

## Accepted limitations

1. **`allowedDelegateClients` binds external actor authorization to specific
   ToolHive clients, and is mandatory, not opt-in.** `allowedActors` or
   `actorMatcher` by itself authorizes "these external tokens may be exchanged
   here," not "…by this particular ToolHive client" — the external actor
   claim (when one exists) is never compared against the authenticated
   ToolHive client ID. Without a separate binding, every ToolHive confidential
   client holding the token-exchange grant would be delegation-equivalent with
   respect to an authorized external token, so compromise of the weakest such
   client would be as good as compromise of all of them.

   `TrustedIssuer.AllowedDelegateClients` (`allowed_delegate_clients` on a
   hand-written `authserver.RunConfig`) closes this per issuer: a list of
   ToolHive client IDs permitted to use that issuer's allowlisted actors, or
   the wildcard `"*"` to explicitly permit any ToolHive client holding the
   token-exchange grant. `validateTrustedIssuer` rejects the field when it is
   empty or absent — permissiveness must be *declared* with the wildcard, not
   obtained by leaving the field out — and rejects the wildcard combined with
   specific client IDs. The wildcard is also rejected when `allow_may_act` is
   enabled: trusting a foreign issuer to name ToolHive clients in `may_act`
   requires explicit per-client containment. `checkDelegationConsent` checks
   the authenticated client against this list on both consent paths:
   `may_act` bypasses `allowedActors` and `actorMatcher` (external-issuer
   authorization) but NOT
   `allowedDelegateClients`, since the validator sets `AllowedDelegateClients`
   for every external token regardless of which path authorized it (see
   limitation 4 below). A self-issued `may_act` (no external issuer involved)
   has no `allowedDelegateClients` equivalent and is unaffected — it remains
   bound by `may_act.sub` alone.

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
   `act` claim records who acted: its outer hop always contains ToolHive's
   issuer and client ID. The external issuer is nested one level in —
   `ValidatedClaims.ExternalIssuer` is set for every token validated by the
   external-issuer path, whether or not it also carries `may_act`. The nested
   entry additionally carries `sub` (the allowlisted actor claim) when the
   allowlist path resolved one; a `may_act`-bearing external token yields
   `act = {iss: <toolhive-issuer>, sub: <toolhive-client>, act: {iss:
<external-issuer>}}` — no client-namespace actor to report there, but the
   issuer is still recorded. Either way, Cedar authorizers key on `sub` and do
   not read `act` — it is an audit trail, not an access control. (AWS STS role
   mapping can read arbitrary claims including `act` via its CEL matcher, so
   "authorizers" here means Cedar specifically, not every consumer.)
4. **`may_act` trust is a per-issuer opt-in, and it bypasses more than one
   thing.** `allow_may_act` is false by default because an enabled issuer
   bypasses BOTH `allowedActors` and `actorMatcher` (external actor
   authorization) and can name a ToolHive client directly. The claim must be
   drawn from ToolHive's own client namespace and must not be influenceable by
   an untrusted party. Its issuer must use explicit `allowedDelegateClients`;
   wildcard delegation is rejected with this opt-in.
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

## RFC 7523 JWT-bearer grant

The embedded authorization server also supports the plain RFC 7523 JWT-bearer
grant (`urn:ietf:params:oauth:grant-type:jwt-bearer`), a separate grant from
RFC 8693 token exchange above. Where token exchange lets an already-authenticated
ToolHive client trade a subject token it holds for a delegated one, the
JWT-bearer grant lets an external party present a signed assertion directly at
`/oauth/token` and receive a ToolHive access token — no ToolHive client, no
delegate client, no consent signal (`may_act`/`allowedActors`/`actorMatcher`)
involved. It is enabled per trusted issuer by setting
`TrustedIssuer.JWTBearerGrant` (`jwt_bearer_grant` on a hand-written
`authserver.RunConfig`, `jwtBearerGrant` on the operator's
`TrustedIssuerConfig`) — independent of that issuer's RFC 8693 delegation
fields, though both may be configured on the same issuer.

### JWT-bearer configuration

`accepted_audiences` identifies this authorization server, while
`allowed_audiences` identifies resources that issued access tokens may target.
They must be disjoint: configuring the same value in both is rejected, because
a resource audience must not also make an assertion acceptable to the token
endpoint. When `accepted_audiences` is omitted, the token endpoint URL is the
only accepted assertion audience.

A `RunConfig` enables the grant per trusted issuer. This example accepts
assertions for the authorization-server URL and permits the exact assertion
subject to request only the listed resource:

```yaml
issuer: https://auth.example.com
allowed_audiences: [https://mcp.example.com]
trusted_issuers:
  - issuer_url: https://issuer.example.com
    jwt_bearer_grant:
      max_assertion_age: 5m
      accepted_audiences: [https://auth.example.com]
      subject_bindings:
        - subject: workload-123
          allowed_resources: [https://mcp.example.com]
```

The equivalent operator configuration uses camelCase fields:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPExternalAuthConfig
metadata:
  name: embedded-auth
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
    allowedAudiences: [https://mcp.example.com]
    trustedIssuers:
      - issuerUrl: https://issuer.example.com
        jwtBearerGrant:
          maxAssertionAge: 5m
          acceptedAudiences: [https://auth.example.com]
          subjectBindings:
            - subject: workload-123
              allowedResources: [https://mcp.example.com]
```

- **Clientless.** `JWTBearerHandler.CanSkipClientAuth` permits a
  credential-free plain assertion. Form-supplied client credentials prevent
  this clientless path. Every storage backend's request-marshaling path still
  needs a non-nil `fosite.Client`, so the handler attaches a synthetic one —
  see "Synthetic subject and client identity" below.
- **Assertion bounds.** Beyond the standard RFC 7523 §3 claims (`iss`, `sub`,
  `aud`, `exp`; `iat` is required here specifically, for the age bound below),
  two policy-level bounds apply:
  - `JWTBearerGrantPolicy.MaxAssertionAge` caps `exp - iat` — independent of
    `exp` itself, which is still checked for plain expiry. This bounds how
    long an assertion can claim to be "fresh" for, not just how long it
    remains unexpired.
  - `JWTBearerGrantPolicy.AcceptedAudiences` is the set of "this AS" identity
    strings the assertion's `aud` must intersect (`audienceIntersects`),
    checked by exact-match membership rather than equality — RFC 7523 §3
    permits a multi-valued `aud`, so an assertion may additionally name other
    audiences without losing its match. Defaults to `[tokenEndpoint]` when
    unset, preserving the original exact-endpoint-match behavior. This exists
    to let an issuer's assertions target this AS by more than one valid name
    (e.g. across an issuer/token-endpoint URL migration) — it is deliberately
    NOT the same relaxation as accepting an arbitrary RFC 8707 resource
    audience, which would let any resource-scoped token satisfy the grant
    instead of only tokens minted for this AS specifically.

  Beyond cryptographic and claim validation, `JWTBearerGrantPolicy.SubjectBindings`
  is an exact-match allowlist: an assertion's `sub` must have a configured
  binding, and the request's `resource` parameter (required, exactly one)
  must be one of that binding's `allowed_resources`. There is no wildcard
  subject or resource — every accepted assertion subject and resource pair is
  named explicitly.
- **Synthetic subject and client identity.** The issued token's `sub` is
  `<assertion issuer>#<assertion subject>` (`session.New`'s first argument,
  matching the delegated-token subject qualification pattern in [Accepted
  limitations](#accepted-limitations) #2, so it can never collide with a
  native ToolHive UUID subject). Its `client_id` claim has the precise format
  `synthetic:jwt-bearer-<base64url SHA-256>`: the base64url value is the
  unpadded SHA-256 digest of `<issuer>\x00<subject>` (`jwtBearerClientID`). It
  is never a real registered `fosite.Client`. `storage.NewSyntheticClient`
  builds the client object attached to the access request, and
  `storage.IsSyntheticClientID` lets a storage backend reconstruct it on read
  (e.g. `RedisStorage.unmarshalRequester`) without a client-registry lookup,
  `RegisterClient`. This mechanism is generic — any future clientless grant
  can reuse it, not just this one.
- **Replay model.** `storage.AssertionJWTConsumer.ConsumeAssertionJWT` atomically
  records `(purpose, issuer, key)` as consumed, keyed until the assertion's own
  `exp`, and returns `fosite.ErrJTIKnown` on a repeat. `key` is not always the
  assertion's `jti`: many real-world IdPs (e.g. Entra ID `client_credentials`
  tokens) never emit one, so `jti` is not a required claim here. `assertionReplayKey`
  uses `jti` when present, and otherwise falls back to a SHA-256 digest of the
  raw assertion JWT string (prefixed `jwt-bearer-noJTI-` so it can never
  collide with a real `jti`) — hashing the serialized assertion identifies
  replays of the exact same assertion without storing the raw JWT. Replay
  protection is never skipped, only re-keyed. The
  JWT-bearer grant's purpose is `"jwt-bearer"` (`jwtBearerReplayPurpose`), kept
  separate from any other assertion-consuming grant so a reused key cannot be
  laundered across purposes. Consumption happens *before* issuance: if token
  issuance somehow fails after that point, the assertion stays consumed rather
  than becoming replayable — a fail-closed choice that costs an otherwise-valid
  assertion its one use on an issuance-side error, rather than risk a second
  grant on a retried replay.
- **Discovery.** `grant_types_supported` in `/.well-known/oauth-authorization-server`
  and `/.well-known/openid-configuration` advertises
  `urn:ietf:params:oauth:grant-type:jwt-bearer` only when at least one trusted
  issuer has `jwtBearerGrant` configured (`AuthorizationServerConfig.JWTBearerGrantEnabled`,
  computed by the same `jwtBearerGrantEnabled` helper that decides whether
  `buildProvider` registers the grant with fosite in the first place — the two
  can't drift out of sync).
- **Validator sharing.** When both RFC 8693 token exchange and the JWT-bearer
  grant are enabled for the same trusted issuers, `buildProvider` constructs a
  single `MultiIssuerTokenValidator` (`tokenexchange.NewSharedTrustedIssuerValidator`)
  and passes it to both `tokenexchange.FactoryWithSharedTrustedIssuerValidator`
  and `tokenexchange.JWTBearerIssuanceFactory`, rather than each building its own —
  a `MultiIssuerTokenValidator` registers a `jwk.Cache` and background refresh
  goroutines per issuer, so building two would double that cost with no
  benefit.
- **No delegation consent.** The JWT-bearer grant does not apply
  `allowedActors`, `actorMatcher`, or `may_act` — those are RFC 8693 delegation
  concepts. An issuer's JWT-bearer subjects are authorized entirely by
  `SubjectBindings`, independent of whatever `allowedActors`/`allowMayAct` that
  same issuer may also have configured for delegation.

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
- **Delegation chain depth and size.** Re-exchanging an already-delegated
  token nests `act` further; `maxDelegationDepth` (10) and the complete
  serialized `act` claim limit (8 KiB) bound it. An exchange exceeding either
  limit fails with `invalid_grant`.
- **Discovery redirects.** The per-issuer HTTP client only follows same-host
  redirects (`networking.SameHostRedirectPolicy()`), so an issuer whose
  `/.well-known/openid-configuration` redirects cross-host cannot be
  onboarded via discovery — set `jwksUrl` explicitly to skip discovery.
- **JWKS caching.** A successful fetch is cached and refreshed on the fixed
  five-minute `jwksRefreshInterval`, independent of response `Cache-Control` or
  `Expires` headers. The 30-second figure is `jwksFetchFailureBackoff`, which
  gates only how often a persistently-failing issuer is retried before
  its first successful fetch — so a just-corrected `jwksUrl` can keep failing
  for up to 30s before the next retry; once a fetch has ever succeeded, this
  backoff no longer applies.
- **Diagnostics.** A non-allowlisted/non-matching actor, a runtime
  `actorMatcher` CEL evaluation failure, and an untrusted issuer all surface
  as the same generic `invalid_request` ("The subject token is invalid or
  could not be verified."), per RFC 8693 §2.2.2. The only discriminator is a
  safe `slog.Debug` log line (the matcher diagnostic names only the configured
  issuer and CEL error, never claims or tokens), so diagnosing a failed
  exchange needs debug logging enabled. The two exceptions are startup
  `slog.Warn` lines — an issuer with both empty `allowedActors` and
  `actorMatcher`, and an `expectedAudience` that does not look like a resource
  identifier — the only non-DEBUG diagnostics this feature emits.
- **HTTPS enforcement for a trusted issuer.** `validateTrustedIssuerURL`
  (`pkg/authserver/config.go`) validates `issuer_url` at config time and,
  unlike the server's own issuer, grants no localhost exemption: an
  `http://localhost` trusted issuer is rejected unless that issuer's own
  `insecure_allow_http` is set. There is no separate runtime scheme check for
  `issuer_url` — only `jwks_url` gets one (`ValidateJWKSURL`, on every fetch;
  shared verbatim between the runtime choke point and the config-time check
  so the two can't drift). The operator exposes both delegate clients and
  `trusted_issuers` on `EmbeddedAuthServerConfig` (`delegateClients` and
  `trustedIssuers`), so both self-issued and external-issuer token exchange
  are configurable through the CRD.
- **`allowPrivateIPs` requires `jwksUrl`.** Enforced at config time by
  `validateTrustedIssuers` (`pkg/authserver/config.go`) for every RunConfig.
  Without a hand-configured `jwksUrl`, OIDC discovery — a document fetched
  from, and thus influenceable by, the external issuer itself — would choose
  the private JWKS dial target, which is exactly what pinning it to
  operator-supplied config prevents.
- **Private-CA discovery/JWKS fetch.** A trusted issuer's `caBundleRef`
  (`cmd/thv-operator/api/v1beta1/mcpexternalauthconfig_types.go`) names a
  namespace-local ConfigMap whose PEM bytes are added to the system roots for
  that issuer's own HTTP client only (`WithSystemRootsPlusCABundle`) — other
  issuers and the upstream-provider clients are unaffected. It has no effect
  when `insecureAllowHTTP` is set, since a plain-HTTP fetch never consults it.
- **Misconfiguration surfaces as a pod crash** for a hand-authored RunConfig
  passed directly to `pkg/authserver`, not an operator condition — check pod
  logs, not `kubectl describe`. Through the operator, a malformed `caBundleRef`
  is instead caught at reconcile time and reported as
  `ConditionReasonInvalidCABundle` on the owning MCPServer, MCPRemoteProxy, or
  VirtualMCPServer before any RunConfig is built.

## Implementation

- `pkg/authserver/server/tokenexchange/handler.go` — `checkDelegationConsent`, `grantScopes`, `grantAndBoundAudiences`
- `pkg/authserver/server/tokenexchange/multi_issuer_validator.go` — `MultiIssuerTokenValidator`, `TrustedIssuer`, `resolveActorAuthorization`, `ValidateJWKSURL`
- `pkg/authserver/server/tokenexchange/validator.go` — `assignClaim`, `buildValidatedClaims`, `validateMayActShape`, `scpToScopeString`
- `pkg/authserver/config.go` — `DelegateClientRunConfig`, delegate-client validation, `validateTrustedIssuerURL`, `validateJWKSEndpointURL`, `validateTrustedIssuers`, `warnTrustedIssuerAudiences`
- `cmd/thv-operator/api/v1beta1/mcpexternalauthconfig_types.go` — `DelegateClientConfig`, `TrustedIssuerConfig`, `EmbeddedAuthServerConfig`
- `cmd/thv-operator/pkg/controllerutil/authserver.go` — `buildDelegateClientRunConfigs`, `buildTrustedIssuerRunConfigs`, `BuildAuthServerRunConfig`
- `pkg/authserver/runner/embeddedauthserver.go` — delegate-client secret-reference resolution at startup
- `pkg/authserver/server_impl.go` — static delegate-client registration and precedence over an existing storage registration
- `pkg/authserver/server/handlers/discovery.go` — advertised token-exchange and JWT-bearer grants, client-secret authentication methods
- `cmd/thv-operator/api/v1beta1/mcpexternalauthconfig_types.go` — shared `DelegateClientConfig` CRD contract
- `cmd/thv-operator/pkg/controllerutil/authserver.go` — Secret-reference to pod-environment conversion
- `pkg/authserver/server/tokenexchange/jwt_bearer_handler.go` — `JWTBearerHandler`, `JWTBearerIssuanceFactory`, `NewSharedTrustedIssuerValidator`, synthetic client-ID minting
- `pkg/authserver/storage/types.go` — `SyntheticClientIDPrefix`, `NewSyntheticClient`, `IsSyntheticClientID`
- `pkg/authserver/storage/redis.go` — synthetic-client-aware `marshalRequester`/`unmarshalRequester`
