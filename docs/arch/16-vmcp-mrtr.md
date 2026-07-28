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

**Spec sources.** Written against the 2026-07-28 revision as published in
`modelcontextprotocol/modelcontextprotocol@main` on 2026-07-27, one day before
the revision's finalization date, when it still lived at `schema/draft`
(`LATEST_PROTOCOL_VERSION = "2026-07-28"`): `schema/draft/schema.ts`,
`/specification/draft/basic/patterns/mrtr`, SEP-2322 (Status: Final), SEP-2577
(Status: Final), and the draft changelog. Anything cited from those sources
should be re-verified against the final text once cut; the MRTR SEP and schema
types were already Final at time of writing.

*Re-checked 2026-07-28 (the nominal finalization date): the revision is
still not cut — no `schema/2026-07-28` directory exists,
`/specification/2026-07-28/` pages 404, and `schema/draft/schema.ts` is
byte-identical to the copy this design was written against. The checklist
below therefore remains pending the final cut.*

**Draft-only dependencies (re-verify against the final cut).** The schema
types and the SEPs are Final and low-risk. Four load-bearing points in this
design rest on the draft *spec page's* text, which refines (or diverges from)
the Final SEP and could still move before the final cut:

- **The three-method table.** SEP-2322 (Final) also allows
  `InputRequiredResult` on `GetTaskPayloadRequest`; the draft page dropped it
  when tasks moved to an extension. If the final restores a fourth method,
  the relay's method allow-list grows — a table edit, not a design change.
