# Auth Server Storage Architecture

The embedded authorization server uses a pluggable storage backend to persist OAuth 2.0 state. This document describes the storage architecture, the available backends, and the Redis Sentinel implementation.

## Overview

The auth server stores OAuth 2.0 protocol state including access tokens, refresh tokens, authorization codes, PKCE challenges, client registrations, user accounts, and upstream IDP tokens. Two storage backends are available:

1. **Memory** (default): In-process storage with mutex-based concurrency. Suitable for single-instance deployments.
2. **Redis**: Shared storage backed by Redis. Supports standalone mode (single endpoint, suitable for managed services like GCP Memorystore Basic and AWS ElastiCache without cluster mode), Cluster mode (Redis Cluster discovery endpoint, e.g. GCP Memorystore Cluster and AWS ElastiCache with cluster mode enabled), and Sentinel mode (high-availability with automatic failover). Required for horizontal scaling across multiple auth server replicas.

```mermaid
graph TB
    subgraph "Auth Server Replicas"
        AS1[Auth Server 1]
        AS2[Auth Server 2]
        AS3[Auth Server N]
    end

    subgraph "Storage Backend"
        direction TB
        Memory[In-Memory Storage<br/>Single instance only]
        Redis[Redis<br/>Standalone, Cluster, or Sentinel<br/>Shared state]
    end

    AS1 -.->|single instance| Memory
    AS1 -->|distributed| Redis
    AS2 -->|distributed| Redis
    AS3 -->|distributed| Redis

    subgraph "Redis Deployment Options"
        Standalone[Standalone<br/>Managed services]
        Cluster[Redis Cluster<br/>Managed services]
        Sentinel[Sentinel Cluster<br/>Self-managed HA]
    end

    Redis --> Standalone
    Redis --> Cluster
    Redis --> Sentinel

    style Memory fill:#fff3e0
    style Redis fill:#e1f5fe
    style Standalone fill:#e8f5e9
    style Cluster fill:#e8f5e9
    style Sentinel fill:#e8f5e9
```

## Storage lifecycle

`authserver.New` and `runner.NewEmbeddedAuthServerWithStorage` borrow supplied
storage on both success and failure. Previously, closing either server closed
the supplied backend, and the runner also closed it on construction errors.
Callers must now explicitly close storage after every server and other user has
stopped. The DCR store accessor is borrowed too; its lifetime follows the storage
owner rather than the server instance. Construction is not transactional: a
failure may leave writes in the borrowed backend, even though instance-owned
runtime resources are released.

`Server.Close` is safe to call concurrently or repeatedly and returns the same
joined cleanup errors. It releases instance-owned upstream idle connections,
metric callbacks, SPIFFE bundle workers, and trusted-issuer JWKS workers, but never
storage. Stop accepting requests and drain in-flight requests before closing a
superseded instance, even when its replacement shares the same backend.
`CloseIdleConnections` remains safe on a live server, but is not a retirement
operation: it does not stop background workers or runner DCR re-check loops.

`runner.NewEmbeddedAuthServer`, which creates its own backend, retains ownership.
It closes that backend exactly once on construction failure or shutdown, after
runtime cleanup, even if runtime cleanup reports an error. Its `Close` also stops
the listener and DCR re-check loops. This does not change `RedisStorage`'s own
ownership of injected versus internally created Redis clients.

## Storage Interface

