# Delegated credential access: storage changes

Draft, revision 2 · 2026-11-13 · against `main` 5f36b560c.
Full proposal: [delegated-access-resolver-actor-policy-user-store.md](delegated-access-resolver-actor-policy-user-store.md)

**Goal.** Delegated tokens (ID-JAG redemption, token exchange, scheduled
runs) and directly presented IdP tokens must read the user's stored
upstream credentials. Today they can't: rows are keyed by `tsid`, and
those tokens have none.

## Today

| | |
|---|---|
| Row | Redis `{prefix}upstream:{tsid}:{provider}` → JSON `UpstreamTokens{AccessToken, RefreshToken, IDToken, ExpiresAt, SessionExpiresAt, UserID, UpstreamSubject, ClientID}`, TTL `ExpiresAt + 30d` |
| Indexes | `{prefix}upstream:idx:{tsid}` (session → row keys); `{prefix}user:upstream:{userID}` (user → row keys, only for `GetLatestUpstreamTokensForUser`) |
| New key per login | `handlers/authorize.go`: `SessionID: rand.Text()` |
| Request read | `pkg/auth/token.go` `loadUpstreamTokens`: no `tsid` → nothing; else `GetAllUpstreamTokens(tsid)`, every provider, any refresh failure → 401 |
| Owner check | none; `storage.ErrInvalidBinding` declared, never returned |
| Refresher | CAS write on the redeemed refresh token; on a failed persist of a rotated token, **unconditional** `DeleteUpstreamTokensForProvider` |
| Chain walk | `nextMissingUpstream` reads only the new `tsid`, so every re-auth walks every upstream (#6831) |

An embedder already user-keys rows by wrapping `storage.Storage` in
decorators (encryption, user keying via the `sessionID` slot, refresh-token
envelope). Nothing below may break it.

## Rules that constrain every change

1. No method added to `storage.Storage` or `UpstreamTokenStorage`, no
   signature change, no optional capability found by type assertion.
   Embedding decorators would promote a new method from the inner backend
   (skipping encryption and keying) or fail an assertion.
2. The canonical user travels on the authorization server's context key,
   `serveridentity.WithCanonicalUser` (`pkg/authserver/identity`). Storage
   cannot import `pkg/auth` (import cycle via `pkg/auth/upstreamtoken`).
3. New storage behaviour is applied by the runner or an explicit setting,
   never by a Go zero value. `tsid` stays in interactive tokens.

## Changes, by stage

### Stage 1 · groundwork (all deployments)

| Change | Where |
|---|---|
| Set the canonical user before touching storage | `device_verification.go` (from `confirmation.ResolvedUserID`); `token.go` `loadUpstreamTokens` load context (from `identity.PlatformUserID`); `refresh_grant_guard.go` (from the stored session's subject). The callback already does it. |
| New sentinel `ErrNoCanonicalUser` | `pkg/authserver/identity` |
| Conditional delete: re-read the row, compare the stored refresh token (plaintext, as storage returns it) with the redeemed one, delete only on a match; on a failed re-read, behave as today | `pkg/authserver/refresher.go` |

The conditional delete is the only behaviour change for every deployment.
It goes through whatever storage the refresher was given, so encrypting
decorators still work. Read and delete are not atomic; the window is
accepted.

### Stage 2 · user-keyed store (standalone, behind a setting)

Setting `upstreamCredentialKeying: session | user` in the authorization
server's run configuration and CRD. Default `session`.

| Change | Where |
|---|---|
| New decorator `userKeyedUpstreamStorage`: writes key on `tokens.UserID`; reads and deletes key on the canonical user from context, `ErrNoCanonicalUser` when absent, never a session fallback | `pkg/authserver/storage` |
| Puts `user:{userID}` into the inner backend's `sessionID` slot | same |
| Owner check in every read: `row.UserID == user` before the expiry check and before refresh, else `ErrInvalidBinding` (request path: generic 500) | same |
| `DeleteUpstreamTokens` (all rows of a session) is a no-op: rows belong to the user | same |
| `ResolveUpstreamTokenRowID` returns the per-user row identity (refresh dedup and the PR #7031 lease work on the shared row) | same |
| Forwards `DCRCredentialStore`, `DeviceCodeStorage` and `DCRClientCounter` explicitly, like every decorator | same |
| One upstream account per (user, provider): a sign-in with a different upstream subject fails, keeps the row, offers reconnect; reconnect is an explicit `SingleLeg` flow that replaces the row on success; synthetic upstreams exempt | `handlers/callback.go` |
| In `user` mode, a failed refresh drops only that provider from `Identity.UpstreamTokens`, except the identity provider | `pkg/auth/token.go` |
| In `user` mode, the guard deletes only the rejected provider's row (conditionally) and refuses on `ErrNoCanonicalUser` | `refresh_grant_guard.go` |
| Chain walk: no code change; under the decorator `GetAllUpstreamTokens` returns the user's rows, so only missing or broken providers redirect (fixes #6831) | — |

Resulting Redis keys (backend code unchanged):

| | `session` | `user` |
|---|---|---|
| Row | `upstream:{tsid}:{provider}` | `upstream:user:{userID}:{provider}` |
| Index | `upstream:idx:{tsid}` | `upstream:idx:user:{userID}` |
| User reverse index | `user:upstream:{userID}` | unchanged |

**Before turning it on:** #6508 (bind upstream refresh tokens to the DCR
client generation) and the cross-replica refresh lease (#4122, PR #7031),
so a shared row never carries an obsolete refresh token and two replicas
never redeem the same rotating one.

**Upgrade:** upgrade every replica, then switch to `user`. Each user signs
in once; old `upstream:{tsid}:*` rows are not read and expire on their own
TTL. No in-place migration (it would need new storage methods, and would
have to pick one account when sessions disagree). One-way.

**Embedders that already key by user skip stage 2.**

### Stage 3 · read service (inert)

| Change | Where |
|---|---|
| `type UserCredentialReader interface { GetValidTokensForUser(ctx, userID, provider string) (*UpstreamCredential, error) }` | `pkg/auth/upstreamtoken` |
| `InProcessService` implements it: refuses empty `userID`, sets the canonical user, calls existing `GetUpstreamTokens(ctx, userID, provider)` (`userID` in the session slot, so per-session service state is per user), refreshes through the existing path | same |
| Missing row or `invalid_grant` on refresh → "needs authorization" with a configured connect link; storage or transient errors → infrastructure error | same, and `pkg/vmcp/auth/strategies` |
| Strategies take the reader as an option, injected where the vMCP is assembled; no reader → delegated and direct routes fail closed | `pkg/vmcp/auth/strategies`, `pkg/vmcp/cli/serve.go` |
| Startup refuses the new routes unless keying is `user` or a typed config field declares the embedder's storage user-keyed | config validation |

An embedder supplies its own `UserCredentialReader` when it wraps reads
with its own rules. Through its decorators, `GetUpstreamTokens` with the
user in context already reaches the right row.

### Stage 4 onwards · consumers

`loadUpstreamTokens` runs only for interactive tokens (`iss` = embedded
issuer **and** `tsid`). Delegated (marker) and direct tokens read one
routed provider through the stage 3 reader. Injection from
`Identity.UpstreamTokens` stays ungated, because embedders fill it
themselves.

## Issues

| Issue | Storage effect |
|---|---|
| #6176 owner check | becomes the decorator's owner check; no owner parameter on `tsid` reads (outside contributor; re-scope) |
| #6831 chain walk | stage 2; no stable session ID (outside contributor; re-scope) |
| #6407 delegated read | stage 3; no stable-ID phase |
| #6885 storage RFC | design home for stage 2 |
| #6508, #4122 / PR #7031 | prerequisites for turning on stage 2 |
| #4279 Redis HASH refactor | independent; no backend key change needed |

## Open

- Consent when a new client inherits a user's connections without an
  upstream prompt.
- How a user disconnects a provider, or is offboarded, now that logout
  deletes nothing.