- **Capability gating** (server requirement 7, "MUST NOT send an
  `inputRequests` that the client has not declared") is page-only — SEP-2322
  does not state it. Capability mirroring is designed around it; if the final
  weakens it to SHOULD, mirroring remains correct (it is also what makes the
  relay deliverable), only the backend-violation error becomes discretionary.
- **Server requirement 6** ("at least one of `inputRequests` or
  `requestState`") is page-only; it affects only what vMCP treats as a
  backend protocol violation.
- **The `requestState` security language** differs between the two: SEP-2322
  says user-specific state "MUST use some mechanism to cryptographically
  bind" it to the user, while the page allows omitting integrity protection
  "only when tampering can cause nothing worse than request failure". The
  workflow-handle design (below) satisfies the stricter reading — state never
  leaves the server and the handle is owner-checked — so it is robust to
  either landing in the final.

## Protocol recap (what MRTR actually is)

The 2026-07-28 revision removes server-initiated requests: "Servers **MUST**
send server-to-client requests (such as `roots/list`, `sampling/createMessage`,
or `elicitation/create`) using the MRTR pattern. The previous pattern of
server-initiated requests is no longer supported." (spec, `basic/patterns/mrtr`).

Instead, a server answers a request it cannot complete with a `Result` whose
required `resultType` is `"input_required"`:

```typescript
// schema/draft/schema.ts
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
| Modern | Legacy | **No MRTR bridge — deliberate.** Parking a live Legacy backend call server-side to synthesize `input_required` rounds is per-round server state with token-capability and replica-affinity costs: the session the revision removed, in different clothes. Costed and rejected in the client-edge limitation section of doc 10; the sanctioned stateful path is the Tasks extension (SEP-2663). The call fails with the two-path contract #6061 (merged) implements in `writeModernCallFailure`: `-32021` + `data.requiredCapabilities` when the client did not declare the capability, an explicit `-32603` naming SEP-2322 when it did. That contract is **permanent for this cell** — MRTR slices supersede it only where the backend is Modern. |
| Legacy | Modern | **In-request bridge**: vMCP fulfills the backend's `inputRequests` through the existing Legacy forwarding seams and retries the backend call itself (below) |
| Legacy | Legacy | Today's server-initiated forwarding, unchanged |

### Cell 1 — Modern client ↔ Modern backend: stateless pass-through

For a plain (non-composite) `tools/call` / `resources/read` / `prompts/get`,
vMCP adds **no state of its own**:

1. **Capability mirroring (egress).** On the backend call, vMCP declares in
   `_meta.io.modelcontextprotocol/clientCapabilities` exactly the relayable
   capabilities the downstream client declared on this request — never more
   (vMCP must not solicit `inputRequests` it cannot deliver downstream), never
   less (an empty declaration makes a compliant backend that needs input
   return `-32021 MissingRequiredClientCapability`, which is today's behavior:
   `mcpparser.ModernRequestMeta` hardcodes `clientCapabilities: {}`).
2. **`input_required` relay (ingress ← egress).** When the backend returns
   `resultType:"input_required"`, vMCP relays `inputRequests` (values opaque,
   verbatim) and `requestState` (opaque, verbatim) to the client in its own
   `input_required` envelope, under the client's original request id.
3. **Retry relay.** The client's retry arrives as a fresh request carrying
   `inputResponses` + `requestState`. vMCP routes it exactly like the first
   round (routing is deterministic by capability name through the routing
   table) and forwards both fields verbatim to the backend.

Round-trip state lives **only** in the backend's `requestState`, whose
integrity/user-binding obligations sit with the backend that minted it, per
the spec. The end-to-end principal binding survives because vMCP's outgoing
auth (OBO / header injection / token exchange) presents the calling
principal's identity to the backend on every round — the backend binds its
state to the same principal each time. vMCP MUST NOT interpret, rewrite, or
append to the relayed `requestState`; the moment a design change requires
vMCP-owned data inside it, that data moves to the workflow-handle machinery
below (D5 protections), not into ad-hoc fields.

**Open decision vs #6059 (resolve at slice 2/3, flagged, not silently
diverged):** the tracking issue sketches step 2 as "wrap the backend's opaque
`requestState` with vMCP routing context (which backend it came from)". This
design deliberately relays it **verbatim** instead, re-deriving the backend on
the retry from the capability name through the routing table — because the
moment vMCP injects its own data into `requestState`, vMCP becomes a state-
minting server under MRTR server requirements 4–5 (attacker-controlled input,
integrity protection, principal binding), a key-management surface the
verbatim relay avoids entirely. The cost is the rerouted-between-rounds edge
case, which is client-recoverable (the new backend rejects foreign state and
re-elicits). If review concludes the wrapped form is preferable, the wrapper
must carry the D5-grade protections, not a bare backend ID.

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
`requestState`. Precedent: go-sdk v1.7's own `serverMultiRoundTripMiddleware`
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
  `Format(iss, sub)` → `iss + "\x00" + sub`, plaintext, not hashed — so the
  two owner-binding mechanisms stay consistent.
- **Owner check on resume**: the retry's authenticated identity must satisfy
  `binding.Format(iss, sub) == status.Owner` before any state is loaded;
  a mismatch is an audit-visible security signal (owner-mismatch resume),
  fails closed, and reveals nothing about the handle's existence.
- **TTL at write** (bounding the replay window per MRTR server requirement 5)
  and **redaction**: the handle never appears in audit or telemetry output.
- This satisfies the spec's requirement to "cryptographically bind the data
  to the original user": the state never leaves the server, and the handle is
  an unguessable capability whose owner is checked server-side — stronger
  than client-side AEAD, with no key-management surface.

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
  envelope, and accepts `inputResponses`/`requestState` params on the retry.

## Sampling: deprecated pass-through, no feature work

Explicit answer to "does sampling deserve an MRTR path": **vMCP builds no
sampling feature; sampling flows through the generic relay only.**

- The MRTR union still carries it: `InputRequest = CreateMessageRequest |
  ListRootsRequest | ElicitRequest` (schema/draft, above) — deprecated types
  stay in the unions during the window ("The following union types reference
  deprecated types but MUST NOT be modified during the deprecation period",
  SEP-2577).
- SEP-2577 (Final) deprecates sampling as of 2026-07-28: "New implementations
  SHOULD NOT add support for deprecated features unless needed for backward
  compatibility with existing counterparts", with "integrate directly with
  LLM provider APIs" as the sanctioned replacement.
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

Verified against go-sdk `v1.7.0-pre.3` (the transitive pin via
`toolhive-core v0.0.34`) and `toolhive-core/mcpcompat`:

1. **mcpcompat drops MRTR fields in both directions.**
   `mcpcompat/mcp.CallToolResult` carries only an unexported `resultType`
   (populated by `UnmarshalJSON`, feeds `NeedsInput()`); it has **no**
   `InputRequests`/`RequestState` fields, so a decoded `input_required`
   result loses its payload. `CallToolParams` (`Name`, `Arguments`, `Meta`,
   `Task`) cannot carry `inputResponses`/`requestState` on a retry. Upstream
   ask: add both field pairs (go-sdk's own `mcp.CallToolResult` already
   exports `InputRequests InputRequestMap` and `RequestState string`).
2. **No no-initialize client primitive** in mcpcompat's public API (tracked
   as #6018) — the reason `pkg/vmcp/client`'s hand-rolled `modernCall` exists
   at all, and therefore where MRTR egress decode/retry params must live
   until #6018's upstream work lands.
3. **go-sdk's MRTR internals are unexported where vMCP needs them.** The
   client retry loop exists (`clientMultiRoundTripMiddleware`,
   `MultiRoundTripOptions.Disabled` for manual handling) but its pieces
   (`fulfillInputRequests`, `setMultiRoundTripRetryParams`) are unexported;
   server-side, `resultType` is settable only via unexported
   `setResultType`/`setCompleteResultType` inside `ServerSession` dispatch —
   exactly the dispatch the stateless Modern path bypasses — so the
   hand-rolled `modern_envelope.go` must emit `input_required` itself.
   Upstream ask (shared with #6018's step 1): exported stateless
   client/server MRTR primitives.
4. **`ServerSession.assertServerInitiatedRequestAllowed` gates by negotiated
   protocol version only** (`v1.7.0-pre.3` `mcp/server.go:1544-1553`:
   `iparams.ProtocolVersion >= protocolVersion20260728`), never consulting
   capability declarations. Harmless for this design — the seams it guards
   are only used on Legacy sessions — but it forecloses any notion of
   re-enabling server-initiated requests for a capability-declaring Modern
   client. Accounted for, no action.
5. **`-32021` at HTTP 400 is unshippable** with current go-sdk clients (their
   transient set is 500/502/503/504/429; any other 4xx is permanent session
   death), which is why the undeclared-capability error lands at HTTP 200 as
   a documented deviation — implemented by #6061
   (`writeModernMissingCapability`), tracked upstream as go-sdk#1117; MRTR
   does not change it, it *removes the case* for capabilities the client
   does declare against Modern backends.

## Sequencing (collision-aware)

In-flight at time of writing: #6050 (modern pagination/subscriptions —
rewrites `modern_dispatch.go`/`modern_envelope.go`), #6033 (kill-switch
removal — `classification.go`, `server.go`, `serve.go` and test helpers),
#6051 (Legacy-pins the forwarding tests, adds doc 10's client-edge limitation
section). Slices are ordered so each lands without touching those files until
its predecessors merge:

1. **Egress MRTR surface (safe now; implemented alongside this doc).** Domain
   type `vmcp.InputRequiredResult`; `pkg/vmcp/client`'s Modern shim decodes an
   `input_required` envelope into a typed, `errors.Is`-compatible error
   carrying the payload instead of the opaque
   `errModernInputRequired`. No behavior change: capabilities are still
   declared empty, so a compliant backend cannot yet send `input_required`;
   the seam #6051 calls "the egress half" simply becomes load-bearing.
   Files: `pkg/vmcp/mrtr.go`, `pkg/vmcp/client/modern.go` (one hook),
   `pkg/vmcp/client/modern_mrtr.go` — none touched by the in-flight PRs.
2. **Ingress envelope + retry params (after #6050/#6033).** `dispatchModern`
   accepts `inputResponses`/`requestState`, threads them through
   `core.CallTool`→`BackendClient`, and renders `input_required` envelopes;
   parser vocabulary for the two params. Post-#6061, the rendering branch
   hooks into `writeModernCallFailure` via `InputRequiredFromError`,
   alongside (never instead of) its authz-priority and capability-refusal
   branches — the two cannot co-occur: the refusal recorder fires only on
   the Legacy-backend forwarding seams, `input_required` only from a Modern
   backend's envelope.
3. **Capability mirroring (activates cell 1; with slice 2, this is what
   issue #6059 tracks).** `ModernRequestMeta` gains a capabilities argument;
   dispatch threads the downstream declaration to the egress call (reusing
   #6061's `modernClientDeclaredCapability` reading of `_meta`); the relay
   goes live end-to-end. The `requestState` open decision above (verbatim vs
   #6059's routing-context wrapper) must be resolved here at the latest. The `-32021`-shaped
   undeclared-capability error contract landed ahead of this in #6061
   (merged 2026-07-28; `-32021` at HTTP 200 as the documented deviation,
   upstream go-sdk#1117). When this slice lands, #6061's declared-capability
   `-32603` message ("multi-round retrieval … which this server does not
   implement") stops being true for Modern backends and needs rescoping to
   the Legacy-backend cell it permanently serves.
4. **Cell-3 bridge.** The in-request fulfillment loop over
   `ElicitationRequester`/`SamplingRequester` with go-sdk-mirrored round caps.
5. **Composite suspend/resume.** Durable `WorkflowStateStore` (Redis) behind
   the `core.Config` seam, D5 handle + owner check + TTL + redaction, resume
   dispatch, owner-mismatch audit signal.

Slices 2–5 each update doc 10's client-edge limitation section (whose current
text truthfully says MRTR is unserved) and the forwarding-test dispositions
from #6051 as their claims become false.

## Testing strategy

- Slice 1: envelope decode (payload extraction, `errors.Is` back-compat with
  the sentinel, malformed-payload tolerance), no-behavior-change pins.
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
  `schema/draft/schema.ts` and `/specification/draft/basic/patterns/mrtr` as
  of 2026-07-27.
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
