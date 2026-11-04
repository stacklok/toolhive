# vMCP Multi Round-Trip Requests (MRTR)

Design for serving MCP 2026-07-28 Multi Round-Trip Requests (SEP-2322) through
the Virtual MCP Server — the Modern replacement for the server-initiated
elicitation/sampling forwarding that exists only on Legacy (2025-11-25)
sessions. This work is tracked by issue **#6059** ("Support MRTR pass-through
for Modern backends in vMCP"), which supersedes the earlier design request
#5759 (closed as not-planned before #6059 was filed); it is the "coherent
future MRTR shape" the client-edge limitation section of
[10-virtual-mcp-architecture.md](10-virtual-mcp-architecture.md) names. That
document carries the **landed rationale** for both current limitations — the
backend-edge one ("elicitation and sampling are unavailable on Modern
backends": empty `clientCapabilities`, `-32021` surfacing) and the client-edge
one (the honest `-32603`/`-32021` contract) — and this document defers to it
for that ground rather than restating it: what follows is the design that
removes the limitations, cell by cell.

Decision framing follows RFC-0083 (`stacklok/toolhive-rfcs`,
`rfcs/THV-0083-stateless-vmcp.md`), which defers MRTR and fixes the
workflow-handle design in its D5; this document sequences D5's enforcement
machinery and specifies the MRTR flows that trigger it.

**Spec sources.** This design follows the finalized MCP 2026-07-28 revision:
`schema/2026-07-28/schema.ts`, `/specification/2026-07-28/basic`, and
`/specification/2026-07-28/basic/patterns/mrtr`, alongside SEP-2322 (Final),
SEP-2577 (Final), and the 2026-07-28 changelog. These are final-version
references; the design does not depend on unpublished draft text.

## Protocol recap (what MRTR actually is)

The 2026-07-28 revision removes server-initiated requests: "Servers **MUST**
send server-to-client requests (such as `roots/list`, `sampling/createMessage`,
or `elicitation/create`) using the MRTR pattern. The previous pattern of
server-initiated requests is no longer supported." (spec, `basic/patterns/mrtr`).

Instead, a server answers a request it cannot complete with a `Result` whose
required `resultType` is `"input_required"`:

```typescript
// schema/2026-07-28/schema.ts
export type InputRequest = CreateMessageRequest | ListRootsRequest | ElicitRequest;
export interface InputRequests { [key: string]: InputRequest; }
export interface InputRequiredResult extends Result {
  inputRequests?: InputRequests;
  requestState?: string;   // opaque to the client
}
export interface InputResponseRequestParams extends RequestParams {
  inputResponses?: InputResponses;
  requestState?: string;
}
```

The client fulfills the `inputRequests`, then **retries the original request**
(new JSON-RPC id — "The JSON-RPC `id` MUST be different between the initial
request and the retry") carrying `inputResponses` and the echoed
`requestState`. The rounds are fully independent requests; any server replica
can serve any round. Only `tools/call`, `resources/read`, and `prompts/get`
may return `input_required` ("Servers **MUST NOT** send `InputRequiredResult`
responses on any other client requests").

Two requirements shape everything below:

- **Capability gating** (server requirement 7): "Servers **MUST NOT** send an
  `inputRequests` that the client has not declared support for in its
  capabilities." Capabilities are declared per request in
  `_meta.io.modelcontextprotocol/clientCapabilities`.
- **`requestState` is attacker-controlled** (server requirements 4–5): the
  minting server "MUST treat `requestState` as an attacker-controlled input",
  must protect its integrity (HMAC/AEAD) when it influences authorization or
  business logic, and SHOULD bind the authenticated principal, a TTL, and an
  originating-request identifier into the protected payload.

## The four bridge cells

vMCP terminates the client connection and re-originates backend calls, so MRTR
has a different shape in each client×backend era combination. The non-MRTR
halves of these cells were built in #6006; this table is the MRTR overlay.

| Downstream client | Backend | MRTR shape |
|---|---|---|
| Modern | Modern | **Stateless pass-through** (the core design, below) |
| Modern | Legacy | **No MRTR bridge — deliberate.** Parking a live Legacy backend call server-side to synthesize `input_required` rounds is per-round server state with token-capability and replica-affinity costs: the session the revision removed, in different clothes. Costed and rejected in the client-edge limitation section of doc 10; the sanctioned stateful path is the Tasks extension (SEP-2663). The call fails with the two-path contract #6061 implements in `writeModernCallFailure`: `-32021` at HTTP 400 with `data.requiredCapabilities` when the client did not declare the capability (go-sdk clients decode the JSON-RPC error body without permanently breaking the connection), or an explicit `-32603` at HTTP 200 naming SEP-2322 when it did. That contract is **permanent for this cell** — MRTR slices supersede it only where the backend is Modern. |
| Legacy | Modern | **In-request bridge**: vMCP fulfills the backend's `inputRequests` through the existing Legacy forwarding seams and retries the backend call itself (below) |
| Legacy | Legacy | Today's server-initiated forwarding, unchanged |

### Cell 1 — Modern client ↔ Modern backend: stateless pass-through

```mermaid
sequenceDiagram
    participant C as Modern client
    participant V as vMCP
    participant B as Modern backend
    C->>V: tools/call (id: 1, _meta.clientCapabilities)
    V->>B: tools/call (id: v1, mirrored clientCapabilities)
    B-->>V: input_required (inputRequests, requestState)
    V-->>C: input_required (relayed verbatim, id: 1)
    note over C: fulfills inputRequests locally
    C->>V: tools/call retry (id: 2, inputResponses, echoed requestState)
    note over V: fresh request — any replica;<br/>backend re-derived from routing table
    V->>B: tools/call retry (id: v2, both fields verbatim)
    B-->>V: complete (id: v2)
    V-->>C: complete (id: 2)
```

For a plain (non-composite) `tools/call` / `resources/read` / `prompts/get`,
vMCP adds **no state of its own**:

1. **Capability mirroring (egress).** On the backend call, vMCP declares in
   `_meta.io.modelcontextprotocol/clientCapabilities` exactly the relayable
   capabilities the downstream client declared on this request — never more
   (vMCP must not solicit `inputRequests` it cannot deliver downstream), never
   less (an empty declaration makes a compliant backend that needs input
   return `-32021 MissingRequiredClientCapability` — mandated by the core
   `_meta` section's MUST, not merely predicted — which is today's behavior:
   `mcpparser.ModernRequestMeta` hardcodes `clientCapabilities: {}`).
2. **`input_required` relay (ingress ← egress).** When the backend returns
   `resultType:"input_required"`, vMCP relays `inputRequests` (values opaque,
   verbatim) and `requestState` (opaque, verbatim) to the client in its own
   `input_required` envelope, under the client's original request id.
3. **Retry relay.** The client's retry arrives as a fresh request carrying
   `inputResponses` + `requestState`. vMCP routes it exactly like the first
   round (routing is deterministic by capability name through the routing
   table) and forwards both fields verbatim to the backend.

**Open question: does the relay pass URL mode through?** A backend's
`inputRequests` can include `elicitation/create` with `mode: "url"`, which
would put a backend-chosen URL in front of the user. Today no backend can do
that: the only URL elicitation vMCP renders is the in-process answer an
embedder's decorator returns (#7022), and a backend's round renders as
`-32603`. Whether the relay forwards URL-mode requests verbatim, filters them,
or requires an opt-in is #6059's call, raised there.

Round-trip state lives **only** in the backend's `requestState`, whose
integrity/user-binding obligations sit with the backend that minted it, per
the spec. Whether the end-to-end principal binding survives that division of
labor **depends on the egress auth strategy**:

- Under the **user-derived** strategies (`token_exchange`, `xaa`,
  `upstream_inject`, `aws_sts`), vMCP's outgoing auth presents the calling
  principal's identity to the backend on every round, so the backend can bind
  its `requestState` to the same principal each time (server requirement 5)
  and reject another user's echo. The claim above holds.
- Under the **shared-credential** strategies — `header_injection` (a static,
  config-mounted secret whose own docstring says it "does not depend on user
  identity") and `unauthenticated` (a no-op) — the backend sees **one vMCP
  identity for every downstream user**. Its principal binding then binds all
  rounds to that shared identity: it cannot protect one downstream user from
  another, because no signal distinguishing them ever reaches it. If
  cross-user misuse of a relayed `requestState` matters for such a backend,
  **vMCP must bind the round to the downstream principal itself, because no
  other component can** — which turns the wrap-vs-verbatim decision below
  from a preference into a configuration-dependent obligation for
  shared-credential egress.

vMCP MUST NOT interpret, rewrite, or append to the relayed `requestState`;
the moment a design change requires vMCP-owned data inside it, that data
moves to the workflow-handle machinery below (D5 protections) or an
equivalent server-side entry, not into ad-hoc fields.

**Open decision vs #6059 (resolve at slice 2/3, flagged, not silently
diverged):** the tracking issue sketches step 2 as "wrap the backend's opaque
`requestState` with vMCP routing context (which backend it came from)". Three
arms, no winner picked here:

1. **Verbatim relay** (this document's default): re-derive the backend on the
   retry from the capability name through the routing table. vMCP never mints
   state, so MRTR server requirements 4–5 (attacker-controlled input,
   integrity protection, principal binding) stay entirely with the backend —
   no vMCP key-management surface. Costs: the rerouted-between-rounds edge
   case, which is client-recoverable (the new backend rejects foreign state
   and re-elicits); and under shared-credential egress it leaves the
   cross-downstream-user gap above unclosed, since the backend cannot close
   it and vMCP added nothing that could.
2. **#6059's wrapper**: vMCP wraps the backend's `requestState` with routing
   context. The moment vMCP injects its own data, it becomes a state-minting
   server under requirements 4–5; the wrapper must carry D5-grade protections
   (integrity, principal binding, TTL) — cryptographic keys and their
   management, the surface arm 1 avoids.
3. **Server-side handle over a verbatim backend round**: relay verbatim *to
   the backend*, but make the **downstream-visible** `requestState` an opaque
   handle into a server-side entry `{backend, capability,
   owner=binding.Format(iss, sub), backend requestState, TTL}` — the same D5
   handle machinery the composite slice below already builds, and **no keys
   at all** (the state never leaves the server, so there is nothing to sign
   or seal). This closes cross-principal replay, cross-backend A→B
   substitution, and the shared-credential gap above in one move. Its cost is
   exactly the durable store this design otherwise keeps off the pass-through
   path: every `input_required` round writes an entry, and plain-tool relay
   stops being stateless at vMCP.

Arm 1 maximizes statelessness, arm 3 maximizes containment with composite-
slice machinery instead of cryptography, arm 2 buys arm 3's properties at the
price of a key-management surface and is dominated by it unless the store is
unavailable. If shared-credential egress must support MRTR with cross-user
protection, arm 1 alone cannot deliver it (see above); the resolution at
slice 2/3 must say which arm serves that configuration.

Failure modes, all client-recoverable per the spec's error-handling guidance
(a server that got unusable `inputResponses` "SHOULD respond with a new
`InputRequiredResult` requesting the missing information again"):

- Backend set changed between rounds and the tool re-routed: the new backend
  fails `requestState` validation and re-elicits or errors — no vMCP handling.
- Backend sends an `inputRequest` whose capability the client (and therefore
  vMCP's mirrored declaration) did not declare: a backend protocol violation.
  vMCP fails the call with an explicit error naming the violation; it never
  forwards a request the downstream client cannot fulfill (server
  requirement 7 applies to vMCP as a server in its own right).
- Unbounded re-elicitation: vMCP relays rounds without counting (the client
  owns its retry budget; go-sdk's client middleware caps at 10 rounds, 3
  consecutive load-shedding rounds). vMCP's per-request deadline bounds each
  individual round.

### Cell 3 — Legacy client ↔ Modern backend: in-request bridge

The inverse direction bridges cleanly because the blocking machinery already
exists. When a Modern backend returns `input_required` during a **Legacy**
client's call, vMCP — inside the same in-flight downstream request, on one
pod — fulfills each `inputRequest` through the existing server-initiated
forwarding seams (`vmcp.ElicitationRequester`, `vmcp.SamplingRequester`, both
capability-gated on what the Legacy client advertised at `initialize`), then
retries the backend call with the collected `inputResponses` and echoed
`requestState`. Precedent: go-sdk's `serverMultiRoundTripMiddleware`
performs exactly this bridge for its handlers ("When a handler returns
InputRequests and the client does not support multi-round-trip, the middleware
fulfills the requests by calling the client directly and reinvokes the
handler"). Round count is bounded (mirror go-sdk's 10/3 caps) and the loop is
single-request-scoped: no durable state, no handle, no replica concern —
the downstream Legacy session vMCP already holds provides the delivery
channel.

`ListRootsRequest` `inputRequests` are refused explicitly on this cell: vMCP
has never had a roots forwarding seam, and roots is SEP-2577-deprecated — an
explicit error beats building a new seam for a feature in its removal window.

## Composite (workflow) tools: where durable state actually starts

The pass-through above is stateless because vMCP is a relay. Composite tools
invert that: **vMCP is the server** executing a multi-step workflow
(`pkg/vmcp/composer`), so when a round trip interrupts a workflow, the
suspended state is vMCP's own. Two triggers:

- a workflow step's Modern backend returns `input_required`, or
- the workflow itself needs user input (the composer's elicitation handler,
  today implemented only over a Legacy session's `ElicitationRequester`).

For a **Legacy** downstream client, neither suspends anything: the existing
blocking seams answer mid-call on the live session, and composites keep
executing synchronously inside one `tools/call` (RFC-0083 Alternative 3 —
the pod-local `InMemoryWorkflowStateStore` remains correct).

For a **Modern** downstream client there is no session to block on, so the
workflow must **suspend**: vMCP returns `input_required` to the client with
the workflow's outstanding `inputRequests`, and the retry — a fresh request,
possibly on another pod — must **resume** it. This, precisely, is the trigger
RFC-0083 D5 sequences the durable machinery on:

- **Round state**: the suspended `WorkflowStatus` (accumulated step outputs,
  position, pending input keys) persists in a `WorkflowStateStore` backed by
  Redis, injected via a `core.Config` seam (per RFC-0083's API-changes list).
  It is too large and too sensitive to round-trip through the client, so the
  client-visible `requestState` is a **handle**, not serialized state.
- **Handle format** (RFC-0083 D5, followed verbatim): a server-minted,
  ≥128-bit opaque random token; an owner field on `WorkflowStatus` holding
  the authenticated `(iss, sub)` pair encoded exactly as the existing
  identity binding does — `pkg/vmcp/session/binding`'s
  `Format(iss, sub)`, which returns `iss + "\x00" + sub` (and an error for
  empty components), plaintext, not hashed — so the two owner-binding
  mechanisms stay consistent.
- **Owner check on resume**: the retry's authenticated identity must satisfy
  `binding.Format(iss, sub) == status.Owner` before any state is loaded;
  a mismatch is an audit-visible security signal (owner-mismatch resume),
  fails closed, and reveals nothing about the handle's existence.
- **TTL at write** (bounding the replay window per MRTR server requirement 5)
  and **redaction**: the handle never appears in audit or telemetry output.
- **With authenticated incoming auth**, this satisfies the spec's requirement
  to "cryptographically bind the data to the original user": the state never
  leaves the server, and the handle is an unguessable capability whose owner
  is checked server-side — stronger than client-side AEAD, with no
  key-management surface.
- **Under `incomingAuth: anonymous`** the owner check is vacuous: every
  anonymous session stores the same `binding.UnauthenticatedSentinel`, and
  `validateCallerBinding` accepts any anonymous caller
  (`pkg/vmcp/session/internal/security`), so "the original user" is not a
  concept the deployment has — the owner-mismatch audit signal can never
  fire, and protection reduces to handle unguessability plus TTL. Slice 5
  **accepts this as a documented single-tenant caveat rather than refusing to
  suspend**: anonymous auth already grants any caller the tools themselves,
  so a stolen handle adds no *tool-invocation* privilege the trust model
  doesn't already concede — the thief could have run the workflow outright.
  It does add a *data-read* privilege the trust model does not concede:
  resuming loads another caller's accumulated step outputs, produced from
  arguments the thief could not necessarily have supplied, so that
  intermediate data is not something they could have obtained on their own —
  a real residual exposure, gated on handle secrecy alone, which the
  redaction requirement above covers. Refusing would break composites for
  exactly the single-operator deployments anonymous auth exists for. A
  multi-user deployment fronting vMCP with anything but authenticated
  incoming auth gets no cross-user workflow isolation, and slice 5's docs
  must say so where `anonymous` is documented.

**The distinction that sizes the infrastructure**: plain-tool pass-through
rounds and the Legacy-client bridge need *no* durable store; only
cross-request workflow suspend/resume does. The store, its injection seam,
the owner field, and redaction all land with the composite slice — nothing
earlier.

## What replaces `ElicitationRequester`

Nothing replaces it one-for-one; the model inverts. `ElicitationRequester` /
`SamplingRequester` are *push* seams — block an in-flight call while the
server asks the client. They remain the implementation of the two Legacy
cells (unchanged) and become the fulfillment mechanism inside the
Legacy-client bridge (cell 3). On Modern paths, the seam is a *value*, not a
call: `input_required` results flow back through the ordinary return path —

- domain: `vmcp.InputRequiredResult` (`inputRequests` as opaque raw values
  keyed by string, plus `requestState`), carried by a typed error from
  `BackendClient` so every existing complete-result path is untouched;
- core: `core.CallTool`/`ReadResource`/`GetPrompt` propagate it (suspending a
  workflow first when the caller is the composer);
- server: `dispatchModern` renders it as the `resultType:"input_required"`
  envelope through `writeModernInputRequired`, and accepts
  `inputResponses`/`requestState` params on the retry. The writer is shared:
  today it renders the URL elicitation answer
  (`vmcp.URLElicitationRequiredError`), converted to an `InputRequiredResult`
  at render time.

## Sampling: deprecated pass-through, no feature work

Explicit answer to "does sampling deserve an MRTR path": **vMCP builds no
sampling feature; sampling flows through the generic relay only.**

- The MRTR union still carries it: `InputRequest = CreateMessageRequest |
  ListRootsRequest | ElicitRequest` (schema/2026-07-28, above). SEP-2577's
  explicit union freeze ("The following union types reference deprecated
  types but MUST NOT be modified during the deprecation period",
  `seps/2577-deprecate-roots-sampling-and-logging.md`) enumerates only
  `ClientNotification`, `ClientResult`, `ServerRequest`, and
  `ServerNotification` — `InputRequest` is **not** on that list (it
  postdates the SEP), so its shape rests on the schema as published plus the
  SEP's blanket "features remain fully functional during the deprecation
  window" guarantee, not on a named freeze. Same practical effect during the
  window; re-check the union at each revision rather than assuming it frozen.
- SEP-2577 (Final) deprecates sampling — along with roots and logging, the
  latter irrelevant to MRTR — as of 2026-07-28: "New implementations SHOULD
  NOT add support for deprecated features unless needed for backward
  compatibility with existing counterparts" (SEP-2577, Capability
  negotiation section), with "integrate directly with LLM provider APIs" as
  the sanctioned replacement.
- vMCP's position under that carve-out: the pass-through relay is
  **type-agnostic** — vMCP forwards whatever `inputRequests` the backend sent
  and the client declared capability for, without interpreting them, so
  sampling transits for the existing counterparts that still declare it; that
  is compatibility plumbing, not added support. The one sampling-specific
  code path retained is the cell-3 bridge fulfilling a sampling
  `inputRequest` through the *existing* `SamplingRequester` — again existing
  counterparts, no new capability. No new sampling machinery of any kind is
  built, and when the deprecation window closes, sampling disappears from
  vMCP by clients/backends dropping it, with zero vMCP code to remove beyond
  the bridge's fulfillment case.

Roots: same union, same deprecation, but unlike sampling there is no existing
vMCP counterpart (no roots forwarding seam has ever existed) — so roots gets
relay-only transit on cell 1 and an explicit refusal on cell 3.

## Gaps in the pinned SDK surface (upstream candidates for toolhive-core)

Verified against go-sdk and toolhive-core's `mcpcompat`:

1. **mcpcompat still drops MRTR fields in both directions.** go-sdk's
   `mcp.CallToolResult` exports `InputRequests` and `RequestState`, but
   `mcpcompat/mcp.CallToolResult` only records the private `resultType` used by
   `NeedsInput()`; it has no fields for the input requests or request state.
   Likewise, mcpcompat's `CallToolParams` has no `inputResponses` or
   `requestState`, so its public client cannot carry a retry round. The payload
   is lost on decode and cannot be sent back on retry. Upstream ask: add both
   field pairs to the compatibility types.
2. **No public one-shot client call path.** go-sdk and mcpcompat can
   negotiate Modern with `server/discover` and issue calls through a
   `ClientSession`. ToolHive's `modernCall` intentionally uses a one-request,
   identity-scoped HTTP path instead, without retaining a backend
   `ClientSession`; replacing that path requires a separate design for
   per-identity projection, error mapping, and per-request build cost.
3. **MRTR helpers and Modern result annotations remain unexported.** go-sdk's
   automatic client retry middleware and its helpers for fulfilling
   `InputRequests` and constructing retry parameters are private. On the server,
   `resultType`, `setCompleteResultType`, and `annotateServerInfo` are also
   unexported and run inside `ServerSession` dispatch, which the stateless Modern
   path bypasses. ToolHive therefore still hand-builds the response envelope to
   emit `resultType` and `_meta.serverInfo` correctly. An upstream ask is to
   expose stateless client/server MRTR primitives.
4. **`ServerSession.assertServerInitiatedRequestAllowed` is version-based.** In
   go-sdk it rejects elicitation, sampling, and roots on protocol version
   2026-07-28 without consulting per-request capabilities. That matches the
   final revision's removal of server-initiated requests; a declared capability
   does not restore the Legacy request path.
5. **The old -32021 status workaround is obsolete.** go-sdk decodes a
   non-2xx JSON-RPC error body and wraps it with `ErrRejected`, so a per-call
   HTTP 400 does not permanently break the client connection by default. ToolHive
   now sends the undeclared-capability `-32021` at the required HTTP 400; the
   declared-capability MRTR limitation remains an explicit `-32603` at HTTP 200.

## Sequencing (collision-aware)

Collision set at time of writing: #6050 (modern pagination/subscriptions —
rewrites `modern_dispatch.go`/`modern_envelope.go`) and #6051 (Legacy-pins
the forwarding tests, adds doc 10's client-edge limitation section) have
both **merged** (2026-07-28); #6033 (kill-switch removal —
`classification.go`, `server.go`, `serve.go` and test helpers) remains open.
Slices are ordered so each lands without touching #6033's files until it
merges:

1. **Egress MRTR surface (safe now; implemented alongside this doc).** Domain
   types `vmcp.InputRequiredResult` and `vmcp.InputRequiredError` (carrier and
   `InputRequiredFromError` extractor live beside the domain type, so slice
   2's server-side rendering never imports the concrete HTTP client and mock
   consumers can construct the error); `pkg/vmcp/client`'s Modern shim decodes
   an `input_required` envelope into that typed, `errors.Is`-compatible error
   instead of the opaque `errModernInputRequired`. Extraction is
   **fail-closed**: a round is extractable only on the three methods that may
   carry one, only for `resultType:"input_required"` exactly, and only when
   the payload decodes strictly and satisfies server requirement 6 — every
   violation keeps pure sentinel semantics, so a slice-2 consumer can never
   relay a malformed or misplaced round. `RequestState` is a `*string`, since
   client requirement 2 makes absent-vs-present-and-empty an observable
   distinction the retry must preserve. No behavior change: capabilities are
   still declared empty, so a compliant backend cannot yet send
   `input_required`; the seam #6051 calls "the egress half" simply becomes
   load-bearing. Files: `pkg/vmcp/mrtr.go`, `pkg/vmcp/client/modern.go` (one
   hook), `pkg/vmcp/client/modern_mrtr.go` — none touched by #6033.
2. **Ingress envelope + retry params (after #6050/#6033).** `dispatchModern`
   accepts `inputResponses`/`requestState`, threads them through
   `core.CallTool`→`BackendClient`, and renders `input_required` envelopes.
   The envelope writer already exists:
   `writeModernInputRequired` (`pkg/vmcp/server/modern_envelope.go`, #7022)
   takes a `*vmcp.InputRequiredResult` and is what the URL elicitation answer
   renders through today (doc 10, "Answering a call with a URL elicitation").
   This slice reuses it for relayed backend rounds rather than adding a second
   writer. Retries already pass through the parser untouched, so this slice
   adds the threading, not acceptance. Today `dispatchModern` reads
   `inputResponses` only to honor a decline or cancel of the URL elicitation
   answer (`writeModernToolCallFailure`); it does not pass them on. Post-#6061, the rendering branch
   hooks into the Modern call-failure classifiers via
   `vmcp.InputRequiredFromError`: `writeModernToolCallFailure` for
   `tools/call` (which already handles the URL elicitation answer there) and
   `writeModernCallFailure` for `resources/read` and `prompts/get`. It sits
   alongside (never instead of) their authz-priority and capability-refusal
   branches — the two cannot co-occur: the refusal recorder fires only on
   the Legacy-backend forwarding seams, `input_required` only from a Modern
   backend's envelope.
3. **Capability mirroring (activates cell 1; with slice 2, this is what
   issue #6059 tracks).** `ModernRequestMeta` gains a capabilities argument;
   dispatch threads the downstream declaration to the egress call (reusing
   #6061's `modernClientDeclaredCapability` reading of `_meta`); the relay
   goes live end-to-end. The `requestState` open decision above (verbatim vs
   #6059's routing-context wrapper) must be resolved here at the latest. The
   undeclared-capability `-32021` contract landed ahead of this in #6061 at the
   spec-required HTTP 400. When this slice lands, #6061's declared-capability
   `-32603` message ("multi-round retrieval … which this server does not
   implement") stops being true for Modern backends and needs rescoping to
   the Legacy-backend cell it permanently serves.
4. **Cell-3 bridge.** The in-request fulfillment loop over
   `ElicitationRequester`/`SamplingRequester` with go-sdk-mirrored round caps.
5. **Composite suspend/resume.** Durable `WorkflowStateStore` (Redis) behind
   the `core.Config` seam, D5 handle + owner check + TTL + redaction, resume
   dispatch, owner-mismatch audit signal.

Slices 2–5 each update doc 10's client-edge limitation section (whose current
text says backend-relayed MRTR is unserved) and the forwarding-test dispositions
from #6051 as their claims become false.

## Testing strategy

- Slice 1: envelope decode (payload extraction, `errors.Is` back-compat with
  the sentinel, fail-closed rejection of malformed/empty/misplaced rounds),
  no-behavior-change pins.
- Slice 2/3: dispatcher round-trip — `input_required` envelope shape
  (resultType, echoed id, relayed keys), retry threading, capability-gate
  refusals (undeclared → no forward; backend violation → explicit error).
- Cell 1 end-to-end: a Modern fake backend demanding one elicitation round;
  assert both rounds' independence (distinct ids, verbatim
  `requestState` echo, no vMCP-side state between them — serve round 2 from
  a *fresh server instance* to prove it).
- Cell 3: Legacy downstream client + `input_required` Modern backend; assert
  fulfillment via the existing seams, round caps, and the roots refusal.
- Slice 5: owner-binding (B cannot resume A's workflow), TTL expiry,
  Redis-unavailable fail-closed — the RFC-0083 test list, verbatim.

## References

- SEP-2322 (Final), SEP-2577 (Final), SEP-2663 (tasks redesign);
  `schema/2026-07-28/schema.ts` and `/specification/2026-07-28/basic/patterns/mrtr`.
- RFC-0083 (`stacklok/toolhive-rfcs`) — D5, Alternative 3, implementation
  plan's deferred list.
- [10-virtual-mcp-architecture.md](10-virtual-mcp-architecture.md) — the
  client-edge limitation section (added by #6051) this design supersedes
  step by step; forwarding seams; #6006 bridge cells.
- Issues: #5743 (epic); **#6059 (tracks the MRTR pass-through this document
  designs — supersedes #5759, the earlier not-planned design request)**;
  #6018 (shim retirement); #5959/#6033 (kill-switch); #6050
  (pagination/subscriptions).
- Adjacent Modern-path issues this design deliberately does NOT cover:
  **#6058** — per-request SSE streaming for `notifications/progress`/
  `notifications/message`; distinct from MRTR (those are notifications, not
  server-initiated requests — MRTR does not touch them), but slice 2's
  `input_required` envelope may ride the same future stream as its final
  message, per the spec's "MAY be sent … as the final message on an SSE
  stream". **#6064** — `ping` removal and the required `resultType` on the
  dispatch path; pure conformance, orthogonal to MRTR. **#6065** —
  `subscriptions/listen` push delivery; a different channel with a fixed
  four-type subscribable set, structurally disjoint from MRTR rounds.
