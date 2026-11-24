# The vMCP side of per-call token binding

This note is for the engineer who picks up "vMCP: accept an access token for
the one call its authorization_details names, once" (#7062) and "vMCP: one
single-use record shared by all replicas for call-bound access tokens" (#7261).

It was written from ToolHive `origin/main` at "Resolve ID-JAG subjects and
issue marked tokens behind the gate" (#7262) (44f7d6368). All `file:line`
references are on that commit. Mecatl references are to Mecatl `main`. The
issue bodies (rewritten 2026-11-24) are the spec; where this note goes beyond
them, it says so.

Short names used after the first mention: "the narrowing issue" for
"Authorization server: narrow a JWT-bearer access token to one MCP call with
authorization_details" (#7061), "the vMCP issue" (#7062), "the shared-record
issue" (#7261), "the Mecatl binding ticket" for "Bind each gateway token to its
call" (stacklok/mecatl#2727), "the cross-replica test ticket" for "Test that a
call-bound gateway token cannot be replayed on another replica"
(stacklok/mecatl#2970), and "the multi-replica parent" for "[Phase 3] Run
mecak8s with the identity broker on more than one replica"
(stacklok/mecatl#2805).

**There is no code to start from.** No branch, PR or prototype exists for the
vMCP side. The `https://toolhive.dev/mcp_tool_call` entry type is not on
`origin/main` yet; it arrives with the `as2-call-binding-5-mcp-tool-call-type`
branch (see the README). Everything below is a reading of the issues, the
Mecatl design, and the current code.

## 1. Why this work exists

Mecatl's identity broker approves each gateway call. Then mecak8s's MCP client
(the harness side, which Mecatl trusts less) sends that call to the vMCP with a
short-lived gateway access token, called `V` in the design. Today `V` works
for any call the user's connections allow until it expires. The goal is that
`V` works for one call only: the call the broker approved, used once.

The chain:

1. **AS-1** signs a call entry into the ID-JAG (#7061, "AS-1 signing").
2. **AS-2** copies that entry into `V` (#7061).
3. **The broker** checks that `V` carries the entry it sent (Mecatl design,
   step (8a)).
4. **The vMCP** accepts `V` only for the call the entry names, once (#7062).
   This is the part this note covers.

Who waits on this:

- "Bind each gateway token to its call" (stacklok/mecatl#2727) depends on the narrowing issue (#7061)
  and the vMCP issue (#7062), released in a ToolHive version that Mecatl pins.
- "Test that a call-bound gateway token cannot be replayed on another replica"
  (stacklok/mecatl#2970) depends on the shared-record issue (#7261) and the Mecatl binding ticket (stacklok/mecatl#2727).
- The Mecatl binding ticket (stacklok/mecatl#2727) sits under "Phase 2, Slice 4: Limited access — Each
  agent calls only the tools its definition and its parent allow, one call per
  token" (stacklok/mecatl#2851). If the ToolHive changes are not released in
  time, that slice closes without the Mecatl binding ticket (stacklok/mecatl#2727), which moves to phase 3.

## 2. What each issue asks (current text)

### The narrowing issue (#7061), AS-1 and AS-2 (blocks the vMCP issue (#7062))

- Entry type `https://toolhive.dev/mcp_tool_call`, one of:
  - a tool call: `server`, `tool` (the name the vMCP routes by, as in
    `params.name`), `arguments_sha256`, `operation_id`;
  - a list call: `server`, `method` (`tools/list` or another list method).
- AS-1 signs the entry into the ID-JAG's `authorization_details` claim. AS-2
  copies it into the access token unchanged. Exactly one entry.
- AS-2 refuses a redemption that still carries an `authorization_details`
  parameter (`parameter_not_allowed`).
- A per-client access-token lifetime, so `mecatl` tokens can live for seconds.

### The vMCP issue (#7062), the vMCP check (this work)

When a token carries an `mcp_tool_call` entry:

- **Tool call.** Accept a `tools/call` only if its server, `params.name`, and
  argument hash match the entry. Compute the hash over `params.arguments` as
  received, before any routing, prefix stripping or rewriting. Refuse
  arguments that are not I-JSON (RFC 7493).
- **List call.** A list-call token accepts only that list call on that server.
  It cannot start a `tools/call`.
- **Single use.** A token's `jti` starts at most one call. Keep the record until
  `exp` plus the clock skew, or until the call ends, whichever is later. The
  **first release keeps this record per replica.** That is a named shortcut,
  removed by the shared-record issue (#7261).
- **Messages for the open call.** Accept the session handshake while the token
  is valid. Accept cancellation, progress and elicitation replies for the open
  call, matched by JSON-RPC ID and session, while the call is open, even after
  the token expires, up to a configured maximum call duration.
- **Refusal.** 403 `insufficient_scope`, and record the refusal.
- **Scope.** The check applies to tokens from the clients that an embedder
  lists. Mecatl lists `mecatl`. The list is empty by default. Other clients are
  unchanged.
- Refuse a batch that carries a call-bound token, and any body that does not
  hold exactly one parsed operation for that token.
- Tokens without the entry behave exactly as today (but see section 4).
- **Where it runs.** A Go check in the vMCP's admission seam, **before** Cedar,
  as a condition that can only deny more. It runs whether or not Cedar is
  configured.
- **Acceptance** includes the shared argument-hash test vectors, the same ones
  Mecatl's client uses.

### The shared-record issue (#7261), the shared record (phase 3)

- Store each used `jti` in a backend every replica reads. Refuse a second
  `tools/call` with the same `jti` on any replica.
- Accept messages for an open call on any replica.
- With the in-memory store and several replicas, startup either refuses the
  configuration or logs an error ("pick one in review").
- Remove the per-replica shortcut from the vMCP issue (#7062).
- The body says "Part of" the vMCP issue (#7062). On GitHub, its parent is "[Phase 3] Run
  mecak8s with the identity broker on more than one replica"
  (stacklok/mecatl#2805), and it has no "blocked by the vMCP issue (#7062)" link.

### Related issues (none of them blocks the vMCP issue (#7062))

- **"vMCP: session identity binding is not enforced for `tools/list` (and other
  list methods)" (#6779).** Refuse list methods on a foreign session. No PR is
  linked.
- **"vMCP: bind sessions to the client, acting agent, and delegated-access
  marker, not just the user" (#6888).** This is check 2 of design step (9).
  the session-binding issue (#6888) keeps today's binding for sessionless JWT-bearer sessions. Confirm that
  a marked `V` takes the delegated route and is bound to `client_id` and
  `act`, and not the sessionless route; otherwise check 2 does not hold for
  Mecatl.
- **"Record the operation and attempt IDs a client reports, at the vMCP"
  (#6979).** This records the `_meta` operation and attempt IDs as reported
  values. The entry's `operation_id` is trusted, because AS-1 signed it, so the
  vMCP can log it with each accept or refuse decision of the vMCP issue (#7062). Comparing it
  with the reported value belongs to the reported-IDs issue (#6979).

## 3. How the vMCP issue (#7062) and the shared-record issue (#7261) relate

**The shared-record issue (#7261) was split out of the vMCP issue (#7062).** An earlier draft of the Mecatl design required
the `jti` record to be "in storage shared by its replicas" as part of the vMCP issue (#7062).
Before the design merged ("docs(agent-identity): bind each gateway call through
the ID-JAG and the broker output check" (stacklok/mecatl#2950), 9f905076c), it
changed: the first release keeps the record per replica, and the shared record
becomes the target. The vMCP issue's (#7062) text now matches.

**Recommended scope:**

| Issue | Owns |
|---|---|
| The vMCP issue (#7062) | The whole check, plus a small storage interface for the `jti` record (atomic "consume if absent, with TTL", "mark call ended", "is call open"). It ships an in-memory implementation and documents that replay protection covers one replica only. |
| The shared-record issue (#7261) | A Redis implementation of that interface; cross-replica acceptance of messages for an open call; the multi-replica guard; removal of the shortcut wording in the vMCP issue (#7062) and in ToolHive docs. |

On the multi-replica guard: "startup refuses or logs an error" cannot be done
inside the vMCP process, because it does not know how many replicas run. The
operator does. For MCPServer and MCPRemoteProxy it already sets the
`SessionStorageWarning` condition with reason
`SessionStorageMissingForReplicas`
(`cmd/thv-operator/api/v1beta1/mcpserver_types.go:199-204`). The same pattern
on VirtualMCPServer is the realistic choice.

## 4. Which clients are checked, and how the vMCP knows

- **The client.** The vMCP reads the `client_id` claim of the access token.
  RFC 9068 requires this claim, and AS-2 sets it (the design's decoded `V` has
  `"client_id": "mecatl"`). Claims are available after authentication as
  `auth.Identity.Claims` (`pkg/auth/identity.go:66`). The same map holds
  `jti`, `exp` and `authorization_details`.
- **Where the list is set.** Mecatl builds the vMCP in Go, through
  `vmcpserver.New` with a `vmcpserver.Config` (Mecatl
  `internal/adapter/mcpbroker/toolhive_process.go`). It sets no `Authz`, uses
  in-memory sessions, and names each backend by its profile name
  (`vmcp.Backend{ID: profile.Name, ...}`). So the setting belongs on
  `pkg/vmcp/server.Config` (`pkg/vmcp/server/server.go:121`), for example a
  `CallBinding` struct with `Clients []string`, `MaxCallDuration`, and an
  optional clock skew. Empty by default, as the vMCP issue (#7062) says.
  - Add no YAML, CLI or CRD field until an operator-managed deployment asks for
    one. The vMCP issue (#7062) says "embedder", which fits a Go-level setting.
- **Two semantic gaps to settle in review:**
  1. *A listed client whose token has no entry.* The Mecatl design (step (8),
     check 8) says the vMCP refuses a `V` that names no call for a listed
     client. The vMCP issue (#7062) says "tokens without the entry behave exactly as today",
     without limiting that to unlisted clients. Recommendation: for a listed
     client, refuse a token with no entry (403, fail closed). This is safe,
     because Mecatl sets the list in the same release in which it starts
     sending entries. This gap is also listed in the README.
  2. *An unlisted client whose token has an entry.* As written, such a token is
     accepted for any call, even though its entry says it is narrower.
     Recommendation: enforce the entry whenever it is present, and use the
     client list only for the "entry required" rule. No issuer emits these
     entries today, so nothing that exists changes. It does change the issue's
     "other clients are unchanged" wording, so agree on it first.

## 5. Where each part goes in the code

### The request pipeline today

The vMCP MCP handler is assembled in `Server.Handler`
(`pkg/vmcp/server/server.go:640-840`). Outermost first:

1. recovery, body limit, write timeout, header validation, forwarded-header
   capture
2. audit (`:813`)
3. auth (`:794`), which checks the token, including `exp`
4. `mcpparser.ParsingMiddleware` (`:790`)
5. usage metrics, telemetry, loopback host guard
6. `classifyingHandler` (`:762`; `classification.go:54`). It sends Modern
   (2026-07-28) requests to `dispatchModern` (`modern_dispatch.go:48`) and
   Legacy requests onward.
7. `sessionOwnershipMiddleware` (`:754`; `session_ownership.go:47`), Legacy
   only.
8. The mcpcompat streamable server. Inside it, the pre-dispatch **CallGate**
   (`call_gate.go:46`) re-runs the Cedar decision for `tools/call`,
   `resources/read` and `prompts/get`.
9. The SDK handlers (`serve_handlers.go`), then the core (`pkg/vmcp/core`).

**The CallGate is installed only when authz is configured**
(`server.go:667`, `if s.authzGateEnabled`). Mecatl configures no authz, so
nothing in the CallGate would run for Mecatl. The core admission seam
(`pkg/vmcp/core/admission.go:38`, the `Admission` interface) is also
Cedar-only. It receives `identity`, a resolved `*vmcp.Tool`, and a decoded
`map[string]any`. It never sees the raw bytes, the JSON-RPC ID, the session or
list methods.

### Recommendation: a new HTTP middleware, not the core seam

Add a call-binding middleware in `pkg/vmcp/server` (for example
`call_binding.go`). Install it unconditionally in `Handler`, just outside
`classifyingHandler` (wrap `mcpHandler` right after `:762`), so it runs before
classification. It then covers Legacy and Modern alike, runs after auth and
parsing, and runs before session ownership, the CallGate, Cedar and the SDK.

This fits the vMCP issue's (#7062) "admission seam, before the Cedar check": it is the
pre-dispatch transport seam where the CallGate already sits.

**Why not in the core.** `core.CallTool` is re-entered for every inner call of
a code-mode script (`pkg/vmcp/codemode/decorator.go:223-227`) and for every
composite step. A single-use check there would refuse the second inner call,
and it could not see the raw arguments.

### Inputs the middleware needs

| Need | Where it is on main |
|---|---|
| Token claims (`client_id`, `jti`, `exp`, `authorization_details`) | `auth.IdentityFromContext(ctx)` → `Identity.Claims` (`pkg/auth/identity.go:66`) |
| Method, JSON-RPC ID, raw `params` bytes as received | `mcpparser.GetParsedMCPRequest(ctx)` → `ParsedMCPRequest.Method`, `.ID`, `.Params` (`pkg/mcp/parser.go:31-37`). `Params` is the raw `json.RawMessage` from the body, after BOM trimming and before any rewrite. |
| `params.name` | `ParsedMCPRequest.ResourceID` for `tools/call` |
| Tool → server (backend) | `s.core.LookupTool(ctx, identity, name)` → `vmcp.Tool.BackendID` (`pkg/vmcp/core/core.go:148`, `pkg/vmcp/types.go:395`). In Mecatl, `BackendID` equals the profile name, which is what the broker puts in `server`. |
| Batches | Already refused for every caller by `DecodeMessage` (`pkg/mcp/message.go:48`, `pkg/mcp/batch.go:46`). The batch rule in the vMCP issue (#7062) holds today. Keep a test for it. |

### Tool-call match

1. Read `params.arguments` from the **raw** `Params` with a token-level decoder.
   Do not use `ParsedMCPRequest.Arguments`: that map is decoded into `float64`,
   and Go's decoder silently replaces invalid surrogates.
2. Hash it (section 6).
3. Compare `params.name` with the entry's `tool`, the route's `BackendID` with
   `server`, and the hash with `arguments_sha256`.
4. Refuse when `BackendID` is empty. This refuses composite tools, the
   optimizer meta-tools `find_tool` and `call_tool`
   (`pkg/vmcp/session/optimizerdec/decorator.go:24-26`), and code mode's
   `execute_tool_script`. A call-bound token must never reach a tool that
   dispatches further calls.
5. `LookupTool` filters by Cedar. A tool that Cedar hides fails the lookup.
   Treat that as a mismatch and refuse with 403. The outcome is the same as
   before (denied); only the error differs.

Why hashing the raw bytes matters: the Legacy handler forwards the decoded map
(`serve_handlers.go:305`), not the bytes received. The I-JSON refusal is what
makes "hash as received" and "what the backend gets" mean the same value.

### List-call match, and answering with one server's tools

- The Mecatl design (step (9) check 3, and `mcp-authorization.md` "Tool
  discovery") says the vMCP **answers a list-call token with only that
  server's tools**. The vMCP issue (#7062) says only "accepts only that list call on that
  server". Make the filtering explicit in the issue: Mecatl's client relies on
  it and never parses routed tool names itself.
- **Modern** is easy. `dispatchModernToolsList` (`modern_dispatch.go:152`)
  already calls `s.core.ListTools`. Filter by `BackendID` there, or in a helper
  the middleware calls.
- **Legacy** is harder. `tools/list` is answered by the SDK from the session's
  registered tool set (`coreSessionTools`, `serve_handlers.go:128`; lazy
  injection through `AddBeforeListTools`, `serve.go:379`). mcpcompat (v0.0.60)
  has before-hooks only; they cannot filter or refuse, and there is no
  after-list hook. Options:
  - (a) For a list-call token, the middleware answers `tools/list` itself from
    `core.ListTools`, filtered by `BackendID`. It first calls the existing
    ownership check (`checkSessionOwnership`, `session_ownership.go:64`), so a
    foreign session still gets 404. This mirrors Modern.
  - (b) Add a per-request list filter hook to mcpcompat in toolhive-core. That
    is a second repository and a version bump.

  Recommendation: (a).
- Other list methods (`resources/list`, `prompts/list`,
  `resources/templates/list`) carry `BackendID` too. Mecatl uses only
  `tools/list`. Accept the others only if the filtering is implemented for
  them as well; otherwise refuse them for a list-call token.
- **Pagination.** A single-use list token can fetch one page. The Modern page
  size is 1000 (`modern_pagination.go:92`). Either rule that a list-call token
  gets the whole filtered list in one page, or refuse a cursor.

### Single-use record and the open call

- **Consume atomically when the call starts.** Use "set if absent, with TTL".
  The authorization server already does this for assertion `jti`s:
  `ConsumeAssertionJWT` (`pkg/authserver/storage/redis.go:1059`, `SET NX` with
  a TTL). Copy that shape; the vMCP must not import authserver storage.
- **TTL.** `max(exp + skew, start + MaxCallDuration)`. Mark the call ended when
  `next.ServeHTTP` returns. The streamed Legacy POST and the Modern dispatch
  both return when the call completes, so nothing needs to extend the TTL
  later.
- **Order.** A later refusal, such as the session-ownership 404 or a Cedar 403,
  still uses up the token. That is acceptable, because the client never reuses
  `V`: on 404 it gets a new `V` (design step (9) failure table). Note it in
  the code.
- **What does not consume the token.** `initialize`,
  `notifications/initialized` and `ping` while the token is valid. Decide on
  GET (standalone SSE stream) and DELETE (end session); see section 8.
- **Messages after `exp`. This is the hardest part.** The auth middleware
  rejects an expired token with 401 (`pkg/auth/token.go:1083`,
  `ErrTokenExpired`). That happens before the parser runs, so no vMCP
  middleware ever sees a late cancellation or elicitation reply. Accepting
  them needs an exception inside or before token validation, applied only when
  all of these hold:
  - the signature and every claim other than `exp` are valid;
  - the token is call-bound;
  - its `jti` has an open-call record;
  - the message is a `notifications/cancelled` whose `requestId` equals the
    recorded call's JSON-RPC ID, or a JSON-RPC response on the recorded
    session.

  Matching a response to the vMCP's own outstanding elicitation request needs
  the SDK's pending-request IDs, which the vMCP does not see today. A first
  version can accept any response on the recorded session while the call is
  open; say so in the PR. This touches `pkg/auth`, which every MCPServer proxy
  shares, so do it as its own branch.
- **Progress.** Progress notifications normally flow from server to client.
  Check whether the client sends any for the open call, for example during
  sampling. If it does not, drop "progress" from the accept list.

### Refusal on the wire

The vMCP issue (#7062) and the design ask for 403 `insufficient_scope`. Write HTTP 403 with
`WWW-Authenticate: Bearer error="insufficient_scope"` (RFC 6750 §3.1) and a
JSON-RPC error body, like the CallGate's `Denial` (403 with
`mcpparser.JSONRPCCodeDenied`). Mecatl's client treats this as "not sent, do
not retry". The middleware sits inside audit (`server.go:813`), so the refusal
is audited the same way a CallGate denial is. Log the `jti`, `client_id`, the
entry's `operation_id`, method, tool and reason. Never log the token or the
arguments.

### Shared type definition

The vMCP and the authorization server must parse the same entry. The
`as2-call-binding-5-mcp-tool-call-type` branch adds `pkg/mcp/toolcalldetails`
with the type constant and the shape check (donor: "Add the MCP tool-call
details type" (3abb073f2)). Import it from there, or agree with whoever owns
that branch to export the entry struct, so both sides use one definition.
Whichever branch lands first creates it.

### Docs to update in the same PRs

- `docs/arch/10-virtual-mcp-architecture.md`: the new pre-dispatch check and
  where it sits relative to the CallGate.
- `docs/authz.md`, or a vMCP page: the 403 `insufficient_scope` behaviour, the
  embedder setting, and the per-replica limit.

### The shared-record issue (#7261) parts

- **Backend.** The vMCP already has Redis settings for sessions
  (`pkg/vmcp/config/config.go:191`, `SessionStorage`; the client is in
  `pkg/transport/session/storage_redis.go`). Rate limiting already "requires
  Redis session storage" (`config.go:194`). Reuse that connection and key
  prefix.
- **Open-call state** must be readable from another replica. Legacy sessions
  rehydrate across replicas through Redis, so a cancellation can reach a
  replica that did not start the call.

## 6. The argument hash and the shared vectors

**Contract** (from both issues and the Mecatl design, step (8), "The argument hash"):

- The hash is the lowercase hex SHA-256 of the RFC 8785 (JCS) serialization of
  `params.arguments`. Use `{}` when `arguments` is absent. `null` arguments also
  hash as `{}`: decided in "MCP broker authorizes gateway calls and sends them
  to the configured gateway" (stacklok/mecatl#2536) and restated in
  the Mecatl binding ticket's (stacklok/mecatl#2727) scope. Not yet in the vMCP issue (#7062) — confirm.
- Refuse a value that is not exactly one JSON object, or that is not I-JSON:
  invalid UTF-8, lone or reversed surrogate escapes, duplicate member names
  (including ones that are duplicates only after decoding escapes), non-finite
  numbers, and integer-valued numbers with magnitude above 2^53.

**Mecatl's implementation.** `internal/adapter/mcpbroker/argumenthash.go`,
added in "feat(mcpbroker): authorize gateway calls with the identity broker
and send them with a per-call token" (stacklok/mecatl#2818) (4c8816d1b).

- It uses the standard library's `encoding/json/jsontext`: a strict token
  decoder for the I-JSON checks, then `Value.Canonicalize()` for JCS. It adds
  its own number-range check, including an exact test for literals that round
  to ±2^53.
- ToolHive is on Go 1.27, where `jsontext` is standard and needs no
  `GOEXPERIMENT`. **Port the same approach**, with the same library and the
  same checks, so the two sides cannot drift.
- ToolHive does not use `jsontext` anywhere yet. `go.mod` has
  `cyberphone/json-canonicalization` and `go-json-experiment/json`, but only
  as indirect dependencies. Do not add a direct JCS dependency.

**Vectors.** `internal/adapter/mcpbroker/testdata/argumenthash/vectors.json`
on Mecatl `main`.

- 16 vectors, all positive: absent, null and empty arguments; the RFC 8785
  examples; UTF-16 key order; escapes; surrogate pairs; literals; nesting;
  whitespace; number notation up to ±2^53.
- Each vector has a `name`, the raw `arguments`, `canonical` and `sha256`.
  `arguments` is embedded as raw JSON, so read it as `json.RawMessage`. Absent
  means the member is missing.
- The refusal cases are **not** in the file yet; they are inline in
  `argumenthash_test.go` (numbers beyond 2^53, 1e400, lone surrogates,
  duplicate keys, non-object top level, trailing data). The Mecatl binding ticket (stacklok/mecatl#2727)
  plans to add refusal cases to the shared set.

**Vendor or mirror?** Mirror it.

- Copy the file verbatim into the ToolHive package's `testdata/`. Note the
  Mecatl source path and commit next to it (in the test, not in the JSON).
- Add the refusal cases as a second section that both sides can read. Invalid
  UTF-8 cannot appear in a JSON file, so store those raw arguments as base64 or
  as a JSON string of the raw bytes.
- Then propose in the Mecatl binding ticket (stacklok/mecatl#2727) that **ToolHive holds the canonical
  copy**: the entry type is `toolhive.dev`, ToolHive is the public dependency,
  and Mecatl already pins the ToolHive module. Module zips include `testdata`,
  so Mecatl's test can compare its copy with the pinned module's copy, located
  with `go list -m -f '{{.Dir}}'`. The other direction would make ToolHive's
  tests depend on Mecatl.

**An interop catch.** ToolHive's message decoder rejects duplicate keys and
also keys that collide after **case folding**, at any depth, including inside
`arguments` (`pkg/mcp/message.go:19-20`, `pkg/mcp/json_ambiguity.go:79`). The
vector "ASCII key order" has both `"A"` and `"a"`.

- The hash function itself must still pass that vector.
- A real `tools/call` with those arguments is refused by the vMCP with -32600
  before the hash runs. That fails closed, but the end-to-end test cannot use
  that vector.
- Mecatl's client should know that the gateway refuses case-fold-colliding
  keys; otherwise it gets a request error, not a hash mismatch.
- ToolHive's decoder already refuses numbers outside the `float64` range in
  params (`message.go:22-23`). The 2^53 rule and the surrogate rule are not
  checked there, so the hash function must check them itself.

## 7. Dependencies and order

1. **The narrowing issue (#7061) must be in a release before Mecatl can turn anything on.** The
   Mecatl design's rollout order: ToolHive ships the AS-2 change before (or
   with) the AS-1 change; then the vMCP check for `mecatl` and Mecatl's
   sending of entries go out together, after Mecatl pins that release. Mecatl
   pins ToolHive v0.66.0 today; ToolHive is at v0.67.0.
2. **The vMCP issue (#7062) can be built now, in parallel.** It depends only on claims in the
   access token. Tests can mint `at+jwt` tokens by hand with `client_id`,
   `jti`, `exp` and an `authorization_details` entry. The one coupling to
   the narrowing issue (#7061) is the shared entry type (section 5).
3. **The vMCP issue (#7062) must be in the same ToolHive release as the narrowing issue's (#7061) AS-1 and AS-2
   changes**, or an earlier one. The Mecatl binding ticket (stacklok/mecatl#2727) sets the client list in
   the same Mecatl change that bumps the pin.
4. **The shared-record issue (#7261) comes after the vMCP issue (#7062).** It is phase 3 work under the multi-replica parent (stacklok/mecatl#2805)
   and is not needed for Slice 4. It unblocks the cross-replica test ticket (stacklok/mecatl#2970).
5. The related issues in section 2 (#6779), (#6888), (#6979) are independent and need not wait (section 2).

## 8. Risks and open questions

1. **Accepting messages after expiry** (section 5). The biggest change. It
   reaches into `pkg/auth` token validation, which MCPServer proxies share.
   Decide whether it ships in the first release. If not, the design's promise
   that "a user can take a minute to answer an elicitation" fails for tokens
   that live 30 seconds. One way around it: Mecatl's maximum call duration
   would bound the token lifetime instead. That would be a design change.
2. **Listed client without an entry, unlisted client with an entry**
   (section 4). The issue and the design disagree on the first. The second is
   a fail-open gap.
3. **Matching an elicitation reply to the open call.** The vMCP does not see
   the SDK's outstanding server-to-client request IDs. Is "any response on the
   recorded session while the call is open" good enough for the first release?
4. **GET (SSE stream) and DELETE with a call-bound token.** The vMCP issue (#7062) refuses "any
   body that does not hold exactly one parsed operation". GET and DELETE have
   no body. Decide whether they are allowed while the token is valid. Mecatl's
   client may open a GET stream to receive the elicitation request.
5. **List-call tokens on Legacy** need either the middleware to answer the list
   itself or a toolhive-core hook (section 5).
6. **Tools that dispatch further calls.** Composite tools, the optimizer's
   `call_tool`, and code mode's `execute_tool_script` have no single server.
   The recommendation is to refuse them for call-bound tokens. Confirm that
   Mecatl never routes them.
7. **The 403 shape.** `WWW-Authenticate` header, JSON-RPC error body, or both?
   Make sure Mecatl's client classifies it as "not sent".
8. **Clock skew value.** Reuse the token validator's leeway, or add a separate
   setting?
9. **The shared-record issue (#7261) multi-replica guard.** An operator condition (there is precedent) or
   a startup error? The vMCP cannot count replicas.
10. **Where the vectors live**, and the case-fold vector (section 6).

## 9. Suggested branches

Sizes are estimates of non-test Go lines. Each branch is one PR unless noted.

| Branch | Issue | Content | Size |
|---|---|---|---|
| `vmcp-call-binding-1-argument-hash` | The vMCP issue (#7062) | The hash function and I-JSON refusals (a `jsontext` port of Mecatl's), the mirrored vectors plus refusal cases, and a test that the case-fold vector hashes correctly. A leaf package with no callers yet. | S, about 120 + tests |
| `vmcp-call-binding-2-tool-call` | The vMCP issue (#7062) | The `CallBinding` setting on `vmcpserver.Config` and the middleware in `Handler`: tool-call matching (name, `BackendID`, hash), the listed-client rules, refusing tools without a single server, 403 `insufficient_scope` plus the audit log, and the record interface with its in-memory version (consume on start, ended on return, TTL `max(exp+skew, start+MaxCallDuration)`). Handshake and `ping` pass. Plus docs. | M to L, about 350 |
| `vmcp-call-binding-3-list-call` | The vMCP issue (#7062) | List-call tokens. Modern: filter `core.ListTools` by `BackendID`. Legacy: answer in the middleware after the ownership check. A list token cannot start a `tools/call`. The pagination rule. | M, about 200 |
| `vmcp-call-binding-4-open-call-messages` | The vMCP issue (#7062) | Accept cancellation and responses for the open call after `exp`, with the exception in `pkg/auth` validation. Needs its own review, because `pkg/auth` is shared with MCPServer proxies. | M, about 200, the riskiest |
| `vmcp-call-binding-shared-record` | The shared-record issue (#7261) | A Redis implementation of the record interface on the vMCP's session Redis (`SET NX` with TTL, plus open-call state), cross-replica tests, the operator condition, and removal of the per-replica wording. | M, about 250 |

`vmcp-call-binding-2-tool-call` and `vmcp-call-binding-3-list-call` can be one
PR if review capacity allows. `vmcp-call-binding-4-open-call-messages` can
follow in the same release. If it misses the release, Mecatl has to decide
(risk 1) whether to ship the Mecatl binding ticket (stacklok/mecatl#2727) with token lifetimes long enough
to cover the call.

## 10. Sources

- ToolHive issues: the narrowing issue (#7061), the vMCP issue (#7062), the
  shared-record issue (#7261), and the related issues (#6779), (#6888) and
  (#6979) named in section 2 (bodies as of 2026-11-24).
- Mecatl issues, with their dependency links: the binding ticket
  (stacklok/mecatl#2727), the cross-replica test (stacklok/mecatl#2970), the
  slice (stacklok/mecatl#2851), and the multi-replica parent
  (stacklok/mecatl#2805).
- Mecatl design (`docs/drafts/agent-identity/` on Mecatl `main`):
  `credentials-and-lifecycle.md` steps (7), (8) "The argument hash", (8a), (9)
  and "Sessions at the vMCP"; `mcp-authorization.md` "Authorization,
  credentials, and dispatch" and "Tool discovery"; `implementation.md` "Test
  the binding at the vMCP", the ToolHive work list (the vMCP row), "Rollout
  order".
- ToolHive code at 44f7d6368, as cited above.
