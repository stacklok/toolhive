# Stateless vMCP Support (MCP 2026-07-28)

## Overview

This document designs dual-protocol operation for the Virtual MCP Server (vMCP) so it
can serve both the current stable MCP revision (2025-11-25) and the upcoming stateless
revision (2026-07-28) on the same endpoint, discriminating per request rather than
requiring a cutover — and, as an aggregating gateway, **bridge** a client on either
revision to backends on either revision.

It is a design/sequencing document (tracking issue
[#5756](https://github.com/stacklok/toolhive/issues/5756)). It is the vMCP counterpart to
the transport-proxy design (`stateless-transport-proxies-design.md` — pending in toolhive
PR #5808, not yet merged to `docs/arch/`; RFC THV-0081, #5755), which explicitly **defers the
cross-generation bridge to vMCP**.
The go-sdk v1.7 bump (#5754), MRTR-based elicitation/sampling passthrough (#5759), parser
2026-07-28 vocabulary (#5757), and caching metadata (#5761) are separate efforts (see
[Out of scope](#out-of-scope)). File and line references point into `pkg/vmcp/` and
`pkg/mcp/`; line numbers drift, so re-locate by symbol name where they no longer match.

**Terminology** (the draft's own terms, used throughout): **Legacy** = the ≤2025-11-25,
`initialize`-based, session-ful revision (`RevisionLegacy` in `pkg/mcp`). **Modern** = the
2026-07-28 stateless revision (`RevisionModern`, `MCPVersionModern = "2026-07-28"`). **Dual-era**
= a client/server that probes-then-falls-back across both. "Stateless" and "session-ful" describe
the Modern and Legacy transport shapes respectively; the terms are used interchangeably with the
revision names below.

## Why this exists

The 2026-07-28 revision removes the mechanisms vMCP's session layer is built around:

- the `initialize` handshake (replaced by per-request `_meta` carrying `protocolVersion`,
  `clientInfo`, `clientCapabilities`, plus a `server/discover` RPC),
- protocol sessions and the `Mcp-Session-Id` header (traffic becomes stateless),
- the standalone GET SSE stream and SSE resumability,
- all server-initiated requests (replaced by **Multi Round-Trip Requests (MRTR)**, SEP-2322),
- `ping`, `logging/setLevel`, and the `notifications/roots/list_changed` notification.

(The Logging *capability* is deprecated, not removed; only the `logging/setLevel` method is
removed. Roots and Sampling are likewise deprecated but remain functional during the window.)

The ecosystem will straddle both revisions for a long time: SDKs (go-sdk v1.7, TS SDK v2)
serve both on one endpoint, and clients, servers, and backends upgrade independently. vMCP
therefore needs **per-request revision discrimination on the client edge** and **per-backend
revision handling on the backend edge**, not a flag day — and, because it is an aggregator
that terminates the client connection and re-originates backend calls, it is the component
that can bridge the era-mismatched combinations a transparent proxy cannot.

## Background: vMCP's core is already stateless — the session layer is a bridge

The single most important fact for this design: **vMCP's core is already stateless with
respect to MCP sessions.** `coreVMCP` (`pkg/vmcp/core/core_vmcp.go`) re-derives everything
per call from `(identity, backend registry, health)`. Its own doc comment states it:
*"It is stateless with respect to MCP sessions: every List/Lookup/Call re-derives the advertised
capability view by health-filtering the backend registry and aggregating on demand ('the core
filters, Serve caches' — caching is added by a Serve decorator, not here)."* That parenthetical is
the load-bearing part: the caching the Modern path removes lives in the Serve decorator, not the
core. `ListTools` is literally aggregate → `admission.FilterTools(ctx, identity, …)`. **That is
per-principal projection, computed per request** — precisely what the stateless revision asks
for ("per-principal projection derived from auth context per request").

All the stateful machinery the tracking issue enumerates lives one layer up, in the **Serve
transport layer** (`pkg/vmcp/server`, `pkg/vmcp/session`), and exists for exactly one reason:
to bridge the stateless core to a *session-ful protocol*. Specifically:

| Mechanism | Location | Purpose | Fate under Modern |
| --- | --- | --- | --- |
| Two-phase `SessionIdManager` / SDK `OnRegisterSession` hook | `server/serve.go`, `server.go` | Create a `MultiSession` when `initialize` fires | Not triggered — no `initialize` |
| `MultiSession` (live backend conns + routing table + tool lists) | `pkg/vmcp/session` | Hold per-session runtime state | Replaced by per-request core calls + shared backend conns |
| 1,000-entry per-pod LRU cache | `sessionmanager/factory.go:defaultCacheCapacity` | Cap live sessions per pod | Dissolves — no sessions to cache |
| Redis session metadata (`transportsession.DataStorage`, `session_data_storage.go`) | `pkg/transport/session` | Persist session for cross-pod restore | Not written on the Modern path |
| Identity binding / hijack prevention | `pkg/vmcp/session/binding`, `internal/security` | Stop a different caller reusing a session id | Moot — no session id to steal; every request is independently authenticated |
| `lazyInjectSessionTools` cross-pod rehydration | `server/server.go:1064` | Re-inject tools when a client lands on a different pod | Dissolves — nothing to rehydrate |
| `sessionAffinity: ClientIP` (CRD) | operator | Sticky-route session-ful clients | Unnecessary for Modern clients |

The design is therefore mostly about **deleting a bridge on the Modern path**, not building
new machinery — the direction this repo's own `.claude/rules/security.md` already mandates
("Prefer Stateless Routing Over Stored Routing"; "Don't Store Internal Addressing in Shared
State"). The genuinely new work is on the **backend edge** (talking statelessly to Modern
backends, and synthesizing sessions for Legacy backends on behalf of a stateless client) and
in **workflow handle state**.

The one caveat unique to vMCP (and not shared with the transport proxies): the Serve path is
built on toolhive-core's `mcpcompat` mcp-go server (`serve.go` imports
`mcpcompat/server`), whose session lifecycle is driven by `initialize`. THV-0081's
"don't gate on go-sdk v1.7" argument (Alternative 2) does **not** transfer wholesale — but it
mostly does, because the Serve path already dispatches straight to the core: on
`s.core != nil`, session registration only sources the advertised set from `core.ListTools`
and installs SDK handlers that call `core.CallTool` / `core.ReadResource` / `core.GetPrompt`
directly (`serve_handlers.go`). See [Server side](#server-side-modern-bypasses-the-serve-session-layer)
for how the Modern path reuses that direct dispatch without the SDK session object.

## How it works

### Revision classification (reuse `pkg/mcp`)

The primitive already exists and is unit-tested: `mcp.ClassifyRevision` / `mcp.ExtractMeta`
(`pkg/mcp/revision.go`, added for the streamable proxy in #5839). `MCPVersionModern =
"2026-07-28"`; the draft error codes (`-32020` HeaderMismatch, `-32021`
MissingRequiredClientCapability, `-32022` UnsupportedProtocolVersion) are defined there. vMCP
reuses it verbatim at the client edge. The spec's Modern signal is an exact `MCP-Protocol-Version:
2026-07-28` header match; the shipped classifier *additionally* treats any of the three named
reserved `_meta` keys (`io.modelcontextprotocol/protocolVersion`, `clientInfo`,
`clientCapabilities`) as a signal — a pragmatic heuristic, not spec text, so a header-less body
that carries them is still classified Modern. A request is **Legacy** when neither signal is
present, with `method == "initialize"` marking a Legacy session start. A Modern signal is never
silently downgraded; malformed metadata on a Modern-signalled request is an error, not a Legacy
fallback (whereas an unrecognized header value *alone*, with no reserved keys, classifies Legacy
by design — the body `_meta` is authoritative for the version).

`_meta` is not the whole story on Streamable HTTP. Per MCP Versioning/Transports and SEP-2243
(HTTP header standardization), a Modern HTTP request MUST carry an `MCP-Protocol-Version`
header that matches `_meta.io.modelcontextprotocol/protocolVersion`; a mismatch is a hard
`-32020` rejection (neither value wins), which `ClassifyRevision(method, meta, protoHeader)`
already enforces through its `protoHeader` argument. The Modern request headers `Mcp-Method`
(all requests) and `Mcp-Name` (`tools/call`, `resources/read`, `prompts/get`) must likewise be
validated against the decoded body at the same decode seam. A `Mcp-Name` value carrying
non-header-safe characters is carried with the Base64 sentinel encoding
(`Mcp-Name: =?base64?{value}?=`), the same rule as `Mcp-Param-*` — so validation must decode it,
not string-compare the raw header. vMCP passes the header to the classifier and validates the
method/name headers there; it does not treat `_meta` alone as authoritative.

> Note: SEP-2243 predates the draft's error-code renumbering and still cites `-32001` for
> HeaderMismatch; the shipped value is `-32020` (renumbered per changelog minor change #12). Use
> the `-3202x` codes, not the SEP's originals.

### Server side: Modern bypasses the Serve session layer

A Modern-classified request takes a **thin dispatcher** that skips the entire SDK/mcpcompat
session object:

1. Reconstruct identity from the token (middleware chain, unchanged — auth is not on the
   session axis).
2. Validate the request via `mcp.ClassifyRevision` (with the `MCP-Protocol-Version` header;
   `_meta` pulled by `mcp.ExtractMeta`). `io.modelcontextprotocol/protocolVersion` and
   `io.modelcontextprotocol/clientCapabilities` are **required** on every Modern request
   (`clientInfo` is optional). The spec's general rule is "a request missing any required field is
   malformed → `-32602` / HTTP `400`", but the shipped classifier (`pkg/mcp/revision.go`) is
   normative here and partitions the failures more finely — the dispatcher rejects with whatever
   `ClassifyRevision` returns:
   - **missing `protocolVersion`, reserved-key signal only, no header** → `MissingModernMetadataError`
     = **`-32602`** (the spec defines no dedicated code for this reserved-key-only case);
   - **missing/mismatched `protocolVersion` *with* a header present** → `HeaderMismatchError` =
     **`-32020`** (the body has nothing valid to match the header against);
   - **`protocolVersion` names a non-Modern version** → `UnsupportedVersionError` = **`-32022`**;
   - **missing `clientCapabilities`** → `MissingClientCapabilityError` = **`-32021`** (note the
     draft distinguishes *absent* capabilities from *present-but-insufficient*; the classifier
     currently maps absence here).

   The Phase 1 classifier therefore *validates presence* of `clientCapabilities`, it does not
   merely extract it for telemetry. One open edge the classifier flags in a `TODO`: it is
   transport-agnostic and cannot yet distinguish "stdio, no header concept" (where `-32602` is
   right) from "HTTP, header omitted" (where the draft makes a missing `MCP-Protocol-Version` a
   `-32020` condition) — the doc tracks this rather than asserting a single code for that case.
3. Dispatch by method directly to the core, which already does per-identity projection and
   admission:
   - `server/discover` → a version/capability envelope (see [server/discover](#serverdiscover));
     it does NOT enumerate items.
   - `tools/list` / `resources/list` / `prompts/list` → the matching `core.List*(ctx, identity)`.
   - `tools/call` → `core.CallTool(ctx, identity, name, args, meta)` (`meta` converted with the
     existing `conversion.FromMCPMeta`); `resources/read` → `core.ReadResource`; `prompts/get`
     → `core.GetPrompt`.
   - a **notification** (no JSON-RPC `id` — e.g. any `notifications/*`) gets no response body:
     acknowledge with `202`/`204` and drop it. The SDK server absorbs these today; the hand-rolled
     dispatcher must define the rule explicitly. (`notifications/initialized` specifically is moot
     under Modern — there is no `initialize` handshake to follow — but the general rule is not.)
   - any *request* method the Modern path does not implement (a stray `ping`,
     `resources/subscribe`, `logging/setLevel`, etc.) MUST return HTTP `404` + JSON-RPC `-32601`
     (method not found) — this is the exact signal dual-era clients probe on, and the mirror of the
     backend-revision detection in [Client side](#client-side-dual-protocol-per-backend).
   - a **JSON-RPC batch** (a top-level array) is **rejected** with a defined error. The SDK server
     absorbs batches today; on the hand-rolled dispatcher a batch would otherwise reach the
     call-gate and per-element audit blind. 2026-07-28 does not rely on batching, so per-element
     dispatch is needless complexity — reject rather than partially implement (input validation on
     a hand-rolled parser is not something to simplify away).
4. Serialize the core result to JSON **in the Modern result shape**. The draft requires each
   result to carry a `resultType`, whose only defined literals are `"complete"` and
   `"input_required"` (underscore) — there is no `"incomplete"` state. Additional values are an
   extension point, but only when advertised/negotiated via capabilities: a recipient that does
   not recognize a `resultType` (and has not negotiated the extension) MUST treat the result as
   invalid, so the serializer emits only `"complete"` unless an extension is in play. The unary
   MVP emits `resultType: "complete"`; `"input_required"` is the sole MRTR state and is deferred
   to #5759. **This serialization is the one genuinely new piece — and a temporary bridge:**
   bypassing the SDK server means producing the full JSON-RPC response envelope (id correlation,
   error-object mapping, the per-method result shapes for `tools/call` vs `resources/read` vs
   `prompts/get`, error-vs-result framing) that `StreamableHTTPServer` produces today. Wherever
   possible, lift the SDK's battle-tested marshaling rather than reinventing it, and plan to retire
   the hand-rolled envelope entirely once go-sdk v1.7's stateless server mode ships. No
   `Mcp-Session-Id` is minted; no `MultiSession` is created; no Redis is written; no binding check
   runs (there is no session to bind).

**Where the seam lives.** `(*Server).Handler` wraps the shared middleware chain (recovery →
body-limit → header-validation → header-capture → auth → audit → `mcpparser.ParsingMiddleware`
→ telemetry → `WriteTimeout` SSE-deadline clearing at `server.go:655`) *externally* around a
single SDK server, assigned at `var mcpHandler http.Handler = streamableServer` (`server.go:607`).
Every stage in that list — including the SSE write-deadline handling — is external, so it
survives the bypass by construction; only the concerns *inside* the SDK server (below) are at
risk. Because parsing and auth run before that line, the parsed request and identity are already
in context there. **The integration point is that line:**
replace `streamableServer` with a classifying `http.Handler` that reads the parsed request,
and routes Modern requests to the hand-rolled dispatcher and everything else to the SDK server.
The branch is "**instead of** entering the SDK server," not "before a stage inside it" — session
validation lives *inside* `NewStreamableHTTPServer`, not in the external chain. The external
chain stays intact by construction.

**What is NOT free on the Modern path** (these are attached to the SDK server being bypassed,
not the external chain, so they must be re-invoked by the dispatcher):

- **The authz call-gate.** `WithCallGate(s.authzCallGate())` is a `NewStreamableHTTPServer`
  *option* (and only present when `s.authzGateEnabled`, i.e. Cedar is configured) — it runs
  *inside* the SDK server. The authorization *decision* is not lost (the core re-runs the same
  admission `Check*` on `CallTool`/`ReadResource`/`GetPrompt`), but the *wire behavior* is:
  pre-dispatch `403`-before-`404`, JSON-RPC 403, and the audit `outcome:"denied"` that depends
  on the gate emitting a 403 the audit middleware observes. If the dispatcher surfaces a denial
  as a 200 tool-error (the natural shape of a direct core call), audit records `success`. The
  dispatcher MUST re-invoke `authzCallGate` (a standalone context-reading closure, reusable) and
  translate `*server.Denial` → HTTP 403 itself.
- **Header-forward application.** `CaptureMiddleware` (external chain) only stuffs allowlisted
  headers into context; today they are *applied* by merging into the per-session backend client's
  header-forward config. The Modern path has no per-session backend client, so capture survives
  but application does not — the per-request/pooled Modern backend connection must read
  header-forward from context and inject it explicitly.
- **Elicitation is structurally incompatible**, not merely deferred. The elicitation adapter is
  bound to the SDK `MCPServer` and driven through the SDK `ClientSession`; with no SDK session on
  the Modern path, SDK-routed elicitation cannot run. Composite-workflow elicitation on the
  Modern path must go through MRTR (#5759).

Telemetry spans, audit *middleware*, parsing, recovery, and body-limit are external and survive.
**Rate limiting also survives** — but for a different reason than the external chain: the vMCP
rate limiter is a **core decorator** (`vmcpratelimit.NewDecorator(coreVMCP, …)` at `server.go:465`,
active when configured), so the Modern dispatcher's direct `core.CallTool` call passes through it
exactly like admission does. It gates `CallTool` (not per-HTTP-request) and requires Redis. The
one open throttling gap is orthogonal to Modern: composite-workflow step-rate limiting is still a
`TODO` (`composer/workflow_engine.go`, security review M-4). Getting the branch point right —
re-homing the call-gate and header-forward, not skipping any middleware — is the main server-side
implementation risk.

This reuses the core dispatch the Serve path already performs (`serve_handlers.go`), minus the
SDK session wrapper, the binding decorator (`enforceSessionBinding`), and the two-phase hook.
It removes the hard *gate* on go-sdk v1.7: the unary MVP is a hand-rolled dispatcher over the
already-directly-callable core (mirroring how the transport proxies hand-roll their Modern path).
But v1.7 is the intended end state for the envelope specifically — adopting
`StreamableHTTPOptions.Stateless` **retires** the hand-rolled marshaling rather than merely
simplifying it. We decline to *block* on the SDK, not to use it.

Legacy requests continue through today's `initialize` → `OnRegisterSession` → `MultiSession`
path unchanged. **Zero behavior change for 2025-11-25 traffic.**

### `server/discover`

`server/discover` is `initialize` minus the session. Per the MCP draft (Discovery), it returns
a version/capability envelope: `supportedVersions`, server `capabilities`, optional
`instructions`, cache fields, and server identity under
`_meta["io.modelcontextprotocol/serverInfo"]` — and creates nothing. Crucially, per the draft
schema `DiscoverResult.capabilities` is **capability flags only** (`tools?: {listChanged?}`,
`resources?: {subscribe?, listChanged?}`, `prompts?: {listChanged?}`, `logging?`, `completions?`,
`experimental?`, `extensions?`) — it structurally **cannot** carry `Tool[]`/`Resource[]`/`Prompt[]`
arrays. The actual item lists stay in `tools/list` / `resources/list` / `prompts/list`, which the
Modern dispatcher routes to `core.List*(ctx, identity)` (admission-filtered, composite-inclusive).

`DiscoverResult` embeds `CacheableResult`, whose `ttlMs` and `cacheScope` fields are
**required** (no `?` in the schema). Full caching semantics are deferred to #5761, but the Phase 2
envelope cannot omit them — an omitting response is not schema-valid. Emit explicit no-cache
values in the interim (`ttlMs: 0`, `cacheScope: "private"`).

**Authorization is still load-bearing — but the risk is a topology leak, not descriptor
enumeration.** `server/discover` is *intentionally* absent from the authz map
(`MCPMethodToFeatureOperation`) today and therefore default-denied (403) — see the comment at
`pkg/authz/middleware.go:57` and `middleware_test.go:470`, which cite the response enumerating
descriptors and bypassing `ResponseFilteringWriter` (that filter only covers
`tools/list`/`prompts/list`/`resources/list`/`find_tool`). Per the current draft schema that
comment overstates the shape: discover cannot leak per-identity descriptors because it carries no
item arrays. The **real** residual risk is finer — the capability *flags* are themselves a
per-identity signal: advertising that a backend/capability exists to an identity not admitted to
any item behind it is a topology/existence leak. So the gate is not `ResponseFilteringWriter`
over descriptors (there are none); it is that **the capability envelope must be computed from the
post-admission projection** — a capability flag appears only when the identity is admitted to ≥1
item behind it (run capability aggregation through the same admission seam `core.List*` uses; an
empty admitted set for a backend yields no advertised capability). This is a genuine difference
from THV-0081: a pass-through proxy labels the method and lets the backend filter; vMCP *serves*
discover itself, so it owns the (post-admission) envelope. This is real design/build work, **not**
a no-behavior-change parser registration. Parser vocabulary (labeling the method,
`Mcp-Method`/`Mcp-Name`) is #5757; the authz-map entry is gated on the post-admission envelope.

### Client side: dual-protocol per backend

Each backend is handled according to **its own** negotiated revision (dual-protocol per
backend, independent of the client edge):

- **Modern backend:** stateless calls — no `initialize`, no `Mcp-Session-Id`, protocol
  metadata carried per request in `_meta`. This requires the backend connection abstraction to
  gain a **no-session mode**: today `backend.Session` *always* initializes and *always* carries a
  `Mcp-Session-Id` (`pkg/vmcp/client/client.go:675`, `pkg/vmcp/session/internal/backend/mcp_session.go:409`,
  both calling `Initialize` with `mcp.LATEST_PROTOCOL_VERSION`). Making the backend session id
  optional touches a core abstraction — so even the "easy" Legacy-client→Modern-backend cell is
  not free.
- **Legacy backend:** today's session-ful `backend.Session` (initialize + `Mcp-Session-Id`),
  unchanged.
- **`x-mcp-header` / `Mcp-Param-{Name}` (SEP-2243).** As an aggregator, when vMCP calls a Modern
  backend whose tool `inputSchema` declares `x-mcp-header`, it MUST construct the corresponding
  `Mcp-Param-*` headers (Base64-sentinel encoding where required) on outgoing `tools/call` POSTs,
  and implement the `HeaderMismatch → refetch tools/list → retry` client flow. Symmetrically, if
  vMCP forwards such a backend schema unchanged to a *Modern client*, its own server-edge
  dispatcher inherits the MUST-validate obligation for `Mcp-Param-*`. Not in the MVP header set
  (`MCP-Protocol-Version`/`Mcp-Method`/`Mcp-Name`); tracked as designed-deferred work.

Backend revision must be detected once per backend (probe `server/discover`; or inspect the
`initialize`/handshake response; or classify the `404`/`-32601` or `400` body on an HTTP backend
that rejects the handshake — the mirror of the server edge's unrecognized-method handling).
**There is no data-model support for this today** — the backend paths always initialize through
the SDK client (`client.go:675`, `mcp_session.go:409`). The design must add a place for the
negotiated era to live and a rule for invalidating it. The **wrong** seam is a `Revision` field on
the registry entry (`vmcp.Backend`): the `immutableRegistry` (static/CLI mode) is build-once and
`Get` returns a copy — there is no write path — and only the operator writes via
`DynamicRegistry.Upsert`. The **right** seam is the health/probe subsystem that already re-probes
backends and is keyed by backend ID (exactly where `HealthStatus` is tracked out-of-band rather
than on the registry entry): store the negotiated era there, re-evaluated on each probe so an
in-place backend upgrade is re-classified. Reuse the subsystem's *keying and lifecycle*, but note
the era probe is a **different request shape** than the health ping — it needs `server/discover`
or a handshake classification, and `ping` itself is removed under Modern, so the health ping alone
cannot classify a Modern backend. Note the mild tension with "prefer stateless routing over stored
routing": a cached era can go stale between probes.

Define the failure mode for a mid-flight misclassification (a backend that flips era between
probes — a correctness/trust-boundary issue, not injection): any `-32601`/`404` or handshake
rejection on the backend edge is a **re-classification trigger** — force a re-probe and retry
under the corrected era. For a genuinely unclassified backend, follow the draft's dual-era
guidance and **probe Modern-first** (`server/discover` or a modern-shaped request), falling back to
`initialize` only on a non-modern error — a Legacy handshake against a Modern-only backend (which
has removed `initialize`/`ping`) is a guaranteed reject, **not** a compatibility-safe superset. The
re-classification, not any default, is the correctness guarantee; the cost is at most one wasted
round-trip per newly-seen backend.

### The dual-era matrix

vMCP sits between a client and N backends and **terminates + re-originates** on both edges.
That is what lets it bridge the cells the transparent proxy cannot (THV-0081 marks the
off-diagonal "Fails" for a pass-through proxy and defers them here). The client edge and each
backend edge are classified independently:

| Client → vMCP | vMCP → Backend | vMCP behavior |
| --- | --- | --- |
| Legacy | Legacy | Today's path, unchanged. |
| Modern | Modern | Both edges stateless; the clean target case. |
| **Legacy** | **Modern** | vMCP terminates the client session (existing machinery) and issues **stateless** calls downstream. Straightforward — vMCP owns the client session; the backend needs none. |
| **Modern** | **Legacy** | vMCP must **synthesize and hold** a backend session on behalf of a stateless client (the agentgateway `stateless_send_and_initialize` pattern). This is the one genuinely new hard cell. See below. |

**Modern-client / Legacy-backend — the hard cell.** A stateless client sends no session id, so
the synthesized backend session cannot be keyed on one. Two sub-cases:

- **Inside a composite workflow:** key the backend session on the **server-minted workflow
  handle** (see [Workflow handle state](#workflow-handle-state)). The handle is the stable
  identifier that survives across the workflow's steps and pods.
- **A bare, single Modern tool call:** there is no cross-request state to preserve, so the
  backend session is *ephemeral* — opened, used, and closed within the request (per-request
  model, [below](#backend-connection-management)).

**Never share a synthesized Legacy-backend session across principals.** A Legacy backend session
is stateful server-side — the backend may bind the identity/OBO established at `initialize`, plus
cursors and subscriptions. Sharing one synthesized session across vMCP principals runs B's calls
inside a session initialized for A's identity: a confused-deputy / cross-tenant leak at the
backend edge, and a loss of the per-request-auth invariant precisely where it matters most.
Synthesized Legacy sessions MUST be partitioned by `(principal, backend)` (or kept strictly
ephemeral per request); only a backend whose session state is provably identity-independent may
be shared, and that must be asserted, not assumed.

The load-bearing half of that mitigation is *which identity the synthesized `initialize`
presents*: it MUST be the **calling principal's** credentials via vMCP's *existing* outgoing-auth
strategies (OBO / header injection / token exchange — not new to this design, and orthogonal to
statelessness; the same per-request outgoing
auth the non-synthesized path uses), never a shared vMCP service identity — otherwise every
principal's calls run under one backend identity and the partition is cosmetic. The partition key
therefore derives from the authenticated identity, not from the workflow handle alone (the handle
scopes *which* held session, the identity scopes *whose*).

**Mixed and partial states.** The matrix treats each backend edge as a settled era, but a
composite workflow can span both: because each backend edge is classified independently (above),
the workflow engine sees per-backend era — a Legacy backend and a Modern backend in the same
workflow each use their own edge semantics, no workflow-global era. Two residual states: a backend
*upgraded mid-workflow* (narrow, given composites execute synchronously within one request — see
[Phase 2](#phase-2--server-side-modern-stateless-path)), and a backend that is *unclassifiable*
(fail closed — see the era-detection failure mode in [Client side](#client-side-dual-protocol-per-backend)).

### Workflow handle state

The tracking issue says "Redis remains for workflow/handle state only" and that "workflow
state must key on server-minted handles, not session IDs." A favorable finding narrows the
work: the composer state store is **already keyed on `WorkflowID`, not the MCP session id**
(`pkg/vmcp/composer/state_store.go`, `workflow_engine.go`). So this is not a re-keying
project. Two changes remain:

1. **Server-minted opaque handle, bound to its creator.** Formalize `WorkflowID` as a durable,
   opaque, vMCP-minted handle (≥128-bit CSPRNG) returned in a tool-call result and passed back as
   a tool *argument* on the next step — the "explicit server-minted handles passed as tool
   arguments" the stateless revision prescribes. **A random token alone is not enough** (see the
   owner-binding invariant below): `WorkflowStatus` (`composer/composer.go`) currently stores no
   owner and `LoadState`/`SaveState` key on `WorkflowID` with no identity, so admission-re-check
   alone lets any principal admitted to the same tools resume another's workflow. Persist the
   creating principal on `WorkflowStatus` and enforce `identity == owner` on every resume, plus a
   TTL/expiry and revocation (the store today only garbage-collects *terminal* workflows, so a
   non-terminal handle is otherwise an indefinitely valid bearer capability). The owner is the
   authenticated `(iss, sub)` pair (issuer + subject — `sub` is unique only within an issuer, so both
   are needed), stored **plaintext exactly as the existing identity binding does**
   (`pkg/vmcp/session/binding` `Format(iss, sub)` → `iss + "\x00" + sub`, per #5306) so the two
   owner-binding mechanisms stay consistent — no separate hashing.
2. **Pluggable Redis backing — and the injection seam that does not exist yet.** A Redis
   `WorkflowStateStore` implementation is the easy half. The harder half: `core.New`
   **constructs the in-memory store internally** (`pkg/vmcp/core/core_vmcp.go:147`,
   `composer.NewInMemoryStateStore(...)`), so there is no way to inject a Redis-backed store
   today. Inject a *built* `WorkflowStateStore` into `core.Config` (dependency, not config —
   go-style "constructors accept dependencies/interfaces"); resolve the memory-vs-Redis choice at
   the composition root from the same Redis config the Serve session storage reads, and hand
   `core.New` the finished store. Do **not** thread `SessionStorageConfig` down into `core.New`
   (that crosses the transport→domain boundary and is config-object-everywhere, anti-pattern #6).
   Also fix ownership: `core.New` today `Stop()`s the store via an `interface{ Stop() }`
   duck-cast and WARNs if absent — a Redis store's `Stop()` is a benign no-op that would trip that
   spurious WARN, so make the lifecycle contract explicit rather than duck-typed. Only with the
   durable store can any pod resume a workflow by handle ("any pod can serve any request"). Follow
   the go-style rule *Write to Durable Storage Before Updating In-Memory State*.

Elicitation-resume within a workflow rides on MRTR (#5759), not on this store.

**Redis store posture.** The workflow-handle store must state its operational posture: keyspace
separation from Legacy session metadata (distinct key prefix or logical DB so the two cannot
collide or be confused in an audit of the store), TTL enforced *at write* (not only via GC of
terminal workflows), and a defined behavior when Redis is unavailable — fail closed for resume
(a resume that cannot verify owner/state is rejected), with any single-pod in-memory fallback
emitting a loud signal rather than silently degrading "any pod can serve any request."

The handle is a bearer capability that travels through tool-argument payloads — which flow
through the parser, audit, and telemetry. It must be redacted there (go-style "never log
credentials/tokens"), but **that redaction is a build item, not a config toggle**: `pkg/audit`
today offers only whole-body switches (`IncludeRequestData`/`IncludeResponseData`) and event-type
exclusion — no field-level redaction. Either add field-level redaction to `pkg/audit` or model the
handle as a structured, redacted field rather than a free tool argument. Passthrough rule: only
the workflow-resume argument may carry the handle; it MUST be stripped from any backend-bound call
(a composite step's arguments to a backend must not leak the vMCP handle). Combined with the owner
binding above, a captured handle is then not by itself sufficient to resume.

### Security invariants

- **Auth is per-request and unchanged.** Authentication runs in the middleware chain before
  routing; making Modern traffic sessionless moves no request off the authenticated path. The
  per-request `_meta` fields are client-controlled telemetry/labels, **never a security
  principal** — admission continues to key off the authenticated identity, exactly as
  `core.admission.FilterTools` does today.
- **Identity binding relocates to the handle; it is not simply removed.** The
  binding/hijack-prevention machinery (`internal/security`) defends a *session id* against reuse
  by a different caller. For a stateless *single request* there is nothing to hijack — each
  request stands on its own authenticated identity, and the binding decorator does not run. But
  the cross-request binding invariant does not vanish; it **moves** to the workflow handle, which
  must carry its own owner binding (above). Replay protection is unchanged from today (bearer
  token, no `jti`/nonce — per the M2M audit); the new non-terminal handle adds a fresh replayable
  bearer artifact that a bound, TTL'd handle keeps time-limited.
- **No internal addressing in shared state.** The Redis workflow-handle store must persist a
  logical handle and workflow state only — never backend URLs/pod addresses (security rule).
  Routing is re-derived from the backend registry at use time.
- **Server-minted handles are capabilities: unguessable AND owner-bound.** A random ≥128-bit
  token plus an admission re-check is necessary but **not sufficient** — admission authorizes
  *tool access*, not *ownership of this workflow instance*, so two principals with the same tool
  grants would otherwise share instances. Enforce `identity == workflow.owner` on every resume in
  addition to the admission re-check, give handles a TTL + revocation, and redact them from
  audit/telemetry (see [Workflow handle state](#workflow-handle-state)).
- **Never share a synthesized Legacy-backend session across principals** — partition by
  `(principal, backend)` or keep ephemeral (see [the hard cell](#the-dual-era-matrix)).
- **Pooled backend connections carry zero credential state.** The shared per-backend pool (below)
  must hold only a bare transport; auth is a per-call header with no client-level default, and OBO
  tokens are cached by `(subject, backend, scope)` — never by backend alone — so no principal's
  credentials bleed onto another's pooled call.

## Implementation phases

Grouped by component; each is independently shippable and everything classifies as Legacy by
default, so existing traffic is unchanged until a Modern request arrives. Scope mirrors
THV-0081: **unary + correctness first; streaming, subscriptions, and MRTR designed-deferred.**

### Phase 1 — Classifier wiring + parser vocabulary (no routing change)

Wire `mcp.ClassifyRevision` at the vMCP client-edge decode seam (with the `MCP-Protocol-Version`
header and `Mcp-Method`/`Mcp-Name` validation), **validating presence** of the required
`protocolVersion` and `clientCapabilities` `_meta` fields and rejecting with the classifier's
per-case code (the `-32602`/`-32020`/`-32021`/`-32022` mapping enumerated in
[Server side](#server-side-modern-bypasses-the-serve-session-layer)). Label `server/discover` in the parser and
source `clientInfo`/`protocolVersion` from `_meta` for telemetry. **Do not** allow-list
`server/discover` in the authz map here — it stays default-denied until Phase 2 gives it a
post-admission envelope (see [`server/discover`](#serverdiscover)); parser labeling alone is not
authorization. No routing change yet.

### Phase 2 — Server-side Modern stateless path

Add the Modern dispatcher at `server.go:607` (replacing `var mcpHandler = streamableServer`)
that calls the core directly for `tools/list`, `tools/call`, `resources/read`, `prompts/get`,
hand-rolling the JSON-RPC response envelope in the Modern `resultType: "complete"` shape and
returning `404`/`-32601` for methods it does not implement. **Re-home what the bypassed SDK
server owned:** re-invoke `authzCallGate` and translate `Denial→403` (preserving the audit
`outcome:"denied"`), and apply header-forward from context on the outgoing backend call. Serve
`server/discover` as a **post-admission** version/capability envelope **and** add its authz-map
entry together with that envelope — the two land as one change, since the entry without the
post-admission computation is the topology leak the current code guards against. No session, no
`Mcp-Session-Id`, no Redis, no binding check. Legacy path untouched.

Composite tools work on the Modern path in this phase with the pod-local in-memory state store:
`core.CallTool` executes a composite **synchronously within the single `tools/call`** (`ExecuteWorkflow`
runs to a terminal state and returns in the same request), so the state is request-local and
"any pod can serve any request" holds — each request is self-contained on whatever pod it lands.
The durable cross-pod handle store (Phase 4) is needed only once a workflow *suspends and resumes*
across requests (MRTR, #5759), which this phase does not introduce. So no composite exclusion and
no early store-injection is required here — this is single-*request*-scoped, not single-pod.

### Phase 3 — Client-side per-backend dual protocol

Detect and cache each backend's revision in the health/probe subsystem (not the registry entry —
see [Client side](#client-side-dual-protocol-per-backend)). Give `backend.Session` a **no-session
mode** so it can call a Modern backend without `initialize`/`Mcp-Session-Id`; keep the session-ful
`backend.Session` for Legacy backends. **Start with per-request backend connections**
([below](#backend-connection-management)); the shared pool is a follow-up.

### Phase 4 — Off-diagonal bridge cells

Legacy-client / Modern-backend (easy: terminate client session, re-originate stateless) and
Modern-client / Legacy-backend (synthesize + hold a backend session for the duration of the single
request — pod-local, since composites execute synchronously; ephemeral for a bare call). Depends on
Phases 2–3. **No durable store required** — both sub-cases are single-request-scoped on one pod;
the durable handle store, its `core.Config` injection seam, and the handle owner-check/redaction
first have a trigger with cross-request suspend/resume (MRTR, #5759), not here.

### Deferred (designed, built separately)

- **Request-scoped streaming** (one POST → N `notifications/progress` then a terminal
  response): additive on the same seam THV-0081 describes; unary responses only in MVP.
- **Subscriptions** (`subscriptions/listen`): long-lived POST-response stream + streaming
  authorization filter; gated on demonstrated demand.
- **`x-mcp-header` / `Mcp-Param-{Name}` (SEP-2243)**: as an aggregator, vMCP must construct
  `Mcp-Param-*` headers (with the HeaderMismatch→refetch→retry flow) when calling Modern backends
  whose tool schemas declare them, and must validate them on the server edge if it forwards such
  schemas to Modern clients. Out of the MVP header set; sequenced after the diagonal cells work.
- **MRTR-based elicitation/sampling passthrough** (#5759): server-initiated requests are
  removed in 2026-07-28; composite workflows that elicit must key their resume point on the
  workflow handle and use MRTR. On the Modern server edge this is not merely deferred — SDK-routed
  elicitation is structurally unavailable (no SDK session), so MRTR is the only path. Designed
  there, not here. Note the MRTR `requestState` security shape mirrors this doc's workflow-handle
  invariants — integrity-protection is a **MUST**, principal-binding/TTL/request-scoping are
  **SHOULD** — so #5759 can reuse the owner-binding design here.
- **go-sdk v1.7 stateless server mode**: an optional simplification of the Phase 2 dispatcher
  once the SDK is adopted (#5754); not a prerequisite.

## Backend connection management

Today a `MultiSession` opens live backend HTTP connections per vMCP session, amortizing
connection setup across a session's requests. Stateless removes the vMCP session those
connections hang on. Per the design decision for this document:

- **MVP — per-request open/close** to Modern backends. Simplest, matches the go-sdk
  `StreamableHTTPOptions.Stateless` semantics ("each request creates a temporary session that's
  closed after the request"), and correct. It reintroduces the connection/file-descriptor churn
  the [scalability doc](13-vmcp-scalability.md#file-descriptor-limits) already flags — *and* a
  per-request OBO/token-exchange cost, an auth-exchange amplifier under load. Both are acceptable
  for correctness-first; the OBO cost is bounded by caching exchanged tokens by
  `(subject, backend, scope)` (see below), and the churn is called out explicitly so this is not
  mistaken for the end state.
- **Next — shared per-backend connection pool** at the vMCP level, keyed on a stable backend
  identifier (not on identity or client session), with per-request auth (OBO/headers) injected
  per call. This is what statelessness is *for*: any pod, any request, no per-session churn. Two
  hard invariants: (1) the pooled object holds **only a bare transport with zero retained
  credential state** — the existing backend abstraction bakes auth in at `initialize` time
  (`client.go:675`, `mcp_session.go:409`), so a pool that reuses it as-is would leak principal
  A's credentials onto B's per-request call; auth must be a per-call header with no client-level
  default, and OBO results cached by `(subject, backend, scope)`. Header-forward resolution is
  likewise **per-call from the current request's context** (`mergeForwardedHeaders` takes the
  forwarded set as a parameter), never cached on the pooled transport — otherwise principal A's
  captured headers ride B's pooled call. (2) It must route through the security-enforcing path (no
  direct-to-container shortcut) and store no internal addresses in shared state.

## Kubernetes and operational considerations

- **Cross-pod affinity dissolves for Modern.** The `registry.go` affinity TODO and the CRD's
  `sessionAffinity: ClientIP` exist only to sticky-route session-ful clients. A Modern client
  is stateless, so any pod serves any request; affinity becomes a Legacy-only concern. Redis
  remains **only** for the workflow-handle store (and Legacy session metadata), not for Modern
  request routing.
- **The 1,000-session per-pod cap and 30-minute session TTL** do not apply to Modern traffic —
  there are no sessions to cap or expire. They remain for Legacy clients. A pod's Modern
  capacity is bounded by CPU/backend-connection limits, not the session cache.
- **Rolling upgrade is safe.** Modern traffic adds no session writes and needs no CRD change,
  so old and new vMCP pods can share one Service; the classifier is auto-detected from request
  content.
- **Per-request aggregation cost.** The core holds no capability cache by design ("the core
  filters, Serve caches"), so it re-derives the aggregated view *per request* — and
  `aggregatedView` calls `AggregateCapabilities`, which queries backends live. The Legacy path
  amortizes this across a session via the `MultiSession` cache; the **Modern path pays a backend
  fan-out on every request**. This is a real cost (not just CPU) and the likeliest scaling
  surprise; a Serve-layer per-identity capability cache with a short TTL is the natural mitigation
  if profiling shows it bites, but it is deliberately out of the MVP (anti-pattern #9 — measure
  first).

## Testing strategy

- **Classifier** — unit tests exist for `ClassifyRevision`; extend for the vMCP decode seam
  (`Mcp-Method`/`Mcp-Name` validation, base64-sentinel `Mcp-Name`, missing-required-field →
  `-32602`).
- **Dispatcher** — per-method dispatch, `resultType: "complete"` envelope, `404`/`-32601` for
  unimplemented methods, notification `202`/drop, batch rejection, and the re-homed call-gate
  (a denied `tools/call` must produce HTTP 403 *and* an audit `outcome:"denied"`, not a 200
  tool-error).
- **Dual-era matrix** — an e2e cell-by-cell suite: Legacy↔Legacy unchanged, Modern↔Modern, and
  the two bridge cells (incl. the `(principal, backend)` partition and calling-principal OBO on a
  synthesized Legacy-backend session).
- **Workflow handle** — owner-binding enforcement (principal B cannot resume A's workflow), TTL
  expiry, and Redis-unavailable fail-closed behavior.

## Observability

New signals worth emitting: classification outcome (Modern/Legacy/rejected) as a span attribute
and counter; per-era request distribution; synthesized-Legacy-session count and lifetime;
workflow-handle resume outcomes (owner-mismatch rejections are a security signal); and the
per-request aggregation/backend-fan-out latency (to catch the cost noted above). `mcp.session.id`
is absent under Modern; W3C trace context in `_meta` is the correlation mechanism (as on the
proxy side).

## Rollback / kill switch

"Everything classifies Legacy by default" is the implicit kill switch, but it is made explicit as a
**dedicated config flag** (not merely the absence of Modern-capable wiring) that forces every request
down the Legacy path, so the Modern dispatcher can be disabled in production without a redeploy or a
wiring change if it misbehaves. Default: Modern off until the dispatcher is proven.

## Decisions and rationale

- **D1 — Delete the bridge, don't rebuild it.** The core is already stateless; the Modern path
  bypasses the Serve session layer rather than reimplementing it statelessly. Smallest diff,
  and it matches the repo's stateless-routing security rules.
- **D2 — Hand-rolled Modern dispatcher over the direct core calls; don't hard-gate on go-sdk
  v1.7.** The Serve path already dispatches to the core directly, so the Modern handler unblocks
  the work from the SDK bump (mirroring THV-0081's Alternative 2, adapted to vMCP's SDK-based
  server). Most of it is assembly of existing parts (classifier, the standalone `authzCallGate`
  closure, `pkg/vmcp/conversion` helpers, header-forward); the **one genuinely new piece is the
  JSON-RPC/`resultType` envelope**, which is a **temporary bridge** — lift the SDK's marshaling
  where possible and retire it into go-sdk v1.7's stateless server mode once that ships. That
  envelope plus the call-gate→403+audit re-homing is the main server-side risk.
- **D3 — vMCP owns the off-diagonal bridge.** THV-0081 defers the era-mismatched cells here
  because only an aggregator that terminates + re-originates can bridge them. Both cells are
  tractable for that reason; the hard one (Modern-client / Legacy-backend) keys the synthesized
  backend session on the workflow handle.
- **D4 — Backend connections: per-request first, pool next.** Correctness before optimization
  (anti-patterns #9); the churn cost is documented, not hidden.
- **D5 — Workflow handles: server-minted, owner-bound capability + pluggable Redis.** The store
  is already workflow-keyed, so this does not re-key anything — but making the handle a real
  capability is security-sensitive: it needs an owner field on `WorkflowStatus`, an
  `identity == owner` check on resume (admission re-check alone is insufficient), a TTL, and
  audit redaction. The Redis backing needs a new injected-store seam on `core.Config` (the store
  is built internally today).
- **D6 — Unary + correctness scope.** Streaming, subscriptions, and MRTR are designed-deferred,
  matching #5755/THV-0081.

## Out of scope

- go-sdk v1.7 adoption / `mcpcompat` stateless plumbing (#5754) — an optional Phase 2
  simplification, not a dependency.
- MRTR-based elicitation/sampling passthrough (#5759).
- Parser 2026-07-28 vocabulary beyond what the Modern path needs (#5757), caching metadata
  `ttlMs`/`cacheScope` (#5761), CIMD/RFC 9207 auth hardening (#5760).
- Request-scoped streaming and `subscriptions/listen` delivery (designed-deferred above).
- The transport proxies themselves (#5755 / THV-0081).

## Related documentation

- `stateless-transport-proxies-design.md` (pending, toolhive PR #5808) — the transport-proxy
  counterpart; defines `ClassifyRevision` and defers the bridge here.
- [`10-virtual-mcp-architecture.md`](10-virtual-mcp-architecture.md) — vMCP architecture.
- [`13-vmcp-scalability.md`](13-vmcp-scalability.md) — session cache, TTLs, Redis sizing, fd
  limits (the constraints the Modern path removes or reshapes).
- RFC THV-0081 (`toolhive-rfcs`) — decision-level counterpart for the proxy work.
- Tracking: #5756 (this), #5743 (epic), #5755 (proxies), #5754 (go-sdk v1.7), #5759 (MRTR).
