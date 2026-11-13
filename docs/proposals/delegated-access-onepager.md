# Delegated credential access

Draft, revision 2 · ToolHive authorization server and vMCP · 2026-11-13

Agents acting for a user need that user's stored provider connections
(their GitHub or Slack tokens) at the gateway, without a browser session.
Today only interactive sign-ins get them. This proposal adds two more ways
in, through the same subject mapping, actor checks and credential store.

Full proposal:
[delegated-access-resolver-actor-policy-user-store.md](delegated-access-resolver-actor-policy-user-store.md)

## Three ways a token reaches a user's credentials

| Path | Flow | Today |
|---|---|---|
| Interactive | user → gateway AS → vMCP | Works. Unchanged. |
| Delegated | agent platform AS-1 → ID-JAG → gateway AS-2 → short gateway token → vMCP | AS-2 redeems an identity assertion and issues a gateway token for the native user. Scheduled runs use this path too. |
| Direct | customer IdP (Okta) → token → vMCP | The agent presents its Okta token straight to the gateway. Nothing in ToolHive mints a token in between. |

All three end the same way: the subject is the native user, the actor is
checked, and the credential is read by (gateway, user, routed provider)
with an owner check. None can reach credentials through another's path.

## Three decisions

**Subject: one subject resolver.** `(issuer, binding claim, value)` →
native user. It never creates a user and fails closed. The binding claim
is set per issuer: Okta `uid`, Entra `oid`, AS-1 `upstream_subject`.
Never the email. A directory lookup is one implementation.

**Actor: separate checks.**

- *Proof* at ID-JAG issuance, through the existing validator hook.
- *Client checks*: `client_id` binding at redemption; a required client
  allowlist (Okta `cid`) on the direct path, reusing token exchange's
  existing one.
- *Policy* on the outermost `act`: one interface for redemption, exchange
  and direct. `act.iss` must match the assertion's issuer.

**Storage: one user-keyed store.** Rows are keyed by (gateway, user,
provider), as a storage decorator behind a setting. No new storage
methods, so existing decorators keep working. An injected reader fetches
only the routed provider and answers "needs authorization" when it is
missing.

## Who may read a user's credentials

Each route needs positive proof. A token is never admitted because a
claim is missing.

| Route | Admitted when | User | Read |
|---|---|---|---|
| interactive | issued by the gateway's AS and carries `tsid` | `sub` | all providers, as today |
| delegated | issued by the gateway's AS and carries the delegated-access marker | `sub`, resolved at mint | routed provider only |
| direct | configured issuer in the same IdP tenant as sign-in; exact `aud`; client allowlist; actor required; scopes; lifetime cap; DPoP when bound; no `tsid` or marker | resolved per request | routed provider only |

The marker is issued only by the gateway's own AS after resolution,
client and actor checks pass, and in external mode only with the user's
approval. Never at login, and never because `may_act` or a wildcard
matched. A marked token cannot be exchanged again and gets no refresh
token.

## What changes where

**ToolHive**

- New `pkg/auth/subject` and `pkg/auth/actor` interfaces.
- Redemption and exchange: per-issuer subject mode and binding claim,
  `act.iss` check, marker.
- Injected reader `GetValidTokensForUser`, admission route on the
  identity, Cedar reads request claims for non-interactive routes.
- Direct-issuer route at the vMCP (or in-process translation, see D5).
- Optional user-keying decorator for standalone installs.

**Embedders**

- Gateway with a directory: nothing required to keep working. To adopt
  the new routes, it implements the resolver and actor policy against its
  directory and supplies its own reader.
- Agent platform (AS-1): wires its actor validator, produces one actor ID
  per agent, and agrees the binding-claim name with the gateway.

## Compatibility

- **Embedders that already key rows by user:** no change on upgrade. No
  storage interface changes, the interactive validator path and its bulk
  load stay, `tsid` and `no_upstream_session` stay in tokens, and every
  new behaviour is off until configured per issuer.
- **Standalone ToolHive:** user keying behind a setting, default
  unchanged. On switching, every replica upgrades first, then each user
  signs in once. One-way.
- **Everyone:** one behaviour change. After a failed refresh-token
  rotation, the refresher deletes the row only if it still holds the
  token it redeemed.

## Stages

| Stage | Contents | Needs |
|---|---|---|
| 1 · groundwork | Canonical user set on every storage caller; conditional delete in the refresher; resource-server validator fixes | — |
| 2 · store | User-keying decorator, owner check, one account per provider with explicit reconnect. Standalone only; embedders that key by user skip it. | DCR generation binding, refresh lease |
| 3 · read | Injected reader, routed-provider read, "needs authorization". Inert. | — |
| 4 · routes | Admission route on the identity, Cedar change, subject resolver and actor policy interfaces | — |
| 5 · marker | Marker defined and refused on re-exchange; issued nowhere yet | — |
| 6 · direct | Direct-issuer route, client allowlist, actor policy, per-request resolution, session binding | 3–5, D5 |
| 7 · delegated | Resolve at redemption, `act.iss` check, approval or admin pre-approval, marker issuance; ID-JAG issuance with the actor validator | 3–5 |

Stages 6 and 7 run in parallel. The read gates always land before
redemption mints native subjects.

## Effect on filed issues

- **Keep:** marker (#6404), subject mapping and approval (#6887, plus the
  direct route), ID-JAG issuance and actor validation (#6898, #6899),
  session binding (#6888, #6779), storage RFC (#6885), DCR generation
  binding (#6508), refresh lease (#4122, PR #7031).
- **Rewrite:**
  - Delegated access epic (#6406): allow the direct route.
  - Credential scope opt-in (#6403): becomes per-issuer configuration.
  - Ownership-checked read (#6407): no stable-ID phase.
  - Owner check (#6176) and chain walk (#6831): move into the decorator,
    with no stable session ID. Both are assigned to an outside
    contributor; re-scope with them.

## Open decisions

| | |
|---|---|
| D3 | Direct-issuer preconditions: dedicated Okta authorization server, same tenant as sign-in, required scopes, DPoP when bound (recommended: all) |
| D4 | Resolve at mint time with short gateway tokens (recommended), or per request |
| D5 | Direct-route checks in the vMCP, by in-process translation at the AS, or in a shared validation library (open) |
| also | Consent when a new client inherits a user's connections; how a user disconnects or is offboarded under user keying |

## Accepted costs

A direct token works until it expires and cannot be revoked by the
gateway. The direct route costs one resolver call per request.