The storage layer implements multiple interfaces from the [fosite](https://github.com/ory/fosite) OAuth 2.0 framework, plus ToolHive-specific extensions:

**Fosite interfaces:**
- `oauth2.AuthorizeCodeStorage` — Authorization code grant
- `oauth2.AccessTokenStorage` — Access token persistence
- `oauth2.RefreshTokenStorage` — Refresh token with rotation
- `oauth2.TokenRevocationStorage` — Token revocation (RFC 7009)
- `pkce.PKCERequestStorage` — PKCE challenge/verifier (RFC 7636)

**ToolHive extensions:**
- `ClientRegistry` — Dynamic client registration (RFC 7591)
- `UpstreamTokenStorage` — Upstream IDP token caching with user binding
- `PendingAuthorizationStorage` — In-flight authorization tracking. Interactive requests have a single-use `awaiting` consent stage before upstream login; only an `approved` record can complete a callback. Each `PendingAuthorization` carries a `BrowserBindingHash`, the SHA-256 of a random cookie value set on the user agent. The consent POST and `/oauth/callback` require the matching cookie before consuming their respective records. Only the hash is stored; an older row without a binding or approval stage cannot complete the flow and must be restarted. `PendingDeviceLogin` carries the same browser binding for the separate device-flow verification-page login, which shares `/oauth/callback`.
- `RememberedConsentStorage` — Optional server-side browser sessions and one approval per `(userID, clientID)`. The browser holds only a random `__Host-thv_consent_<sha256>` secret; storage holds its SHA-256 digest. Each approval records the redirect URI, scopes, and resource selected for that client. Memory and Redis implement this capability; decorators expose the underlying backend via `storage.Unwrap`. See [Interactive client approval](#interactive-client-approval).
- `UserStorage` — Internal user accounts and provider identity linking
- `DCRCredentialStore` — DCR client metadata and credential persistence; intentionally NOT embedded in `Storage` (each backend implements it separately and call sites reach it via an explicit `stor.(DCRCredentialStore)` type assertion). Secret-based clients store hashed secrets; `private_key_jwt` clients store their inline public JWKS and signing algorithm. Outbound (upstream) registrations resolved by `pkg/auth/dcr` are keyed by `(Issuer, UpstreamID, RedirectURI, ScopesHash)` plus the optional operator-set `dcrConfig.rotationKey`. `dcrConfig.clientName` sets the RFC 7591 `client_name` sent at registration; when empty, ToolHive registers as "ToolHive MCP Client". It is display-only metadata shown on the upstream's authorization screen and is never part of the registration's cache key, so changing it does not rename an already-registered client: the new value applies only the next time ToolHive registers a fresh client, for example after rotating via `rotationKey`. It is capped at 256 characters. Names with leading or trailing whitespace, or containing control or format characters, are rejected. Changing the rotation key addresses a new row, so the next start registers a fresh upstream client; the previous row stays in place and carries no TTL unless the upstream asserted `client_secret_expires_at`. The client registers with the token endpoint auth method the resolver prefers among those the upstream advertises (`client_secret_basic`, then `client_secret_post`, then `none`), or with the OAuth2 upstream's `tokenEndpointAuthMethod` when it is set, for an upstream whose token endpoint does not accept the preferred method. A registration response that names another method than the configured one is refused, and so is a resolution that still names one, such as a row a replica on an older configuration stored first; the next start or recheck replaces that row. On a cache hit the resolver also checks the cached client against the upstream and replaces the row when the upstream rejects it, or when the row was registered with another method than the configured `tokenEndpointAuthMethod`, provided the store implements `dcr.Replacer`. `dcrConfig.registrationProbe` selects the check: `tokenEndpoint` (default) redeems a random authorization code and reads `invalid_client` as "gone"; `authorizationEndpoint` sends an authorization request without following redirects and reads a direct 400/401/403 carrying an RFC 6749 JSON error object with `error` `invalid_client`, `unauthorized_client` or `invalid_request` as "gone" (RFC 6749 §4.1.2.1); `none` skips the upstream check but not the method check above. Each setting has a blind spot the rotation key covers: `tokenEndpoint` misses an upstream that validates the grant before authenticating a public client (it then answers `invalid_grant` for any client_id) and skips `private_key_jwt` clients, which it cannot sign for; `authorizationEndpoint` misses an upstream that renders its unknown-client error with 200, a redirect, or a page rather than a JSON error object; with `none` only the rotation key, or a change of the configured method, retires a registration. A false rejection by either probe re-registers at every start and, when registration also fails, fails startup. The check runs at start; `dcrConfig.registrationProbeInterval` (a Go duration of at least `1m`) repeats it while the server runs. Each tick re-runs the resolution against the stored row: a rejected registration is replaced, a missing one is registered, and when the result names a different client than the one in use, including a row another replica replaced, the upstream provider switches to it in place (`upstream.ClientCredentialsUpdater`; OIDC providers also replace their ID token verifier). A failed tick is logged and leaves the provider on its current client. An authorization started under the previous client and completed after the switch redeems its code with the new client, and upstream refresh tokens bound to the previous client stop refreshing, as with a restart. Each tick sends at most one probe request to the upstream from every replica (none with `registrationProbe: none`), plus a registration request when the probe rejects the client; the `tokenEndpoint` probe appears in upstream logs as a failed code redemption, so an interval of 5m to 15m suits most upstreams. Endpoints the run config leaves to discovery follow the new registration, and each replica waits a random part of the interval before its first tick and the interval ±10% after that.
- `AssertionJWTConsumer` — atomic single-use consumption for validated JWT assertions. It is deliberately separate from `Storage`, so only assertion-grant composition requires replay protection.
- `ExchangeGrantStorage` — optional server-side records behind refresh tokens issued through RFC 8693 token exchange. A callable discovers it with `storage.Unwrap(stor).(ExchangeGrantStorage)`. Memory and Redis implement it. See [Exchange grant storage](#exchange-grant-storage).

**Implementation:**
- Interface definitions: `pkg/authserver/storage/types.go`
- Outbound DCR cache key and scope hash: `pkg/oauthproto/dcrkey` (standard library only). `storage.DCRKey` and `storage.ScopesHash` are deprecated aliases.
- Outbound DCR persistence contract: `pkg/oauthproto/dcrstore` owns `Credentials`, the four-method `Store`, `Validate`, and the concurrent-change sentinel. The shared not-found sentinel lives in `pkg/storageerr`; `storage.ErrNotFound` is the same value, and stores signal a miss by matching it with `errors.Is`. `storage.DCRCredentials` and `storage.DCRCredentialStore` are aliases of the `dcrstore` types.
- OAuth token-endpoint error classification: `pkg/oauthproto/oautherr` owns the transient, permanent, rejected-refresh-grant, and parse-error predicates. DCR probes, authserver upstream providers, and client token sources import it directly. Its implementation and tests have no dependency on `pkg/auth` or `pkg/authserver`; `pkg/auth/oautherr` retains deprecated forwarding functions only. Caller-specific error wrappers, sentinels, redaction, and HTTP response mappings remain with their existing owners, unchanged.
- Resolver boundary: `pkg/auth/dcr` must not import `pkg/authserver`. `dcr.NewStorageBackedStore` adapts any `dcrstore.Store`, including the memory and Redis backends here, and never closes or unwraps it. `dcr.NewInMemoryStore` is an operation-scoped map for CLI flows, with no sweeper or capacity limit.
- Global resolver singleflight remains process-wide and keyed by the existing five-field encoding, not by store identity. Isolation between independent stores with identical keys is deferred; shared storage also does not provide distributed singleflight.
- Memory backend: `pkg/authserver/storage/memory.go`
- Redis backend: `pkg/authserver/storage/redis.go`

## Upstream subject identifiers in access tokens

The auth server can optionally include an RFC 9493 `sub_id` claim for access tokens issued after an upstream OIDC login through the authorization-code or device flow, including access tokens minted from their refresh tokens (not token exchange, jwt-bearer, or client credentials). The claim uses the issuer and subject from the validated upstream ID token:

```json
"sub_id": {
  "format": "iss_sub",
  "iss": "https://identity.example.com",
  "sub": "upstream-subject"
}
```

Together with `sub_id`, the auth server sends a second claim that a consumer can key on across upstream app registrations. Its name, `https://toolhive.dev/upstream_subject`, is a collision-resistant (public) claim name under RFC 7519 section 4.2:

```json
"https://toolhive.dev/upstream_subject": {
  "iss": "https://login.microsoftonline.com/<tenant-GUID>/v2.0",
  "claim": "oid",
  "value": "<the user's oid>"
}
```

- `iss` is the validated issuer of the upstream ID token, the same value as `sub_id.iss`.
- `claim` is the upstream's `subject_claim` setting, or `"sub"` when the setting is not set.
- `value` is that claim's value from the same validated ID token. It is the value ToolHive already uses to key its own local user.

The claim is sent only when `sub_id` is sent, under the same setting. It is present for every OIDC upstream, also when `claim` is `"sub"` and `value` repeats `sub_id.sub` (Okta, Google, Keycloak). A consumer therefore always reads one claim, and the claim keeps the same structure when an operator sets `subject_claim` later (its values still change; see below). A consumer that needs an identifier that stays the same across upstream app registrations should key on (`iss`, `claim`, `value`), not on `sub_id`.

Rules for consumers:

- Compare all three members as exact strings. Never compare `value` alone: the same person has a different `iss` for Entra v1 and v2 endpoints, for an Okta org and a custom authorization server, and for custom URL domains.
- Allowlist the (`iss`, `claim`) pairs you accept, for example (`https://login.microsoftonline.com/<tenant-GUID>/v2.0`, `oid`), and fail closed on any other pair. Use the exact `iss` string that appears in the upstream ID token.
- Treat the value as an ownership key only when `claim` names an immutable identifier that the IdP never assigns to another person, such as `sub` or Entra `oid`. `email`, `preferred_username`, `upn` and `unique_name` can be reassigned to another person, who would then inherit the previous owner's sessions. Microsoft documents Entra `email` as mutable and not guaranteed to be correct.

`subject_claim` is not restricted by the auth server. The same value already keys the local user, so this adds no new risk inside ToolHive, but a consumer that trusts the exported key must apply the allowlist above.

This identifies the same user as the token's existing ToolHive `iss` and `sub`; those outer claims do not change. Pure OAuth2 upstreams (including those that resolve identity through userInfo or identityFromToken) and synthetic identities never receive `sub_id` or the upstream subject claim. A later upstream login in a multi-provider chain does not replace or fill in the identity established by the first login.

Disclosure is disabled by default. Only Go code that embeds the auth server can enable it:

- Callers of the embedded auth server runner pass `runner.WithSubjectIdentifierAudiences`.
- Callers of `authserver.New` set `Config.SubjectIdentifierAudiences`.

```go
server, err := runner.NewEmbeddedAuthServerWithStorage(ctx, runConfig, stor,
	runner.WithSubjectIdentifierAudiences([]string{"https://mcp.example.com"}))
```

The setting is not part of the RunConfig, so config files, `thv`, vMCP and operator CRDs cannot enable it.

Each entry must be an exact member of the allowed audiences, and an empty list disables disclosure. The setting only controls whether `sub_id` and the upstream subject claim appear; it never grants an audience. The setting is re-evaluated for newly issued and refreshed access tokens, so turning it off stops the claims at the next refresh; it cannot retract identifiers already disclosed in existing tokens. Turning it on adds the claims only to sessions whose login happened while the list was non-empty; other users get them from their next login. Adding an audience to a list that is already non-empty takes effect at the next refresh, because logins record the identity even for unlisted audiences. (`claim`, `value`) is fixed when the user logs in. If an operator changes `subject_claim`, the new claim takes effect at the user's next login (which also changes ToolHive's own `sub`), not at refresh.

Sending `sub_id` and the upstream subject claim together links Entra's pairwise `sub` to the tenant-wide `oid`. RFC 9493 section 6.1 asks transmitters to send several identifiers for one subject together only when the recipient already knows they are related or the correlation is needed. Here the correlation is the purpose of the feature, and only the listed audiences receive it, so list only first-party resource servers. `oid` differs per tenant, so it does not correlate a user across tenants.

A device-flow token gets every audience its client is registered with. For a dynamically registered client that is the full allowed-audience list at registration time. So device-flow tokens carry `sub_id` and the upstream subject claim only when all of those audiences are listed.

### What `sub_id.sub` contains

`sub_id.iss` and `sub_id.sub` are the exact `iss` and `sub` of the validated upstream ID token. If an OIDC upstream sets `subject_claim` to another claim, ToolHive keys its local user on that claim, but `sub_id.sub` is still the raw `sub`. So `sub_id.sub` can differ from the subject ToolHive uses to identify the user. This is deliberate: RFC 9493 `iss_sub` means the issuer's own `iss` and `sub`.

Some IdPs issue a `sub` that is unique per application. Microsoft Entra ID (workforce and external tenants, not Azure AD B2C) is the common example: its `sub` is different for each app registration (client ID), while `oid` is the same across apps. If you register the upstream app again with a new client ID, every user's `sub` changes, so every `sub_id` changes. A client that stored the old value (for example to record who owns a session) will no longer match. Switching between the Entra v1 and v2 endpoints changes `iss` with the same effect. With `subject_claim: oid`, ToolHive's own `sub` stays stable across such a change, but `sub_id` still changes.

The upstream subject claim stays the same across app registrations when `subject_claim` names a cross-app claim such as Entra's `oid`. It still changes when `iss` changes, and at the user's next login after `subject_claim` is changed.

### One upstream identity, one user

The auth server keeps one user per identity link. Two different values describe an upstream identity, and they are not the same key:

- The **link key** finds the local user. It is the upstream's `name` (the provider ID) plus the value of its `subject_claim` (`sub` when the setting is not set).
- The **`sub_id`** records the raw `iss` and `sub` of the upstream login. It is not used to find users.

Two users carry the same `sub_id` when the link key changes while the raw `iss` and `sub` stay the same. Two different `sub_id` values can also end up on one user, when the link key stays the same and the raw `iss` or `sub` changes (see the list below).

Only the first configured upstream creates users. Every login chain starts there, and later upstreams in a chain carry the user already resolved by the first one. A later upstream never creates a user or a link. If a login reaches user resolution through a later upstream without a carried user (for example a `SingleLeg` authorization that sets no `ResolvedUserID`), the auth server looks the identity link up. It resolves to the linked user when a link exists. When none does, the browser callback refuses with `access_denied` and the device flow shows a 403 page. The subject resolver for ID-JAG redemption is built from the first upstream only.

Today only the browser callback and the device flow create users, both through `UserResolver.ResolveUser`. Token exchange, JWT bearer, ID-JAG redemption, delegate clients and SPIFFE client credentials only look users up, and synthetic identities skip the resolver. Any future path that creates users (for example provisioning on peer-issuer ID-JAG redemption) must go through `UserResolver` so it shares the behaviour below.

A login that is in flight when the upstreams are reordered is refused if it would create a user through an upstream that is no longer first, and succeeds if that upstream's identity is already linked. A login in flight when its upstream is renamed fails earlier, with `server_error`, because the old name is no longer configured. In both cases the person starts the login again.

When two first logins for the same identity run at the same time, the identity link is written only if no link exists yet, on both the memory and Redis backends. The login that loses reads the link the winner wrote, rolls back the user it created, and returns the winner's user, so both logins get the same user. If reading the winner's user fails, the losing login fails with `server_error` and the person signs in again. Every failed link write is settled by reading the link before anything is deleted, because the error may not say whether the write committed (for example a lost reply from Redis). The resolver keeps its own user if the link points at it, and adopts the owner's user if the link points elsewhere. If there is no link, or the link cannot be read, it deletes nothing and returns the error: a write that timed out can still commit after the read. A user row left without a link is harmless, because nothing resolves to it.

Which key a change affects decides what happens:

- **Link key changes, raw `iss` and `sub` stay the same.** The next login creates a new user, so one person has two users with the same `sub_id`. This happens when:
  - the value of `subject_claim` changes for one person (for example `subject_claim` is `email` or `preferred_username` and the user's email changes);
  - `subject_claim` itself is changed to a claim with a different value for that person (this also gives a new ToolHive `sub`);
  - the first upstream is renamed, because the link key uses the upstream name and existing links no longer match;
  - the upstreams are reordered so that another upstream is first. Existing links are kept, but the new first upstream has no links of its own, so it creates new users.
- **Link key stays the same, raw `iss` or `sub` changes.** The login finds the existing user, so one user carries two different `sub_id` values. This happens when an upstream keeps its name but points at another issuer. Subject values from different IdPs can then collide, and two different people can share one user. Binding each link to the issuer and subject claim that created it is tracked as "Bind provider-identity rows to the issuer and subject claim that created them" (#7256).

### Upstream IdP notes

**Microsoft Entra ID.**

- Set `subject_claim: oid`.
- Use the tenant-specific v2.0 authority `https://login.microsoftonline.com/<tenant-GUID>/v2.0`, with the tenant GUID rather than a domain name. The `common`, `organizations` and `consumers` authorities publish a templated issuer (`.../{tenantid}/v2.0`) in discovery, which never equals a token's `iss`, so ToolHive's strict issuer check rejects them.
- `oid` is in the ID token only when the `profile` scope is requested. ToolHive's default scopes include it; if you override the scopes without it, logins fail.
- v1.0 tokens use the `https://sts.windows.net/<tid>/` issuer, so any other system that compares identities must use the same v2.0 endpoint.
- A B2B guest has a different `oid` and `iss` in each tenant: two identities by design.
- Azure AD B2C: the issuer in the ID token depends on the tenant's token-compatibility setting (default `https://<domain>/<tenant-GUID>/v2.0/`; the alternative `.../tfp/<tenant-GUID>/<policy>/v2.0/` includes the policy, so it differs per user flow). The authority's discovery issuer must equal the token's `iss`; check this in your tenant before relying on it. By default B2C `sub` is the user's object ID and is not pairwise, so leave `subject_claim` unset.

**Okta.**

- Okta documents the ID token `sub` as the user's unique identifier; in practice it is the Okta user ID (`00u...`). Leave `subject_claim` unset. Do not use the access-token `sub`, which can be the login or email. Okta's documentation does not state that the ID token `sub` is the same across OIDC apps or that it is never reassigned, so treat those as untested assumptions.
- The issuer differs between the org authorization server (`https://<org>.okta.com`), each custom authorization server (`https://<org>.okta.com/oauth2/<id>`) and a custom URL domain. A custom authorization server's issuer mode (`ORG_URL`, `CUSTOM_URL`, `DYNAMIC`) decides which hostname appears in `iss`; with `DYNAMIC`, `iss` follows the hostname the request used. An administrator changing the mode or enabling a custom domain changes `iss` for new tokens. Systems that compare identities must use the same authorization server and hostname.
- Do not use `uid` (it appears only in access tokens), or `email`, `preferred_username` or `login`, as the subject claim.

### Storage and upgrades

The verified upstream identity is recorded only when disclosure is enabled (`SubjectIdentifierAudiences` is non-empty). When the list is empty, new logins do not record it, because nothing reads it. When it is recorded, it is stored on pending authorizations, device requests, pending device confirmations, and in the stored authorization-code, access-token and refresh-token sessions. In Redis it is the JSON key `verified_upstream`, an object with the members `iss`, `sub`, `stable_claim` and `stable_value`. The stored names `stable_claim` and `stable_value` differ from the emitted `claim` and `value` because the stored object also holds the raw `sub`. Sessions created on v0.57.0, or while the list was non-empty, keep the stored identity until their refresh token expires; there is no cleanup step. Sessions whose login happened while the list was empty have none, so they get no `sub_id` or upstream subject claim until the user logs in again. Do not switch the key you use for a user based on whether `sub_id` is present: a user can lack it until their next login, and nothing in a log, metric or response explains the absence. If disclosure is enabled by a rolling restart, logins served by a replica that still has the empty list record nothing, so those users get no `sub_id` until their next login after every replica has the setting.

The Redis key layout, Lua scripts and TTLs do not change. Replicas running the older version decode every record this version writes; they drop the unknown field.

After disclosure is turned off, stored access and refresh sessions may still hold the claim issued earlier. Replicas running this version never issue it again: every token request removes the claim first and adds it back only if the current setting allows it. Older replicas do not do this (see below).

During a rolling upgrade or after a rollback, replicas running an older version do not know the `verified_upstream` field. When an older replica handles an upstream callback, a code redemption, a device-code poll, or a refresh, it drops this field from what it stores. A `sub_id` or upstream subject claim already present in a stored refresh session is a plain JWT claim to older code, so an older replica that refreshes the session issues both claims again unchanged, without checking the disclosure setting, and keeps them in the session it stores. The first refresh after that on a replica running this version removes the claims and cannot add them back. From then on, tokens from that refresh chain carry neither claim until the user logs in again through an upgraded replica.

The device flow has two more cases. When an older replica handles the device confirmation, or serves any device token poll after the confirmation (each poll rewrites the stored device request), the stored upstream identity is lost and that login gets neither claim.

Rollout order for embedders that enable disclosure: first roll out this version with disclosure off, and enable it only when no older replica is left. Users get the claims from their next login after that. To roll back, turn disclosure off and let the new version serve refreshes before going back to the older version.

## Access token type (`typ`)

Every JWT has a JOSE header, and its `typ` member says what kind of JWT it is. By default the auth server writes `"typ": "JWT"`. An embedder can opt in to `"typ": "at+jwt"`, the value RFC 9068 section 2.1 defines for JWT access tokens:

```json
{"alg":"RS256","kid":"…","typ":"at+jwt"}
```

The setting is Go-only. Set `authserver.Config.AccessTokenType`, or pass `runner.WithAccessTokenType`. The accepted values are `""` and `"JWT"` (the default) and `"at+jwt"`. Any other value makes server construction fail. The setting is not a RunConfig field, so config files, the CLI and operator CRDs cannot set it yet.

When the value is `at+jwt`, every JWT access token the server issues gets that type. This covers the authorization code grant (with PKCE), the refresh grant, the device code grant, RFC 8693 token exchange, the RFC 7523 JWT-bearer grant, ID-JAG redemption and the SPIFFE client credentials grant. The client credentials grant uses the same access-token strategy as the others, so it gets the same `typ`. Refresh tokens and authorization codes are opaque HMAC tokens, not JWTs, so the setting does not change them. An ID-JAG this server may issue in the future keeps its own type, `oauth-id-jag+jwt`.

The type is written when a token is signed. It is not stored with the session. A change therefore takes effect at the next refresh, including for sessions created before the change.

ToolHive's own consumers accept both types and preserve compatibility with ordinary tokens that omit `typ`. The token validator in `pkg/auth` (also used by vMCP) and the token exchange grant's subject- and actor-token validators specifically reject `oauth-id-jag+jwt`, case-insensitively and with or without the optional `application/` prefix. This is targeted ID-JAG rejection, not a general access-token type allowlist: an ID-JAG cannot be used in place of an access token, even with an otherwise valid signature and claims. Configuring an expected audience remains the main defence against one token type being used in place of another; this check is a backup.

Assertion redemption is separate from those access-token validation paths. The JWT-bearer grant only accepts assertions from configured trusted issuers, and the server's own issuer can never be one, so it never accepts this server's own access tokens as assertions. With `at+jwt` enabled, the grant also refuses them by `typ`: it accepts assertions with no `typ`, `""` or `"JWT"`. These values are compared exactly. An assertion whose `typ` is exactly `oauth-id-jag+jwt` is routed to the ID-JAG redemption handler, which requires client authentication, so the plain JWT-bearer handler never sees one and refuses that value if it ever did.

Redemption is deliberately narrower than the deny side above. RFC 7515 Section 4.1.9 says `typ` is compared case-insensitively and a value without `/` is treated as if `application/` were prepended, and the access-token checks follow that rule. Redemption compares the ID-JAG type exactly, as the ID-JAG draft requires of an issuer. An ID-JAG with a variant `typ`, such as `application/oauth-id-jag+jwt`, is refused with `invalid_grant` rather than redeemed. This fails closed. Revisit it if an issuer is found to emit a variant spelling.

Compatibility warning: some resource servers reject `at+jwt` by default. Java resource servers built on the default type verifier of Nimbus JOSE+JWT accept only `JWT` or no `typ`, and they reject `at+jwt`. This includes Spring Security before 6.5. Enable `at+jwt` only when every resource server that validates these tokens accepts it.

The default may switch to `at+jwt` in a later release, once the setting is also a RunConfig field.

## Non-fatal secondary chain legs

By default, a failed leg of a multi-upstream authorization chain ends the authorization with an error to the client. An embedder can opt in to treating a failed leg after the first as skipped. The setting is Go-only: set `authserver.Config.NonFatalSecondaryLegs`. It is not a RunConfig field, so config files, the CLI and operator CRDs cannot set it yet.

With the option on, these failures on a non-first leg are recorded and the walk goes on to the next missing leg: an error the upstream IdP returns in place of a code, a failed code exchange or identity resolution (ID token, userinfo, or token-derived subject) at the leg's callback, and a failure to build the leg's authorization URL. Each skipped leg is logged once at WARN with the provider name and failure class. The walk stores nothing for the skipped leg. A credential stored by an earlier authorization is left in place.

These stay fatal whatever the setting is: any failure on the first (identity) leg, a `SingleLeg` authorization, a missing, unknown, expired or replayed state, a failed browser-binding check, the consent-stage check, a pending whose stored redirect URI cannot be parsed, an upstream provider that is not configured, and any storage failure.

Skipped legs are carried in the `skipped_upstreams` field of the stored `PendingAuthorization` (Redis JSON, omitted when empty), so the next leg's callback does not prompt for them again. The handler validates the field when it loads the pending: every entry must be a member of the carried chain, must not be the first upstream, and must not repeat. A first-leg pending that carries a chain or skipped legs is rejected too. A pending that fails validation ends the authorization with a server error.

Because a kept credential may be unusable, the option has a caveat. ToolHive's token validator and the upstream refresh guard treat an unusable stored credential as fatal for the whole session. With storage that keeps credentials across authorizations, a kept credential that cannot be refreshed makes the issued token unusable until the credential is replaced.

During a rolling upgrade, an older replica does not know `skipped_upstreams`. If it handles the callback of a leg that carries the field, it drops the field when it stores the next pending, and it then prompts the skipped leg again. A second failure on that leg is fatal. A replica with the option off still honours skips that another replica recorded, for the life of that pending (10 minutes). Upgrade every replica before enabling the option. Before rolling back to an older version, turn the option off and let in-flight authorizations drain.

## Callback identity and storage context

The first upstream callback passes an `upstreamfilter.FilterPrincipal` to `upstreamfilter.UpstreamFilter`. Both contracts are owned by `pkg/authserver/upstreamfilter`, which does not depend on the root authserver, server, or storage packages. Go embedders implement this contract and set `authserver.Config.UpstreamFilter` without importing HTTP handlers. Previously these contracts were owned by `pkg/authserver/server/handlers`; `handlers.FilterPrincipal` and `handlers.UpstreamFilter` remain type aliases, so existing implementations and `handlers.WithUpstreamFilter` calls remain source-compatible. This changes import ownership only, not authorization behavior.

`FilterPrincipal` is a getter contract (`GetSubject`, `GetCanonicalUserID`, `GetName`, `GetEmail`, `GetClaims`), not a promise that implementations contain only those fields. The callback passes a private concrete projection with only those five credential-free values; tokens and upstream credentials are not included. The claims map is upstream-provided and read-only, and may be nil when unavailable.

`GetSubject()` is the first upstream's mapped subject, and `GetCanonicalUserID()` is the canonical storage-user ID. They are different values and must not be swapped. `pkg/authserver/upstreamfilter/contract.go` documents where each value comes from and the existing caller obligations: defensive claims decoding, fail-closed handling of missing claims for security-relevant filters, and read-only access. The filter still runs only on the first leg; the handler retains its private projection and continuation logic, preserves configured order, ignores unknown names, and fails authorization on filter error. No new concurrency or retained-value lifetime guarantees are introduced. Implementations written for the older `auth.PrincipalInfo` parameter still need the prior getter-signature migration; this ownership change does not restore that removed signature.

The server owns the credential-free canonical-user context contract in `pkg/authserver/identity`: `WithCanonicalUser` writes the explicit storage user and `CanonicalUserFromContext` reads only that key. An empty setter value leaves the context unchanged. This context value identifies which user storage should key on; it is not bearer authentication and does not establish an authenticated principal. The server getter intentionally does not interpret host identity-only contexts.

ToolHive bridges its host API to this contract: `auth.PlatformUserContextKey` aliases the server key, `auth.WithPlatformUser` and `auth.PlatformUserFromContext` wrap the server helpers, and `auth.CanonicalUserFromContext` checks the explicit key first, then falls back to the host `Identity.PlatformUserID`, typically attached on authenticated request paths. Context lookup alone is not proof of authentication. A direct explicit value, including an empty string, takes precedence. Host storage decorators that require this identity fallback must use the host getter or provide an explicit adapter; using the server getter alone on an identity-only host context will not resolve a user.

The device flow sets the canonical user to the resolved user of the confirmed
device code before it stores upstream tokens, with the option on or off. With
the option on, the refresher reads only through the user-keyed layer, which
refuses a row owned by another user with `ErrInvalidBinding`.

On the token validator's upstream load path for tokens of the embedded issuer, the explicit key is always set and the identity fallback is not used; see [Validator rule for embedded-issuer tokens](#validator-rule-for-embedded-issuer-tokens).

The group-based implementation in `pkg/authserver/upstreamfilter` strictly decodes claims with `pkg/oauthproto/claims.DecodeStringArray`; absent, malformed, or mixed-type group claims fail closed. The decoder belongs to the OAuth protocol claims package, not ToolHive's general `pkg/auth` package.

## Identity resolution for pure OAuth2 providers

For pure OAuth 2.0 upstream providers (`OAuth2Config`), OIDC is unavailable and there is no ID token. `BaseOAuth2Provider.ExchangeCodeForIdentity` resolves user identity through a three-way priority chain. Each path has distinct implications for `UserStorage`, `UpstreamTokenStorage`, and the Redis secondary index.

### IdentityFromToken (priority 1)

An operator opt-in path that extracts identity claims directly from the token endpoint response body, skipping the userinfo HTTP call entirely.

**When the path triggers.** `IdentityFromToken` is configured on the upstream provider (`p.config.IdentityFromToken != nil`). The `tokenResponseRewriter` intercepts the token endpoint response and runs extraction against the raw pre-rewrite body; the result is available to `ExchangeCodeForIdentity` without an additional round-trip.

**Subject format.** Real, stable subject string extracted from the token response body via a gjson dot-notation path (e.g. `username`, `authed_user.id`). For token responses that embed a JWT, the `@upstreamjwt` modifier decodes the payload for further drilling (e.g. `access_token|@upstreamjwt|sub`). The `@upstreamjwt` modifier performs no signature verification — it is intended only for JWTs received directly from the upstream token endpoint over a TLS-authenticated channel. The returned `*Identity` carries `Synthetic = false`. Path semantics and trust-model notes are documented on the runtime config struct `IdentityFromTokenConfig` in `pkg/authserver/upstream/identity_from_token.go`. The corresponding CRD type (`cmd/thv-operator/api/v1beta1.IdentityFromTokenConfig`) is defined in a sibling PR; operator-to-runner translation of this config lands separately.

**Go embedders.** `authserver.New` and `upstream.NewOAuth2Provider` register `@upstreamjwt` automatically before constructing providers. This also covers `authserver.DefaultUpstreamFactory` and both embedded runner constructors, including `NewEmbeddedAuthServerWithStorage`. Previously, callers that bypassed `NewEmbeddedAuthServer` needed explicit registration or modifier-bearing identity paths failed at token exchange. Existing calls to `upstream.RegisterModifiers()` remain safe: registration is guarded by a process-wide `sync.Once`.

**`UserResolver` interaction.** Because `Identity.Synthetic` is false, `callback.go` takes the normal path: `UserResolver.ResolveUser` runs, a row is created (or looked up) in `UserStorage`, a provider-identities entry is written, and `UpdateLastAuthenticated` is called. `UpstreamTokens.UserID` carries the resolved internal user UUID, not the raw operator-supplied subject string.

**Reverse-index implication (Redis backend).** Stable user IDs mean `KeyTypeUserUpstream` works as designed — one set per user accumulates that user's upstream row keys across re-authentications. No set churn.

**Operator visibility.** The `IdentitySynthesized` condition does not fire for upstreams using `IdentityFromToken`. `SyntheticIdentityUpstreams()` (the controller-side predicate that drives the condition) skips upstreams where either `userInfo` or `identityFromToken` is configured — only the dual-unconfigured case is reported as synthesis-mode.

**Implementation.**
- `pkg/authserver/upstream/oauth2.go` — `ExchangeCodeForIdentity` priority 1 branch
- `pkg/authserver/upstream/identity_from_token.go` — `IdentityFromTokenConfig`, `extractIdentityFromTokenResponse`, `@upstreamjwt` modifier
- `pkg/authserver/upstream/token_exchange.go` — `tokenResponseRewriter.RoundTrip` extracts identity from the raw pre-rewrite body

### UserInfo endpoint (priority 2)

Existing behavior. When `IdentityFromToken` is unconfigured and `userInfo` is set, `fetchUserInfo` is called with the upstream access token. Subject, name, and email come from the userinfo response. `UserResolver.ResolveUser` runs normally, `Identity.Synthetic` is false.

### Synthesis-mode subjects (priority 3)

Reached when both `IdentityFromToken` is unconfigured AND `userInfo` is absent. The embedded auth server synthesizes a non-PII subject by hashing the upstream access token. The mode changes what `UserStorage` and `UpstreamTokenStorage` see and is observable to operators inspecting stored state.

**When the path triggers.** Pure OAuth 2.0 upstream provider (`OAuth2Config`) where both `IdentityFromToken` and `userInfo` are unconfigured. Reached at `BaseOAuth2Provider.ExchangeCodeForIdentity` as the final fallback. OIDC providers and OAuth2 providers with either `IdentityFromToken` or `userInfo` configured are not affected.

**Subject format.** `tk-` followed by 32 lowercase hex characters (the first 16 bytes of `SHA-256(accessToken)`), e.g. `tk-89abcdef0123456789abcdef01234567`. The output is opaque: assuming the upstream issues opaque (non-JWT) bearer tokens, the digest reveals nothing about the input that an attacker holding a candidate token could not already confirm by re-hashing. The returned `*Identity` carries `Synthetic = true`; the `upstream.IsSynthesizedSubject(string)` predicate lets bare-string consumers recognize the prefix.

**`UserResolver` bypass.** The bypass is gated on `Identity.Synthetic` in `callback.go` — synthesis is the only path that sets this field. Synthetic identities skip `UserResolver.ResolveUser` entirely — no row is created in `UserStorage`, no entry is written to provider-identities, and `UpdateLastAuthenticated` is not called. The synthesized subject rotates per access token, so persisting it would create a fresh `users` row on every re-authentication. `UpstreamTokens.UserID` therefore carries the `tk-…` value directly rather than a stable internal UUID.

**Reverse-index implication (Redis backend).** The `KeyTypeUserUpstream` secondary-index set under `thv:auth:{ns:name}:user:upstream:{userID}` is designed around stable user IDs — one set per user, holding the keys of all of that user's upstream token rows (one per session and provider). Under synthesis the userID rotates with every re-authentication, so each session lands in its own one-element set. Reads continue to work, but set churn is much higher than under OIDC. The existing TODO in `pkg/authserver/storage/redis.go` (on `warnOnCleanupErr`) to scan and clean up stale secondary-index entries applies, and synthesis-mode workloads make a periodic scan more important.

**Operator visibility.** When at least one configured OAuth2 upstream has both `userInfo` and `identityFromToken` unconfigured, the controller surfaces the `IdentitySynthesized` condition on the `MCPExternalAuthConfig` and `VirtualMCPServer` status (Reason `IdentitySynthesizedActive`, naming the affected upstreams). The condition flips to `False` (Reason `IdentitySynthesizedInactive`) once every upstream has either `userInfo` or `identityFromToken` configured.

**Implementation.**
- `pkg/authserver/upstream/oauth2.go` — `synthesizeIdentity`, `synthesizeSubjectFromAccessToken`, `IsSynthesizedSubject`
- `pkg/authserver/upstream/types.go` — `Identity.Synthetic`
- `pkg/authserver/server/handlers/callback.go` — `UserResolver` bypass on `Identity.Synthetic`
- `cmd/thv-operator/controllers/mcpexternalauthconfig_controller.go` and `cmd/thv-operator/controllers/virtualmcpserver_controller.go` — `IdentitySynthesized` advisory condition

## Interactive client approval

For interactive authorization-code requests, `/oauth/authorize` validates the client, redirect URI and a single allowed resource **before** showing a consent page. If there is exactly one configured allowed audience, it is selected when `resource` is omitted; with multiple allowed audiences the client must specify one `resource`. For example, a client registered with `https://client.example/callback` can select an allowed audience at authorization time:

```http
GET /oauth/authorize?response_type=code&client_id=CLIENT_ID&redirect_uri=https%3A%2F%2Fclient.example%2Fcallback&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&resource=https%3A%2F%2Fmcp.example%2F&state=illustrative-state-value HTTP/1.1
```

Clients must generate a fresh, unguessable `state` for each request and verify the returned value; do not reuse the illustrative value above.

**Client compatibility:** When the server has multiple allowed audiences, clients must send `resource` on `/oauth/authorize` before consent; choosing it only at `/oauth/token` is too late. A single configured audience defaults when omitted at authorization, and a bound code or refresh token inherits its original audience when `resource` is omitted at the token endpoint. An explicit token-endpoint `resource` must match that audience exactly.

The page leads with a plain-language summary and the classified destination of the redirect ("this computer" for loopback, the host plus its punycode form for other http(s) hosts, "an app that opens `<scheme>://` links" for custom schemes, or the full URI when it cannot be classified), followed by the client name, labelled as given by the app and not verified. A collapsed "Technical details" section holds the full redirect URI, client ID, scopes, resource and the first upstream provider's internal name. It does not redirect to the upstream identity provider until a browser-bound, same-origin POST to `/oauth/consent` approves the request. Denial returns `access_denied` to the validated client redirect. The pending consent handle and each upstream callback state are single-use, browser-bound records with a 10-minute absolute lifetime from the initial authorization (not extended by chaining). The device flow uses its separate verification and approval page.

The "You will sign in with … next." line shows the upstream's optional `DisplayName` (`display_name` in `UpstreamRunConfig`; not yet settable from the `MCPExternalAuthConfig` CRD), falling back to its `Name` when the display name is unset, empty after sanitizing, or when the pending authorization's upstream is no longer configured. Technical details keep showing `Name` as the internal name. The label is resolved at render time from the handler's upstream list, so a handler rebuilt with a different upstream set shows the current value while pending authorizations in shared storage stay valid. `DisplayName` is display-only: it is never compared, persisted, or used as a key. Upstream token storage, remembered consent (`ProviderID`), pending authorizations (`UpstreamProviderName`) and callback routing all stay keyed by `Name`, so changing a display name leaves every stored record valid.

At render, control and format runes (bidi overrides, zero-width characters, newlines) are dropped, surrounding whitespace is trimmed, the result is capped at `maxConsentTextRunes` with a trailing ellipsis, and the template HTML-escapes it. Sanitizing removes only those runes: other invisible characters, such as Hangul fillers, are kept and can render as a blank label. A malformed label is cleaned at render and never rejected by validation, so a cosmetic field cannot fail server start or an embedder's rebuild of its upstreams.

**Embedder obligation.** Sanitizing does not judge meaning: printable misleading text such as "Acme Okta (verified)" is shown in full, and the page presents the label as trusted text on the anti-phishing gate. An embedder must therefore populate `DisplayName` only from operator-controlled values, never from tenant-, admin- or client-writable input, and not from metadata fetched from the upstream, such as its discovery document.

The form action uses the configured browser-facing authorization base, including any path prefix. The POST accepts only that base's browser-serialized Origin (including normalized IP addresses, domain names and ports), not a missing, null, or foreign Origin. `Referrer-Policy: same-origin` preserves Origin on native same-origin form submissions without leaking the consent URL to other sites.

The page's CSP blocks scripts, framing and base URL changes. It deliberately omits `form-action 'self'`: browsers also apply that restriction to the POST's redirect to an external IdP or validated client URI, including dynamic loopback ports. The template has only the escaped, configured form target, and the POST additionally requires the single-use browser-binding cookie. No COOP header is set on the consent page, to avoid severing an OAuth popup's opener.

On an HTTPS authorization endpoint with a backend implementing `RememberedConsentStorage`, the consent page offers a "Don't ask again for this app on this browser" checkbox that skips the page for 7 days (the browser session lifetime) when the same app asks for the same access. Only after a completed flow with a non-synthetic first-upstream identity does the server store an approval and issue a secure, HttpOnly, SameSite=Lax host-only cookie at `/` named `__Host-thv_consent_<sha256>`. The suffix is the full lowercase SHA-256 hex digest of the byte-length-prefixed ordered pair `(GetAccessTokenIssuer(), GetAuthorizationEndpointBaseURL())`, using the exact configured strings (the authorization base defaults to the issuer). This separates independent servers on the same browser host even across paths or ports, while replicas with the same pair use the same name. The server-side browser session expires after 7 days and the per-user, per-client approval after 30 days; neither lifetime slides on use. Re-approval overwrites the previous approval. Remember-storage write failures do not prevent code issuance; they simply leave the browser without reusable approval.

A later request can skip the page only if both records are live, the session provider matches the first configured upstream, the client ID and resource match, requested scopes are a subset of the approved scopes, and the redirect URI matches (except that ports may differ for the same registered loopback redirect). This does not bind a remembered approval to one local listener: a process listening on another allowed loopback port can receive a code for a request it initiates with the same client ID without a new ToolHive consent page if the upstream login completes as the remembered user; PKCE still applies. A downstream `prompt=consent` shows ToolHive's page again; `prompt=login` alone does not force upstream reauthentication. The upstream login still runs. Before writing user or upstream-token state, its first callback must resolve to the same non-synthetic provider subject and linked user; an account switch revokes the browser session and returns `access_denied`, so the client must start a new authorization. A missing, expired or successfully read but malformed record shows consent again; storage read and decode failures fail closed with `server_error`.

**Current boundaries:** When the embedded auth server is mounted on a transparent MCP proxy, that proxy removes every request cookie whose name starts with `__Host-thv_consent_` before forwarding to backends (including redirects and recovery), regardless of suffix; other backend cookies remain available. Proxies without an embedded auth server leave cookies untouched. There is no user-facing forget or sign-out control for remembered approvals; they expire automatically. The audience binding applies to newly authorized codes and their refreshes; refresh tokens issued before this binding retain their existing audience behavior. See [Redis storage configuration](../redis-storage.md#data-model) for the key schema and [remote MCP authentication](../remote-mcp-authentication.md) for client-side authentication context.

### Administrator consent preauthorization

The Go-only `authserver.Config.ConsentExemptClientIDs` field and the embedded runner's `WithConsentExemptClientIDs` option allow an embedder to skip ToolHive's consent page for explicitly listed client IDs. An empty list preserves the ordinary consent behavior. The policy skips only ToolHive's interactive consent page; `prompt=consent` takes precedence over the exemption and requires that page even when the client is allowlisted.

> Listing a client ID preauthorizes whatever registration currently holds that ID, for whichever user completes upstream authentication. It is not per-user consent, and runtime eligibility checks do not prove where the registration came from.

An ID qualifies only when explicitly allowlisted and its current registration is eligible: confidential, not back-channel-only, with the authorization-code grant, code response type and at least one redirect URI. At runtime, exemption also requires a code-only request with one explicit registered redirect URI and no loopback redirect rewrite. The client registration used for the decision is resolved normally at request time; startup validation does not freeze its metadata or prove its provenance.

This is an administrator trust decision over the registration currently at the ID, not user consent scoped to an account, redirect, scope, audience or grant. Replacing the registration's credentials or redirect URIs transfers the exemption to the replacement. Expanding its permitted scopes, audiences or grants can expand what is preauthorized. An embedder must control registration writes for exempt IDs. HTTP Dynamic Client Registration (DCR) can qualify when its server-minted ID is explicitly allowlisted and eligible, but DCR IDs are server-generated and registration is create-only: a DCR caller cannot choose or overwrite an exempt ID. CIMD replacement targets URL-shaped IDs, which this allowlist excludes. These registration-write restrictions do not restrict access to the authorization endpoint. A privileged provisioner with registration-write access can still replace an exempt registration; this policy does not protect against that.

`DCRIssued` continues to govern its existing retention and replacement behavior; it is not an exemption-eligibility signal. DCR-issued exempt clients remain subject to the existing TTL and in-memory eviction behavior. A client can expire or be evicted and then needs to be reprovisioned. The exemption does not change retention. Removing an ID restores ordinary consent decision-making for new authorization requests only: it does not revoke tokens or automatically cancel already-approved flows.

A consent-exempt ID does not make client admission serve a client. A stored client that admission hides is not found, so it is not eligible for the exemption. See "Client admission" in [Configured clients across restarts](#configured-clients-across-restarts). Listing such an ID fails startup.

This behavior is a deliberate standards trade-off. The [stable MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization) states: “MCP proxy servers using static client IDs MUST obtain user consent for each dynamically registered client before forwarding to third-party authorization servers.” ToolHive is that proxy. Allowlisting a dynamically registered client is a deliberate deviation from this requirement, not an exception. More broadly, administrator preauthorization trades individual user approval for the embedder's control over the allowlist and registration writes.

#### Downstream transaction binding remains the embedder's responsibility

Knowing an exempt client ID can be enough to send a victim an authorization link. If the victim already has an upstream SSO session, authentication may complete without interaction, and a code may be delivered to the legitimate client's callback without a new user action. The exemption does not establish user intent.

The downstream client must maintain its own binding for every authorization transaction:

1. Generate fresh, unpredictable `state` and an S256 PKCE verifier/challenge for each transaction.
2. Bind the stored transaction to the initiating client user/session and browser or equivalent user-agent context. A live state belonging to another user's session is not sufficient.
3. At the downstream callback, reject unsolicited, mismatched, expired, cancelled or already-consumed state before exchanging the code or attaching credentials to a session.
4. Select the PKCE verifier from that stored transaction, not from callback or other attacker-controlled input, and use it at the token endpoint.
5. Preserve issuer and other existing authorization-response checks.

State is checked at the callback; the verifier is used at token exchange and is not normally carried in the callback. PKCE and confidential-client authentication alone do not prove which browser or session initiated the callback, or that the user intended the initial authorization navigation. ToolHive's upstream state, PKCE and browser-binding cookie protect the upstream hop; they do not replace the downstream client's transaction binding.

#### Prompt behavior

`prompt=consent` takes precedence over both administrator exemption and remembered approval, so ToolHive displays its consent page. `prompt=none` and `prompt=login` are not specially implemented: an exempt `prompt=none` request may continue to upstream authentication, which is not guaranteed to be silent, and `prompt=login` does not force upstream reauthentication. The current behavior does not establish OIDC `prompt=none` compliance.

## Memory Backend

The in-memory backend uses Go maps protected by `sync.RWMutex` for thread safety. A background goroutine runs periodic cleanup of expired entries.

**Characteristics:**
- Zero external dependencies
- State is lost on restart
- Cannot be shared across replicas
- Suitable for development and single-instance deployments
- JWT-bearer assertion replay tracking is per-process. Each accepted assertion is
  retained through its `exp`; a replay sent to a different process is not
  detected.

**Implementation:** `pkg/authserver/storage/memory.go`

## Redis Backend

The Redis backend stores all OAuth 2.0 state as JSON-serialized values in Redis.
JWT-bearer assertion replay tracking uses the same shared Redis namespace, so
all processes configured with that backend observe a consumed assertion. The
backend hashes a length-prefixed `(purpose, issuer, replay key)` tuple before
using it in a Redis key, preventing ambiguous key encoding and keeping the
assertion identifier out of Redis key names. A successful atomic `SET NX` marker
expires at the assertion's `exp`; a duplicate unexpired marker returns
`fosite.ErrJTIKnown`, and a Redis error is returned so the grant fails closed.

### Connection Architecture

Three connection modes are supported. Client construction (standalone, cluster, or sentinel), connection-mode validation, TLS plumbing, and connectivity verification are delegated to the shared `github.com/stacklok/toolhive-core/redis` package; `RedisStorage` holds a `redis.UniversalClient` so the same call sites work across all three topologies.

- **Standalone**: A single endpoint for managed Redis services. The caller is responsible for endpoint availability (the managed service handles HA internally).
- **Cluster**: A Redis Cluster discovery endpoint (e.g., GCP Memorystore Cluster, AWS ElastiCache with cluster mode enabled). Enabled by setting `clusterMode: true` alongside `addr` in the CRD.
- **Sentinel**: Connects via Sentinel for self-managed high-availability deployments. Sentinel handles master discovery, automatic failover, and configuration updates.

### Multi-Tenancy

Each auth server instance has a unique key prefix derived from its Kubernetes namespace and name:

```
thv:auth:{namespace:name}:
```

The `{namespace:name}` portion is a Redis hash tag. In standalone and Sentinel modes, hash tags have no functional effect but impose no overhead. The format ensures keys remain co-located in the same hash slot if the deployment were ever migrated to Redis Cluster.

**Implementation:** `pkg/authserver/storage/redis_keys.go`

### Key Design

Keys follow the pattern `{prefix}{type}:{id}`:

```
thv:auth:{default:my-server}:access:abc123
thv:auth:{default:my-server}:refresh:def456
thv:auth:{default:my-server}:user:user-uuid
```

Secondary indexes use Redis Sets to enable reverse lookups:

```
thv:auth:{default:my-server}:reqid:access:{request-id}  → {sig1, sig2}
thv:auth:{default:my-server}:user:upstream:{user-id}     → {thv:auth:{default:my-server}:upstream:session1:github, ...}
```

With user-keyed storage, upstream rows are keyed `{prefix}upstream:user:{escaped user ID}:{provider}`
and the per-user index key is `{prefix}upstream:idx:user:{escaped user ID}`.
The set `{prefix}user:upstream:{user ID}` holds the full row keys, one per
provider, such as `{prefix}upstream:user:{escaped user ID}:{provider}`, instead
of one key per session and provider. `DeleteUser` deletes each key in the set,
then the wrapper deletes the per-user index, so an index left without a TTL by a
token that never expires does not outlive the user.

### Exchange grant storage

`ExchangeGrantStorage` keeps one record per refresh token issued through token exchange, found by the token's HMAC signature. Time decisions for records (live, expired, revoked) are made in Go against the store clock; the record's Redis TTL is only garbage collection. The replay marker is different: its window follows Redis's own TTL. The create script checks whether the marker key exists, and Go only skips the marker once its window has already passed. `ExchangeGrantMarkerMargin` absorbs clock skew and the time between reading the clock and running the script. Both effects can only lengthen the window, which is the safe direction.

| Key | Type | Contents | TTL |
|---|---|---|---|
| `{prefix}xgrant:{signature}` | HASH | `data`: immutable JSON of the record without `RevokedAt`. `rev_ms`: revocation time in Unix ms, absent while the grant is live | `ExpiresAt` (plus 1 ms, see below) |
| `{prefix}assertion-jwt:{digest}` | STRING | Replay marker for the subject access token's `(issuer, jti)`, built with the `exchange-grant` purpose. The value is the grant signature. Written only for a top-level grant | `SourceExpiresAt` plus `ExchangeGrantMarkerMargin` (1 minute), plus 1 ms |

Creating a grant is one Lua script. It runs every check before any write: a key of the wrong type is an error, then an existing record returns `ErrAlreadyExists`, then an existing marker returns `fosite.ErrJTIKnown`. Only then does it write the record, set its TTL, and write the marker. Concurrent creates for one `jti` therefore admit exactly one. The script touches only its declared keys, which share the `{ns:name}` hash tag of the store prefix, so it is safe on Redis Cluster.

Revoking is a single `HSETNX` of `rev_ms` (inside a small script that first checks the record exists, since a bare `HSETNX` would create a HASH with no TTL). It is idempotent, keeps the first revocation time, and does not change either TTL. Get and revoke return `ErrNotFound` for a record past `ExpiresAt` even if Redis has not evicted it.

Each TTL has 1 ms added because liveness is inclusive: a grant is usable at exactly `ExpiresAt`, and a marker still blocks replay at exactly `SourceExpiresAt` plus the margin. miniredis deletes a key when its TTL reaches zero, so the extra millisecond keeps the boundary instant inclusive there. On real Redis it only extends a key's life by 1 ms. For a record, Go decides, so the extra time only affects garbage collection. For a marker it lengthens blocking by at most 1 ms, which is the safe direction. TTLs are capped at 100 years so a far-future `exp` cannot overflow.

**Persistence expectation.** Recent writes can be lost when Redis restarts without AOF, or when it fails over to a replica that had not yet received them. A lost replay marker allows a second exchange of the subject access token. A lost revocation makes the grant, and its children, usable again until `ExpiresAt`. A lost record only makes its refresh token unusable. Enable AOF (and prefer a strict fsync policy) if these losses matter.

### Consistency Model

The implementation uses different strategies based on consistency requirements:

- **Lua scripts** for strict atomicity: upstream token storage with user reverse-index cleanup, last-used timestamp updates
- **Pipelines** (`MULTI`/`EXEC`) for batched operations: authorization code invalidation, token session creation with secondary index updates
- **Individual commands** with best-effort cleanup: token revocation, refresh token rotation — partial failures are safe since orphaned keys expire via TTL

### Refresh Coordination Scope

When an expired upstream access token is refreshed, `UpstreamTokenStorage` derives
an opaque SHA-256 identity from its storage-defined row representation. For
Redis, that representation is its row-key format, so two `(sessionID,
providerName)` pairs that happen to produce the same Redis key resolve to the
same identity — that's safe because they're the same physical row. What's
not allowed is aliasing two pairs that are genuinely different rows: the
auth server uses this identity only as the key for its in-process
`singleflight` group, then its leader re-reads the row before redeeming its
refresh token, and it reads/writes under its own sessionID — aliasing
distinct rows would cross credentials between sessions. This prevents
duplicate redemption from stale concurrent callers served by the same
process; the identity is not persisted, logged, or exposed.

This is deliberately narrower than #4122's original in-process-only scope: it
does not by itself provide distributed coordination across replicas. That
coordination now exists as a second, independent layer:
`UpstreamTokenStorage.CompareAndSwapUpstreamTokens` conditions a refresh write
on the refresh token currently stored still matching the value the caller
redeemed with, failing the write (`ErrConcurrentRefresh`) instead of
overwriting when another replica already rotated the row first. `refreshAndStore`
writes through this method rather than an unconditional `StoreUpstreamTokens`.
Redis implements the comparison and the write as one atomic Lua script;
`MemoryStorage` implements it under its existing mutex. `singleflight` remains
the process-local optimization described above — it avoids a redundant
upstream call and Redis round-trip for concurrent requests inside one
process — while the CAS write is what makes the *stored* row deterministic
across processes: whichever replica's write lands first wins, and every
losing replica's write fails instead of silently clobbering it.

This is a storage-ordering guarantee, not a guarantee that concurrent
redemption is safe at the upstream provider. Both replicas still call
`provider.RefreshTokens` before either one's CAS write runs, so for a
provider enforcing strict single-use rotation (RFC 9700 §4.14.2 replay
detection), two concurrent redemptions of the same refresh token can still
be indistinguishable from a replay at the IdP, which may revoke the grant
regardless of which replica's write wins here. CAS is fully sufficient only
where the provider tolerates a short grace/leeway window in which more than
one redeemed child stays valid (e.g. Read.ai's stated behavior) — outside
that window, closing the gap requires serializing the *redemption* itself
(a distributed lock around the read-redeem-write sequence), not just the
write. That lock is a deliberate follow-up, not implemented here: this layer
only prevents storage corruption from a lost write race, and its own log
distinguishes a genuine lost race (an unexpired row on re-read) from a row
that is simply gone (deleted by logout or evicted by TTL, `ErrNotFound` on
re-read) — the latter is expected behavior, not a race, and refuses to
resurrect the deleted row.

### Upstream Refresh Rejections and the Refresh Grant

An upstream refresh fails in one of three ways. In a definitive rejection, the
IdP answers with the OAuth `invalid_grant` code on anything but a 5xx or 429,
and ToolHive treats that upstream refresh token as dead. Some IdPs also use
the code for account or policy state that can clear without a new sign-in; see
the [trade-offs](#remembered-rejections) below. In another structured
verdict, the IdP answers with a different OAuth error code (for example
`invalid_client`) on a 4xx: retrying will not help, but the refresh token is
not what was rejected. Any other failure (a network error, a 5xx, a 429, a 4xx
with no OAuth error code, storage) is transient. The definitive classification
is `oautherr.IsRejectedRefreshGrant`, shared with the client-side token source.
The provider wraps
`upstream.ErrGrantRejected` only for the definitive case. Under the
cross-replica race described above, a rotating IdP also answers
`invalid_grant` to the losing replica. The refresher therefore re-reads the
row after a rejection, and if the stored row holds a different, unexpired
token it returns that row instead. This works only once the winning
replica's write has landed, so a rejection can still occasionally reflect a
lost race rather than a dead credential. The refresher and
`upstreamtoken.ErrRefreshFailed` carry it through, so it can be detected with
`errors.Is` on the error from `GetValidTokens`. It can also be detected on the
per-provider errors that `GetAllUpstreamCredentials` returns in its `failed`
map.

#### Remembered rejections

Each auth server process remembers the refresh tokens its IdP rejected with a
definitive `invalid_grant` for 5 minutes (at most 10,000 entries). While an
entry exists, the refresher answers a refresh of that token without calling
the IdP, except when the [refresh grant guard](#upstream-refresh-guard) is
asking: it skips the memory and asks the IdP (see the guard section for a
refresh already in flight), and still adds a fresh rejection to the memory. Without the memory, a row that holds an expired access token and a
dead refresh token would cost one IdP call on every request that reads it.
After the first rejection, the refresh failures behind the repeated WARN lines
are answered from this memory and do not reach the IdP; a DEBUG line says so.

An entry is keyed on the storage row, a client registration generation, and a
SHA-256 digest of the row's refresh token, access token and access token
expiry. It stops matching when:

- another replica or a new sign-in stores a different refresh token in the row;
- another replica refreshes successfully without rotating the refresh token,
  which stores a new access token, or the same access token with a new expiry,
  next to the same refresh token;
- the upstream's client registration is replaced through the server
  (`UpdateUpstreamClientCredentials`, used by the DCR recheck), because an IdP
  may answer `invalid_grant` for a refresh token issued to another client.
  Replacing a registration directly on a provider does not do this.

The generation is one counter for all providers, so a switch costs the other
providers one IdP call each. Nothing removes entries by hand. The memory never
writes to storage, so a replica that wins a refresh race after this one's
rejection can still store its rotated token. Only definitive rejections are
remembered: a 5xx, a 429, a network error, a storage error, or any other OAuth
error code is not remembered and is retried on every request (the ID-token
path has its own backoff). A rejection is also not remembered when the
provider's error carries no `*oauth2.RetrieveError` with a response, which a
third-party provider implementation may return.

An entry keeps only the OAuth error code and the HTTP status, never the
provider's error text or body. Each hit builds a new error that still matches
`upstream.ErrGrantRejected` and carries a `*oauth2.RetrieveError` with that
code and status, and its message says the rejection was remembered and the
provider was not called, so logs tell it from a fresh rejection.

Trade-offs:

- Some IdPs answer `invalid_grant` for account or policy state, for example a
  disabled account, a lockout, a Conditional Access block or missing consent
  on Microsoft Entra. This can clear without a new sign-in. After an
  administrator fixes it, bearer requests on the same replica can keep failing
  for up to 5 minutes, where before they recovered on the next request. The
  client's next refresh grant asks the IdP again (see
  [Upstream refresh guard](#upstream-refresh-guard)), so a client that answers
  the 401 with a refresh grant recovers as soon as the IdP accepts. Only a
  client that never sends a refresh grant waits out the 5 minutes.
- Losing an entry (eviction, restart, another replica) costs one more IdP
  call, which remembers the rejection again.
- Each bearer request in this state still logs three WARN lines for the failed
  refresh: two from the token service (`upstream token refresh failed` where
  it refreshes, and `upstream token refresh failed; provider will require
  re-authentication` where it collects credentials) and one from the auth
  middleware (`upstream token refresh failed; returning 401`). The error text
  in the first two says the rejection was remembered.
- GitHub answers a dead refresh token with HTTP 200 and `bad_refresh_token`,
  which is not a definitive rejection, so nothing is remembered there.

Every answer from the token endpoint, on refresh and on the authorization code
exchange alike, also keeps its `*oauth2.RetrieveError` in the same chain,
behind an error whose message is the provider's sanitized text. The chained
copy carries no response body and only the response's status code and status,
so it cannot reach the outgoing request or its credentials. Callers classify
it with the `oautherr` predicates: `IsPermanentCredentialError` is true for any
structured verdict, `invalid_grant` or `invalid_client` alike, and false for a
transient failure. "Permanent" means only that retrying will not help;
`invalid_client` indicts the client registration, so a fresh login would fail
the same way. A caller deciding whether to ask the user to reconnect a provider
keys off `IsRejectedRefreshGrant`.

The embedded auth server's own `refresh_token` grant keeps the token session
(`tsid`). A refreshed access token therefore points at the same upstream
credential. If that credential is dead, the bearer validator answers 401
again, and a client that only ever refreshes loops. A built-in upstream
refresh guard breaks the loop by default (see below). Beyond it,
`Config.RefreshGrantHandlers` lets an embedder add its own refresh-grant
checks. The guard and the handlers run in order, only on the `refresh_token`
grant, after fosite's built-in refresh handler has authenticated the client
and loaded the refresh session. They run before any tokens are issued or the
refresh token is rotated. Returning `fosite.ErrInvalidGrant` answers HTTP 400
`invalid_grant`, which pushes the client into a full authorization that
creates a new `tsid`. Another fosite error keeps its own status and code, and
a plain Go error answers HTTP 500. Only the request phase is delegated: the
response phase and client-authentication skip are never called, so the seam
can refuse a grant but not change what is issued. The guard and the
handlers are registered as one fosite handler, because fosite drops a second
handler of the same concrete type.

#### Upstream refresh guard

This section describes the default session-keyed storage. With user-keyed
storage the guard works differently; see
[Upstream refresh guard under user-keyed storage](#upstream-refresh-guard-under-user-keyed-storage).

The guard is registered whenever at least one upstream is configured, ahead
of the embedder's handlers, which therefore see only grants it accepted. It
refuses the grant with `invalid_grant` when the token session's upstream
credential is definitively dead. This is the canonical list; the code and the
other docs link here.

| Cause | Refresh token revoked |
|---|---|
| The session has no readable upstream rows | No |
| Rows exist but none holds an access token | Yes |
| A row's access token is expired and its provider is no longer configured, so nothing can refresh it | No |
| A row's access token is expired and the row has no refresh token | Yes |
| The row was deleted while the guard refreshed it, including by the refresher itself when the IdP rotated the refresh token and the new one could not be saved | No |
| The IdP rejects the row's refresh with `invalid_grant` (`upstream.ErrGrantRejected`) | No (the session's upstream rows are deleted; session-keyed storage) |

A revoking refusal also revokes the grant's access tokens, as an RFC 7009
revocation of the refresh token would. It applies only when the stored rows
prove the credential can never be used again. The other refusals keep the
refresh token, because their cause could be wrong: a rotating IdP also
answers `invalid_grant` to a replica that lost a refresh race, and Redis's
bulk read skips a row it cannot parse (for example one written by another
version during a rolling upgrade) and reports none. An unconfigured provider
is decided by the serving replica's config rather than by the stored rows, and
during a rolling deploy that adds an upstream an old replica does not know it
yet. If such a refusal is wrong, the client's next refresh succeeds; otherwise
it is refused again.

A kept token can be retried, and public clients (`token_endpoint_auth_method`
`none`, the DCR default) need nothing but the token to do so. A retry of the
cheap causes repeats a storage read. For an IdP rejection, a retry would
repeat an upstream refresh call and the 500ms recheck, so once the rejection
is confirmed the guard deletes all of the session's upstream rows. The client
is sent to a new `tsid`, so nothing reads them again, and a retry is refused
as having no rows, on any replica. Every row goes, not only the rejected
provider's: with the siblings kept, a retry would see only usable rows and be
granted a token that never gets a credential for the rejected provider. The
rows are not the refresh token, so a lost race still costs one
re-authorization rather than the grant, though the refusal can no longer
correct itself on a retry.

A refusal is logged at WARN when it revokes or follows a confirmed IdP
rejection, which the row deletion limits to once per session. The other
refusals repeat on every retry of a kept token (for example after logout), so
they are logged at DEBUG.

The guard refreshes an expired row through the shared refresher before
deciding, one provider at a time. Upstream refresh is otherwise lazy, so an
idle client's stored token is often expired while its upstream refresh token
still works, and refusing on stored state alone would force every idle client
to sign in again. Any other outcome is uncertain and grants the refresh, since
the bearer validator answers those with 503 or a retryable 401 anyway:

- a transient upstream failure (network, 5xx, 429);
- a storage error, unless the refresher removed the connection because it could
  not save a rotated refresh token (it deletes the row, or with user-keyed
  storage clears its refresh token; either way the error wraps
  `storage.ErrNotFound`, the refusal above);
- the request being cancelled; or
- the check running past its 10-second budget. A refresh still in flight then
  completes in the background and stores its result.

To tolerate the lost-race window described above, the guard does not refuse
on the first rejection. It waits briefly (500ms) and re-reads the row. If the
row has changed (a different refresh token, access token, ID token or access
token expiry), it counts as live and the grant goes through. This is a
heuristic: a winner whose write lands after the wait is missed. The client is
refused and its rows deleted, so the winner's write fails, but it keeps its
refresh token. A recheck that cannot complete (the request is cancelled, or
the read fails) is uncertain too. A session without a `tsid` (a session-less
grant) is not checked.

The guard refreshes through the shared refresher but skips its
[remembered rejections](#remembered-rejections), so it asks the IdP (see
below for a refresh already in flight).
If the IdP used `invalid_grant` for an account or policy state that an
administrator has since fixed, the client's next refresh grant picks the fix
up. The refresh succeeds and stores a new row, the remembered rejection stops
matching it, and bearer requests work again. A fresh rejection that the guard
receives is remembered like any other, so bearer requests after it do not
call the IdP. A guard call that joins a refresh already in flight for the
same row gets that call's result, which may be a remembered rejection, because
the call it joined was started by a request that does not skip the memory. The
guard then refreshes once more, which reaches the IdP unless another such call
is in flight again. If that answer is also remembered, the guard cannot tell
whether the credential is dead and grants. The guard refuses and deletes rows
only after a rejection the IdP just gave, never after a remembered one.
Embedder handlers in
`Config.RefreshGrantHandlers` that call the shared refresher do not skip the
memory.

The recheck covers the remaining race. After the IdP rejects the refresh, the
guard re-reads the row and refuses and drops the session's rows only if the
row is unchanged. If another replica refreshed successfully while the guard
waited, without rotating the refresh token (for example once an administrator
cleared an IdP-side block), the row keeps its refresh token but holds a new
access token or expiry. In ToolHive's own storage, every write to a
session's row under the same refresh token is a successful IdP refresh, so the
IdP accepted that refresh token, and the guard grants and keeps the row. A
storage layer that shares rows across sessions can also show a fresh access
token from a new sign-in; the guard grants then too, because the row holds a
usable access token, without that meaning the IdP accepted the refresh token.

**Upgrade impact.** The guard is on by default. After an upgrade, a session
whose upstream credential is already dead is refused on its first refresh
instead of looping. A refresh token's useful life is now also bounded by the
upstream credential's liveness, even in a deployment whose resource server
never reads upstream tokens: once the upstream refresh token stops working,
the client signs in again.

**Opt-out.** `Config.DisableUpstreamRefreshGuard` turns the guard off. It is
available only to Go embedders of `pkg/authserver`; the RunConfig, CLI and
operator CRDs do not expose it, so `thv`, vMCP and the operator always run the
guard. Exposing the switch is tracked in
[#6931](https://github.com/stacklok/toolhive/issues/6931).

### Serialization

All values are stored as JSON. The implementation uses defensive copies on read and write to prevent caller mutations from affecting stored data.

### TTL Management

Redis TTL is used for all time-bounded data. TTL values are derived from OAuth 2.0 token lifetimes:

| Data Type | Default TTL |
|---|---|
| Access tokens | 1 hour |
| Refresh tokens | 30 days |
| Authorization codes | 10 minutes |
| Pending interactive authorizations (`pending:{handle or state}`) | Remaining time until the initial `CreatedAt` + 10 minutes; deadline checked before consent/callback consumption |
| Remembered browser sessions (`consent:session:{SHA-256 cookie digest}`) | Up to 7 days, remaining fixed lifetime |
| Client approvals (`consent:approval:{SHA-256 JSON pair}`) | Up to 30 days, remaining fixed lifetime |
| PKCE requests | 10 minutes |
| Invalidated codes | 30 minutes |
| DCR-issued clients (public, confidential, and private_key_jwt), never used | 24 hours |
| DCR-issued clients, after their first successful token exchange | 30 days, renewed on each exchange |
| JWT-bearer replay markers | Assertion `exp` |
| `private_key_jwt` client assertion replay markers | Deadline set by the server: assertion `exp` plus 65 seconds |
| Users / Providers | No expiry |

The in-memory backend applies the same client expiry: each DCR-issued entry
carries an expiry, reads treat an expired entry as absent, and the cleanup
loop removes it. A pre-provisioned client (no DCR marker) never expires. Both
backends renew on the same proven-use signal (a successful token
exchange/refresh), which also raises a new client from 24 hours to 30 days.

#### DCR client cap

`/oauth/register` is unauthenticated and every successful call persists a
client, so both backends cap the number of live clients it created. The cap
defaults to 10,000 (`DefaultMaxDCRClients`) and is set with
`storage.max_dcr_clients` in the auth server RunConfig, which the operator
fills from the embedded auth server's `storage.maxDCRClients`; `0` means
unlimited.
It counts only clients carrying the DCR-issued marker. Operator-configured
clients and CIMD clients are never counted and never rejected.

At the cap, `RegisterClient` returns `ErrClientCapacity` and writes nothing.
No existing client is evicted: eviction let a caller registering at the full
rate push out every idle real client, logging its user out. The handler
answers HTTP 503 with an RFC 7591 `server_error` and a `Retry-After` header
giving the seconds until the oldest counted client expires, the earliest
moment a slot can free up.

**Sizing.** A client never used for a token exchange lives 24 hours, so a
caller registering at the full rate keeps up to `rate × replicas × 86,400`
unused clients alive. At the default rate of 1/s that is about 86,400 per
replica, well above the default cap: one replica's worth of traffic fills it
in about 2.8 hours, and new registrations then get 503 for up to 24 hours
after the traffic stops, while the spam entries expire. A client promoted by a
token exchange holds its slot for 30 days, which no cap value mitigates. Size
the cap above the real client count plus the headroom you want during an
attack, and lower `registration_rate_limit` (the CRD's
`registrationRateLimit`) to make the cap take longer to fill.

On Redis the count lives in a sorted set, `{prefix}client-idx`, holding each
counted client ID scored by its expiry in Unix milliseconds. It shares the
client rows' hash slot, so one Lua script registers atomically on Redis
Cluster: report an existing row as a conflict, trim entries whose expiry has
passed, reject if the remaining count is at the cap, otherwise write the row
with its TTL and add it to the set. `RenewClientTTL` sets the row's TTL and
its score in one script, and `ReconcileConfiguredClient` drops a stale entry
in the same transaction as the row. `UpsertDCRIssuedClient` refuses counted
clients, so it cannot bypass the cap. Scores come from the replica's clock,
passed to the scripts as an argument: Redis `TIME` is in the `@fast` ACL
category, which the documented auth-server ACL does not grant. Clock skew
between replicas, milliseconds under NTP, only shifts when an entry is
trimmed relative to its row's expiry. Because the count is in Redis, every
replica enforces one cap, unlike the per-process rate limiter.

Rows written before the index existed have no entry. On startup,
`BackfillDCRClientIndex` scans the client rows and adds each counted row with
its remaining TTL (`ZADD NX`, so an existing entry keeps its score) before
the server is constructed. On Redis Cluster it scans the master that owns the
prefix's hash slot. Reads are batched per SCAN page, and a row it cannot parse
is skipped and counted in one WARN line instead of failing startup. It logs
one INFO line with the rows scanned, the entries added and the duration, and
a WARN naming `storage.max_dcr_clients` when the live count is already at or
above the cap. A Redis error during the backfill is logged at WARN and
startup continues, unlike a failed legacy upstream migration: skipping the
backfill only leaves the cap undercounting older rows, which is how the
server behaved before the cap, and the next start fills in what was missed.
It runs on every startup. During a rolling upgrade, replicas still on the old version
keep writing and renewing rows without index entries; rows written after the
last upgraded replica started stay uncounted until the next restart reruns
the backfill, or until they expire.

**Upgrading.**

- A Redis deployment already holding more than the cap in live DCR clients
  rejects every new registration right after the upgrade, until enough of
  them expire; the backfill logs a WARN at startup when this happens. Raise
  `max_dcr_clients` or set it to `0` before upgrading. `0` removes the cap
  entirely, so prefer a large finite value (two to three times the observed
  live client count) and lower it again once the population drains.
- On Kubernetes, `storage.maxDCRClients` exists only in the CRDs of the
  release that enforces the cap, and the CRDs ship in the separate
  `operator-crds` chart. Upgrade in this order: (1) upgrade the
  `operator-crds` chart; (2) set `storage.maxDCRClients` on each Redis-backed
  `MCPExternalAuthConfig` and `VirtualMCPServer` `authServerConfig`;
  (3) upgrade the `operator` chart. With the older CRDs the API server drops
  the field as unknown, and an older operator ignores it, so setting it
  earlier has no effect, and upgrading the operator first enforces the
  default cap before you can raise it.
- Backfilled rows keep their current TTL, up to 30 days, so a pre-existing
  spam population can take that long to drain. Only clients registered after
  the upgrade get the 24-hour unused TTL.
- During a rolling upgrade, an old replica's `RenewClientTTL` on a row a new
  replica indexed sets the row's TTL to 30 days but leaves its index score at
  24 hours, so that row goes uncounted after 24 hours. The backfill's
  `ZADD NX` does not correct it; the row is counted again on its next renewal
  by an upgraded replica.

CIMD clients are not counted against the DCR cap. The in-memory backend caps
its CIMD rows separately at 10,000 (the old whole-map cap used to cover them):
past it, `UpsertDCRIssuedClient` refuses a new row with `ErrClientCapacity`,
while replacing an existing row still succeeds. The CIMD decorator's
write-through is best-effort and logs the refusal at Debug; the in-memory
backend keeps sessions as Go values rather than rehydrating their client from
the row, so a refused row costs at most a document refetch. On Redis,
persisted CIMD rows are bounded only by the per-process `/oauth/authorize`
CIMD rate limit (1/s) and their 30-day TTL.

Two metrics expose the cap: the gauge `toolhive_authserver_dcr_clients`
(unexpired counted clients, read from storage at collection time) and the
counter `toolhive_authserver_dcr_capacity_rejections` (registrations rejected
at the cap). Rejections are also logged at WARN, at most once every 15
minutes, with the cap, the live count, the `Retry-After` value and how many
rejections were held back since the previous line, so a deployment without
telemetry still sees the cap. The volume is bounded by time, not by the
unauthenticated caller's request rate. Neither the cap filling nor clearing
is logged, since a caller holding the cap flips between the two each time a
slot expires.

**Behaviour change for public clients registered after this feature:** a
public client registered before this change was stored as a bare
`*fosite.DefaultClient` on the Redis backend, which does not carry a
`token_endpoint_auth_method`, so fosite never enforced one at the token
endpoint. A public client registered after this change persists
`token_endpoint_auth_method: "none"` and is rebuilt as a
`*fosite.DefaultOpenIDConnectClient`, so fosite's method pinning now applies.
The practical effect: a public client that presents a **non-empty**
`client_secret` alongside its `client_id` at `/oauth/token` is now rejected
with `invalid_client`, where it previously succeeded. An empty or absent
secret is unaffected — fosite only compares secrets when one is actually
presented. Clients registered before the upgrade keep the old, tolerant
behavior (their stored row has no auth method and reads back as a bare
`*fosite.DefaultClient`). This brings the Redis backend in line with the
in-memory backend, which has always enforced the pinned method.

### Configured clients across restarts

Operator-declared clients are written at startup by `ReconcileConfiguredClient`. The rule depends on what already sits at the client ID:

- A DCR-issued client is never overwritten. Startup fails.
- A SPIFFE placeholder and a non-placeholder client never replace each other, in either direction. Startup fails.
- Two SPIFFE placeholders replace each other whatever differs (scopes, resources, or the SPIFFE identity, which includes grant types), so an edited association takes effect on rollout. The placeholder row is inert, so this authorizes nothing differently. A SPIFFE placeholder stores no audiences, but every placeholder stored by v0.65.0 carries them. So on Redis, the first start of the new version logs the WARN below once for each association, even if `audiences` was already removed from the configuration. Each replica that reads the old row before it is rewritten logs it, and it can repeat if a v0.65.0 replica restarts during the rollout and writes the old row back. Later restarts do not log it. During the rollout, old and new replicas each serve their own configuration, and nothing marks when it ends. The server logs the same WARN as for delegates, including old and new resources and whether the identity changed. A grant-type change appears only as `identity_changed=true`, because the placeholder stores no grant types. See [SPIFFE Association Declarations](18-spiffe-association-declarations.md).
- Two non-placeholder clients, such as delegate clients, are replaced. A changed scope, audience, grant type, redirect URI or token endpoint auth method takes effect on restart, and so does a rotated secret or JWKS key. The one refusal is a flip between public and confidential, which fails startup. When scopes, audiences, grant types, redirect URIs or the token endpoint auth method change, the server logs a WARN, `configured client replaced with a different policy`, with the `client_id` and the old and new values of each. This covers replicas that roll out with different redirect URIs and overwrite each other's shared row. Secret and JWKS rotation and response type changes do not log it. An unchanged restart does not log this warning (the per-delegate startup WARN from `warnBlanketSelfIssuedExchange` still fires on every start for delegates whose `token_exchange.self_issued_subject_tokens` is `any` or that have no `token_exchange` block).

Before this rule, a changed delegate scope or audience on Redis failed startup with "already registered as a different configured client", and the new pod never became ready. The memory backend never hit this, because it starts empty.

**Rolling updates.** Delegate rows are shared through Redis, and every token request reads the row. During a rollout with two different configs:

- A replica that restarts with the older config writes its old policy back. That policy stays until any replica next restarts.
- One request can combine the delegate status of one replica with the policy of another. Delegate status comes from each replica's own config. The policy comes from the shared row.
- Secret rotation already behaves this way.
- The ID-JAG grant gate (a client that sets `grant_types` must list the JWT-bearer grant) also depends on each replica's own config, because the set of clients it applies to comes from that config while the client's grants come from the shared row.
- SPIFFE clients are not affected. Each replica serves them from its own config, and the placeholder row carries no policy that a request uses.
- A replica can grant scopes and audiences from the shared row that its own config would reject. The request-time checks in `pkg/authserver/server/tokenexchange/handler.go` for requested scopes (`grantScopes`) and for audiences requested through the `audience` parameter (`grantAudiences`) use only the stored client's scopes and audience. A `resource` parameter (`grantResourceAudience`) and the default audience (`grantDefaultAudience`) are also checked against the replica's own `allowedAudiences` on every request. So narrowing a delegate is not a reliable revocation until every replica runs the new config and no old-config pod restarts. If an old-config pod restarts after the rollout, the wider policy stays in the row until a new-config pod restarts. The subject token still bounds what is granted.

**Rollback and mixed versions.** Once new code has replaced a delegate row with a different policy, an older binary (v0.47.0 up to this change) that starts against that row fails startup with "already registered as a different configured client". Reverting the config alone does not fix this, because the stored row holds the new policy and the older binary's strict check compares it with the reverted config. To roll back across a delegate policy edit, use one of these:

1. Roll out the previous policy on the new binary so the row goes back, then roll back the binary.
2. Delete the stored row immediately before rolling back to the older binary with the older config.

A binary-only rollback works when the older binary's config already matches the stored row. During an upgrade, an older-binary pod that restarts (crash, eviction) after new pods replaced the row with a different policy fails startup and crash-loops until the rollout finishes. Newer binaries add a `configured` field to delegate rows. Older binaries ignore it, so they can still read the rows. A row an older binary rewrites loses the field and is then recognized by the legacy delegate shape (see "Client admission" below).

**Implementation:** `configuredClientConflict` in `pkg/authserver/storage/types.go`, called by both backends.

**Client admission.** A configured client removed from config must stop working even though its row is still in the backend. `ClientAdmissionStorage` (`pkg/authserver/storage/client_admission.go`) wraps the caller's store and overrides only `GetClient`. It classifies the stored row by what the row itself shows, in this order:

| Class | How the row is recognized | Served when |
|---|---|---|
| Placeholder | the inert SPIFFE reservation row | never from storage (the SPIFFE overlay serves SPIFFE clients from config) |
| Configured | the stamp that `ReconcileConfiguredClient` writes, or the legacy delegate shape (below) | its ID is a configured delegate client in this server's config |
| Registered | the registration marker (DCR and CIMD) | always |
| Unmarked | anything else, such as a client an embedder writes with `RegisterClient` | always |

The Configured and Registered classes never overlap.

The stamp is a `configured` field on the Redis row and a side map in the memory backend. Only `ReconcileConfiguredClient` sets it. It is not part of the client fingerprint, so it never causes a "different policy" conflict.

**Legacy delegate shape.** Every delegate row written by any release since v0.43.0 has one shape: a bare `*fosite.DefaultClient` that is confidential, has grant types of exactly `urn:ietf:params:oauth:grant-type:token-exchange`, and has no token endpoint auth method. A row with that shape and no stamp is classed as Configured. This covers delegates removed before the upgrade, and rows an older binary rewrote without the stamp. Public clients, clients with other grant types, and DCR or CIMD rows do not match. The rule does not look at who wrote the row. An embedder row that is confidential, has grant types of exactly token-exchange and has no token endpoint auth method matches it too, and is hidden unless its ID is a configured delegate. On Redis this also covers such a client registered as an OIDC client with an empty auth method, because it reads back as a plain client. The memory backend serves that OIDC case.

A row that is Configured but not declared, or a Placeholder, is reported as not found, the same error an unknown ID gets. The server logs one WARN per client ID for the life of the server instance. The wrapper sits directly on the caller's store, below the CIMD decorator and the SPIFFE overlay. Admission never deletes or rewrites a row.

What this means for a delegate you remove from `delegate_clients`, after the next restart, on both backends:

- It is not found. It cannot authenticate at the token endpoint, so it cannot exchange tokens.
- Access JWTs already issued to it stay valid until they expire. Admission does not revoke them.
- A self-issued subject token issued to it can still be exchanged by other delegates until it expires. This is an accepted exposure.
- On Redis, if you add the same ID back, its stored refresh tokens work again until the configured refresh-token lifespan ends (7 days by default, at most 30). This applies to configured login clients. Rotate the credential when you re-add a client.
- To remove the row as well, delete it manually: `DEL thv:auth:{ns:name}:client:<id>`.

A pending request for a removed client is refused too. Consent and the upstream callback (including an upstream error response) answer a plain-text HTTP 400 containing `invalid_client`, send nothing upstream, and write no upstream tokens or user rows. On the device verification page, submitting the user code (`POST /oauth/device`), completing the upstream login (`/oauth/callback`) and confirming (`POST /oauth/device/confirm`) each check that the device request's client still resolves. When it no longer resolves, the page answers HTTP 400 before any upstream redirect or code exchange, and the device request is never authorized. Denying still closes the request, because deny runs before the check. Any client lookup that ends in not-found gets this answer, including a CIMD document fetch that fails transiently with no servable cache entry; storage and transport failures answer HTTP 500.

**Rolling updates.** Admission is per replica, so a rollout is not atomic in either direction:

- A removed delegate is fully revoked only after every replica restarts on the new binary and config. A replica still on the old config, or on a binary without admission, keeps serving it.
- A newly added delegate gets `invalid_client` on a replica that runs client admission but still has the old config, because that replica does not declare it. It works once that replica restarts. A replica on a binary without admission serves it.

**What admission does not cover.** The Redis session lookup that rebuilds the client for a stored authorization-code, PKCE, refresh-token or access-token session (`unmarshalRequester`) reads the backend directly, so it is identity-only. That is safe because fosite compares the session's client ID with the client that authenticated through the admitted store. Do not treat a rehydrated client as an authentication result. Device requests store only a client ID string. Code that reads the backend directly also bypasses admission, as does a binary that lacks it.

**Embedders.** Clients an embedder writes with `RegisterClient` are Unmarked and are always served, except a row that matches the legacy delegate shape above. No declaration is needed and nothing changes for the usual embedder clients (public clients, or clients with other grant types). A later change (#6937) will add `ProvisionedClients` to the config so embedders can declare them. A consent-exempt ID does not vouch for a client. A delegate client is consent-exempt only if it is a configured login client; see [Administrator consent preauthorization](#administrator-consent-preauthorization).

With the memory backend, a restarted server starts empty and registers only the configured delegates (`pkg/authserver/server_impl.go`). See [Token Exchange and Delegation](17-token-exchange-delegation.md).

**Implementation:** `ClientAdmissionStorage` in `pkg/authserver/storage/client_admission.go`, installed by `newClientAdmissionStorage` in `pkg/authserver/server_impl.go`.

## User-keyed upstream storage

By default the auth server stores upstream tokens per login session (`tsid`).
A user who signs in from a second client gets a second set of upstream tokens
and has to connect every provider again. User-keyed storage keys the rows by
user and provider instead. All of a user's clients then share one connection
per provider, and a second client does not repeat the upstream sign-in for a
provider that is already connected.

### Turning it on

It is an option for Go embedders only:

- `authserver.Config.UserKeyedUpstreamStorage`, or
- `runner.WithUserKeyedUpstreamStorage()` on `NewEmbeddedAuthServerWithStorage`.
  `NewEmbeddedAuthServer` builds its own storage and refuses the option.

It is not part of RunConfig. `thv`, vMCP's runner-built storage and operator
deployments are unchanged and keep session-keyed rows. The server wraps the
storage it is given as the outermost layer, so every consumer sees user-keyed
rows. `Unwrap` and `ConsumeAssertionJWT` pass through to the layers below.

`GetLatestUpstreamTokensForUser` passes through unchanged. It is keyed by its
`userID` argument, and the wrapper adds no check against the context user; the
backends compare each row's `UserID` with the argument. It can still return
session-keyed rows written before the option was on.

Startup refuses three combinations:

- `UpstreamFilter` together with user-keyed storage. The read side does not
  apply the filter per user yet.
- Storage that already contains the in-tree user-keyed layer, whether the
  option is on or off. The check runs on the storage passed at construction.
  It follows `Unwrap() storage.Storage` and
  `Unwrap() storage.UpstreamTokenStorage`. It cannot see another decorator that
  keys rows by user. An `InProcessService` uses the same walk to report
  user-keyed storage, so a decorator the embedder puts over `IDPTokenStorage()`
  should expose one of them; otherwise the validator's startup check is
  skipped.
- `NewEmbeddedAuthServer` with the option, because it builds its own storage.

Two warnings:

- Do not enable it over another decorator that keys rows by user. Rows would be
  keyed twice and deletes would find nothing.
- Read upstream tokens through the server's `IDPTokenStorage()`. A token
  service built over the raw storage handle skips the wrapper and reads
  session-keyed rows.

Revoking a token does not remove the shared row. The refresher never deletes
the user's row. When it cannot store a rotated refresh token it clears the dead
refresh token from the row with a compare-and-swap against the token it
redeemed, and keeps the row. If a sign-in stored a different token in the
meantime the swap fails and that row is left alone. A small window remains: a
sign-in that carried the same dead token forward before the swap loses it, and
its fresh access token is replaced by the expired one, so the user signs in once
more. With the option off the refresher deletes the row, as before. When the
delete or the clear succeeds, its error wraps `storage.ErrNotFound`, so the
per-user reader and the guard treat the connection as needing authorization; a
lost swap or a storage error returns a plain error. The cleared row also gets an
expired access token, so the connection needs authorization at once, whether the
refresh was for an expired access token or only for an expired ID token. Without
that, a row whose upstream gave no `expires_in` has no expiry and no storage
lifetime, and would be served for ever.

### Rollout and rollback

Switch every replica at the same time. Do not run a mixed fleet, because the
two modes read different rows:

- A session that signed in on an option-off replica has session-keyed rows
  only. If it refreshes on an option-on replica and the user has no user-keyed
  row yet, the guard finds no row for the user, and the grant fails with
  `invalid_grant` and its refresh token is revoked. A retry on an option-off
  replica fails too, because the token is revoked. If the user already signed
  in on an option-on replica, the guard and the bearer validator read that
  user's row instead, and the session continues on option-on replicas.
- A session that signed in on an option-on replica has a user-keyed row only. If
  it refreshes on an option-off replica, the guard finds no rows under the
  session and the grant fails with `invalid_grant`; that replica does not
  revoke the token. Bearer requests that reach an option-off replica get 401,
  because the validator finds no upstream access token for the session.

Turning it on: expect one forced sign-in per user. Rows written before the
option was on are ignored. A client that refreshes before its user has signed
in again on any client is refused and its refresh token revoked, not only
refused, so that client signs in again. Clients that refresh after the user's
new sign-in find the user's new row and continue without signing in.

Turning it off (rollback): the user-keyed rows and their indexes stay in Redis.
On the Redis backend they are the keys `{prefix}upstream:user:*` and
`{prefix}upstream:idx:user:*`, and a row for a token that never expires has no
TTL. The per-user lookup set `{prefix}user:upstream:{uid}` also still lists
them. With the option off, a sign-in for which the identity provider returns no
refresh token copies one forward from the user's latest row for that provider
(`GetLatestUpstreamTokensForUser` reads that set and prefers rows that never
expire), so it can copy a dead or stale token from a leftover row into the new
session row. Delete both key patterns before or right after the rollback
(for example `SCAN` with each pattern and `UNLINK` the keys). The leftover
members of `{prefix}user:upstream:{uid}` are then skipped as dangling. The
memory backend needs nothing, because it is cleared on restart.

### Behaviour

- **Key.** The row is found by user and provider. The `tsid` argument is
  ignored. The user is read only from the auth server's canonical-user context
  key (`identity.CanonicalUserFromContext`), never from the authenticated
  `Identity`. Sign-in sets it to the login subject, the bearer validator to the
  token's `sub`, the refresh guard to the session subject, the refresher to the
  row's `UserID` when the context has none, and `GetValidTokensForUser` to its
  argument. With no user, or an empty one, a read or write fails with
  `ErrNoCanonicalUser`. A direct caller of `Server.UpstreamTokenRefresher()`
  that puts no user in the context gets the row's own `UserID`, so such a
  caller must only pass rows it has already checked belong to the user in
  question. The user ID is escaped in the Redis key, so no user ID can address
  another user's row.
- **Owner check.** Every read compares the row's `UserID` with the context user
  before it reports anything, including expiry. A row owned by someone else, or
  by no one, returns `ErrInvalidBinding` and is never handed back, even when it
  is expired (an expired row would otherwise come back with its refresh token).
  When a bulk read meets such a row it skips it, so that provider counts as not
  connected, and logs one ERROR with the row's user and the provider. It does
  not log the expected user. Walking the sign-in chain again then repeats that leg.
- **Writes.** A write is refused when the tokens are nil, carry no `UserID`, or
  name a different user than the context. The row is keyed by `tokens.UserID`.
  `DeleteUpstreamTokens` (delete by session) always returns an error, because it
  cannot be answered by user and silently succeeding would let a logout look
  complete. `DeleteUpstreamTokensForProvider` removes the context user's row.
- **Row ID.** Two sessions of one user resolve to the same row ID, so they
  share one refresh within a process. Across replicas only the
  compare-and-swap applies.
- **Lifetime.** A row lives until its access token's `ExpiresAt` plus 30 days.
  A token that never expires gets no storage lifetime. The exception is a user
  with a synthesized subject (an OAuth2 upstream with no identity lookup): that
  subject changes on every login, so its rows keep the session lifetime bound.
- **Reader.** `InProcessService.GetValidTokensForUser(ctx, userID, provider)`
  returns a valid credential for one user and provider, refreshing an expired
  row. It checks the owner itself, so it is safe over storage that is not
  user-keyed. A user with no usable connection (no row, a placeholder row, an
  expired row that cannot be refreshed, or a refresh rejected with
  `invalid_grant`) gets `ErrNeedsAuthorization`, with the cause still
  reachable through `errors.Is`. A foreign row gets `ErrInvalidBinding`. A
  storage failure or a temporary refresh failure is returned as it came.

The refresh grant guard and the sign-in carry-forward also change under this
option; see
[Upstream refresh guard under user-keyed storage](#upstream-refresh-guard-under-user-keyed-storage).

**Implementation:** `pkg/authserver/storage/user_keyed_upstream.go`,
`pkg/auth/upstreamtoken/service.go`, `pkg/auth/token.go`

### Validator rule for embedded-issuer tokens

This rule applies to every token validator bound to the embedded issuer,
whether or not user-keyed storage is on. When it loads upstream tokens, the
load context always carries an explicit canonical user: the token's `sub` when
the token's admission route is `interactive`, and an empty user for any other
token. The admission route is described under "Admission route" in
[Token Exchange and Delegation](17-token-exchange-delegation.md). A token is
interactive only when the validator is bound to the embedded issuer, has an
audience configured and verified the JWT with the embedded server's in-process
keys, and the token has a `tsid` and neither a delegated-access marker nor
proof of a session-less mint. Four effects:

- On that load path, `auth.CanonicalUserFromContext` no longer falls back to
  `Identity.PlatformUserID`.
- A host storage decorator that keys by user sees an empty user for any token
  that is not interactive. It should return `storage.ErrNoCanonicalUser` for
  the empty user; the validator then loads nothing and does not fail the
  request. An empty but non-nil result is not the same: for a token with a
  `tsid` the validator answers 401, because the session then holds no upstream
  access token. Any other error from the load fails the request with 503. With user-keyed storage, such a token loads nothing and
  the request is not failed.
- A validator bound to the embedded issuer that has no audience, or no
  in-process key provider, gives every token the empty route. With user-keyed
  storage an interactive token then loads no upstream tokens, so embedders
  that turn the option on must configure all three requirements: the
  validator is bound to the embedded issuer (`WithEmbeddedAuthServerIssuer`
  equal to its issuer), it verifies with the in-process key provider
  (`WithKeyProvider`), and it has an audience. With session-keyed storage the
  load is unchanged.
- `auth.NewTokenValidator` refuses to start without those three requirements
  when its upstream token reader reports user-keyed storage
  (`upstreamtoken.UserKeyedReader`). An `InProcessService` built over
  `IDPTokenStorage()` reports it, and a reader that wraps another must forward
  the wrapped reader's answer. The request-time behaviour described here and
  below applies only behind a reader that does not report it. A validator for
  another issuer must not be given the embedded server's upstream token
  reader: it is refused at startup for lacking the three requirements.

A validator not bound to the embedded issuer does not touch the canonical
user. With session-keyed storage it loads as before. With user-keyed storage
the load fails with `storage.ErrNoCanonicalUser`, because the user-keyed layer
reads only the user the embedded server put in the context, and the request
gets a 503. This is a configuration error, so it is reported instead of
serving the request without upstream tokens.

### Upstream refresh guard under user-keyed storage

With [user-keyed upstream storage](#user-keyed-upstream-storage), all of a
user's clients share one row per provider. The guard changes to fit:

- It checks only the identity provider's row (the first configured upstream),
  looked up by the session's user. Other upstreams are connectors a user can
  add or lose without ending the login, so they no longer decide a refresh.
- It refuses when that row is missing (or was deleted during the refresh), has
  a placeholder or empty access token, belongs to another user, is expired with
  no refresh token, or its refresh was rejected with `invalid_grant` and the
  rejection is confirmed by the recheck described under
  [Upstream refresh guard](#upstream-refresh-guard). It also refuses when the
  session has no user, or when the identity provider is not configured on this
  server. This recheck compares against the refresh token the guard read before
  the refresh. If that read failed, no rejection can be confirmed and the grant
  goes through. The
  snapshot is not the token the refresh redeemed: if another sign-in installs a
  new refresh token in between and the identity provider rejects that token,
  the recheck takes it for a successor and grants once, and the next refresh
  is refused and revoked. When the identity provider's row belongs to
  the user and holds an access token that has not expired, the guard grants
  without asking the identity provider, since the row needs no refresh. If that
  row's refresh token is dead, the next refresh grant after the access token
  expires is refused and the token revoked.
- A refusal revokes the presented refresh token (and its access tokens) when
  the row is missing, was deleted during the refresh, has a placeholder or
  empty access token, is expired with no refresh token, or its refresh was
  rejected with a confirmed `invalid_grant`. A client that follows the OAuth
  rules discards a refresh token that was refused with `invalid_grant`. If the
  refused token were not revoked, no legitimate party would hold it, yet it
  would work again as soon as the user signs in on any client and the shared
  row becomes live. Rotation's reuse detection could no longer catch a stolen
  copy. Revoking keeps a refused token refused.
- A refusal keeps the token when the row belongs to another user, when the
  session has no user, and when the identity provider is not configured on
  this server. These are data or configuration faults that a new sign-in does
  not fix, and the token should work again once the fault is fixed.
- Migration trade-off: right after the option is turned on no user has a row
  yet. A client that refreshes before its user signs in again is refused and
  its token is revoked. That user has to sign in again anyway, because the
  missing row requires it.
- It deletes no row. The row belongs to every client of the user, so one
  client's refresh must not remove another client's connection. After a
  confirmed rejection it does clear the rejected refresh token from the row,
  with a compare-and-swap that expects that token: a refresh token a later
  sign-in stored is left alone, and a failure to clear is logged at DEBUG and
  does not change the refusal. Clearing keeps a later sign-in from carrying the
  dead token forward. The other clients of the user then find a row without a
  refresh token, so once its access token has expired they are refused and
  revoked.
- It skips the [remembered rejections](#remembered-rejections) and asks the
  identity provider, as the session-keyed guard does. It refreshes once more
  when it joined a refresh that answered from the memory, and grants at DEBUG
  if that answer is remembered too. It refuses and revokes only after a
  rejection the identity provider just gave, so a client whose block an
  administrator has since cleared is not refused on a stale answer.
- A failure it cannot judge (a network error, a 5xx, a storage error, or the
  check running past its time budget) grants the refresh and logs one WARN per refresh grant. The session-keyed
  guard logs that case at DEBUG; the user-keyed guard keeps the WARN on purpose,
  because it is the only signal that a credential could not be checked. A
  recheck that cannot finish, or a cancelled request, grants quietly at DEBUG.

Three cases are accepted: the refresh grant succeeds, but the bearer validator
still answers 401, so the client refreshes again in a loop.

- A dead connector (not the identity provider): the guard does not look at it,
  and the validator answers 401 for any provider that fails to load. This lasts
  until "Re-authorization walks the full upstream chain instead of evaluating
  existing credentials" (#6831) is fixed.
- A removed or renamed upstream: its rows stay in storage. Once a row's access
  token has expired the refresher cannot refresh it, because the server no
  longer has that provider, so the validator answers 401 for it. The guard does
  not look at it and grants. Delete the rows with
  `DeleteUpstreamTokensForProvider`. If the renamed upstream is the identity
  provider, the first refresh is refused and revoked instead; after the user
  signs in again, the old row makes clients loop as above.
- A Redis row that cannot be parsed: the guard grants with a WARN on every
  refresh, where the session-keyed guard skips such a row and refuses. The loop
  ends when the row's storage lifetime ends. A row whose tokens never expire
  has none, so the user must connect the provider again, which replaces the row,
  or the row must be removed.

On sign-in, when the IdP returns no refresh token, the callback keeps the
refresh token already stored for the user and provider, as it does without
user keying. With user keying the row is shared, so every write that could
replace a refresh token is a compare-and-swap on the token the callback read:

- A prior row of the same upstream account: the login's tokens are written with
  the prior refresh token, swapping on that token. If a refresh rotated the row
  in the meantime, the swap loses and the rotated row is kept; the older token
  is never put back.
- A prior row of a different upstream account: the login replaces it, swapping
  on the prior row's refresh token.
- No prior row, a prior row without a refresh token, or a prior row that cannot
  be read: the login writes its tokens only over a row that has no refresh
  token, so a refresh token a concurrent client stored in the meantime is never
  erased.
- Synthetic providers (a synthesized subject) have no account to compare. They
  carry the prior refresh token without an account check. That is a limitation
  and it is the same with the option off. For them a lost swap always counts
  as a same-account winner and merges.

When a swap loses, the callback reads the winning row again:

- Winner of the same account: the login's access token, ID token and expiry are
  merged with the winner's refresh token in one more compare-and-swap on that
  token. If that swap also loses, the newer winner is kept and the login
  succeeds. There is no further retry.
- Winner of a different account: the winner is kept and the login fails with a
  server error. The user can sign in again, and that second login replaces the
  row.
- Winner that cannot be read: the login fails, because the stored account is
  unknown.
- Row deleted between the lost write and the re-read: the login's tokens are
  written again over the empty slot, with a swap that expects no refresh
  token. If the slot is gone again on the next re-read, that swap is repeated
  once more; after that the rules above apply.

A row owned by another user is replaced and logged at ERROR; the wrapper writes
the row under the context user, so this is not expected to happen.

### Adopting user-keyed storage in an embedder

Most mistakes in adopting the option do not fail. They show up as users who
look disconnected, refreshes that fail, or a guard that never refuses. Check
each item before turning the option on.

1. **Validator.** The bearer validator must be bound to the embedded issuer
   (`auth.WithEmbeddedAuthServerIssuer` equal to its issuer), verify with the
   in-process key provider (`auth.WithKeyProvider`), and have an audience.
   `auth.NewTokenValidator` refuses to start without them when its reader
   reports user-keyed storage (see the next item). A validator for another
   issuer must not be given the embedded server's upstream token reader.
2. **Reader wrappers.** A `TokenReader` that wraps another must implement
   `upstreamtoken.UserKeyedReader` and return the wrapped reader's answer.
   Otherwise the validator cannot see the user-keyed storage, the startup
   check is skipped, and a wrong validator setup shows up only at request time.
3. **One storage handle for upstream tokens.** Read, write, refresh and delete
   upstream tokens only through the server's `IDPTokenStorage()`, or a reader
   built over it. A storage handle kept from before the server was built sits
   below the user-keyed layer: through it, users look disconnected, refresh
   checks find no row, and deletes remove nothing. This also applies to code
   that wraps the refresher and re-reads the row.
4. **The user on the context.** Code of the embedder that touches upstream rows
   must put the user on the context with `identity.WithCanonicalUser` (or
   `auth.WithPlatformUser`). The user-keyed layer does not read the
   authenticated `Identity`, so a context with only an `Identity` fails with
   `ErrNoCanonicalUser`. A check that treats that error as temporary turns
   into a check that always passes.
5. **No second user-keyed layer.** Remove any decorator of the embedder that
   already keys rows by user. Two such layers key every row twice, and deletes
   find nothing. The server refuses storage that contains its own user-keyed
   layer, but cannot detect a layer of the embedder. A decorator the embedder
   puts over `IDPTokenStorage()` should expose `Unwrap() storage.Storage` or
   `Unwrap() storage.UpstreamTokenStorage`, so a reader built over it still
   reports user-keyed storage (see item 2).
6. **One key prefix per auth server.** Under session keying, rows of different
   auth servers never meet, because every `tsid` is unique. Under user keying,
   two auth servers that share a Redis key prefix share the row of a user and
   provider name. Give each server its own prefix.
7. **Existing rows.** Rows written before the option was on are not read.
   Users without a user-keyed row must sign in again, and until they do their
   refresh grants are refused. Users connect their providers again, or the
   embedder moves the rows itself.
   The layers below see a new session ID for each row (`user:` and the escaped
   user ID). A layer that binds data to the session ID, such as encryption with
   the session ID as associated data, cannot read old rows under the new ID.
8. **Lifetimes.** A row for a token that never expires has no storage lifetime
   and a zero `SessionExpiresAt` (users with a synthesized subject excepted).
   Code that treats `SessionExpiresAt` as the end of a connection, or compares
   rows by their expiry fields, must handle zero.
9. **Disconnect and user deletion.** Neither the refresher nor the refresh
   guard deletes a user's row. When the refresher cannot save a rotated refresh
   token it only clears the dead refresh token from the row by compare-and-swap,
   and the user connects again. The one remaining server-side delete is the
   callback's teardown after a confirmed identity mismatch in a multi-upstream
   chain, which the user-keyed layer makes unreachable in practice because it
   refuses a row owned by another user on read. The embedder offers disconnect
   (`DeleteUpstreamTokensForProvider` on `IDPTokenStorage()`, with the user on
   the context) and user deletion
   (`DeleteUser` on `IDPTokenStorage()`, asserted to `storage.UserStorage`).
   Only the user-keyed layer removes the user's row index on Redis, so
   `DeleteUser` on a storage handle below it leaves that index behind.
   Disconnecting the identity provider (the first upstream) ends the user's
   login on every client at its next refresh grant: the guard finds no row,
   refuses and revokes. Disconnecting a connector does not. Removing or renaming an upstream leaves its rows in
   place; once their access tokens expire every client gets 401 from the
   validator while the guard still grants, so clients loop. Delete those rows
   with `DeleteUpstreamTokensForProvider`. If the renamed upstream is the
   identity provider, the first refresh is refused and revoked instead; after
   the user signs in again, the old row makes clients loop as above.
10. **Refresh guard.** The built-in guard checks only the identity provider's
    row. It runs before the embedder's `RefreshGrantHandlers`, so those see only
    grants it accepted. Set `DisableUpstreamRefreshGuard` to keep only the
    embedder's own check.
11. **Replicas and refresh token rotation.** With more than one replica and an
    identity provider that rotates refresh tokens and detects reuse (for
    example Okta, Entra ID or Auth0), shared rows make the cross-replica
    rotation race in "Handle multi-pod refresh token rotation race condition"
    (#4122) much more likely. One lost race can revoke the token family at the
    provider and, through the guard, sign the user out on every client. Use
    user-affine routing or a single replica until #4122 is fixed.

## Configuration

### CRD Configuration

In Kubernetes, storage is configured via the `MCPExternalAuthConfig` CRD:

```
MCPExternalAuthConfig
  └── spec.embeddedAuthServer.storage
        ├── type: "memory" | "redis"
        └── redis
              ├── addr (standalone or cluster)  ─── mutually exclusive ───  sentinelConfig
              │                                                                   ├── masterName
              │                                                                   ├── sentinelAddrs[] (or sentinelService)
              │                                                                   └── db
              ├── clusterMode (optional; requires addr — Redis Cluster protocol)
              ├── aclUserConfig
              │     ├── usernameSecretRef  (optional; omit for password-only AUTH)
              │     └── passwordSecretRef
              ├── tls (optional, master/cluster connection)
              │     ├── caCertSecretRef
              │     └── insecureSkipVerify
              ├── sentinelTls (optional, sentinel connection only)
              │     ├── caCertSecretRef
              │     └── insecureSkipVerify
              ├── dialTimeout
              ├── readTimeout
              └── writeTimeout
```

**Implementation:** `cmd/thv-operator/api/v1beta1/mcpexternalauthconfig_types.go`

### RunConfig Serialization

When passing configuration across process boundaries (operator → proxy-runner), the CRD configuration is converted to `RunConfig` format where Secret references become environment variable references.

**Implementation:** `pkg/authserver/storage/config.go`

## Security Considerations

- **ACL or legacy authentication**: Redis ACL users (Redis 6+) provide fine-grained access control. When a username is omitted, go-redis sends legacy password-only `AUTH`, which is required for managed Redis tiers that do not expose an ACL subsystem (e.g. GCP Memorystore Basic/Standard HA, Azure Cache for Redis).
- **Key prefix isolation**: Each auth server is restricted to its own key prefix via Redis ACL rules (`~thv:auth:*`).
- **Credential handling**: In Kubernetes, credentials are stored in Secrets and injected as environment variables. They are never written to disk or logged.
- **TLS support**: TLS is supported for both master and Sentinel connections via `tls` and `sentinelTls` in the CRD. For managed services with private CAs (e.g. GCP Memorystore), provide the CA certificate via `caCertSecretRef`.

### Enabling Confidential Client Registration

`AllowConfidentialClientRegistration` is **off by default**. `/oauth/register` is unauthenticated, so turning it on lets any caller who can reach the endpoint obtain a client credential — there is no admission control to gate who receives a `client_secret`. This is the tradeoff an operator accepts by enabling it, not a bug to work around.

Enabling it permits DCR of confidential clients: `/oauth/register` additionally accepts `token_endpoint_auth_method` values `client_secret_basic` and `client_secret_post` (the `"none"` public-client default still applies on omission), and mints a `client_secret` returned exactly once in the registration response — it is never re-displayed or re-issued. Disabling the flag afterward does not revoke or reject secrets already minted; this gate only affects new registrations, not the token endpoint's acceptance of existing credentials.

Two restrictions apply regardless of this flag:

- Confidential registrations are restricted to https non-loopback redirect URIs. A client reachable on loopback or a private scheme is, by construction, a public client that cannot keep a secret (OAuth 2.1 §2.1), so the server declines to mint it one.
- Combining `AllowConfidentialClientRegistration` with `InsecureAllowHTTP` is rejected outright at validation — an unauthenticated secret-minting endpoint served over plaintext HTTP would let the secret be intercepted in transit.

See the [storage consequences](#ttl-management) above for what happens to a confidential registration over time: it is subject to the same DCR-issued expiry and cap as public DCR clients, on both backends.

**Loopback issuers need an explicit opt-in when combined with confidential registration.** The issuer validation that underlies `InsecureAllowHTTP` always permits a plain-`http://` issuer when the host is loopback (e.g. `http://localhost:18080`), independent of that flag — this is what lets `thv` run locally without setting up TLS just to test the auth server. Forcing TLS onto every loopback deployment instead would just push operators toward `InsecureAllowHTTP`, which is worse: that flag also disables the non-loopback host check entirely.

But a loopback `http://` issuer combined with `AllowConfidentialClientRegistration: true` means a locally-run auth server would mint client secrets over cleartext at an unauthenticated endpoint — validation rejects that combination by default. Set `InsecureAllowConfidentialOverLoopbackHTTP: true` to opt in when that is genuinely fine, e.g. local development and testing where the traffic never leaves the machine. Do not rely on this shape in a cluster: the server still listens on all interfaces there, "localhost" in the issuer is just a string, and such a deployment is only reachable in practice through something like a port-forward — it does not make the connection loopback-only. Both flags default to off, so nothing changes for an existing deployment unless the operator sets them.

**Rate limiting on `/oauth/register`.** Because the endpoint is unauthenticated, `Handler` also rate-limits it, by default 1 request/second sustained with a burst of 5 (`pkg/authserver/server/handlers/handler.go`). Set `registration_rate_limit` (`requests_per_second` and `burst`, both positive) in the auth server RunConfig to change it. In the operator CRD it is the embedded auth server's `registrationRateLimit` token bucket: `maxTokens` is the burst and `maxTokens / refillPeriod` the sustained rate, so the default is `maxTokens: 5` with `refillPeriod: 5s`. For a sustained rate R per second with a burst of B, set `maxTokens` to B and `refillPeriod` to B/R: 10 per minute with a burst of 3 is `maxTokens: 3`, `refillPeriod: 18s`. The limiter is a field on `Handler`, so it is per-process — running N replicas behind a load balancer allows roughly N times the configured rate, not a shared global rate. Over the limit the endpoint returns 429 with a `Retry-After` of at most 1/rate seconds, rounded up, minimum 1 (so 1 at the default rate); a partly refilled bucket frees a token sooner.

**DCR client metadata.** Redis persists the complete DCR client shape, including the inline public JWKS and `token_endpoint_auth_signing_alg` for `private_key_jwt` clients. These registrations use the same TTLs as public and secret-based confidential DCR clients: 24 hours until first use, then 30 days of inactivity. A successful token exchange or refresh renews the TTL; reading a client during authorization does not. The in-memory backend applies the same expiry. Pre-provisioned clients are not subject to this DCR retention policy.

### Enabling `private_key_jwt` Dynamic Client Registration

`AllowPrivateKeyJWTRegistration` is **off by default** and is separate from
`AllowConfidentialClientRegistration`. The former permits unauthenticated
`/oauth/register` requests to create clients that authenticate at the token
endpoint with their own signed JWT assertions; the latter permits the endpoint
to mint `client_secret` credentials for `client_secret_basic` or
`client_secret_post`. Enabling one does not enable the other. Because registration
is unauthenticated, either flag should be enabled only when any caller able to
reach the endpoint is trusted to create a client of that type.

This implementation accepts **inline `jwks` only** for `private_key_jwt` DCR.
`jwks_uri` is explicitly rejected; the client must include at least one valid
public signing key with `use: "sig"`. `token_endpoint_auth_signing_alg` is
mandatory and must match a key in the set. The accepted algorithms are `RS256`,
`RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`, and `ES512`
(EdDSA is excluded because the pinned fosite version cannot verify it — see
`crypto.SupportedClientKeyAlgorithms`). RSA keys must have a 2048–8192-bit
modulus and an odd public exponent between 3 and 65537, and a registration may
carry at most 10 inline JWKS keys. The registration response contains the
client metadata and no `client_secret`.

Stored `private_key_jwt` clients are checked against these rules again every
time they are loaded. A client registered before the limits existed whose keys
exceed them is rejected with `invalid_client` (the reason is logged at debug
level with its `client_id`) and must re-register. Re-registration issues a new
`client_id`, so update any `AllowedDelegateClients` entries that name the old
one.

A `client_assertion` must be a compact JWS whose protected header carries no
`jwk` member and whose header and payload are JSON nested no deeper than
10000 levels; otherwise the token and device-authorization endpoints answer
`invalid_client` before parsing it. An OpenID Connect `request` object on
`/oauth/authorize` gets the same check, before the client is looked up, and is answered with
`invalid_request_object`, without a redirect. Keys are always resolved from the
client's registered JWKS, so an embedded key was never used, and JWT claims
are a JSON object, so conforming tokens pass. The check keeps untrusted JWTs
away from the go-jose v3 parser inside the pinned fosite version, which has an
unbounded embedded-key validation (GHSA-mgcc-cww5-fp2q) and an unbounded JSON
nesting depth (GHSA-vvm8-gf78-28g4). It goes away once fosite moves to go-jose
v4. Each rejection is logged at warn level. A `request_uri` is never fetched,
because no client can register request URIs.

A `private_key_jwt` DCR registration is restricted to the RFC 8693 token-exchange
grant (`urn:ietf:params:oauth:grant-type:token-exchange`) and cannot request the
authorization-code or refresh-token grants. If `grant_types` is omitted, this
exchange grant is supplied automatically. Discovery advertises
`private_key_jwt` and the supported signing algorithms only when
`allowPrivateKeyJWTRegistration` is enabled or a configured delegate client
uses `private_key_jwt`. It advertises `client_secret_basic` and
`client_secret_post` when confidential DCR is enabled or a configured delegate
client authenticates with a secret. Token exchange is advertised by default and is
omitted when `inbound_grants` leaves out `token_exchange`.

The registration and token endpoints must use HTTPS for confidential-client
registration, exactly as described above. `private_key_jwt` registration has
no equivalent restriction: a `private_key_jwt` DCR response never contains a
`client_secret` or any other secret, so there is nothing for cleartext HTTP
to expose. It is governed only by the issuer's general `insecureAllowHTTP`
setting, the same as a public (`none`) client — no loopback-specific opt-in
is needed or offered.

A client proves possession of its private key by sending a signed
`client_assertion` at `/oauth/token`; ToolHive stores only the registered public
JWKS. The assertion's `aud` must hold the issuer and nothing else; see
[Client-assertion audience](17-token-exchange-delegation.md#client-assertion-audience).
The server accepts a clock difference of 60 seconds in the assertion's `exp`,
`iat` and `nbf`, so an assertion from a client whose clock runs a little fast or
slow still works. `exp` must be at most 5 minutes plus that 60 seconds ahead of
the server's current time, and it must not be before `iat`. Refusals use
`invalid_client` and do not report the server time.
The server decides the replay marker deadline, 65 seconds after the assertion
`exp` (the 60 second margin plus a 5 second grace), and hands it to the store, so
an assertion cannot be replayed during the margin. This holds for any store
that keeps the `jti` until the deadline it is given, provided the store's
`ClientAssertionJWTValid` or `SetClientAssertionJWT` is atomic for a `jti`.
ToolHive's backends reserve the `jti` in `ClientAssertionJWTValid`; fosite's
`MemoryStore` checks and inserts in `SetClientAssertionJWT`. The grace does not
cover clock differences between replicas that share Redis, and during an upgrade
a replica on the older version keeps the marker only until `exp`; see
[the limits](17-token-exchange-delegation.md#secretless-dcr-delegate-flow) in
the token exchange page. One shared rule, `server.ValidateClientAssertionExp`, refuses an
assertion unless that deadline is in the future and `exp` is not too far ahead.
An `exp` of 0 or a wrapped value fails it, because the marker would otherwise be
recorded as already expired. The provider and the clock skew fallback use a
wrapper around the backend that applies the rule and passes on the deadline; see
[Secretless DCR delegate flow](17-token-exchange-delegation.md#secretless-dcr-delegate-flow).
The in-memory and Redis backends store the deadline they are given. They refuse
a deadline in the past or more than 7 minutes 5 seconds ahead
(`server.ValidateClientAssertionMarkerDeadline`) in `SetClientAssertionJWT`.
A refused marker leaves the short reservation in place, so the `jti` stays
known.
Redis also persists the registered client and its JWKS under
the auth server's isolated key prefix, so replicas sharing Redis can use the
same registration. The in-memory backend is process-local and loses registrations
on restart.

This client-authentication flow is distinct from the existing RFC 7523 §2.1
JWT-bearer **grant**. The JWT-bearer grant accepts an external assertion as the
bearer grant itself and issues a token without authenticating a registered OAuth
client. `private_key_jwt` is RFC 7523 §2.2 client authentication: it authenticates
an already registered client, which then uses its permitted token-exchange grant.
The two flows have separate enablement and purposes; one does not configure or
substitute for the other.

### Delegation consent

A private-key JWT client is subject to the same delegation consent checks as any
other authenticated ToolHive client. For an externally issued subject token,
the trusted issuer's `AllowedDelegateClients` list must contain the registered
ToolHive `client_id`, or explicitly contain `"*"` where wildcard delegation is
intended. The check is based on the authenticated client ID, not whether it used
a secret or `private_key_jwt`; `allowedActors`/`actorMatcher` alone do not bind
the exchange to a particular ToolHive client.

The following capabilities are deliberately deferred and are not supported by
this feature: remote `jwks_uri` key retrieval, SPIFFE/SVID client
authentication, AWS STS `act` claim mapping, and ID-JAG chaining.

### Forcing Confidential Registration for a Known Redirect URI

Some MCP clients declare themselves public in their RFC 7591 registration (`token_endpoint_auth_method: "none"`) and then refuse to proceed because the response carries no `client_secret` — a self-contradictory request no conformant server can satisfy as written (Perplexity is the known case). RFC 7591 §3.2.1 permits the server to substitute client metadata during registration, and `ForceConfidentialRedirectURIs` uses that permission: it lists redirect URIs that are always registered as confidential clients, overriding a requested (or omitted) `"none"`.

A registration whose `redirect_uris` contains an exact match for one of these entries is issued a real `client_secret` and reported back as `token_endpoint_auth_method: "client_secret_post"` — never `client_secret_basic`, because the Python MCP SDK these clients are typically built on constrains the field to `["none", "client_secret_post"]`.

Matching is exact string equality, not a prefix or scheme-relaxed match, and that is deliberate rather than a limitation: an attacker who registers with someone else's callback URI is issued a secret for a client whose authorization codes are delivered to that someone else's redirect endpoint, not to the attacker. The secret is useless without also controlling the callback, so exact matching does not hand out a usable credential for another client.

This requires `AllowConfidentialClientRegistration` to be set, and every entry must be an https non-loopback URI — the same restriction ordinary confidential DCR already enforces, so the override cannot be used to slip a secret to a client that is a public client by construction.

Treat each entry as a targeted, temporary accommodation for one misbehaving client, not a general escape hatch: by setting it, the operator is asserting that the specific client behind that redirect URI can actually hold a secret. Remove the entry once the client is fixed to handle a `"none"` registration correctly.

## Related Documentation

- [Redis Storage Configuration Guide](../redis-storage.md) — User-facing setup guide
- [Operator Architecture](09-operator-architecture.md) — CRD and controller design
- [Core Concepts](02-core-concepts.md) — Platform terminology

## CIMD Storage Decorator

When `authServer.cimd.enabled: true` is set, the embedded authorization server wraps its storage backend in a `CIMDStorageDecorator` before passing it to fosite. This decorator enables MCP clients to present HTTPS URLs as `client_id` values without first calling `/oauth/register`.

### What it does

`CIMDStorageDecorator` embeds the full `storage.Storage` interface and overrides `GetClient` and `RenewClientTTL`. When fosite calls `GetClient("https://vscode.dev/oauth/client-metadata.json")` during an authorization request:

1. The decorator detects the HTTPS URL using `oauthproto.IsClientIDMetadataDocumentURL`
2. It fetches the Client ID Metadata Document from that URL via `pkg/oauthproto/cimd.FetchClientMetadataDocument` (with SSRF protection, 10 KB cap, 5-second timeout)
3. It builds a `fosite.Client` from the document fields, caches it for `cimd.cache_fallback_ttl` (default 5 minutes), persists it as a DCR-issued row for token-endpoint session rehydration, and returns it to fosite
4. Concurrent fetches for the same URL are deduplicated via `singleflight`
5. Fetches on a cache miss or expiry are rate-limited to 1 per second with a burst of 5, per process, on every route that resolves a `client_id` (authorize, token, device authorization). Failed fetches count against the limit. A refused lookup with no servable stale entry (see below) returns a `fosite.ErrNotFound` error that wraps `storage.ErrCIMDRateLimited`, which the handlers answer with HTTP 429 and `Retry-After: 1`. Some routes can still be answered from the persisted write-through row instead; see "Persisted-row fallbacks" below.

   When the limiter refuses a fetch, or the fetch is attempted and fails transiently (the host delivered no usable response: a connection, TLS, or resolver failure, a refusal by the fetcher's own private-address guard at dial time, a timeout, a 200 whose body was cut short, a 5xx, a 408, or a 429; `cimd.ErrDocumentUnavailable`), and the client has an expired cache entry less than `MaxStale` past its expiry, the decorator serves that entry instead of returning 429 or `invalid_client`. `MaxStale` is set to the configured access token lifespan (default 1 hour) and is not user-configurable: a refresh arrives about one access-token lifetime after the last use, so a smaller bound would never cover a refresh. While budget is available an expired entry is still re-fetched and re-validated. A stale answer neither renews the entry nor rewrites the persisted row, so every later lookup re-fetches again until the host answers, and the caller waits for the re-fetch to fail, up to the 5-second fetch timeout, before it receives the stale answer (there is no background revalidation). A definitive failure of the re-fetch (a 3xx or any other 4xx from the host, whatever happened to its body, an unexpected content type, a malformed body, a document that fails validation, or one this server's policy rejects) is returned to that caller and evicts the expired entry, so neither a later limiter refusal nor a later transient failure serves the pre-failure document from the cache: the next lookup needs a successful fetch. The persisted row is not evicted: on `/oauth/token` a later limiter refusal can still be answered from it for up to 30 days after the last successful fetch, and the other row fallbacks under "Persisted-row fallbacks" below read it only to finish an authorization already in progress, within the bounds given there. A transient failure with an entry already past `MaxStale` evicts that entry too. One `CIMD document fetch failed` warning is logged per failed fetch, with `fallback=expired_cache` and `age_past_expiry` when the entry answered, or `fallback=none`.

   The trade-off is freshness. Points to know:
   - An unauthenticated caller can create the limiter pressure by sending more than one distinct `client_id` per second, so the stale window can be held open on purpose.
   - Stale serving applies to `/oauth/authorize` and `/oauth/device_authorization` as well as `/oauth/token`. A `redirect_uri` or policy that the client has since withdrawn can stay in effect for up to the cache TTL plus `MaxStale`: 65 minutes on defaults, and about 24 hours 5 minutes if an operator raises `access_token_lifespan` to its 24 hour maximum. Before the limiter existed the bound was the cache TTL (5 minutes).
   - The transient-failure case adds no window of its own: it shares `MaxStale` with the limiter case, so the bounds above hold for both. A host that answers a 3xx or a 4xx other than 408 or 429, or serves a document this server rejects, ends the cache window at once, because the re-fetch evicts the entry; a host that is unreachable or answers 5xx, 408, or 429 cannot. The persisted-row fallbacks below are not ended by it.
   - The cache and the limiter are per process. With several replicas, each replica has its own window.
   - This is a deliberate choice of availability over freshness. The CIMD draft (section 5) says a fetch that does not return 200 must be treated as an error, and sections 5.1 and 5.2 say a server SHOULD re-fetch periodically and SHOULD abort on a failed fetch. Serving a stale entry or row after a failed fetch departs from that on purpose. It also departs from RFC 9111 revalidation for documents sent with `no-store` or `must-revalidate`. The fixed cache TTL, which ignores the document's HTTP cache headers (section 5.2 SHOULD), predates the limiter.

   This widens but does not close the lockout. Stale serving does not cover a cold cache (first use, or after a restart), other replicas that have not cached the client, or an entry evicted from the LRU, for example by an attacker hosting enough valid documents to push it out.

   Persisted-row fallbacks. The persisted client row is written on every successful fetch. Three places read it when the limiter refuses and no stale cache entry is servable. They differ on purpose, and unifying them is deferred.
   - `/oauth/callback` and the consent step (`callbackClient` in `handlers/callback.go`) read the row on any `ErrCIMDRateLimited` through the admitted below-CIMD view that `NewHandler` selects once at construction (`storage.ClientStoreBelowCIMD`, stopping at `ClientAdmissionStorage`) and keeps on the handler; request handling does not walk the decorators. The fallback still passes client admission. There is no drift check, and if the read fails the caller gets the base lookup's error. They need the row because the pending authorization is already consumed and the login could not be retried. The snapshot is bounded by the pending-authorization lifetime plus the cache TTL plus `MaxStale`.
   - Device verification: the device submit, the device upstream-login completion, and `POST /oauth/device/confirm` (all through `rejectUnresolvableDeviceClient` in `handlers/device_verification.go`, which calls `callbackClient`) read the row the same way. Nothing is consumed at these steps, and the lookup only decides yes or no; the resolved client is not used to issue anything. The device request was created only after `/oauth/device_authorization` resolved the client, so the snapshot is bounded by the device code lifetime plus the cache TTL plus `MaxStale`.
   - `/oauth/token` only. Some clients discard their stored session when a token request fails, so a 429 on a refresh would log the user out. The handler marks the request (`storage.WithPersistedClientFallback`), and the decorator then reads the row from the layer under it. A drift check applies first: the row must be a public client written by the CIMD fetch for this id, every grant type on it must still be one the server allows for CIMD clients, the row's audience must equal the configured allowed audiences, and when `scopes_supported` is set every row scope must be in `scopes_supported` or the baseline scopes. This protects against an operator changing that configuration before a restart. When the row is missing, the storage read fails, or the check fails, the caller gets the original rate-limit error (429), never the base error. `/oauth/authorize` and `/oauth/device_authorization` still return 429. The row is shared by all replicas on Redis, so a replica that never served the client benefits. The memory backend loses its rows on restart, so it gets no protection right after one (its sessions also end on restart).

   The `/oauth/token` fallback skips re-validation against the document. The decorator does not renew CIMD rows when a token request succeeds, because every successful fetch already writes the row again. The age of the row is therefore bounded the same way on both backends:
   - The row expires 30 days (`DefaultDCRClientTTL`) after the last successful fetch. The memory backend also loses it on restart.
   - Rolling upgrade: a replica that still runs the older version renews the row (resets it to the full 30 days) on every successful token request. While such replicas serve token traffic, the row can stay in place beyond 30 days from the last fetch. This ends when all replicas run the new version.

   Revocation lag. This is how long a client's withdrawn or changed document can stay in effect:
   - On the main branch before the limiter: about 5 minutes (the cache TTL). The token route fetched the document live every time.
   - Limiter budget available: unchanged, about 5 minutes.
   - Limiter empty and a cache entry present: until the entry expires plus `MaxStale`. That is 65 minutes on defaults, and up to about 24 hours 5 minutes if `access_token_lifespan` is raised to its 24 hour maximum.
   - Limiter budget available, the document host unreachable or answering 5xx, 408, or 429, and a cache entry present: until the entry expires plus `MaxStale`, the same bound as the limiter case. A 3xx, any other 4xx, or an invalid document ends it at once by evicting the entry; on `/oauth/token` the next bullet then applies.
   - Limiter empty, no cache entry, `/oauth/token`: up to 30 days since the last successful fetch, on both backends. A definitive re-fetch failure reaches this state at once, because it evicts the entry.

   An unauthenticated caller can hold the limiter empty (about 6 requests, then one request per second with distinct URL-shaped `client_id` values), so every one of these windows can be held open on purpose, not only the stale one. A stolen refresh token for a client whose document was withdrawn is usable for up to this lag, but never beyond the refresh token's own lifetime (7 days by default, at most 30 days, at least 1 hour). These windows bound only how long old client metadata is accepted at later lookups; tokens and grants already issued follow their own expiry and revocation, so none of them is a revocation bound for issued credentials.

   What a client sees:
   - A refused lookup always answers with status 429 and `Retry-After: 1`. The body differs by route. `/oauth/token` and `/oauth/device_authorization` also send `Cache-Control: no-store`, `Pragma: no-cache` and an `application/json` body `{"error":"temporarily_unavailable","error_description":"..."}`. This follows the RFC 6749 section 5.2 error format, although that section defines no rate-limit code, and `temporarily_unavailable` is defined by RFC 6749 only for the authorization endpoint (section 4.1.2.1), so at the token endpoint it is an extension code. `/oauth/authorize` answers with a `text/plain` body `rate limit exceeded, retry later`, because the client is unresolved there and its `redirect_uri` cannot be trusted. `/oauth/register` and the device flow's own limiters still answer plain text. None of the clients we checked honour `Retry-After`, and clients that discard their stored credentials on any failed refresh gain nothing from the JSON body.
   - It applies to `/oauth/token` and `/oauth/device_authorization` as well as `/oauth/authorize`. All three share one bucket per process. The bucket is not per IP address and not per route.
   - A cache hit costs no token. Only a miss or an expiry does. A fetch that fails or is rejected is not cached and costs a token. `/oauth/authorize` can look the client up up to 3 times per request (the back-channel check, fosite, and the loopback rewrite for `http://localhost` redirect URIs), so a failing authorize request can cost 2 to 3 tokens.
   - The earlier limiter on `/oauth/authorize` that spent a token on every URL-shaped `client_id`, including cache hits, is gone.
   - `GET /oauth/authorize` answers 400 with a plain-text body for any `multipart/*` `Content-Type`, for CIMD and non-CIMD clients alike, because authorize parameters must come from the query string. The server logs this at WARN with the media type and path.

   Known limits:
   - Stale serving needs a cache entry. `/oauth/device_authorization` and `/oauth/authorize` have no other fallback, so after a restart, or on a replica that never served the client, they can return 429 where they used to fetch the document live. `/oauth/token` returns 429 only when no usable row exists either.
   - Junk traffic on `/oauth/token` or `/oauth/device_authorization` can use up the shared bucket and so deny new sign-ins at `/oauth/authorize`.
   - A device flow that polls `/oauth/token` can end with a 429 when the bucket is empty and there is neither a stale cache entry nor a persisted row. RFC 8628 section 3.5 tells a client to stop polling on any error other than `authorization_pending`, `slow_down`, `access_denied` or `expired_token`.
   - A failed fetch that is not a limiter refusal is covered by stale serving when the failure is transient (a document host timeout, a 5xx, a 408 or 429, a DNS or connection failure) and an entry within `MaxStale` exists; the persisted-row fallback answers only a limiter refusal. Without such an entry, and on every definitive failure, the request gets `invalid_client` exactly as before the limiter existed, and clients that discard their session on a failed refresh lose it.
   - `cacheFallbackTtl` has no upper bound. The configuration check only requires a positive duration when CIMD is enabled. Because token success no longer renews CIMD rows, a value above 30 days can let cache hits outlive the Redis row. Keep it well below 30 days.

All other `Storage` methods (`RegisterClient`, token storage, upstream token storage, etc.) delegate to the underlying backend unchanged, except `RenewClientTTL` (CIMD client ids are skipped, other ids are delegated) and `ConsumeAssertionJWT` and `Unwrap`, which forward to the wrapped backend. DCR clients (opaque string IDs) continue to work exactly as before.

### Unwrap pattern

The storage decorators (`CIMDStorageDecorator`, the CIMD shape guard, `SPIFFEStorageDecorator` and `ClientAdmissionStorage`) implement `Unwrap() Storage`, and `storage.Unwrap` follows the chain to the concrete backend. Runtime code depends on this at these places:

- **`DCRCredentialStore` assertion** (`newServer`): The `DCRCredentialStore` interface is narrower than `Storage` and not embedded in it. `storage.Unwrap(stor).(storage.DCRCredentialStore)` reaches the concrete backend through the decorators.
- **`RedisStorage` migration** (`runLegacyMigration`): A type assertion to `*storage.RedisStorage` is needed to run a one-shot data migration.
- **`DeviceCodeStorage` assertion** (`buildDeviceFlowFactory` and `handlers.NewHandler`): The device flow reaches the device-code store on the concrete backend.
- **`RememberedConsentStorage` assertion** (`handlers.NewHandler`): Remembered consent is only offered when the concrete backend implements it.
- **Continuation fallback** (`handlers.NewHandler`, used by `callbackClient` in `handlers/callback.go`): deliberately bypasses the CIMD decorator. When the CIMD fetch limiter refuses a lookup during callback, consent, or device verification, it reads the persisted client row through the admitted below-CIMD view (`storage.ClientStoreBelowCIMD`, which stops at `ClientAdmissionStorage`). That view is selected once by `NewHandler` and kept on the handler, not found by walking the decorators during the request, so the fallback still passes client admission and never reaches the raw backend.

The `AssertionJWTConsumer` capability is separate: `CIMDStorageDecorator` implements `ConsumeAssertionJWT` and forwards it to a capable backend. If the backend lacks the capability, the decorator returns an error instead of accepting an assertion with unknown replay status; replay protection therefore fails closed.

### Air-gapped environments

When the embedded authorization server is deployed in an environment that cannot reach `https://toolhive.dev/oauth/client-metadata.json` or any public CIMD metadata URL, set `authServer.cimd.enabled: false`. Clients will fall back to DCR (`/oauth/register`) which uses only the local storage backend and requires no outbound connectivity. Stale serving does not change this: a cache entry exists only after a successful fetch, so a server that can never fetch a document never resolves the client.

**Implementation:** `pkg/authserver/storage/cimd_decorator.go`

## SPIFFE Storage Decorator

**Current status: active when SPIFFE associations are configured.** The decorator is installed whenever `spiffeTrustDomains`/`inboundGrants.spiffeClientAuth` are configured, which requires a `file` bundle source (other source types fail validation). See [SPIFFE Association Declarations](18-spiffe-association-declarations.md) for the full status.

When top-level `spiffeTrustDomains` and `inboundGrants.spiffeClientAuth` are configured, the embedded authorization server wraps its storage backend in a `SPIFFEStorageDecorator` — installed as the outermost decorator, after CIMD (`decorateStorageForSPIFFE` in `pkg/authserver/server_impl.go`). This decorator overlays statically configured OAuth clients ahead of the dynamic DCR/CIMD backend. `newServer` also wires the configured bundle source into the X.509 and JWT verification interfaces. The proxy runner and vMCP can serve authorization-server routes on a TLS listener at fixed port 8443, which X.509-SVID client authentication needs. The decorator reserves configured client IDs; it does not itself verify SVIDs.

### What it does

`SPIFFEStorageDecorator` embeds the full `storage.Storage` interface and overrides `GetClient`, `RegisterClient`, and `ReconcileConfiguredClient`. `GetClient` checks its static client map first and only falls through to the wrapped storage (CIMD or the shape guard, then client admission, then the backend) when the requested client ID is not one of the configured associations. `RegisterClient` and `ReconcileConfiguredClient` reject any DCR, delegate-client, or configured-client attempt that targets a client ID reserved by a static SPIFFE association. CIMD never calls `RegisterClient`; it durably persists resolved clients via `UpsertDCRIssuedClient` instead (a best-effort write-through for token-endpoint session rehydration — see the CIMD section above), which explicitly refuses to clobber a configured/SPIFFE-reconciled client at the same ID, so it cannot collide with a static association.

Its clients come entirely from the configured SPIFFE trust-domain and client-association declarations (see [SPIFFE Association Declarations](18-spiffe-association-declarations.md)). They are built once at startup, held in memory, and never written to the storage backend (memory or Redis); they are never eligible for dynamic registration or replacement.

At startup, the decorator durably claims each configured static client ID in the storage backend (memory or Redis) via `ReconcileConfiguredClient` (`preflightDurableCollisions`), using an inert placeholder rather than the real client. A placeholder is created when the ID is unclaimed and replaced when one from a prior run already holds it; a changed association logs the replacement WARN and takes effect on rollout (see [Configured clients across restarts](#configured-clients-across-restarts)). Startup is refused only when the ID is DCR-issued or holds a non-placeholder client. This closes a cross-replica race that a read-only `GetClient` check alone cannot: with Redis and multiple replicas, an older or still-rolling replica without this SPIFFE config could otherwise DCR-register the same client ID after a newer replica's read-only check passed. The reverse collision can't happen: the decorator's `GetClient` always checks its static map first, so a durable client can never shadow a static one.

**Implementation:** `pkg/authserver/storage/spiffe_decorator.go`
