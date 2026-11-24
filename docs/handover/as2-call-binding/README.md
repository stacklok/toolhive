# Handover: binding a gateway access token to one MCP call

This folder hands over the per-call token binding work. It is not meant for
merge into `main`; it lives on the branch `donor/as2-call-binding-handover`.

| File | What it holds |
|---|---|
| `README.md` (this file) | What the work unlocks, what it builds on, the donor branches, the six target branches with a section each, open decisions, conventions |
| `CONTEXT.md` | The design, why it changed, the error table, the review findings |
| `DONOR-MAP.md` | Hunk-by-hunk map from the donor commits to the target branches |
| `VMCP.md` | The vMCP side: "vMCP: accept an access token for the one call its authorization_details names, once" (#7062) and "vMCP: one single-use record shared by all replicas for call-bound access tokens" (#7261) |

**The issue bodies are the spec.** "Authorization server: narrow a JWT-bearer
access token to one MCP call with authorization_details" (#7061) and "vMCP:
accept an access token for the one call its authorization_details names, once"
(#7062) were rewritten on 2026-11-24. Where these notes and an issue disagree,
the issue wins. Rules the notes carry that the issues do not state yet are
marked **"not yet in the issue — confirm"**.

All `file:line` references are on `origin/main` at "Resolve ID-JAG subjects and
issue marked tokens behind the gate" (#7262) (44f7d6368).

Short names used after the first mention: "the narrowing issue" (#7061),
"the vMCP issue" (#7062), "the shared-record issue" (#7261), "the client-auth
PR" for "Refuse unauthenticated ID-JAG clients and cnf-bound assertions"
(#7214), "the subject-resolution PR" for "Resolve ID-JAG subjects and issue
marked tokens behind the gate" (#7262), and "the Mecatl binding ticket" for
"Bind each gateway token to its call" (stacklok/mecatl#2727).

## What this unlocks

Today a client that redeems an ID-JAG at ToolHive's authorization server (AS-2)
gets an access token (V) that is good for any call to the vMCP until it
expires. Mecatl's harness sends tool calls that Mecatl's identity broker
approved, and it should hold a token that serves only the approved call, once.

The chain, in order:

1. "Authorization server: narrow a JWT-bearer access token to one MCP call with
   authorization_details" (#7061). AS-1 signs the call entry into the ID-JAG,
   and AS-2 copies it into V. **This is what the six branches below deliver.**
2. "vMCP: accept an access token for the one call its authorization_details
   names, once" (#7062). The vMCP enforces the entry and uses the token once.
   GitHub records it as blocked by the narrowing issue (#7061). See `VMCP.md`.
3. "Bind each gateway token to its call" (stacklok/mecatl#2727). Mecatl's
   broker sends the entry and checks it; it needs the narrowing issue (#7061) and the vMCP issue (#7062) released in
   a ToolHive version Mecatl pins (Mecatl pins v0.66.0 today).
4. "Test that a call-bound gateway token cannot be replayed on another replica"
   (stacklok/mecatl#2970), phase 3. It needs "vMCP: one single-use record
   shared by all replicas for call-bound access tokens" (#7261), which removes
   the first release's per-replica record.

Steps 1 to 3 sit under "Phase 2, Slice 4: Limited access — Each agent calls
only the tools its definition and its parent allow, one call per token"
(stacklok/mecatl#2851). If the ToolHive changes are not released in time, that
slice closes without the Mecatl binding ticket (stacklok/mecatl#2727), which moves to phase 3.

## What is already merged

| PR | What it gives this work |
|---|---|
| "Share the authorization_details reader" (#7217) | `readDetailsParam` and `detailsSubject` (`pkg/authserver/server/tokenexchange/authdetails.go:40`, `:53`) |
| "Pass redemption handler dependencies as a struct" (#7220) | `issuanceDeps` (`issuance_deps.go:19`), so no constructor churn |
| "Extract the shared JWT-bearer redemption tail" (#7249) | the `redeemer`: `complete` (`redeemer.go:75`), `populate` (`:112`), `assertionBoundedLifetime` (`:141`), `newRedemptionSession` (`:175`) |
| "Gate ID-JAG subject resolution behind a setting" (#7254), "Check the actor at ID-JAG redemption" (#7255), "Resolve ID-JAG subjects and issue marked tokens behind the gate" (#7262) | subject mapping; the `redemption` carries the resolved subject and the delegated-access marker; the five-minute marker cap (`redeemer.go:56`) |
| "Refuse unauthenticated ID-JAG clients and cnf-bound assertions" (#7214) | ID-JAG redemption always has an authenticated client; `cnf` refused for every issuer (`hasConfirmationClaim`, `jwt_bearer_handler.go:125`) |
| "Require the issuer as the only client assertion aud" (#7213) | client assertions must use the issuer as `aud` |

The redeemer has named fields only. It does **not** embed `issuanceDeps`,
because that would put the unfiltered trusted-issuer list one field away from
both handlers. Per-handler extras (the subject resolver, the lifespan map, the
details registry) come in as `FactoryOption`s (`factory.go:47`) and live on the
handler or the redeemer.

## The donor branches

The old design sent the call entry as a redemption parameter. Its code is on
three branches, pushed for reference only:

- `donor/as2-call-details-1-registered-details`: "Accept registered details on
  JWT-bearer redemption" (1b1f0498f), "Apply review fixes to redemption
  details" (3d6b3b21b)
- `donor/as2-call-details-2-mcp-tool-call-type`: 1b1f0498f, "Add the MCP
  tool-call details type" (3abb073f2), "Test tool-call details in the agent
  flow test" (c96ca8053)
- `donor/as2-call-details-3-client-lifespan`: the above, then "Add per-client
  access token lifespan" (962dec550)

They sit on "Release v0.67.0 (#7205)" (884ee1fbc), before four refactors.
**Do not rebase them.** Read them with `git show <sha> -- <path>` or
`git diff 884ee1fbc 3d6b3b21b -- <path>`, and take code hunk by hunk.
The `-2` and `-3` donors do not contain 3d6b3b21b. `DONOR-MAP.md` lists every
hunk and where it goes.

```sh
git fetch origin 'refs/heads/donor/*:refs/remotes/origin/donor/*'
```

## Known gaps in the issues

These are places where the issue text is stale or incomplete. Fix them in the
issues, or confirm with the issue author before relying on either reading.

1. **The narrowing issue's (#7061) "Today" line is stale.** It says "the server already parses the
   `authorization_details` entry, but keeps it out of the issued JWT". On
   `origin/main`, only token exchange reads the parameter
   (`readDetailsParam` callers: `authdetails.go:74`, `exchange_grant.go:100`).
   Neither redemption handler reads the parameter or the claim; an ID-JAG's
   claim is dropped silently. AS-1 refuses the parameter at minting
   (`checkIDJAGExtraParameters`, `id_jag_mint.go:220`).
2. **The narrowing issue (#7061) names `assertionBoundedLifetime` "in `jwt_bearer_handler.go`".** It
   moved to `redeemer.go:141` with "Extract the shared JWT-bearer redemption
   tail" (#7249).
3. **The shared-record issue (#7261) has no "blocked by" link to the vMCP issue (#7062)**, and its GitHub parent is "[Phase
   3] Run mecak8s with the identity broker on more than one replica"
   (stacklok/mecatl#2805), while its body says "Part of" the vMCP issue (#7062). Pick one parent.
4. **The vMCP issue (#7062) says "tokens without the entry behave exactly as today"** for every
   client. The Mecatl design (step (8), check 8) has the vMCP refuse a listed
   client's token that names no call. Decide whether the "as today" rule
   applies to unlisted clients only. See `VMCP.md`, section 4.
5. **Rules in these notes that the issues do not state yet** (each marked in
   `CONTEXT.md`):
   - a plain RFC 7523 assertion that carries `authorization_details` keeps being
     refused (`invalid_grant`, `invalid_assertion`);
   - a claim that is not a JSON array of objects is `invalid_grant`,
     `invalid_assertion` (the Mecatl design says the same);
   - when `mcp_tool_call` is registered for token exchange, its hook accepts
     the entry only when `requested_token_type` is the ID-JAG type;
   - a narrowed token cannot be exchanged (the terminal-token rule);
   - AS-2 is released before, or with, AS-1;
   - the first release lets a hook drop whole entries only, while the narrowing issue (#7061) allows
     narrowing for types other than `mcp_tool_call`;
   - the per-client lifetime is capped at 24h.

## Order and dependencies

| Branch | Base | Depends on | Size (non-test lines) |
|---|---|---|---|
| `as2-call-binding-1-registry-reachable` | `origin/main` | nothing | ~20 |
| `as2-call-binding-2-client-lifespan` | `origin/main` | nothing (the redeemer merged in "Extract the shared JWT-bearer redemption tail" (#7249)) | ~110 + generated |
| `as2-call-binding-3-terminal-details` | `origin/main` | nothing; must merge no later than `as2-call-binding-4-as2-claim` | ~110 |
| `as2-call-binding-4-as2-claim` | `origin/main` after its dependencies merge | `as2-call-binding-1-registry-reachable`, `as2-call-binding-3-terminal-details` | ~300 |
| `as2-call-binding-5-mcp-tool-call-type` | `origin/main` | nothing; merge after `as2-call-binding-4-as2-claim` | ~145 |
| `as2-call-binding-6-as1-signing` | `origin/main` after `as2-call-binding-5-mcp-tool-call-type` merges | `as2-call-binding-5-mcp-tool-call-type`; **released** no earlier than `as2-call-binding-4-as2-claim` | ~130 |

- **Can start now, in parallel:** `as2-call-binding-1-registry-reachable`,
  `as2-call-binding-2-client-lifespan`, `as2-call-binding-3-terminal-details`,
  `as2-call-binding-5-mcp-tool-call-type`.
- **Release rule** (not yet in the issue — confirm): AS-2 processing
  (`as2-call-binding-4-as2-claim`) ships in a release before, or together with,
  AS-1 signing (`as2-call-binding-6-as1-signing`). An older AS-2 ignores the
  claim, so V would come out unrestricted.
- Together the six branches close the narrowing issue (#7061). The vMCP issue (#7062) and the shared-record issue (#7261) are separate; see
  `VMCP.md`.

---

## `as2-call-binding-1-registry-reachable`

**Goal.** One rule, in one place, for when an `authorization_details`
registry is reachable. Behaviour-preserving now; widened by
`as2-call-binding-4-as2-claim`.

**Base and dependencies.** `origin/main`. Nothing.

**Today.** Three sites check "a registry needs token exchange":
`Config.validateAuthorizationDetailsRequiresTokenExchange`
(`pkg/authserver/config.go:2552`), `newEmbeddedAuthServerWithStorage`
(`pkg/authserver/runner/embeddedauthserver.go:364`) and
`validateAuthorizationDetailsRegistryForRunConfig`
(`embeddedauthserver.go:725`).

**Donor files.**

```sh
git show 3d6b3b21b -- pkg/authserver/config.go pkg/authserver/config_test.go
git show 3d6b3b21b -- pkg/authserver/runner/embeddedauthserver.go pkg/authserver/runner/embeddedauthserver_test.go
git diff 884ee1fbc 3d6b3b21b -- pkg/authserver/config.go   # both donor commits together
```

Take `ErrAuthorizationDetailsRegistryUnreachable`,
`AuthorizationDetailsRegistryReachable` and the call sites.

**Write fresh.** Drop the JWT-bearer half for now:
`AuthorizationDetailsRegistryReachable(tokenExchangeEnabled bool) bool`, and
keep today's error text "authorization details registry requires token
exchange to be enabled". `as2-call-binding-4-as2-claim` adds the
`trustedIssuers` parameter, `|| JWTBearerGrantEnabled(trustedIssuers)`
(`server_impl.go:682`), and the new text.

**Tests.** The reachability table from 3d6b3b21b
(`TestEmbeddedAuthServer_AuthorizationDetailsRegistryReachability`, the
`config_test.go` rows), token-exchange rows only.

**Size.** About 20 non-test lines in 2 files; about 60 test lines.

**Client-visible change.** None.

**Start prompt.**

> Implement the branch `as2-call-binding-1-registry-reachable` in ToolHive,
> from `origin/main`. Read `docs/handover/as2-call-binding/README.md` (its
> section for this branch) and `DONOR-MAP.md` on the branch
> `donor/as2-call-binding-handover`. Take the reachability function and error
> from donor commit 3d6b3b21b, keep the behaviour token-exchange-only, and use
> it at the three sites. Scoped tests and lint on `./pkg/authserver/...`.

---

## `as2-call-binding-2-client-lifespan`

**Goal.** A per-client access-token lifetime for ID-JAG redemption (#7061,
"Lifetime per client"), so `mecatl` tokens can live for seconds.

**Base and dependencies.** `origin/main`. Nothing (the redeemer merged in
"Extract the shared JWT-bearer redemption tail" (#7249)).

**Donor files.**

```sh
git show 962dec550 -- pkg/authserver/config.go pkg/authserver/config_test.go
git show 962dec550 -- pkg/authserver/runner/embeddedauthserver.go
git show 962dec550 -- pkg/authserver/server_impl.go          # take delegateAccessTokenLifespans only
git show 962dec550 -- pkg/authserver/server/tokenexchange/id_jag_handler_test.go   # port the test
git show 962dec550 -- docs/arch/17-token-exchange-delegation.md
```

Take `DelegateClientRunConfig.AccessTokenLifespan` (JSON/YAML
`access_token_lifespan,omitempty`, a Go duration string),
`AccessTokenLifespanDuration`, `checkDelegateAccessTokenLifespan`, the two
validate hooks, `DelegateClient.AccessTokenLifespan`, the
`resolveDelegateClients` parsing, `delegateAccessTokenLifespans`.

**Write fresh.**

- Add `options ...FactoryOption` to `IDJAGIssuanceFactory`
  (`id_jag_handler.go:537`), after `resolver`, and a new option such as
  `WithClientAccessTokenLifespans(map[string]time.Duration)`. If
  `as2-call-binding-4-as2-claim` lands first, it adds the parameter instead.
  Update the `FactoryOption` doc, which names only the token-exchange
  factories.
- Keep the map on the ID-JAG handler, not on `issuanceDeps`. Copy the client's
  value into a new `redemption.clientLifespan`.
- In `redeemer.assertionBoundedLifetime`: base = client lifespan if set, else
  server lifespan; then the minimum with the ID-JAG's remaining time; then the
  marker cap last. Plain JWT-bearer leaves it at zero.
- Regenerate swagger and the SDK (`task docs`); never cherry-pick generated
  files.

**Rules.** A set value must be greater than 0 (#7061) and at most 24h (not yet
in the issue — confirm). RunConfig only, no CRD field, so no migration; say so
in one PR line.

**Tests.** Config parse and bounds (four rows from 962dec550). Ported
`TestIDJAGHandler_ClientAccessTokenLifespan` on the main fixtures
(`redeemer_test.go`, `redemption_fixtures_test.go`): client lifespan shorter
than the assertion, longer than the assertion (silently shortened), longer
than the server lifespan, the plain grant ignores it, a client without a value
gets the server lifespan. Add a marker-cap case, as the narrowing issue's (#7061) acceptance asks.

**Size.** About 110 non-test lines in 6 files plus generated files; about 120
test lines.

**Client-visible change.** Operator-visible. Before: an ID-JAG redemption
token lives the shortest of the server lifespan, the ID-JAG's remaining time,
and (marked tokens) the marker cap. After: the client's lifespan, when set,
replaces the server lifespan in that list. Docs: a row in the client metadata
table and a "Lifetime of a redeemed token" section in
`docs/arch/17-token-exchange-delegation.md`, with the narrowing issue's (#7061) example table and the
advice to set AS-1's ID-JAG target lifetime to at least the client lifespan
plus the redemption delay.

**Gates.** Scoped tests and lint on `./pkg/authserver/...`; `task docs`;
`task docs-verify`; `task sdk-test`; `task sdk-lint` (`task test` does not
cover the nested `sdk/go` module).

**Start prompt.**

> Implement the branch `as2-call-binding-2-client-lifespan` in ToolHive, from
> `origin/main`. Read the README section for this branch and the 962dec550
> rows in `DONOR-MAP.md` under `docs/handover/as2-call-binding/` on the branch
> `donor/as2-call-binding-handover`, and the lifetime part of the narrowing issue (#7061). Take
> the config and validation from donor commit 962dec550; wire the map through
> a `FactoryOption` on `IDJAGIssuanceFactory` into
> `redeemer.assertionBoundedLifetime`. Regenerate docs and SDK.

---

## `as2-call-binding-3-terminal-details`

**Goal.** A token that carries `authorization_details` cannot be exchanged for
another token, so a narrowed V stays narrow (not yet in the issue — confirm).
Replace the single marker check with a set of facts.

**Base and dependencies.** `origin/main`. Nothing. Must merge no later than
`as2-call-binding-4-as2-claim`.

**Today.** `hasDelegatedAccessMarker` (`handler.go:324`) is checked at four
sites in `pkg/authserver/server/tokenexchange/`: the RFC 8693 subject token
(`handler.go:244`), the RFC 8693 actor token (`handler.go:499`), the subject
token that opens an exchange grant (`refresh_token_handler.go:464`), and the
actor token at ID-JAG minting (`refresh_token_id_jag.go:187`).

**Donor files.**

```sh
git show 3d6b3b21b -- pkg/authserver/server/tokenexchange/restriction.go
git diff 884ee1fbc 3d6b3b21b -- pkg/authserver/server/tokenexchange/handler.go \
  pkg/authserver/server/tokenexchange/refresh_token_handler.go \
  pkg/authserver/server/tokenexchange/refresh_token_id_jag.go \
  pkg/authserver/server/tokenexchange/export_test.go \
  pkg/authserver/server/tokenexchange/delegated_access_marker_test.go \
  pkg/authserver/server/tokenexchange/delegated_access_marker_actor_test.go
git show 3d6b3b21b:pkg/authserver/server/tokenexchange/redemption_details.go   # refuseSignedDetails
```

Keep the hints, reasons and WARN text.

**Write fresh.**

- `restriction.go`:
  `type restrictions struct{ DelegatedAccess, AuthorizationDetails, Confirmation bool }`
  and a nil-safe `restrictionsOf(*ValidatedClaims)` that checks presence in
  `Claims` and `Extra`. Delete `hasDelegatedAccessMarker`. `Confirmation`
  folds in `hasConfirmationClaim`; its one call site keeps its own refusal.
- Each site picks its own precedence. At the four RFC 8693 sites, marker first
  (unchanged wire text), then details:
  - subject: `invalid_request`, hint "The subject token carries
    authorization_details and cannot be exchanged."; WARN "Subject token
    refused", `reason=authorization_details_token`;
  - actor: `invalid_request`, hint "The actor token carries
    authorization_details and cannot be exchanged.";
  - opening an exchange grant: `invalid_request`,
    `subject_token_authorization_details`;
  - actor at ID-JAG minting, self-signed only:
    `invalid_request`, `actor_token_authorization_details`.
- A fifth site at redemption: refuse an assertion that carries the claim on
  both redemption forms (`refuseSignedDetails`), so the restriction is never
  dropped silently. `as2-call-binding-4-as2-claim` narrows this to plain
  RFC 7523 only.

**Tests.** A per-site table: details only, marker only, both (the donor's
precedence regression came from the "both" case); an external issuer on the
RFC 8693 subject path; nil `Extra`. `TestHasDelegatedAccessMarker` becomes
`TestRestrictionsOf`.

**Size.** About 110 non-test lines in 5 files plus docs.

**Client-visible change.** Before: a subject or actor token carrying
`authorization_details` was exchanged and the details dropped; a redemption
assertion's claim was dropped. After: refused. This includes **external**
subject tokens on RFC 8693 exchange. Say in the PR that this is what stops a
narrowed V from being exchanged at an AS-1 that trusts AS-2. Docs: turn the
"A marked token is terminal" table in the "Delegated-access marker" section of
`docs/arch/17-token-exchange-delegation.md` into a terminal-token table.

**Start prompt.**

> Implement the branch `as2-call-binding-3-terminal-details` in ToolHive, from
> `origin/main`. Read the README section for this branch and `CONTEXT.md`
> section 4 under `docs/handover/as2-call-binding/` on the branch
> `donor/as2-call-binding-handover`. Replace `hasDelegatedAccessMarker` with a
> `restrictions` set; refuse tokens carrying `authorization_details` at the
> four exchange sites and on redemption. Donor code is in commit 3d6b3b21b,
> but it uses a single-value classifier; do not copy that shape.

---

## `as2-call-binding-4-as2-claim`

**Goal.** AS-2 processes the ID-JAG's signed `authorization_details` claim
(ID-JAG §4.4.1, RFC 9396), copies the granted entries into V and the token
response, and refuses the redemption parameter. This is the core of the narrowing issue (#7061).

**Base and dependencies.** `origin/main` after
`as2-call-binding-1-registry-reachable` and
`as2-call-binding-3-terminal-details` merge.

**Donor files.**

```sh
git diff 884ee1fbc 3d6b3b21b -- pkg/authserver/server/tokenexchange/redemption_details.go \
  pkg/authserver/server/tokenexchange/redemption_details_test.go \
  pkg/authserver/authorizationdetails/ \
  pkg/authserver/authorization_details_integration_test.go
git show 3d6b3b21b -- pkg/authserver/server/tokenexchange/handler_test.go   # newDetailsRegistry
git show 3d6b3b21b -- docs/arch/17-token-exchange-delegation.md docs/authz.md docs/arch/README.md
```

Take `Pending.Empty`, `detailsRefusal`, `setRedemptionDetailsClaim` and
`echoRedemptionDetailsClaim` (moved onto the redeemer), the test fixtures
(`newCallDetailsRegistry`, `bigEntry` with an integer above 2^53, the failing
hook), and the HTTP signed-JWT test.

**Write fresh.** See `CONTEXT.md`, section 2, for the full rules and error
table.

1. **Raw bytes.** `verifySignature` (`validator.go:312`) also decodes the
   payload into `map[string]json.RawMessage` and returns the
   `authorization_details` bytes; `ValidatedClaims` gains a raw field (nil when
   the key is absent).
2. **Plain RFC 7523** keeps refusing the claim: `invalid_grant`,
   `invalid_assertion` (not yet in the issue — confirm).
3. **ID-JAG**, in `redeemer.complete` before consume, when the claim is
   present: not a JSON array of objects → `invalid_grant`, `invalid_assertion`
   (not yet in the issue — confirm); no registry or type not registered for
   JWT-bearer → `invalid_authorization_details`, `unknown_type`; shape problem
   → `malformed_entry`; `[]` → `empty_grant`; then `Pending.Grant` (hook
   reasons pass through, for example `single_entry_required`); then the output
   check (each granted entry equals a signed entry, compared with
   `UseNumber`; otherwise `server_error`).
4. **The parameter.** Present (anything but absent or a single `""`) →
   `invalid_authorization_details`, `parameter_not_allowed`, on both
   handlers, before consume.
5. **Hook request.** `ClientID` = the authenticated client; `GrantedAudience` =
   `[resource]`; `Subject` from `detailsSubject` with `External: true` and the
   `authorization_details` key removed from `Subject.Claims`.
6. **Wiring.** `JWTBearerIssuanceFactory` (`jwt_bearer_handler.go:438`) and
   `IDJAGIssuanceFactory` (`id_jag_handler.go:537`) take
   `options ...FactoryOption`; pass
   `tokenexchange.WithAuthorizationDetailsRegistry`
   (`factory.go:56`) from `buildJWTBearerFactoriesForConfig`
   (`server_impl.go:994`), which has `cfg`. Widen the reachability rule.
7. **Log.** One WARN "authorization_details rejected" per refusal with
   `client_id`, a fixed `reason`, and for hook refusals the hook's code and
   reason. Never the claim value.

`Registry.Parse` (`authorizationdetails.go:141`) returns one error for both
"unknown type" and "bad shape"; the `unknown_type` / `malformed_entry` split
needs new code.

**Tests.** One unit test per error-table row on both handlers, with the
consumer never called on a refusal; output-check tests with a misbehaving hook
(changed entry, extra entry, `1` vs `1.0`); a repeated-member test (go-jose
already refuses it); the HTTP test with a test-signed ID-JAG carrying an entry
(response member and V's claim equal the entry, integer precision kept);
`TestIntegration_AuthorizationDetailsIgnoredOnOtherGrants` flips to the
parameter refusal; the Redis round-trip shape; the JWT-bearer reachability
rows.

**Size.** About 300 non-test lines over 9-10 production files. At the limit:
if it grows, split the raw-bytes step (about 40 lines, behaviour-preserving)
into its own first PR.

**Client-visible change.** Before → after:

- Redemption with an `authorization_details` parameter: ignored → 400
  `invalid_authorization_details`, `parameter_not_allowed`.
- ID-JAG carrying the claim: claim ignored, V unrestricted → claim processed;
  granted entries in V and in the response; refused per the table.
- ID-JAG carrying the claim at a server without a registry: accepted
  unrestricted → 400 `invalid_authorization_details`, `unknown_type`.
- Plain RFC 7523 assertion carrying the claim: claim ignored → 400
  `invalid_grant`, `invalid_assertion` (already refused if
  `as2-call-binding-3-terminal-details` landed first).
- A registry with token exchange off but the JWT-bearer grant on: startup
  error → accepted.
- State in the PR and at the site: ID-JAG §4.4.1 lets AS-2 "modify, filter, or
  omit"; this server only drops whole signed entries (no narrowing inside an
  entry, no enrichment). Per-type narrowing is "Narrow authorization_details
  on token exchange and refresh" (#6912).

Docs: rewrite "Authorization details" and add rows to "Redemption refusal
reasons" and "Bound ID-JAG assertions" in
`docs/arch/17-token-exchange-delegation.md`; update `docs/arch/README.md` and
`docs/authz.md`.

**Start prompt.**

> Implement the branch `as2-call-binding-4-as2-claim` in ToolHive, from
> `origin/main` (after `as2-call-binding-1-registry-reachable` and
> `as2-call-binding-3-terminal-details` merged). Read the narrowing issue (#7061), then the
> README section for this branch and `CONTEXT.md` section 2 under
> `docs/handover/as2-call-binding/` on the branch
> `donor/as2-call-binding-handover`. AS-2 processes the ID-JAG's signed
> `authorization_details` claim from raw bytes, runs the hooks, checks the
> granted entries against the signed ones, copies them into the token and the
> response, and refuses the redemption parameter with `parameter_not_allowed`.
> Donor fixtures are in commit 3d6b3b21b; its parameter design is obsolete.

---

## `as2-call-binding-5-mcp-tool-call-type`

**Goal.** The `https://toolhive.dev/mcp_tool_call` entry type and its hook, as
The narrowing issue (#7061) defines it.

**Base and dependencies.** `origin/main`. Nothing to build; merge after
`as2-call-binding-4-as2-claim`.

**Donor files.**

```sh
git show 3abb073f2 -- pkg/mcp/toolcalldetails/toolcalldetails.go
git show 3abb073f2 -- pkg/mcp/toolcalldetails/toolcalldetails_test.go
```

It applies cleanly. New package `pkg/mcp/toolcalldetails`, depending only on
`authorizationdetails` and `oauthproto`.

**Write fresh.**

- `Register` lists **both** `oauthproto.GrantTypeTokenExchange` (AS-1 minting)
  and `oauthproto.GrantTypeJWTBearer` (AS-2 redemption). The donor lists only
  JWT-bearer.
- The entry, per the narrowing issue (#7061): a tool call (`server`, `tool`, `arguments_sha256` as
  64 lowercase hex characters, `operation_id`, all non-empty strings) or a list
  call (`server`, `method` in `tools/list`, `prompts/list`, `resources/list`,
  `resources/templates/list`). No other members; never mixed. Exactly one
  entry. Reasons: `malformed_entry`, `single_entry_required`,
  `invalid_arguments_hash`, `invalid_method`, all with
  `invalid_authorization_details`. Grants the entry unchanged.
- On token exchange, refuse unless `req.RequestedTokenType ==
  oauthproto.TokenTypeIDJAG` (not yet in the issue — confirm; reason name
  `id_jag_required` is a proposal). Without it, plain RFC 8693 exchange would
  accept and echo the entry while the issued token carries nothing.
- Fix the package doc: AS-1 accepts the entry on the ID-JAG request and signs
  it into the ID-JAG; AS-2 accepts it only in the signed claim; a plain
  JWT-bearer assertion carrying the claim and the redemption parameter are
  both refused. Name RFC 8785 and the `{}` rule for absent arguments.
- Nothing registers the type by default; an embedder passes it with
  `WithAuthorizationDetailsRegistry`.

**Tests.** The donor table, plus the grant-type list, the
`requested_token_type` rule, and acceptance on JWT-bearer with an empty
`RequestedTokenType`.

**Size.** About 145 non-test lines in 1 file; about 120 test lines.

**Client-visible change.** None by default (an opt-in type). Docs: a short
paragraph in "Authorization details" in
`docs/arch/17-token-exchange-delegation.md` naming it as a type an embedder
can register.

**Start prompt.**

> Implement the branch `as2-call-binding-5-mcp-tool-call-type` in ToolHive,
> from `origin/main`. Read the entry definition in the narrowing issue (#7061) and the README
> section for this branch under `docs/handover/as2-call-binding/` on the
> branch `donor/as2-call-binding-handover`. Start from donor commit 3abb073f2
> (`pkg/mcp/toolcalldetails`); register the type for both token exchange and
> JWT-bearer, and add the `requested_token_type` check on token exchange.

---

## `as2-call-binding-6-as1-signing`

**Goal.** AS-1 accepts the entry on the ID-JAG request, checks it with the
type's hook, signs the granted entries into the ID-JAG's
`authorization_details` claim, and echoes them (#7061, "AS-1 signing").

**Base and dependencies.** `origin/main` after
`as2-call-binding-5-mcp-tool-call-type` merges (for the flow test). Released
no earlier than `as2-call-binding-4-as2-claim` (not yet in the issue —
confirm).

**Donor files.**

```sh
git show c96ca8053 -- pkg/authserver/delegated_agent_flow_integration_test.go
```

Only the flow-test row and the reworded row 6.1 comment ("AS-2 here is a
namespaced server: it has no local user for AS-1's subject, so sub is the
issuer-qualified AS-1 subject."). Everything else is fresh.

**Write fresh.**

- The embedder registry reaches `RefreshTokenHandler` through a
  `RefreshTokenOption`, set in `buildTokenExchangeFactories`
  (`server_impl.go:943`) from `cfg.AuthorizationDetailsRegistry`. The private
  exchange-grant registry stays.
- `checkIDJAGExtraParameters` (`id_jag_mint.go:220`) stops refusing the
  parameter.
- In `decideIDJAG` (`refresh_token_id_jag.go:56`): read and parse right after
  `checkIDJAGRequest` with `readDetailsParam` (absent or `""` → no claim;
  repeated, including `["",""]` → `invalid_request`, the shared "must not be
  repeated" text); no registry or type not registered →
  `invalid_authorization_details`, `unknown_type`; `[]` → `empty_grant`. Run
  `Grant` after `presentedGrant` and `selectSubjectClaims`, before the actor
  validator, with `Request{ClientID, Actor: ClientID, RequestedTokenType:
  TokenTypeIDJAG, GrantedAudience: [target issuer], Subject: {grant subject and
  client}}`. `decide` runs in both handler phases, so the hook runs twice;
  that is the documented rule.
- `idJAGMint` (`id_jag_mint.go:109`) gains the granted JSON; `mintIDJAG`
  (`:376`) sets `claims["authorization_details"]` when present (the name is
  already reserved).
- The response echoes the granted JSON as `authorization_details` whenever the
  claim is set.

**Tests.** Unit tests (`id_jag_mint_test.go`, `refresh_token_id_jag_test.go`):
the parameter cases, the claim in the signed ID-JAG, the response member, the
hook sees `RequestedTokenType` ID-JAG and the grant's subject, no claim
without the parameter, no actor-validator call on a details refusal. Flow test
(`delegated_agent_flow_integration_test.go`): register the type on **both**
AS-1 and AS-2; send the entry on the ID-JAG request (`requestIDJAGWith`);
redeem with no parameter; the ID-JAG claim, the response member and V's claim
are equal; a second entry is refused **at AS-1** with `single_entry_required`;
a redemption that still sends the parameter gets `parameter_not_allowed`;
`expires_in` stays within the ID-JAG's remaining time.
`two_server_id_jag_integration_test.go`: a full AS-1 → AS-2 round trip.

**Size.** About 130 non-test lines over 5 files; about 220 test lines.

**Client-visible change.** Before → after:

- ID-JAG request with `authorization_details`: always 400
  `invalid_authorization_details` ("not supported when requesting an ID-JAG")
  → accepted for registered types and signed into the ID-JAG; refused per the
  rules above otherwise.
- `authorization_details=&authorization_details=` (two empty values) on an
  ID-JAG request: treated as absent → 400 `invalid_request` "must not be
  repeated", as on plain token exchange.

Docs: "Checks and errors", "Request and response", "Claims and subject" and
"Redemption and known gaps" in the ID-JAG part of
`docs/arch/17-token-exchange-delegation.md` (step 2 there says
`authorization_details` is refused).

**Start prompt.**

> Implement the branch `as2-call-binding-6-as1-signing` in ToolHive, from
> `origin/main` (after `as2-call-binding-5-mcp-tool-call-type` merged). Read
> the "AS-1 signing" part of the narrowing issue (#7061), then the README section for this
> branch and `CONTEXT.md` section 3 under `docs/handover/as2-call-binding/` on
> the branch `donor/as2-call-binding-handover`. AS-1 accepts the
> `authorization_details` entry on the ID-JAG request, runs the type's hook,
> signs the granted entries into the ID-JAG and echoes them. Rework the
> flow-test row from donor commit c96ca8053.

---

## The vMCP side

The vMCP issue (#7062) and the shared-record issue (#7261) are not part of the six branches. No code exists for them.
`VMCP.md` covers what each issue asks, where the check goes (a new HTTP
middleware in `pkg/vmcp/server`, before classification, run whether or not
Cedar is configured), the argument hash and the shared vectors with Mecatl,
the per-replica record and how the shared-record issue (#7261) replaces it, risks, and five suggested
branches. The vMCP issue (#7062) can be built now in parallel: its tests can mint tokens by
hand. Its one coupling to the branches above is the entry type in
`pkg/mcp/toolcalldetails`.

## Open decisions

1. **Which `sub` the details hook sees on a resolved redemption.** The proposal
   is the assertion's subject only. With subject mapping, V's `sub` is the
   local user, while the hook's `Subject` comes from the ID-JAG.
2. **`access_denied` for a hook's policy refusal.** `authorizationdetails.Error`
   allows only `invalid_authorization_details` and `invalid_request`
   (`pkg/authserver/authorizationdetails/errors.go:56`). No registered type
   refuses on policy. Add a code when a type needs one.
3. **External RFC 9396 tokens on RFC 8693 exchange.** After
   `as2-call-binding-3-terminal-details`, a trusted IdP's access token that
   carries `authorization_details` is refused as a subject token instead of
   exchanged with its details dropped. Recommendation: keep it (fail closed)
   and state it in the PR.
4. **An ID-JAG with the claim at an AS-2 without a registry is refused after
   `as2-call-binding-4-as2-claim`.** If an IdP such as Okta puts
   `authorization_details` into its ID-JAGs, those redemptions start failing.
   Not checked for Okta or xaa.dev.
5. **The reason name for the `requested_token_type` check** (`id_jag_required`
   is a proposal).
6. **The rules marked "not yet in the issue — confirm"**, and the issue gaps
   above.
7. **Discovery.** Whether to advertise `authorization_details_types_supported`:
   "Advertise authorization_details types in metadata and DCR" (#6915).
8. **vMCP questions** (expiry exception in `pkg/auth`, listed clients without an
   entry, GET and DELETE, list filtering on Legacy): `VMCP.md`, section 8.

## Conventions

- **Naming.** In PRs, commits and issues, name an issue or PR by its title with
  the number in parentheses, a commit by its subject with the hash in
  parentheses, and a branch by its name.
- **Scoped tests while working.** The Taskfile's `test` task runs the whole
  repository. While working, run only the packages you touched, with the
  Taskfile's flags:

  ```sh
  go test -ldflags=-extldflags=-Wl,-w -race ./pkg/authserver/...
  golangci-lint run --allow-parallel-runners --fix ./pkg/authserver/...
  ```

  Add `./pkg/authserver/runner/...` or `./pkg/mcp/toolcalldetails/...` when
  those change. Before opening the PR, run `task lint-fix` and `task test`
  once on the final tree. For generated changes: `task docs`,
  `task docs-verify`, `task sdk-test`, `task sdk-lint`.
- **Commits.** Imperative mood, capitalized subject, no trailing period, at
  most 50 characters, no Conventional Commits prefixes (`feat:`, `fix:`). The
  body explains what and why. Stage files by name, never `git add -A`.
- **PRs.** Follow `.github/pull_request_template.md`. One logical change per
  PR; about 400 changed lines and 10 files as guidance, not counting tests,
  docs and generated files. Put every client- or operator-visible change in
  the PR body as before → after, and update the matching page under `docs/` in
  the same PR. State a deliberate deviation from a sibling code path in the PR
  body or at the site.
