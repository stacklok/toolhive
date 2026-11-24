# Call binding: design context

This note is for whoever takes over the call-binding work:

- "Authorization server: narrow a JWT-bearer access token to one MCP call with authorization_details" (#7061)
- "vMCP: accept an access token for the one call its authorization_details names, once" (#7062)
- on the Mecatl side, "Bind each gateway token to its call" (stacklok/mecatl#2727)

It records the decisions and findings from the design and review rounds that
the code and the issues do not state. **The current issue bodies are the
spec.** Where this note and an issue body disagree, the issue wins. Rules that
appear here but not in the issue are marked "not yet in the issue — confirm";
check them with the issue author before you build on them.

Short names used after the first mention: "the narrowing issue" (#7061),
"the vMCP issue" (#7062), "the shared-record issue" (#7261), "the client-auth
PR" for "Refuse unauthenticated ID-JAG clients and cnf-bound assertions"
(#7214), "the subject-resolution PR" for "Resolve ID-JAG subjects and issue
marked tokens behind the gate" (#7262), and "the Mecatl binding ticket" for
"Bind each gateway token to its call" (stacklok/mecatl#2727).

Terms used below:

- **AS-1:** the authorization server that issues the ID-JAG. In Mecatl, this is
  ToolHive's embedded authorization server inside Mecatl.
- **AS-2:** the authorization server in front of the vMCP. It redeems the
  ID-JAG.
- **V:** the access token that AS-2 issues.
- **The broker:** Mecatl's identity broker. It approves calls, asks AS-1 for the
  ID-JAG, and redeems it at AS-2.
- **Steps (7), (8), (8a), (9):** the numbered steps of the Mecatl design
  (`docs/drafts/agent-identity/credentials-and-lifecycle.md` in the Mecatl
  repository): (7) the ID-JAG request to AS-1, (8) the redemption at AS-2,
  (8a) the broker's output check, (9) the vMCP decision.

---

## 1. The design, and why it changed

### Old design (the code on the donor branches)

The broker redeemed the ID-JAG at AS-2 with the RFC 7523 JWT-bearer grant. It
sent the approved call as an RFC 9396 `authorization_details` **request
parameter** of type `https://toolhive.dev/mcp_tool_call`. AS-2 checked the
entry with a registry hook and signed it into V.

### Current design: the call is signed into the ID-JAG at AS-1

- At step (7), the broker sends the call entry on the ID-JAG request to AS-1.
  AS-1 checks the entry's shape with the type's hook and signs the granted
  entries into the ID-JAG's `authorization_details` claim.
- At step (8), the broker redeems with only `grant_type`, `assertion` and
  `resource`. AS-2 processes the signed claim and copies the granted entries
  into V. AS-2 may narrow entries, but never widens them.

### Why it changed

**Standards.** ID-JAG draft-06 defines `authorization_details` as a claim, not
as a redemption parameter:

- §4.3.3: the IdP "MUST include the granted values … in the …
  authorization_details claims of the issued ID-JAG".
- §4.4.1: the redeeming server "MUST parse it as a JSON array and process each
  authorization detail object", and "MAY modify, filter, or omit".
- §4.4 defines only `grant_type` and `assertion` at redemption.

A redemption parameter is legal OAuth (RFC 7521 §4.1 and RFC 9396 §3), but it
sits outside the ID-JAG profile. The old branch also refused any ID-JAG that
carried the claim, which conflicts with §4.4.1.

**Who chooses the restriction.** With a redemption parameter, whoever presents
the ID-JAG chooses the restriction. In practice the broker is the only party
that holds both the ID-JAG and the `mecatl` client key, and client
authentication at redemption is enforced since "Refuse unauthenticated ID-JAG
clients and cnf-bound assertions" (#7214). So signing the call at AS-1 is
defence in depth, not a fix for an open hole. It does fix the call at issuance,
which protects against a buggy or compromised path between minting and
redemption.

**Absent details mean no restriction.** RFC 9396 treats details as grants:
removing an entry narrows the token. In this feature the details *restrict* a
token that would otherwise be broad. A V with `aud` set to the vMCP and no
`authorization_details` claim can make any call. As a result, everything that
drops details fails open:

- re-exchanging a token without carrying the claim over;
- a hook that omits or filters entries (§4.4.1 allows that);
- treating `[]` as absent;
- a resource server that ignores claims it does not know (RFC 7519).

The refusals in this design exist because of this. See section 4 for terminal
tokens, and section 2 for `[]` and for the rule that granted entries must equal
signed ones.

**Mecatl already reserves the slot.** Mecatl's design plans for AS-1 to write
the agent's ceiling into the ID-JAG as `authorization_details` later. AS-2 has
to process signed details anyway.

### Decided

**No authorization server requires the entry.**

- AS-1 does not refuse an ID-JAG request that has no entry.
- AS-2 issues V without a claim when the ID-JAG carries none. This matches
  the narrowing issue's (#7061) acceptance line "a redemption without `authorization_details` behaves
  exactly as today".
- The broker enforces both sides: it sends an entry on every gateway call, and
  its output check (step (8a)) requires V's claim to equal the entry it sent,
  compared as decoded JSON values. In Mecatl's words (stacklok/mecatl#2727):
  the call fails with `call_entry_missing` or `call_entry_mismatch`.
- This replaced a planned per-client "require authorization_details" flag at
  AS-2. That flag could not stop an attacker from choosing the entry, only from
  sending no entry at all.
- A later option for clients other than Mecatl: let AS-1 require an entry per
  configured ID-JAG target (for example a per-target list of required types).
  Nothing is filed.

**The redemption parameter is dropped.** A redemption that still carries
`authorization_details` is refused with `invalid_authorization_details` and
reason `parameter_not_allowed` (#7061). It is not ignored. This matches how
AS-1 already refuses the parameter at minting today (`checkIDJAGExtraParameters`).

**Rollout: AS-2 before AS-1.** Not yet in the issue — confirm. (The Mecatl
design's "Rollout order" says it.)

- AS-2 processing must ship in a release **before**, or in the same release
  as, AS-1 signing. An older AS-2 ignores the claim, so V would come out
  unrestricted.
- Mecatl's broker sends the entry only once its pinned ToolHive release
  contains both halves (stacklok/mecatl#2727, "Scope").
- "Bind each gateway token to its call" (stacklok/mecatl#2727) merges together
  with that pin bump. Until then, phase 1's unbound gateway calls stay as they
  are.

---

## 2. AS-2: processing the signed claim

**Plain RFC 7523 assertions keep refusing the claim.** Not yet in the issue —
confirm. The processing applies to ID-JAGs only. A plain RFC 7523 assertion
that carries `authorization_details` is refused with `invalid_grant`,
`invalid_assertion`, because RFC 7523 defines no such claim. On `origin/main`
today the claim is dropped silently: redemption copies only `act`.

**Raw bytes.** `verifySignature`
(`pkg/authserver/server/tokenexchange/validator.go:312`) decodes claims into
`map[string]any` through go-jose. Re-encoding that map rounds integers above
2^53 and loses member order. Read the claim's raw bytes with a second decode
into `map[string]json.RawMessage`, which matches keys exactly. Do **not** use a
struct with a `json.RawMessage` field: `encoding/json` matches struct tags
without regard to letter case, so `"AUTHORIZATION_DETAILS"` would fill the
field.

**Duplicate members.** go-jose v4's JSON decoder already refuses a repeated
member, at any depth, in every JWT (`json/decode.go` in go-jose v4.1.9). So an
exact repeated member is already `invalid_grant`, `invalid_assertion`. This
needs a test, not code.

**Hooks.** Every signed type's hook runs on the signed entries.
`Pending.Grant` only calls hooks for types present in its input. If a caller
could drop every entry of one type, that type's hook would never run, and a
type that expresses a limit would be lifted.

**Output check.** Every granted entry must equal one of the signed entries,
compared as JSON values decoded with `UseNumber`. The result must not be empty.
A hook that returns an entry that was not signed has broken its contract; that
is `server_error`, with the text hidden from the client.

**Whole entries only in the first release.** The narrowing issue (#7061) says AS-2 "grants an
`mcp_tool_call` entry unchanged, and may narrow other types". The first release
allows only whole signed entries: a hook may drop entries but not narrow inside
a field and not enrich (RFC 9396 §6.1 and §7.1). For `mcp_tool_call` that is
exactly the issue's rule. For other types it is stricter than the issue allows.
Document the limit. A per-type "narrow" function is the natural extension; it
is tracked by "Narrow authorization_details on token exchange and refresh"
(#6912).

**Hook input.** Remove the lossy `float64`/`[]any` copy of the claim from the
hook's `Subject.Claims`, or document that hooks must read `Entries`.

**Token and response.** Both come from the same bytes, read from the session,
as on the donor branch (`setRedemptionDetailsClaim` and
`echoRedemptionDetailsClaim`). The narrowing issue (#7061) requires the response member whether or
not the redemption asked. After a Redis round trip the stored claim becomes
`[]any`; a future reader must accept both forms.

**Ordering.** All of this runs before the `jti` is consumed, so a refusal the
client can fix does not burn the ID-JAG.

### Error table

The reason names come from the narrowing issue (#7061): `malformed_entry`, `unknown_type`,
`single_entry_required`, `empty_grant`, `parameter_not_allowed`, or a reason
from the type's hook, in `error_description`.

| Case | Response |
|---|---|
| The claim is not a JSON array of objects (including `"authorization_details": null`, which decodes as the non-nil bytes `null`) | 400 `invalid_grant`, `invalid_assertion`. Not yet in the issue — confirm; the Mecatl design (step (8) failure table) says the same. |
| An exact member name is repeated anywhere in the JWT | 400 `invalid_grant`, `invalid_assertion`. go-jose already refuses it; add a test. |
| No registry, or a type that is not registered for the JWT-bearer grant | 400 `invalid_authorization_details`, `unknown_type` |
| An entry does not fit its type's generic shape (for example a member repeated with different letter case, which `Registry.Parse` refuses as ToolHive policy, or an entry over the size or depth limit) | 400 `invalid_authorization_details`, `malformed_entry` |
| The claim is `[]` (`Pending.Empty()` after `Parse`) | 400 `invalid_authorization_details`, `empty_grant`. A new ID-JAG would be the same, so no retry. Treating `[]` as absent would fail open. |
| A second call entry (including the same entry listed twice) | 400 `invalid_authorization_details`, `single_entry_required` (the `mcp_tool_call` hook's reason) |
| The hook refuses the entry | 400 `invalid_authorization_details` with the hook's reason (for `mcp_tool_call`: `malformed_entry`, `invalid_arguments_hash`, `invalid_method`, `single_entry_required`) |
| A hook refuses by policy | 400 `access_denied` would fit §4.4.3, but is **unreachable today**: `authorizationdetails.Error` allows only `invalid_authorization_details` and `invalid_request` (`pkg/authserver/authorizationdetails/errors.go:56`). No registered type refuses on policy. |
| A hook returns an entry that was not signed | 500 `server_error` |
| The redemption carries the `authorization_details` parameter | 400 `invalid_authorization_details`, `parameter_not_allowed`. Read it with the shared `readDetailsParam`. A single `""` counts as absent; any repeat, including `["",""]`, is refused with the same reason, because redemption never accepts the parameter. |

`Registry.Parse` returns the same error for "unknown type" and for a shape
problem today, so the `unknown_type` / `malformed_entry` split needs new code
(check the type against the registry first, or give `Parse` typed errors).

There is no "empty grant" case beyond `[]`. `Pending.Grant` already treats a
hook that returns zero entries as a broken contract (`server_error`). A hook
that wants to grant nothing refuses with an `Error`.

---

## 3. AS-1: signing the entry into the ID-JAG

The narrowing issue (#7061), "AS-1 signing (token exchange)": AS-1 accepts an entry of the same type
on the ID-JAG request, checks its shape and the registered types, and signs the
granted entries into the ID-JAG's `authorization_details` claim. An empty grant
or an unknown type is refused with `invalid_authorization_details`. An
authorization server refuses any type it has no registered validator for.

**Where.** ID-JAG minting from an exchange grant runs through the RFC 8693
token-exchange grant, in `refresh_token_id_jag.go` (`decideIDJAG`, line 56) and
`id_jag_mint.go`. Today `checkIDJAGExtraParameters` (`id_jag_mint.go:220`)
refuses `authorization_details`. It must accept it.

**Order.**

- Parse the parameter right after `checkIDJAGRequest`.
- Call `Grant` after `presentedGrant` and `selectSubjectClaims`, and before the
  actor validator. The hook `Request` reads the grant's subject and client,
  which only exist after `presentedGrant`.
- Sign the granted entries into the claim, and echo them in the response
  (§4.3.4).

**Registration.** The `mcp_tool_call` type must list **both** grant types:
token exchange, which is used at minting, and JWT-bearer, which is used at
redemption.

**The `requested_token_type` check.** Not yet in the issue — confirm.
Registering the type for token exchange would otherwise let a plain RFC 8693
exchange accept the entry without binding any token: the token-exchange handler
only echoes details and never puts them into a token. The type's hook should
accept the entry on token exchange only when `requested_token_type` is the
ID-JAG type (`oauthproto.TokenTypeIDJAG`). The reason name `id_jag_required` is
a proposal.

**"Signs unchanged".** AS-1 signs the **hook's granted entries**. For
`mcp_tool_call` these are the same JSON value, but not necessarily the same
bytes: `Grant` re-encodes the JSON, compacting it and escaping HTML. The
broker's output check compares decoded JSON, so this is safe.

---

## 4. Terminal tokens: tokens that carry restrictions must not be exchanged

Not yet in the issue — confirm. The narrowing issue (#7061) says the entry "can only restrict the
token, never widen" it; this section is how a narrowed token stays narrow.

**The rule.** A token that carries the delegated-access marker or an
`authorization_details` claim is refused as an input wherever a new token is
minted from an existing one:

- the RFC 8693 `subject_token` (`handler.go:244`);
- the RFC 8693 `actor_token` (`handler.go:499`);
- the `subject_token` that opens an exchange grant
  (`refresh_token_handler.go:464`);
- the `actor_token` at ID-JAG issuance (`refresh_token_id_jag.go:187`);
- an assertion at redemption, for the details claim (plain RFC 7523 only, once
  AS-2 processes the claim on ID-JAGs).

The line numbers are the current `hasDelegatedAccessMarker` call sites on
`origin/main`, in `pkg/authserver/server/tokenexchange/`.

**Model it as a set of facts.** Each site applies its own precedence. The donor
branch used one classifier that returned a single value in priority order,
marker first. Reusing it at the redemption check caused a regression: an
assertion carrying both the marker and the details claim was classified as
"marker". Redemption does not refuse the marker, so the token was issued with
the signed details silently dropped. The target shape is:

```go
type restrictions struct{ DelegatedAccess, AuthorizationDetails, Confirmation bool }
```

Each site maps that struct to its own hint and reason. Keep the marker-first
wire text at the RFC 8693 sites unchanged. `Confirmation` is the `cnf` check
from the client-auth PR (#7214) (`hasConfirmationClaim`, `jwt_bearer_handler.go:125`); its one call
site keeps its own refusal.

**Issuer scope per site:**

- Only the RFC 8693 `subject_token` accepts tokens from external trusted
  issuers. That is the one place where "refused whatever the issuer" applies.
- RFC 8693 actor tokens, and the `subject_token` that opens an exchange grant,
  are only ever self-issued tokens.
- At ID-JAG issuance, only actor tokens signed by this server are refused. An
  external actor token is the configured actor validator's decision. An
  external actor's details would describe the agent's own limits, not the
  subject's grant.

**Why the external-subject refusal matters.** If an AS-1 trusts AS-2 as an
external issuer, this refusal is what stops a narrowed V from being exchanged
at AS-1 into an unrestricted token. Say so in the PR.

**Trade-off.** A narrowed token cannot be re-exchanged for another audience. A
trusted IdP's RFC 9396 token that is exchanged today (details dropped) is
refused afterwards. Carrying details over through a per-type hook would be a
possible later extension.

---

## 5. Per-client lifespan

The narrowing issue (#7061) asks for "a configurable access-token lifetime for a client", refused
when zero or negative, and an issued `exp` that is the earliest of the
per-client lifetime, the ID-JAG's `exp`, and (for a marked token) the marker
cap.

- **What exists.** `redeemer.assertionBoundedLifetime`
  (`pkg/authserver/server/tokenexchange/redeemer.go:141`) already applies:
  the server's access-token lifespan, the ID-JAG's remaining validity, and the
  five-minute marker cap (`delegatedAccessMarkerCap`, `redeemer.go:56`).
- **How to add it.** Pass a `map[clientID]time.Duration` to
  `IDJAGIssuanceFactory` (`id_jag_handler.go:537`) as a `FactoryOption`, the
  same mechanism token exchange uses for the details registry
  (`factory.go:47`, `:56`). That factory has no options parameter on
  `origin/main` yet; add `options ...FactoryOption` after `resolver`, and
  update the `FactoryOption` doc, which still names only the token-exchange
  factories. Keep the map on the ID-JAG handler, never on the shared
  `issuanceDeps`. Copy the client's value into the `redemption` and use it as
  the base in `assertionBoundedLifetime`: client lifespan if set, else server
  lifespan; then the minimum with the ID-JAG's remaining time; then the marker
  cap last.
- **Config rules,** from "Add per-client access token lifespan" (962dec550):
  `access_token_lifespan` on the delegate client, a Go duration string. It must
  be greater than 0 (as the narrowing issue (#7061) asks) and at most 24h (not yet in the issue —
  confirm; it is the server-wide maximum). It applies to ID-JAG redemption
  only. It is RunConfig-only, with no CRD field, so no migration. It needs
  swagger and SDK regeneration and the SDK module gates.
- **Why a map.** A per-client policy type was considered and dropped. See
  section 6.

---

## 6. Things the review rounds found

- **Size.** The first version touched 21 files. Most of that was constructor
  and factory plumbing. The refactors that have since merged (see the README)
  mean each remaining branch touches a handful of files.
- **The precedence regression.** See section 4. Write a test with **both claims
  present** for every site.
- **"Present in the map means restricted".** `ConfiguredClientPolicy` in
  `handler.go` means "restricted" just by being present in the map. So you
  cannot add a field to it without restricting clients by accident. A
  per-client policy type to fix this was dropped: SPIFFE association clients
  force `explicitGrantClients` to stay separate, so only one channel would fold
  into it. Revisit when another per-client setting arrives. Until then, use
  small maps.
- **Empty values.**
  - At AS-1, `nonEmptyValues` (`id_jag_mint.go:505`) treats `["",""]` as
    absent. The shared reader from "Share the authorization_details reader"
    (#7217), `readDetailsParam` and `detailsSubject`
    (`authdetails.go:40`, `:53`), treats it as repeated. Use the shared reader
    everywhere.
  - A single `""` means absent (RFC 6749 §3.2).
  - `[]` is a value. It is refused on redemption (`empty_grant`), while token
    exchange accepts and echoes it. That asymmetry is deliberate: on token
    exchange, details never go into the token.
- **Unchanged rules that were nearly broken:**
  - consume before issue;
  - the elapsed-expiry text "The JWT bearer assertion has expired.";
  - plain JWT-bearer replaces any resolved client with a synthetic one, so the
    hook's `ClientID` is `""` on the plain grant;
  - the 60-second client-assertion clock-skew recovery path also checks `aud`.
- **Open: which `sub` the hook sees on a resolved redemption.** The proposal is
  the assertion's subject only. With subject mapping, the token's `sub` is the
  local user, while the hook's `Subject` is built from the ID-JAG. Not decided.
- **Hook client identity.** Document `authorizationdetails.Request.ClientID`
  as: authenticated on token exchange; on ID-JAG redemption, the authenticated
  client (public clients are refused since the client-auth PR (#7214)); empty on the plain grant.
  Doc-only, no new fields.

---

## 7. The Mecatl design

The Mecatl design for this flow merged as "docs(agent-identity): bind each
gateway call through the ID-JAG and the broker output check"
(stacklok/mecatl#2950). It matches this note on every main point:

- the entry is sent at step (7) and signed by AS-1;
- there is no redemption parameter; a redemption that sends one is refused
  with `invalid_authorization_details`, `parameter_not_allowed`;
- AS-2 only narrows;
- the broker runs the output check (8a);
- AS-2 ships before AS-1, then the Mecatl pin;
- the operation ID travels inside the entry (`operation_id`), not as a separate
  parameter;
- the vMCP list of checked clients is empty by default, and Mecatl lists
  `mecatl`.

Most items found while reviewing the design were folded in before it merged
(the minting failure row, the two meanings of "repeated", the
`parameter_not_allowed` reason). Before relying on the rest, check the design
on Mecatl `main` for:

1. The "hook refuses on policy → `access_denied`" case. It is not reachable in
   ToolHive today (section 2).
2. How the broker reads V's claim in step (8a). AS-2 also returns the granted
   entries as the `authorization_details` member of the token response, from
   the same session, so the broker can treat V as opaque.
3. Which PR returns the granted entries in the response. The narrowing issue (#7061) now says it
   belongs to the call binding, not to "Delegated access: subject mapping at
   ID-JAG redemption" (#6887).

---

## 8. Open decisions

1. **Which `sub` the details hook sees on a resolved redemption** (section 6).
2. **Whether hooks may refuse with `access_denied`.** That needs a new code in
   `authorizationdetails.Error`. Deferred until a type needs it.
3. **Whether to advertise `authorization_details_types_supported`** in
   discovery. Tracked by "Advertise authorization_details types in metadata and
   DCR" (#6915). If done, filter by the enabled grants, and note that the types
   apply only at the token endpoint.
4. **The rules marked "not yet in the issue — confirm"** above: plain RFC 7523
   refusing the claim, the claim-shape refusal as `invalid_grant`, the
   `requested_token_type` check and its reason name, whole entries only, the
   terminal-token rule, the 24h cap, and AS-2 released before AS-1.
5. **The issue-text gaps** listed in the README ("Known gaps in the issues").
