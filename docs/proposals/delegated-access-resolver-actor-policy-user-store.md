# Delegated access: one subject resolver, separate actor checks, one user-keyed credential store

Status: draft proposal, 2026-11-13, revision 2 (after a review by four
reviewers: architecture, embedder compatibility, security, OAuth
standards). Written against `main` at 5f36b560c. When it is ready, it
moves to the RFC repository as an RFC.

## 1. Problem

A gateway runs ToolHive's embedded authorization server and a vMCP. The
vMCP calls backends with the user's stored upstream credentials (for
example the user's GitHub token). Three kinds of token must be able to
use those credentials:

1. **Interactive.** The user signed in through the gateway's own
   authorization server. This works today.
2. **Delegated through the gateway's authorization server.** An agent
   platform runs its own login server (called AS-1 here). AS-1 issues an
   ID-JAG for an agent acting for a user. The agent platform redeems the
   ID-JAG at the gateway's authorization server (AS-2) and receives a
   short gateway token. Token exchange of an external subject token is
   the same case. So is a scheduled run that the agent platform starts
   with no user present.
3. **Direct.** The customer's identity provider (Okta, as the first case)
   issues a token to an agent. The agent presents that token directly to
   the gateway's vMCP. Nothing in ToolHive mints a token in between.

All three must end in the same place:

