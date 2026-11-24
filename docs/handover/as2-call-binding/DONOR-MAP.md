# Donor map: the old as2-call-details branches onto the as2-call-binding branches

This file maps the code on the donor branches onto the six planned
`as2-call-binding-*` branches. It was checked against `origin/main` at
"Resolve ID-JAG subjects and issue marked tokens behind the gate" (#7262)
(44f7d6368).

The donor code implements the **old** design (the call entry as a redemption
parameter). The reason names and rules in "Authorization server: narrow a
JWT-bearer access token to one MCP call with authorization_details" (#7061)
win over anything in the donor commits.

Short names used after the first mention: "the narrowing issue" (#7061),
"the vMCP issue" (#7062), "the shared-record issue" (#7261), "the client-auth
PR" for "Refuse unauthenticated ID-JAG clients and cnf-bound assertions"
(#7214), "the subject-resolution PR" for "Resolve ID-JAG subjects and issue
marked tokens behind the gate" (#7262), and "the Mecatl binding ticket" for
"Bind each gateway token to its call" (stacklok/mecatl#2727).

## How to read the donors

The donor branches are pushed for reference only:

| Pushed as | Commits on top of "Release v0.67.0 (#7205)" (884ee1fbc) |
| --- | --- |
| `donor/as2-call-details-1-registered-details` | "Accept registered details on JWT-bearer redemption" (1b1f0498f), "Apply review fixes to redemption details" (3d6b3b21b) |
| `donor/as2-call-details-2-mcp-tool-call-type` | 1b1f0498f, "Add the MCP tool-call details type" (3abb073f2), "Test tool-call details in the agent flow test" (c96ca8053) |
| `donor/as2-call-details-3-client-lifespan` | the three commits above, then "Add per-client access token lifespan" (962dec550) |

```sh
git fetch origin 'refs/heads/donor/*:refs/remotes/origin/donor/*'
git show 3abb073f2 -- pkg/mcp/toolcalldetails/toolcalldetails.go      # one commit, one file
git diff 884ee1fbc 3d6b3b21b -- pkg/authserver/config.go               # 1b1f0498f + 3d6b3b21b as one diff
git show 3d6b3b21b:pkg/authserver/server/tokenexchange/restriction.go  # the file as it was at that commit
```

- **Do not rebase or cherry-pick the donor branches whole.** They sit on an old
  base, before four refactors. Take code file by file and hunk by hunk.
- The `-2` and `-3` donor branches are stacked on 1b1f0498f and do **not**
  contain the review fixes in 3d6b3b21b. For anything 3d6b3b21b changed, take
  the code from 3d6b3b21b.
- Build status of the donors was not checked. The commit message of 3d6b3b21b
  lists the tests it added; nothing here proves they pass.
- A fourth local branch, for a per-client "require authorization_details"
  flag, was dropped with that design and is not pushed (see CONTEXT.md,
  section 1, "Decided").

## What main already replaces

| PR | What it replaces in the donor code |
| --- | --- |
| "Refuse unauthenticated ID-JAG clients and cnf-bound assertions" (#7214) | ID-JAG redemption now always has an authenticated client. The donor text "a public client is only identified by `client_id`" is out of date. |
| "Share the authorization_details reader" (#7217) | `readDetailsParam` and `errRepeatedDetailsParam` (`authdetails.go`) replace the donor's own reading of `form["authorization_details"]`. `detailsSubject` replaces the Subject block in the donor's `redemptionDetailsRequest`. Note: `detailsSubject` sets `External` from `ExternalIssuer`; the donor always sets `External: true`. |
| "Pass redemption handler dependencies as a struct" (#7220) | `issuanceDeps` replaces the positional constructors. The donor adds a `detailsRegistry` parameter to both redemption constructors and an extra `nil` at about 15 test call sites. Drop all of these. |
| "Extract the shared JWT-bearer redemption tail" (#7249) | `redeemer.complete`, `redeemer.populate`, the `redemption` struct and `newRedemptionSession` (`redeemer.go`) replace the donor's edits inside both `HandleTokenEndpointRequest` bodies (`copyActorClaim`, the `replayPurpose` helper, the synthetic-client comment, the session building). |
| "Gate ID-JAG subject resolution behind a setting" (#7254), "Check the actor at ID-JAG redemption" (#7255), "Resolve ID-JAG subjects and issue marked tokens behind the gate" (#7262) | They add `idJAGRedemption`, the `delegatedAccess` field, `delegatedAccessMarkerCap` (5 minutes) in `assertionBoundedLifetime`, and a `resolver` argument on `IDJAGIssuanceFactory`. That factory has no `FactoryOption` parameter yet. |

None of the donor's own work is on main. Main has no `restriction.go`, no
`redemption_details.go`, no `Pending.Empty`, no `pkg/mcp/toolcalldetails`, no
per-client lifespan and no registry-reachability rule. Main does not refuse an
`authorization_details` claim in a redemption assertion; it drops it silently,
because redemption copies only `act`.

## Trial merges

Done with `git merge-tree` (nothing written to a branch):

- The squash of 884ee1fbc..3d6b3b21b onto main merges without conflicts in
  `config.go`, `config_test.go`, the runner, `authorizationdetails`,
  `handler.go`, `refresh_token*.go`, `restriction.go`,
  `redemption_details*.go`, the docs and the marker tests.
- It conflicts in `id_jag_handler.go`, `jwt_bearer_handler.go`,
  `server_impl.go`, `integration_test.go`, and five redemption test files. The
  test files conflict only on constructor call sites.
- 3abb073f2 and c96ca8053 apply cleanly.
- 962dec550 conflicts in `id_jag_handler.go`, `jwt_bearer_handler.go`,
  `server_impl.go`, `id_jag_handler_test.go` and
  `docs/arch/17-token-exchange-delegation.md`.

Classes: **(a)** already on main, **(b)** reuse as is, **(c)** reuse with
rework, **(d)** drop.

## 1b1f0498f + 3d6b3b21b (read as one diff)

Command: `git diff 884ee1fbc 3d6b3b21b -- <path>`. Paths under
`pkg/authserver/server/tokenexchange/` are given by file name only.

| File / hunk | Class | Target branch | Notes |
| --- | --- | --- | --- |
| `pkg/authserver/config.go`: `ErrAuthorizationDetailsRegistryUnreachable`, `AuthorizationDetailsRegistryReachable`, `validateAuthorizationDetailsRegistryReachable` (replaces `validateAuthorizationDetailsRequiresTokenExchange`) | b/c | `as2-call-binding-1-registry-reachable` | Land the token-exchange-only form first; `as2-call-binding-4-as2-claim` adds the JWT-bearer half (`JWTBearerGrantEnabled`, `server_impl.go:682`). |
| `pkg/authserver/config.go`: comment on `Config.AuthorizationDetailsRegistry` | c | `as2-call-binding-4-as2-claim` | True only once redemption reads the registry. |
| `pkg/authserver/runner/embeddedauthserver.go`: both reachability sites (`newEmbeddedAuthServerWithStorage`, `validateAuthorizationDetailsRegistryForRunConfig`) | b | `as2-call-binding-1-registry-reachable` | The runner file has not changed on main. |
| `runner/embeddedauthserver.go`: `WithAuthorizationDetailsRegistry` comment | c | `as2-call-binding-4-as2-claim` | |
| `config_test.go`: the "only the JWT-bearer grant passes" row and the new error text | c | `as2-call-binding-1-registry-reachable` (token-exchange rows), `as2-call-binding-4-as2-claim` (JWT-bearer rows) | |
| `runner/embeddedauthserver_test.go`: `TestEmbeddedAuthServer_AuthorizationDetailsRegistryReachability` | c | same split as above | |
| `authorizationdetails/authorizationdetails.go`: `Pending.Empty` + `TestPending_Empty` | b | `as2-call-binding-4-as2-claim` | Use it for `empty_grant` on a signed `[]`. |
| `authorizationdetails.go`: comments on `Request` fields per grant | c | `as2-call-binding-4-as2-claim` | Rewrite `ClientID`: ID-JAG redemption always has an authenticated client (#7214); plain JWT-bearer passes an empty value. |
| `restriction.go`: `restriction` enum, `terminalRestriction`, `hasClaim` | c | `as2-call-binding-3-terminal-details` | Change the enum (marker first) into a set of facts (CONTEXT.md, section 4). `hasClaim` stays; it matches `hasConfirmationClaim` on main and both can share it. |
| `handler.go`: `refuseTerminalToken`, the subject and actor call sites, `hint*AuthorizationDetails`, `reasonAuthorizationDetailsToken`; `hasDelegatedAccessMarker` removed | c | `as2-call-binding-3-terminal-details` | Merges cleanly. Change the `switch` to the set. Keep the hints and reasons. |
| `refresh_token_handler.go`: `reasonSubjectAuthorizationDetails`, `reasonActorAuthorizationDetails`, the `decideTopLevel` switch, `loginSubjectError` case | c | `as2-call-binding-3-terminal-details` | Change to the set. |
| `refresh_token_id_jag.go`: `refuseDelegatedAccessActor` switch + comment | c | `as2-call-binding-3-terminal-details` | Change to the set. Rename the function: it now refuses both facts. |
| `redemption_details.go`: `refuseSignedDetails` | c | `as2-call-binding-3-terminal-details`, then `as2-call-binding-4-as2-claim` | In the terminal-details branch, refuse a signed claim on both redemption forms, so the restriction is never dropped. With the set, the donor comment "Not terminalRestriction..." is no longer needed. In the AS-2 claim branch, keep the refusal for plain RFC 7523 only. |
| `redemption_details.go`: `redemptionDetailsClaim` | c | `as2-call-binding-4-as2-claim` | The parameter path becomes a refusal: present or repeated → `invalid_authorization_details`, `parameter_not_allowed`. `Parse` → `Empty` → `Grant` carry over, but run on the raw bytes of the signed claim, followed by the subset check. "No registry" and "type not registered" become `unknown_type`; `[]` becomes `empty_grant`. |
| `redemption_details.go`: `redemptionDetailsRequest` | c | `as2-call-binding-4-as2-claim` | Build the Subject with `detailsSubject`. Keep `ClientID` and `GrantedAudience`. |
| `redemption_details.go`: `setRedemptionDetailsClaim`, `echoRedemptionDetailsClaim` | c | `as2-call-binding-4-as2-claim` | Add a `details json.RawMessage` field to `redemption`. Set the claim in `newRedemptionSession` and echo it in `redeemer.populate`. Keep the Redis note (`[]any` after a round trip). |
| `redemption_details.go`: `detailsRefusal` | b | `as2-call-binding-4-as2-claim` | Drop `reasonRepeatedParameter`; the parameter is refused whatever its shape. |
| `jwt_bearer_handler.go`: `checkAssertionClaims`, `replayPurpose`, the details call, the synthetic-client comment, `populateAccessTokenResponse` echo, `JWTBearerIssuanceFactory(... options ...FactoryOption)` | a/c | `as2-call-binding-3-terminal-details`, `as2-call-binding-4-as2-claim` | The handler body is now on the redeemer (#7249), so drop those hunks. Keep the variadic `FactoryOption` on `JWTBearerIssuanceFactory`. |
| `id_jag_handler.go`: details call before consume, `copyActorClaim`, `IDJAGIssuanceFactory(... options ...FactoryOption)` | a/c | `as2-call-binding-2-client-lifespan`, `as2-call-binding-4-as2-claim` | `copyActorClaim` is already in `newRedemptionSession`. Add `options ...FactoryOption` once, in whichever of the two branches lands first. |
| `factory.go`: `FactoryOption` / `WithAuthorizationDetailsRegistry` comments | c | `as2-call-binding-2-client-lifespan`, `as2-call-binding-4-as2-claim` | |
| `server_impl.go`: `buildJWTBearerFactories` gets the registry | c | `as2-call-binding-4-as2-claim` | It conflicts. Main has `buildJWTBearerFactoriesForConfig` and a `resolver` argument. |
| `docs/authz.md` hunk | c | `as2-call-binding-4-as2-claim` | Say "when the ID-JAG carries the claim", not "when the redemption asked". |
| `docs/arch/README.md` line | c | `as2-call-binding-4-as2-claim` | |
| `docs/arch/17`: new "Call details on redemption" section | c | `as2-call-binding-4-as2-claim` | Most of the text describes the parameter design. Keep the boundary text ("the server does not look inside", "receivers must refuse types they do not understand"), the `[]` reasoning, and "checks run before consume". Drop "What this does not guarantee" and the "deliberate departure from section 4.4.1" text (also in the bound ID-JAG section): the new branch makes the server conform. Rewrite the refusal table with the narrowing issue's (#7061) reasons. |
| `docs/arch/17`: "Authorization details" section edits | c | `as2-call-binding-1-registry-reachable` (the wiring sentence), `as2-call-binding-4-as2-claim` (the rest) | |
| `docs/arch/17`: delegated-access marker section, the "terminal in the same four places" paragraph | c | `as2-call-binding-3-terminal-details` | Describe the set and the per-site precedence. |
| `export_test.go`: `ActorTokenOptions.Details` | b | `as2-call-binding-3-terminal-details` | |
| `delegated_access_marker_test.go`: subject and actor rows, `TestTerminalTokenRefusedAtGrantOpening`, `TestTerminalRestriction` | c | `as2-call-binding-3-terminal-details` | Change the "both claims, marker takes precedence" row into a per-site table. |
| `delegated_access_marker_actor_test.go`: `wantReason` + Details row | b | `as2-call-binding-3-terminal-details` | |
| `handler_test.go`: `newDetailsRegistry(..., grantTypes...)` + "JWT-bearer only type" row | b | `as2-call-binding-4-as2-claim` | |
| `redemption_details_test.go` (`TestRedemptionDetailsClaim_Refusals`, `_RedemptionPaths`, the scenario table) | c | `as2-call-binding-4-as2-claim` | Keep the fixtures: `newCallDetailsRegistry`, the entries, `bigEntry` (integer precision, which raw bytes must keep), the failing hook, and the "signed marker and details" rows. Move the scenarios from the form to the signed claim. Build the handlers through `issuanceDeps` and `redemption_fixtures_test.go`. |
| `pkg/authserver/authorization_details_integration_test.go`: `TestIntegration_TokenExchangeDetailTypesRefusedOnRedemption` | c | `as2-call-binding-4-as2-claim` | Any parameter is now refused (`parameter_not_allowed`); check that the hook is not called. |
| `authorization_details_integration_test.go`: `TestIntegration_AuthorizationDetailsJWTBearerRedemptionOverHTTP` | c | `as2-call-binding-4-as2-claim` | The premise inverts. This hook ADDS a member (`added_by_hook`); the subset check must refuse that. Keep the HTTP setup and the signed-JWT check, and use an ID-JAG that carries the claim. Drop the plain row: plain refuses the claim. |
| `pkg/authserver/integration_test.go`: `signIDJAGAssertionFor` | c | `as2-call-binding-4-as2-claim` | It conflicts. Extend it to sign an `authorization_details` claim. |
| `id_jag_handler_test.go`, `jwt_bearer_handler_test.go`, `id_jag_mint_shape_test.go`, `jwt_bearer_redis_integration_test.go`, `redemption_refusal_test.go` constructor edits | d | — | Positional-argument edits; `issuanceDeps` replaces them. |

## 3abb073f2 (`pkg/mcp/toolcalldetails`)

Commands: `git show 3abb073f2 -- pkg/mcp/toolcalldetails/toolcalldetails.go`
and `git show 3abb073f2 -- pkg/mcp/toolcalldetails/toolcalldetails_test.go`.
It applies cleanly.

| Part | Class | Target branch | Notes |
| --- | --- | --- | --- |
| Package doc, `Type`, reason constants, `check`, `listMethods`, hash regexp | b/c | `as2-call-binding-5-mcp-tool-call-type` | Add RFC 8785 and the `{}`-when-absent rule to the doc, as the narrowing issue (#7061) states them. Fix the claim that the type is "accepted on the JWT-bearer grant ... and on no other grant". |
| `Register`: `GrantTypes` | c | `as2-call-binding-5-mcp-tool-call-type` | Register for `GrantTypeTokenExchange` and `GrantTypeJWTBearer`. |
| `grant` | c | `as2-call-binding-5-mcp-tool-call-type` | On token exchange, accept only `req.RequestedTokenType == oauthproto.TokenTypeIDJAG` (not yet in the issue — confirm). On JWT-bearer, return the entry unchanged. |
| `TestRegister_Validation` | b | `as2-call-binding-5-mcp-tool-call-type` | |
| `TestRegister_GrantTypes` | c | `as2-call-binding-5-mcp-tool-call-type` | Both grants, plus a refusal row for `requested_token_type`. |

## c96ca8053 (flow-test row)

Command: `git show c96ca8053 -- pkg/authserver/delegated_agent_flow_integration_test.go`.
It applies cleanly; the file has not changed on main.

| Part | Class | Target branch | Notes |
| --- | --- | --- | --- |
| Registry wiring in `startFlow` (`toolcalldetails.Register` → `WithAuthorizationDetailsRegistry` on AS-2) | b/c | `as2-call-binding-6-as1-signing` | AS-1 needs the same registry once it signs entries. |
| `redeemWith` + success and `single_entry_required` asserts | c | `as2-call-binding-6-as1-signing` | Send the entry on the ID-JAG request to AS-1 (`requestIDJAGWith`). Redeem without the parameter. Assert that the response member, V's claim and the ID-JAG claim are equal. A second entry is refused **at AS-1** with `single_entry_required`. Add a row where a redemption that still sends the parameter gets `parameter_not_allowed`. Keep the `expires_in` ≤ ID-JAG check. |
| Reworded row 6.1 comment ("AS-2 here is a namespaced server...") | b | `as2-call-binding-6-as1-signing` | Still correct after the subject-resolution PR (#7262), which keeps resolution behind the gate. |

## 962dec550 (per-client lifespan)

Command: `git show 962dec550 -- <path>`

| File / hunk | Class | Target branch | Notes |
| --- | --- | --- | --- |
| `pkg/authserver/config.go`: `DelegateClientRunConfig.AccessTokenLifespan`, `AccessTokenLifespanDuration`, `checkDelegateAccessTokenLifespan`, the two validate hooks, `DelegateClient.AccessTokenLifespan` | b | `as2-call-binding-2-client-lifespan` | No conflict in the trial merge. |
| `config_test.go`: four lifespan rows | b | `as2-call-binding-2-client-lifespan` | |
| `runner/embeddedauthserver.go`: `resolveDelegateClients` parses it | b | `as2-call-binding-2-client-lifespan` | |
| `server_impl.go`: `delegateAccessTokenLifespans` | b | `as2-call-binding-2-client-lifespan` | |
| `server_impl.go`: passing the map to `IDJAGIssuanceFactory` positionally | c | `as2-call-binding-2-client-lifespan` | Use a new `FactoryOption`, for example `WithClientAccessTokenLifespans(map[string]time.Duration)`, after `resolver`. |
| `id_jag_handler.go`: `clientLifespans` field | c | `as2-call-binding-2-client-lifespan` | Set it from the option in the build closure. Copy the client's value into a new `redemption.clientLifespan` in `idJAGRedemption`. |
| `jwt_bearer_handler.go`: `baseLifespan`, the third argument of `assertionBoundedLifetime` | c | `as2-call-binding-2-client-lifespan` | Move this into `redeemer.assertionBoundedLifetime`: base = client lifespan or server lifespan, then the minimum with the ID-JAG's remaining time, then the marker cap last. Plain JWT-bearer leaves it at zero. |
| `id_jag_handler_test.go`: `TestIDJAGHandler_ClientAccessTokenLifespan` (4 cases) | c | `as2-call-binding-2-client-lifespan` | Port it to `redeemer_test.go` / `redemption_fixtures_test.go`. Add a marker-cap case, as the narrowing issue's (#7061) acceptance asks. |
| `docs/arch/17`: field-table row + "Lifetime of a redeemed token" | c | `as2-call-binding-2-client-lifespan` | It conflicts. Add the marker cap as the third bound, like the table in the narrowing issue (#7061). |
| `docs/server/{docs.go,swagger.json,swagger.yaml}`, `sdk/go/{openapi.*,client/oas_*_gen.go}` | d | `as2-call-binding-2-client-lifespan` | Do not cherry-pick. Regenerate with `task docs` and the SDK generator. |

## Per target branch

| Target branch | Start from | Write fresh | Expected conflicts | Carry-over |
| --- | --- | --- | --- | --- |
| `as2-call-binding-1-registry-reachable` | `git diff 884ee1fbc 3d6b3b21b --` `pkg/authserver/config.go` (reachability hunks), `pkg/authserver/runner/embeddedauthserver.go` (two sites), `pkg/authserver/runner/embeddedauthserver_test.go`, `pkg/authserver/config_test.go`, and the wiring sentence in docs/arch/17 | Drop the JWT-bearer half for now | None | ~90% |
| `as2-call-binding-2-client-lifespan` | `git show 962dec550 --` `pkg/authserver/config.go pkg/authserver/config_test.go pkg/authserver/runner/embeddedauthserver.go`, `delegateAccessTokenLifespans` from `pkg/authserver/server_impl.go`, and the docs section | `FactoryOption` on `IDJAGIssuanceFactory`, `redemption.clientLifespan`, the redeemer lifetime order, ported tests, regenerated swagger and SDK | `server_impl.go`, `id_jag_handler.go`, `jwt_bearer_handler.go`, docs/arch/17 | ~60% |
| `as2-call-binding-3-terminal-details` | `git diff 884ee1fbc 3d6b3b21b --` on `restriction.go`, `handler.go`, `refresh_token_handler.go`, `refresh_token_id_jag.go`, `export_test.go`, `delegated_access_marker_test.go`, `delegated_access_marker_actor_test.go` (all under `pkg/authserver/server/tokenexchange/`), `refuseSignedDetails` from `redemption_details.go`, and the docs marker-section hunk | The set of facts, per-site precedence, the redemption claim refusal as a site, a per-site test table | None for these files. `refuseSignedDetails` goes into the redeemer front halves by hand. | ~70% |
| `as2-call-binding-4-as2-claim` | `git diff 884ee1fbc 3d6b3b21b --` on `redemption_details.go`, `redemption_details_test.go`, `pkg/authserver/authorizationdetails/`, `pkg/authserver/authorization_details_integration_test.go`, `newDetailsRegistry` in `handler_test.go`, and the docs section | Raw claim bytes from the verified payload (the decoded `Claims` map loses integer precision); every signed type's hook; the subset check; the parameter refusal; `unknown_type` vs `malformed_entry`; `empty_grant`; echo in `redeemer.populate`; the registry option on both JWT-bearer factories; the JWT-bearer half of the reachability rule | `server_impl.go`, `integration_test.go`, both handler files | ~30% |
| `as2-call-binding-5-mcp-tool-call-type` | `git show 3abb073f2 -- pkg/mcp/toolcalldetails/` | Both grant types, the `requested_token_type` check, new test rows, doc fixes | None | ~75% |
| `as2-call-binding-6-as1-signing` | `git show c96ca8053 -- pkg/authserver/delegated_agent_flow_integration_test.go`, reworked | AS-1 accepts the entry (`checkIDJAGExtraParameters` in `id_jag_mint.go` refuses it today); the embedder registry reaches the refresh-token factory (a `RefreshTokenOption`); AS-1 signs the granted entries into the ID-JAG claim and echoes them; `empty_grant` and `unknown_type`; docs on the ID-JAG claims table | None on the flow test | ~10% |