- the subject is the native user (the gateway's own user ID, for example
  a user directory's ID);
- the actor (the agent) is checked;
- the credential is read by (gateway, native user, routed provider), and
  the stored row must belong to that user.

None of the three may reach credentials through another one's path.

Two constraints on every change:

- An existing embedder that already keys credential rows by user through
  its own storage decorators, builds `authserver.Config` itself, and
  relies on today's validator, session and refresh-grant behaviour, must
  keep working unchanged after upgrading ToolHive (section 6).
- Standalone ToolHive may change its storage schema, but only behind a
  setting, with a documented upgrade path (section 5).

### What blocks this today

- Credentials are stored per `tsid` (token session ID), and the read
  returns nothing for a token without `tsid` (`pkg/auth/token.go`,
  `loadUpstreamTokens`). Delegated tokens have no `tsid`, so they never
  get credentials.
- `loadUpstreamTokens` reads `tsid` from any validated token, whatever
  its issuer. That is safe only while the validator trusts one issuer.
- No read checks that the stored row belongs to the token's user
  (`storage.ErrInvalidBinding` is declared and never returned).
- ID-JAG redemption needs every subject listed in `subject_bindings`, and
  issues `sub = iss#sub` instead of a native user
  (`tokenexchange/id_jag_handler.go`).
- ID-JAG redemption copies `act` without checking it.
- The resource-server validator trusts one issuer and checks no client
  claim (`azp`, `cid`, `client_id`). Its OIDC discovery does not compare
  the discovered issuer with the configured one, and it skips the
  audience check when no audience is configured.
- External token exchange always binds the subject on `sub`. For Okta
  access tokens from a custom authorization server, `sub` defaults to the
  user's login (often the email). The immutable ID is `uid`.
- The interactive chain walk reads only the new session's rows, so every
  re-authorization walks every upstream again ("Re-authorization walks
  the full upstream chain instead of evaluating existing credentials
  (#6831)").

## 2. Decisions

### 2.1 One subject resolver

A new leaf package, `pkg/auth/subject`, with no import of `pkg/auth`, so
that `tokenexchange`, the vMCP and embedders can all import it:

```go
// Ref names a user at a trusted issuer by one stable claim.
type Ref struct {
	Issuer string // the issuer the identity belongs to, from configuration
	Claim  string // the binding claim configured for that issuer, e.g. "uid"
	Value  string // the claim's value from a verified token
}

type Resolver interface {
	// Resolve returns the native user ID. It never creates a user, and it
	// never returns "" with a nil error.
	Resolve(ctx context.Context, ref Ref) (string, error)
}

var (
	ErrUnknown     = errors.New("subject not provisioned")  // refuse
	ErrAmbiguous   = errors.New("subject maps to >1 user")  // refuse
	ErrUnavailable = errors.New("resolver unavailable")     // 503 / server_error
)
```

- Callers treat an empty user ID as an error, whatever the resolver
  returned with it.
- The binding claim is configured per issuer. Okta: `uid`. Entra: `oid`.
  Configuration validation of this new binding claim rejects `email`,
  `preferred_username` and `upn`, and warns on `sub` for Okta custom
  authorization servers, where `sub` defaults to the login. The existing
  upstream `OIDCConfig.SubjectClaim` is not validated this way, because
  embedders set it at runtime.
- **AS-1's `upstream_subject`** (`{iss, claim, value}`) becomes a `Ref`
  only after two checks. The trusted-issuer configuration for AS-1 lists
  which upstream issuers AS-1 may name; any other issuer is refused
  (ID-JAG §9.8: the subject's own issuer does not establish trust). The
  claim must be the binding claim configured for that upstream issuer.
  RFC 9493 `sub_id` (format `iss_sub`), which ToolHive already emits, may
  replace the private claim later. `aud_sub` (ID-JAG §3.1) is not used:
  in external mode AS-1 does not know AS-2's user ID.
- **Cross-issuer mapping.** One identity provider can use different
  issuers for sign-in and for access tokens. For Okta, a dedicated custom
  authorization server (`https://{org}/oauth2/{id}`) is not the sign-in
  issuer, but its `uid` equals the sign-in ID token's `sub`. The resolver
  configuration states this mapping explicitly: `(direct issuer, uid)`
  resolves as `(sign-in issuer, sub)`.
- Standalone ToolHive implements the resolver from its own
  provider-identity records, without the create path. An embedder with a
  user directory implements it with its directory lookup.
- `UserStorage.GetProviderIdentity` stays for interactive login. It is
  keyed by a configured upstream's provider name, and trusted issuers are
  not upstreams, so it is not the resolver.

The resolver runs at two places:

- **At mint time,** in ID-JAG redemption and in external token exchange,
  for issuers configured with `subject_mode: resolve`. The issued token's
  `sub` is the native user (RFC 9068 §2.2). Issuers left on the default
  `subject_mode: namespaced` keep today's `iss#sub`.
- **On every request,** for direct issuers (section 2.4), unless decision
  D5 picks the in-process translation (section 2.5).

### 2.2 The actor: proof, client checks, actor policy

| Check | Question | Where | Mechanism |
|---|---|---|---|
| Actor proof | Is the agent's credential real, and may this agent act for this user? | ID-JAG issuance (AS-1) | `ActorTokenValidator` (exists, merged, not wired) |
| Client binding | Is the client presenting the assertion the one it was issued to? | ID-JAG redemption | ID-JAG §4.4.1 `client_id` check (exists) |
| Client authorization | Was this token issued to an allowed client? | direct path; external token exchange | per-issuer client claim allowlist (exists for token exchange as `ActorClaim` + `AllowedActors`; reused for the direct path) |
| Actor policy | Is this actor allowed for this issuer, client and user? | ID-JAG redemption, token exchange, direct path | `actor.Policy` (new) |

Client checks and actor policy are separate inputs. ID-JAG §9.10 says the
authenticated client does not identify the actor, and a permissive actor
rule must never switch off the client check.

On the direct path, the client check is client *authorization*, not
binding: nobody authenticates the presenter, so it says only that the
token was issued to an allowed client. Proof of who presents the token
comes only from DPoP. The validator library reports the client from the
claim each identity provider uses (Appendix A); the allowlist is required
and cannot be a wildcard. Token exchange already has this check (the
trusted issuer's `ActorClaim` with `AllowedActors` or the CEL
`ActorMatcher`), and the direct path uses the same configuration.

The actor policy, in the leaf package `pkg/auth/actor`:

```go
type Actor struct {
	Source  string         // "act" or "client": where this identity provider names the actor
	Issuer  string         // act.iss, or the token's iss when act has no iss
	Subject string         // act.sub, or the client ID when Source is "client"
	Claims  map[string]any // the outermost act object, or the agent claims the profile names
}

type Request struct {
	TokenIssuer string
	ClientID    string // a separate input; never used as the actor
	Actor       *Actor // nil when the token has no act
	User        string // the resolved native user
	Resource    string
}

type Decision struct {
	Allow     bool
	Reason    string // stable reason code for errors and audit
	Principal string // the actor identity used for session binding and audit
}

type Policy interface {
	Evaluate(ctx context.Context, req Request) (Decision, error)
}
```

- Only the outermost `act` counts (RFC 8693 §4.1).
- Identity providers name the actor differently. Okta and RFC 8693
  issuers use `act`. Entra never emits `act` and names the agent as the
  client (`azp`, with an agent marker for Entra Agent ID), and the IETF
  agent-authentication draft does the same. So each issuer has exactly
  one actor source, `act` or `client`, set by its profile (Appendix A),
  with no fallback from one to the other.
- The default source is `act`, and a token without `act` is refused.
  An issuer can be configured to take the client as the actor when `act`
  is absent, for example for an agent's own authorization-code tokens.
  When the source is `client`, the client allowlist and the actor policy
  test the same value; audit records the source so this is visible.
  Outside that configuration, the client is never used as the actor.
- The default implementation is configuration: allowed actor issuers, an
  allowlist of actor subjects, and its own CEL expression evaluated over
  `Request.Actor` only. It does not reuse the client-check matcher.
- An embedder with a directory can implement the policy with a directory
  lookup that resolves the actor to a principal (for example a service
  account), and refuses an unknown one.

What each path checks:

- **AS-1 issuance:** actor proof (the agent platform's validator), then
  AS-1 writes `act = {iss: AS-1, sub: <agent id>}`.
- **AS-2 redemption:**
  1. client binding (§4.4.1);
  2. `act.iss` must equal the ID-JAG's `iss`, or be in the actor issuers
     configured for that trusted issuer. Anything else is refused before
     the policy runs, so AS-1 cannot claim another platform's issuer;
  3. actor policy on `act.iss`. `act.sub` is AS-1's opaque per-agent ID
     and means nothing to AS-2, so an approval keyed by `act.iss` covers
     every agent AS-1 mints. Per-agent control rests on AS-1's actor
     proof. This is deliberate;
  4. in external mode, an unrevoked approval for (gateway, user, client,
     `act.iss`) ("Delegated access: subject mapping and user approval
     (#6887)").
- **Direct:** client authorization, then actor policy on the actor the
  validator library reports.

### 2.3 One user-keyed credential store, without new storage methods

**The rule:** no method is added to the `storage.Storage` aggregate or to
`UpstreamTokenStorage`, no existing signature changes, and no new
optional storage capability is looked up by type assertion.

Why: embedders wrap ToolHive's storage in decorators that embed
`storage.Storage` and override only what they need, for example
encryption at rest with a fresh nonce on every write, or a refresh-token
envelope. A method added to the interface is promoted from the innermost
backend through every decorator: it would skip the encryption and the
keying, and fail silently. A method on a separate optional interface is
not promoted at all, so a type assertion on the outer decorator fails,
and a capability check at construction would stop the embedder from
starting.

**The canonical user in the context.** Storage cannot import `pkg/auth`
(the import would close a cycle through `pkg/auth/upstreamtoken`), so the
contract is the authorization server's own key,
`serveridentity.WithCanonicalUser` / `serveridentity.CanonicalUserFromContext`
in `pkg/authserver/identity`. `auth.CanonicalUserFromContext` already
prefers that key. Every caller that touches upstream storage on behalf of
a user sets it:

| Caller | Sets the user from |
|---|---|
| OAuth callback | the resolved user (already does, `callback.go`) |
| Device-code verification | `confirmation.ResolvedUserID` |
| Validator bulk load (`loadUpstreamTokens`) | `identity.PlatformUserID`, set on the load context |
| Refresh-grant guard | the stored session's subject |
| Read service (`GetValidTokensForUser`) | its `userID` argument |
| Refresher | inherits the caller's context (`context.WithoutCancel` keeps values) |

**User keying is a storage decorator,** with the same contract as the
one an existing embedder already runs:

- Writes take the user from `tokens.UserID`. Reads and deletes take it
  from the canonical-user key. A read or delete with no user in the
  context returns a distinct error, `storage.ErrNoCanonicalUser`. It never
  falls back to a session key.
- It writes the composite user key into the session slot of the inner
  backend. With the Redis backend the keys become:

  | Key | Today | With the decorator |
  |---|---|---|
  | Row | `{prefix}upstream:{tsid}:{provider}` | `{prefix}upstream:user:{userID}:{provider}` |
  | Per-session index | `{prefix}upstream:idx:{tsid}` | `{prefix}upstream:idx:user:{userID}` |
  | User reverse index | `{prefix}user:upstream:{userID}` | unchanged |

  The Redis backend's code does not change. "Refactor upstream token
  storage from STRING+SET to HASH with per-field TTL (#4279)" stays
  independent.
- It checks the owner inside every read: the row's `UserID` must equal the
  canonical user, before the expiry check and before any refresh. A
  mismatch is `storage.ErrInvalidBinding`. On the request path that is a
  generic 500, never a 503 and never a sign-in prompt.
- `DeleteUpstreamTokens` (delete all rows of a session) deletes nothing
  under the decorator, because rows belong to the user, not the session.
  Signing out of one client, or a refresh-grant refusal for one client,
  does not remove the user's connections. In v1 rows leave only by
  expiry; an explicit disconnect and offboarding come later (B6).
- `ResolveUpstreamTokenRowID` returns the user-keyed row's identity, so
  the process-local refresh deduplication and the cross-replica refresh
  lease ("Serialize upstream refresh across replicas", PR #7031) work on
  the shared row.

**One upstream account per (user, provider)** is enforced in the OAuth
callback, not in the decorator, because the decorator cannot tell an
ordinary sign-in from a reconnect. A sign-in that returns a different
upstream subject for a provider the user already connected fails with an
error that names the provider and offers the reconnect link. The existing
row is kept. Reconnect is an explicit single-provider flow
(`PendingAuthorization.SingleLeg`) bound to the signed-in user; it
replaces the row only after the exchange succeeds. Upstreams that produce
synthetic subjects (a new subject on every flow) are exempt.

Standalone ToolHive turns the decorator on with a setting (section 5). An
embedder that already has its own user-keying decorator keeps it and does
not turn this one on. The decorator is applied only by the runner or by
an explicit setting, never because a Go field has its zero value.

**The read service.** A small exported interface in
`pkg/auth/upstreamtoken`:

```go
// UserCredentialReader reads one provider's credential for one user,
// refreshing it when expired.
type UserCredentialReader interface {
	GetValidTokensForUser(ctx context.Context, userID, provider string) (*UpstreamCredential, error)
}
```

- `InProcessService` implements it: it refuses an empty `userID`, sets the
  canonical user in the context, and calls the existing
  `GetUpstreamTokens` with `userID` as the session argument (so the
  service's per-session state, such as ID-token suppression and refresh
  deduplication, is per user). Under a user-keying decorator, the call
  goes through the embedder's own encryption and keying.
- An embedder supplies its own implementation when it wraps the reader
  with its own rules (for example which providers a user has enabled).
- The vMCP's outbound strategies receive the reader through an option,
  injected where the vMCP is assembled. With no reader configured, the
  delegated and direct routes fail closed.
- It reads one provider only: the provider of the tool's route. Never a
  provider named in the request.
- No row, or a refresh refused with `invalid_grant`: a defined "needs
  authorization" result carrying the connect link. The link is
  configured, because embedders mount the authorization server's routes
  themselves. Not 401, not 403.
- A storage or transient refresh failure is an infrastructure error.
- Startup refuses to enable the delegated or direct routes unless the
  user-keying setting is `user`, or the embedder sets a typed
  configuration field declaring that its storage keys rows by user.

**The refresher's delete becomes conditional.** Today, when a provider
rotates a refresh token and the new one cannot be stored, the refresher
deletes the row unconditionally. It now reads the row again, compares
the stored refresh token (in plaintext, as the storage returns it) with
the one it redeemed, and deletes only on a match. All three calls go
through the storage it was given, so decorators that encrypt or wrap the
refresh token keep working. The read and the delete are not atomic; the
window is accepted. If the re-read fails, the refresher behaves as today.
This change applies to every deployment (section 6).

**The interactive request path.** For interactive tokens the validator
keeps loading every provider into `Identity.UpstreamTokens`, as today, so
embedders whose policy gates read it keep working. With the decorator on
(`user` mode only), a failed refresh of one provider drops that provider
from the identity instead of failing the whole request, except for the
identity provider, whose failure still fails it; otherwise one dead
connection would put every client of the user into a sign-in loop. The
refresh-grant guard, in `user` mode, deletes only the rejected
provider's row, conditionally, and refuses the grant when the storage
returns `ErrNoCanonicalUser`.

**The interactive chain walk** reads the user's rows through the same
`GetAllUpstreamTokens` call when the decorator is on, because the
decorator ignores the session ID. Only missing or broken providers need a
redirect. This fixes #6831 without a stable session ID. A newly
authorized client therefore gets the user's existing connections without
an upstream prompt; this is accepted (B1).

### 2.4 Who may read user U

The validator records an admission route on the identity:

```go
type AdmissionRoute int

const (
	RouteUnspecified AdmissionRoute = iota // zero value: today's behaviour
	RouteInteractive
	RouteDelegated
	RouteDirect
)
```

| Route | Admitted when | User | Credential read |
|---|---|---|---|
| Interactive | `iss` is the embedded authorization server and the token carries `tsid` | `sub` | validator bulk load into `Identity.UpstreamTokens`, as today |
| Delegated | `iss` is the embedded authorization server and the token carries the delegated-access marker | `sub`, resolved at mint time | `GetValidTokensForUser`, routed provider |
| Direct | `iss` is a configured direct issuer and the token passes the direct-issuer checks | resolved per request | `GetValidTokensForUser`, routed provider |

- Each route needs positive proof. None is "a claim is missing".
- `loadUpstreamTokens` runs only for the interactive route. A `tsid`
  claim on a token from any other issuer is ignored.
- Injection from `Identity.UpstreamTokens` keeps working for any identity
  that has it, whatever its route, because embedders fill it themselves
  on some paths. The route gates only the new read.
- `SessionlessGrant` stays, with its current consumers. It is true for
  the delegated and direct routes. Marked tokens keep the existing
  `no_upstream_session` claim, so embedders that skip sign-in prompts for
  session-less grants keep doing so.
- **Cedar.** With `primaryUpstreamProvider` set, Cedar today accepts a
  nil `UpstreamTokens` only for session-less grants. It now evaluates the
  request token's claims whenever the route is not interactive, so `act`
  stays visible to policy on delegated and direct tokens.
- `tsid` stays in interactive tokens. It stops being a storage key when
  the user-keying decorator is on, but it remains the proof of an
  interactive login together with the embedded issuer, and some
  embedders use it to recognise the login behind a refresh grant. A
  token issued before an upgrade keeps working.
- A self-issued token exchanged through `may_act` or a wildcard delegate
  client keeps `sub` = the user but has neither `tsid` nor the marker, so
  it reads nothing.

**The delegated-access marker** ("Add a delegated-access marker that
gates credential lookup for approved delegated tokens (#6404)"):

- a URI-named claim (`https://toolhive.dev/claims/delegated_access`),
  honoured only when `iss` is the embedded authorization server;
- issued only on ID-JAG redemption or token exchange, after the subject
  resolved, the client checks and the actor policy passed, and, in
  external mode, an unrevoked approval exists for (user, client,
  `act.iss`);
- issued only for trusted issuers configured with
  `grants_credential_access: true`. Configuration refuses
  `grants_credential_access` and `subject_mode: resolve` on the embedded
  server's own issuer;
- never issued at login, and never because `may_act`, a wildcard
  delegate client or a CEL match succeeded on its own;
- terminal: token exchange and ID-JAG issuance refuse a marked token as
  subject token or actor token, and no refresh token is issued with a
  marked token.

**What redemption issues.** The gateway token's `aud` is within the
ID-JAG's `resource`, its `exp` is no later than the ID-JAG's, and it is
bound to the same DPoP key when the ID-JAG carried `cnf`. Whether a used
`jti` is refused is the per-issuer reuse policy in #6887.

**Scheduled runs** that the agent platform starts with no user present
use the delegated route: the platform's grant leads to a marked gateway
token, and the read finds the user's rows directly. No stable session ID
has to be stamped on the grant.

**Where the direct route runs.** The agent sends its identity provider's
token to the vMCP. The vMCP's incoming-authentication middleware (the
resource-server side) validates it with the shared validator library
(Appendix A). The authorization server never sees the token on this
path. It only owns the credential rows, and its refresher renews an
expired upstream token when the vMCP reads it. Section 2.5 describes an
alternative that moves these checks into the authorization server.

**What the vMCP needs from the library:** a verified principal, or a
refusal it can map to an RFC 6750 error. The principal carries the
issuer, the user binding (`Ref`: issuer, claim, value), the client, the
actor (section 2.2), the scopes, and the DPoP key binding, if any. How
each identity provider expresses these is the library's concern.

**What ToolHive then does:**

- refuses a token carrying `tsid`, the delegated-access marker or
  `no_upstream_session`, and refuses configuration where a direct issuer
  equals the embedded issuer or a direct audience equals the embedded
  issuer;
- checks the client allowlist and runs the actor policy (section 2.2);
- resolves the user binding (section 2.1). Unknown or ambiguous: 403 with
  a ToolHive-defined error body (not an RFC 6750 error code). Resolver
  unavailable: 503 with `Retry-After`;
- requires the issuer to belong to the same identity-provider tenant as
  the gateway's own sign-in identity provider. Its administrator can
  already sign in as any user, so trusting its tokens adds no new party
  that can act for users.

The operator configures a direct issuer with three required values (the
issuer, the audience, and a non-empty client allowlist), a profile name
that tells the library how that identity provider shapes its tokens, and
optional required scopes and actor settings. The issuer alone is never
enough: it would admit every token the tenant mints, for any audience and
any application.

The direct validator sets `PlatformUserID` to the resolved user. Session
binding (#6888) uses that user, never the token's own `sub`.

MCP clients find authorization servers through the protected-resource
metadata (RFC 9728). A direct issuer that is not listed there works only
for agents configured in advance (banked item B7).

### 2.5 Alternative: in-process translation at the authorization server

Instead of checking a direct token itself, the vMCP passes it to the
authorization server's exchange logic, in process. The authorization
server makes its usual checks on the Okta token and mints a marked
gateway token. The vMCP then continues as for the delegated route. The
agent still sends only the Okta token.

This is internal token translation, not an RFC 8693 exchange: RFC 8693
§2.1 warns that unauthenticated exchange lets a compromised token be
turned into other tokens. The translation is never reachable from the
token endpoint, and the minted token never leaves the process. The
minted token says:

- `client_id`: a reserved identifier for the vMCP translator, never the
  Okta `cid`, which could collide with ToolHive client IDs, approvals or
  `AllowedDelegateClients`;
- the Okta client, in a separate URI-named claim;
- `act`: the Okta token's outermost `act`, with `iss` set to the Okta
  issuer;
- `aud`: the vMCP resource only;
- `exp`: no later than the Okta token's `exp`;
- `cnf`: copied when the Okta token was DPoP-bound.

```text
agent ──Okta token──▶ vMCP incoming auth
                        │ issuer is a direct issuer; signature verified;
                        │ DPoP checked
                        ▼
                      authorization server, in process:
                        client check + actor policy + resolve
                        + lifetime cap  →  marked token (never leaves)
                        │
                        ▼
                      delegated route: read (gw, user, routed provider)
```

**What it gains:**

- The checks run in one place for the exchange and the direct variant.
- The vMCP's validator stays single-issuer for everything it admits.
- Audit records one kind of decision for both variants.

**What it costs:**

- A mint on every request, or a cache keyed by a hash of the Okta token
  and the DPoP key thumbprint, valid until the earlier of the two
  tokens' `exp`. DPoP is checked on every request before the cache
  lookup. A cache delays a deactivation until the cached token expires.
- The vMCP still recognises the direct issuer and verifies the signature
  before handing the token over.
- The security properties do not change: the Okta token is still
  reusable until it expires and cannot be revoked by the gateway.
- Token exchange needs a mode with no requesting client.

A third variant keeps the checks in one shared validation library that
both the vMCP and token exchange call, and mints nothing. On security
alone there is little to choose between the three. **Choosing** is
decision D5.

## 3. What changes where

### ToolHive

| Area | Change | Tracked in |
|---|---|---|
| `pkg/auth/subject` (new leaf) | `Resolver` interface, errors, a standalone implementation | #6887 |
| `pkg/auth/actor` (new leaf) | `Policy` interface, configuration implementation with its own CEL over the actor | #6887 |
| `tokenexchange/id_jag_handler.go` | per-issuer `subject_mode`; allowed upstream issuers for `upstream_subject`; resolve instead of `subject_bindings` and `iss#sub`; client binding as today; `act.iss` check; actor policy; approval in external mode; marker; output `aud`/`exp`/`cnf` | #6887, #6404, #6892 |
| `tokenexchange/handler.go`, `validator.go` | per-issuer binding claim for external subject tokens; resolve; marker; refuse marked subject and actor tokens | #6887, #6404 |
| `tokenexchange/id_jag_mint.go` | wire the actor validator into issuance; refuse marked subject tokens | #6898, #6899, #6404 |
| `pkg/authserver/identity` | `ErrNoCanonicalUser`; the canonical-user key is the storage contract | #6885 |
| `pkg/authserver/storage` (new decorator) | user keying, owner check, `DeleteUpstreamTokens` as a no-op | #6885, #6176, #6407 |
| `pkg/authserver/server/handlers` | callback: one account per (user, provider), explicit reconnect; device verification sets the canonical user | #6831, #6885 |
| `pkg/authserver/refresh_grant_guard.go` | set the canonical user; in `user` mode delete only the rejected provider's row; refuse on `ErrNoCanonicalUser` | #6831 |
| `pkg/authserver/refresher.go` | conditional delete | #6831 |
| `pkg/auth/upstreamtoken` | `UserCredentialReader`, implemented by `InProcessService` | #6407 |
| `pkg/auth/token.go` | admission route; `loadUpstreamTokens` only for the interactive route; canonical user on the load context; per-provider refresh failure in `user` mode | #6407 |
| `pkg/auth` (new validator) | direct-issuer validator, routed by issuer ahead of the embedded-server validator, sets `PlatformUserID` from the resolver | new |
| `pkg/auth` validator fixes | compare the discovered issuer; do not skip the audience check silently | new (small, independent) |
| `pkg/authz/authorizers/cedar` | evaluate request claims when the route is not interactive | #6407 |
| `pkg/vmcp/auth/strategies` | reader option; for delegated and direct routes, read the routed provider; "needs authorization" result with a configured connect link | #6407 |
| vMCP sessions | for delegated and direct sessions only, bind also client, `act.iss`, `act.sub` and route; every other binding stays byte-identical | #6888, #6779 |
| Configuration | user-keying setting in the authorization-server run configuration; trusted-issuer `subject_mode`, binding claim, allowed upstream issuers, actor issuers, `grants_credential_access` through `MCPExternalAuthConfig`; direct issuers in the vMCP configuration, the `VirtualMCPServer` CRD and its converter; the typed "storage keys by user" declaration | #6403 (rewritten), new |
| Documentation | `docs/arch/11-auth-server-storage.md`, `docs/arch/17-token-exchange-delegation.md` | with each change |

Out of scope for the first version: delegated and direct reads for a
plain `MCPServer` through the `upstreamswap` middleware (banked item B5).

### An embedder with a user directory

Nothing is required to keep working (section 6). To adopt the new routes:

- Its directory-backed user resolver implements `subject.Resolver`.
- It may implement `actor.Policy` with its directory, so that an agent
  maps to a directory principal.
- It supplies a `UserCredentialReader` that applies its own reader rules,
  sets the typed "storage keys by user" field, and configures the connect
  link.
- It passes the new trusted-issuer and direct-issuer fields through
  explicitly, where it builds `authserver.Config` and the vMCP's incoming
  authentication itself; until it does, the new routes stay off.
- Where it routes tokens by issuer ahead of the authorization-server
  bearer chain (for example for workload tokens), it adds the direct
  issuers to the issuers that bypass that routing.
- It may add the owner check to its own decorator, comparing against the
  logical user rather than the composite stored value.

### An agent platform that runs AS-1

- Registers its actor validator with ToolHive's hook (#6899).
- Produces one actor ID per agent instance.
- Uses, in `upstream_subject`, the claim the gateway binds on for the
  shared identity provider. Okta ID tokens have no `uid`, and their `sub`
  holds the same value, so the two sides agree on the claim name, or the
  gateway's resolver maps it explicitly (section 2.1).

## 4. Order of work

Each step changes nothing for a deployment that does not configure it,
except the conditional delete in step 1, which applies to everyone.

1. **Groundwork.** The canonical user set by the device verification, the
   validator's load context and the refresh-grant guard;
   `ErrNoCanonicalUser`; the conditional delete in the refresher; the
   `pkg/auth` validator fixes.
2. **Store.** The user-keying decorator with the owner check, the callback
   account rule and reconnect, per-provider refresh failure and the
   guard's per-provider delete in `user` mode, behind the setting.
   Requires "Embedded auth runtime DCR does not bind upstream refresh
   tokens to the DCR client generation (#6508)" and the refresh lease
   (PR #7031) before anyone runs shared rows with rotating providers.
3. **Read service.** `UserCredentialReader`, the strategy option and the
   "needs authorization" result. Nothing calls it yet.
4. **Routes and interfaces.** The admission route, the Cedar change,
   `pkg/auth/subject`, `pkg/auth/actor`. The interactive route requires
   the embedded issuer from this step on.
5. **Marker.** Issued nowhere yet; refused as subject and actor token at
   token exchange and ID-JAG issuance. The delegated route reads only
   marked tokens.
6. **Direct route** (track A). The direct-issuer validator, client
   authorization, actor policy, per-request resolution, session binding
   (#6779, then #6888). Needs D5.
7. **Delegated route** (track B). Resolution at redemption and at token
   exchange, the `act.iss` check, approval in external mode (or
   administrator pre-approval), marker issuance, and ID-JAG issuance with
   the actor validator (#6898, #6899). This comes after steps 3 to 5:
   once redemption mints a native `sub`, the gateway token names a real
   user, and only the read gates keep it from that user's rows.
8. **Later, a separate announced change:** the CLI and CRD default for new
   standalone configurations becomes `user`.

Steps 6 and 7 run in parallel. For an embedder whose storage already keys
rows by user, step 2 is not needed for steps 6 and 7.

## 5. Standalone ToolHive: new installs and upgrades

The setting, for example `upstreamCredentialKeying` in the
authorization-server run configuration:

- `session` (default): today's behaviour.
- `user`: the user-keying decorator is on.

**Memory storage:** nothing persists, so there is nothing to migrate.

**New installs:** the examples and the documentation use `user`. The
default stays `session` for now, because a CRD default cannot tell a new
object from an existing one. Changing the CLI and CRD default is step 8.

**Upgrades with Redis:** after switching to `user`, each user signs in
once. The old `upstream:{tsid}:*` rows are not read. They expire on their
own TTL (access-token expiry plus 30 days). Nothing to clean up.
Migrating rows in place is not offered: it would need new storage
methods, and when a user's sessions held different upstream accounts for
one provider it would have to pick one.

Every replica must run the new version before the setting changes. A
replica still in `session` mode would refresh the old rows while the
others use the new ones.

Switching from `user` back to `session` is not supported. The release
notes and the upgrade guide say so.

## 6. Compatibility with an embedder that already keys rows by user

Such an embedder pins a ToolHive release, builds `authserver.Config` and
the vMCP's incoming authentication itself, wraps ToolHive's storage in its
own decorators (encryption at rest, directory-backed users, user keying,
refresh-token envelopes), fills `Identity.UpstreamTokens` itself on some
paths, binds sessions to issuer and subject, skips sign-in prompts for
session-less grants, and uses `tsid` to recognise the login behind a
refresh grant.

| Step | Effect on that embedder |
|---|---|
| 1. Groundwork | **The conditional delete changes behaviour for everyone:** after a failed persist of a rotated refresh token, the row is deleted only if it still holds the redeemed token. It goes through the embedder's decorators (read, plaintext compare, delete). The canonical user set by ToolHive callers is the same key the embedder already reads. |
| 2. Store | none: the decorator is off unless set, and never applied by a zero value |
| 3. Read service | none until the embedder supplies or wires a reader |
| 4. Routes, Cedar | none: the route's zero value is today's behaviour; injection from `Identity.UpstreamTokens` is not gated; `SessionlessGrant` keeps its meaning; the interactive route's issuer check matches what the embedder's tokens already carry |
| 5. Marker | none until an issuer is configured to issue it; marked tokens keep `no_upstream_session` |
| 6. Direct route | none until a direct issuer is configured; session bindings for other identities stay byte-identical |
| 7. Delegated route | none: only for issuers set to `subject_mode: resolve` and `grants_credential_access`, which the embedder does not pass through yet |
| 8. Default flip | none: a CLI and CRD default, not a Go zero value |

Rules that keep this true:

- No method is added to `storage.Storage` or `UpstreamTokenStorage`, no
  signature changes, and no optional storage capability is asserted.
- The interactive validator path, including the bulk load into
  `Identity.UpstreamTokens`, does not change outside `user` mode.
- `tsid` and `no_upstream_session` stay in the tokens that carry them
  today.
- New identity and session fields are added only for the new routes.
- Every new behaviour is off until configured per issuer.

## 7. Effect on filed issues

| Issue | Effect |
|---|---|
| Delegated client access to a user's connections (#6406) | Its rule that a directly presented external token never reaches the read is reversed. Add the direct route. |
| Add upstreamCredentialScope opt-in and enforce its issuer trust (#6403) | The rule that the incoming issuer must equal the embedded issuer blocks the direct route, unless D5 picks the in-process translation. Replace the `session`/`platformUser` scope with the user-keying setting and per-issuer configuration. |
| Add a delegated-access marker (#6404) | Add: URI name, honoured only for the embedded issuer, terminal at ID-JAG issuance too, no refresh token, approval required in external mode. |
| Ownership-checked credential read (#6407) | Keep the read design. Drop phase 1 (reading through a stable session ID) and the embedded-issuer-only gate. The read is an injected interface, not a storage capability. |
| Upstream token read cannot verify ownership (#6176) | The owner check moves into the user-keying decorator. Adding an owner parameter to every `tsid`-keyed read is no longer needed, and would change exported signatures that embedders implement. Assigned to an outside contributor; re-scope with them. |
| Re-authorization walks the full upstream chain (#6831) | Keep the problem. The fix is the user-keying decorator, the callback account rule with explicit reconnect, the conditional delete and the guard changes, not a stable session ID. A stable ID was agreed with the assignee, including a derivation the token endpoint could use for scheduled runs; scheduled runs now use the delegated route. Re-scope with the assignee before any code. |
| User-owned upstream credential storage (RFC) (#6885) | Becomes the main path. This proposal answers most of its list. |
| Delegated access: subject mapping and user approval (#6887) | Keep. Add allowed upstream issuers, the `act.iss` check, the cross-issuer mapping, per-request resolution and actor policy for the direct route. |
| ID-JAG issuance (#6898), actor validation at issuance (#6899) | Unchanged; they are the actor proof. |
| vMCP session binding (#6888) | Extend to direct sessions, only for the new routes. |
| Handle multi-pod refresh token rotation race condition (#4122), PR #7031 | Compatible: the lease is keyed by the row identity, which the decorator provides. Needed before shared rows with rotating providers. |
| Embedded auth runtime DCR does not bind upstream refresh tokens to the DCR client generation (#6508) | Still needed for standalone ToolHive, before shared rows. |
| Refactor upstream token storage from STRING+SET to HASH with per-field TTL (#4279) | Independent. |

## 8. Open decisions

- **D1. Interactive proof.** Decided: `tsid` with the embedded issuer.
- **D2. Upgrade path for standalone Redis.** Decided: re-authorize once,
  with every replica upgraded before the setting changes.
- **D3. Direct-issuer configuration.** Decided: provider-agnostic. The
  operator gives the issuer, the audience and a client allowlist, plus a
  profile name; identity-provider specifics live in the validator library
  (Appendix A). The issuer must share the tenant of the gateway's sign-in
  identity provider. Required scopes and refusing unbound tokens are
  per-issuer options.
- **D4. Resolution time for minted tokens.** Decided: at mint time. ID-JAG
  redemption and token exchange resolve the subject once; the gateway
  token's `sub` is the native user, and the approval check (external
  mode) uses that same user. The vMCP does not resolve marked tokens
  again. The resolver refuses a deactivated user as it refuses an unknown
  one. A resolver outage at the token endpoint returns 503 or
  `server_error`, never `invalid_grant` or `consent_required`. A marked
  token's lifetime is capped (a few minutes), whatever
  `delegationLifespan` says; ID-JAG-derived tokens are already capped by
  the ID-JAG's lifetime. A user deactivated in the directory therefore
  keeps delegated access for at most that cap, plus directory sync delay.
  The agent platform mints a new ID-JAG on each cache miss of its gateway
  token, so in practice resolution happens about once per gateway-token
  lifetime per (session, agent, gateway, user), not once per call.
  Offboarding (B6) removes the user's rows, which ends credential use at
  once. Direct tokens are still resolved on every request.
- **D5. Where the direct route makes its checks.** Decided: in the vMCP's
  incoming authentication first (section 2.4), built on the toolhive-core
  `authn` validator stack (toolhive-core PRs #506 to #511, with the trust
  set in #514 to #516) as a reusable library. The stack already covers
  issuer and keys, discovery issuer equality, per-issuer keys, exact
  audience, the algorithm allowlist, `exp`/`nbf`/future `iat`, lifetime
  caps, the ID-JAG `typ` deny list, required scopes with
  `insufficient_scope`, and issuer routing with reserved issuers. Gaps to
  fill: in toolhive-core, an option for untyped tokens (missing `typ` or
  `JWT` only) and DPoP proof verification (until it exists, tokens with
  `cnf` are refused, the stack's default); in ToolHive, `Check`s for
  `jti`, the client claim and the refused claims, the actor policy, the
  configuration rules on `aud` and issuers, and error mapping. The stack
  must merge and be released before stage 6. The in-process translation
  (section 2.5) stays an alternative. Moving the authorization server's
  external-token validation onto the same library is optional and
  separate.

## 9. Banked items

Found in review, not resolved here, because each needs a decision:

- **B1. Consent for shared connections.** Decided: accepted as is. With
  user keying, a newly authorized client, possibly self-registered, gets
  the user's existing connections without an upstream prompt, the same
  way single sign-on grants a new application the user's session. The
  user still passes the authorization server's own consent for that
  client. Documented in the upgrade guide.
- **B2. Marker value.** Decided: checks use presence only. The value may
  name the trusted issuer or the approval record for audit, but nothing
  requires it and no check reads it.
- **B3. Actor across identity providers.** Decided: one actor source per
  issuer, `act` or `client`, set by the profile, with no fallback. Default
  `act`, refusing tokens without it; an issuer may opt in to taking the
  client as the actor when `act` is absent. The Entra profile uses
  `client`, optionally restricted to Entra Agent ID identities.
- **B4. Approval record (open question).** The approval is a per-user
  consent record, one row per (gateway, user, client, actor issuer) with
  a revoked state, created by the user through the approval link or by an
  administrator's pre-approval. It is not a directory service account:
  in the enterprise domain model an agent working for a person acts as
  that person, and service accounts are for autonomous workloads. Open:
  whether the record lives in the gateway authorization server's storage
  or in the directory, and whether approving an actor issuer (covering
  every agent of that platform) is acceptable. Per-agent approval is not possible with opaque,
  per-instance agent IDs.
- **B5. Plain `MCPServer`.** Decided: tracked, not in v1. Delegated and
  direct reads through the proxy runner's `upstreamswap` middleware come
  after the vMCP.
- **B6. Removing a user's rows.** Decided: in the plan, not in v1, and
  not blocking. v1 relies on expiry (access-token expiry plus 30 days).
  Later: a per-provider disconnect the user triggers, and an
  administrator offboarding operation that deletes all of a user's rows
  (which an embedder's directory can call).
- **B7. Discovery.** Decided: not listed. The vMCP's RFC 9728 metadata
  keeps listing only its embedded authorization server, and the
  `resource_metadata` in its 401 challenges keeps pointing there. Direct
  issuers are configured trust for agents provisioned out of band, not
  authorization servers that MCP clients should discover; RFC 9728
  allows a resource not to advertise every server it accepts. Listing
  them would send generic MCP clients to the customer's identity
  provider (failed or unexpected client registrations, interactive
  prompts on a path meant for agents) and has no effect on audience
  binding. This departs from the MCP rule that clients send only tokens
  from the server's own authorization server, by deliberate,
  out-of-band arrangement. If agents later need discovery, they get a
  separate resource identifier with its own path-based metadata and
  audience.
- **B8. Removing `tsid`.** Decided: keep it as the proof of an
  interactive login; revisit only once embedders no longer use it for
  refresh-grant checks.

## 10. Accepted costs

- A direct token works until it expires. The gateway cannot revoke it and
  nothing makes it single use. DPoP and the lifetime cap reduce this.
- The direct route costs one resolver call per request. A cache brings
  back a staleness window.
- With user keying, a user's clients share one row per provider. Signing
  out of one client does not remove the upstream connection.
- The refresher's conditional delete is a read followed by a delete, not
  an atomic operation.

## 11. Not verified

- What Okta puts in a real access token from the customer's tenant:
  `uid`, `cid`, `act`, `typ`, `cnf`, and the lifetime.
- Whether the directory lookup is cached, and its latency under load.
- Whether the refresher renews a stored ID token that an embedder's
  policy gate reads.
- How PR #7031's lease is keyed, beyond its description.

## Appendix A. What the validator library must provide

The direct route uses the toolhive-core `authn` validator stack as a
library. These are the requirements this proposal places on it, not part
of the proposal itself. The library owns how each identity provider
shapes its tokens.

**Output.** A verified principal (issuer, user binding as issuer + claim +
value, client, actor with its source, scopes, DPoP key binding) or a
typed refusal that maps to `invalid_token`, `insufficient_scope` or
`server_error`.

**Checks that configuration can never switch off:**

- signature with that issuer's keys only; `iss` matched exactly; the
  discovery document's issuer equal to the configured issuer; templated
  issuers (for example Entra's `{tenantid}`, `/common`, `/organizations`)
  refused;
- algorithm allowlist, never `none` or symmetric algorithms;
- `aud` matched against the configured audience;
- `exp` required, `iat` not in the future, `nbf` honoured, and a lifetime
  cap that can be tuned under a hard ceiling but not disabled;
- the client claim present;
- the binding claim present, and never `email`, `upn` or
  `preferred_username`;
- the subject is a user, not the client itself or an application
  identity (for example Entra `idtyp=app`);
- only the outermost `act` considered;
- DPoP enforced whenever the token carries `cnf`.

**Profiles.** A profile sets the claim names and token shape for one kind
of identity provider. A starting set, to be confirmed against real tokens:

| | generic RFC 9068 | Okta | Entra | Keycloak |
|---|---|---|---|---|
| `typ` | `at+jwt` | absent | `JWT` | `JWT` |
| client claim | `client_id` | `cid` | `azp` (v2), `appid` (v1) | `azp` |
| scope claim | `scope` | `scp` | `scp` | `scope` |
| binding claim | `sub` (operator confirms it is immutable) | `uid` | `oid`, with `tid` equal to the issuer's tenant | `sub` |
| token ID | `jti` | `jti` | `uti` | `jti` |
| actor source | `act` | `act` | `client` | `act` (unverified) |
| issuer form | — | custom authorization server only; org server refused | per-tenant only | — |

**Guards when a profile accepts a `typ` other than `at+jwt`:** refuse a
token carrying `nonce`, refuse an `aud` equal to the client claim, and
require the scope claim, so that ID tokens are never accepted as access
tokens. The ID-JAG `typ` is always refused.

**Entra specifics:** `xms_act_fct` containing 11 marks an Entra Agent ID
identity and can be required; `xms_sub_fct` containing 13 means the
subject is an agent's own user account, refused unless allowed.

**Gaps in the stack today** (PRs #506 to #516, none merged; ToolHive pins
toolhive-core v0.0.59): an option for tokens without `at+jwt`, DPoP proof
verification including the nonce retry, and claim-level checks for the
token ID, the client claim and the refused ToolHive claims (these can be
`Check`s written by ToolHive). Until DPoP verification exists, tokens with
`cnf` are refused.
