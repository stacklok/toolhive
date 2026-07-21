# ADR-0001: Untrusted MCP Server Egress Credential Broker

- **Status**: Accepted
- **Date**: 2026-07-21
- **Deciders**: ToolHive core team
- **Source**: Final consolidated plan, `.claude/scratch/untrusted-mcp-egress-broker-plan.md` §9
  (sections 1–8 are the design history and evidence trail; §9 supersedes them where they conflict).
- **Wave-0 implementation specs**: `.claude/scratch/wave0/01`–`04`, Wave-1 CRD handoff at
  `.claude/scratch/wave0/wave1-handoff.md`.

---

## 1. Context

ToolHive fronts MCP servers written by third parties. Some deployments need to run servers the
operator **does not trust** — arbitrary community images whose code may be malicious or
compromised — while still letting those servers call upstream APIs (GitHub, Google, …) **as the
human driving the agent** (Case 1: user-delegated upstream OAuth).

The design constraint that shapes everything else:

> **A malicious or compromised MCP server must never possess a real credential** — not in an
> environment variable, not in a file, not in a header — **not even a short-lived one**. A
> malicious server replays what it receives immediately; TTL is irrelevant when the thief is the
> intended recipient.

Consequence: a server that holds no credentials can make no authenticated call itself. A **trusted
broker must terminate every credentialed connection** and attach the credential at a boundary the
server cannot reach.

### The exposure being closed (verified against code)

Today the user's live upstream OAuth token is handed to the backend MCP server:

- The vMCP `upstream_inject` strategy sets `Authorization: Bearer <real upstream token>` on the
  HTTP request vMCP sends to the backend (`pkg/vmcp/auth/strategies/upstream_inject.go:85`), via
  the auth RoundTripper in `pkg/vmcp/client/client.go` and
  `pkg/vmcp/session/internal/backend/mcp_session.go`.
- The proxy-runner `upstreamswap` middleware does the same replacement on the gateway path
  (`pkg/auth/upstreamswap/middleware.go:179-186`).
- A server's own static upstream credential is delivered as a K8s Secret env var on the backend
  container (`WithSecrets(m.Spec.Secrets)` at
  `cmd/thv-operator/controllers/mcpserver_controller.go:1071`, materialized as
  `ValueFrom.SecretKeyRef` in
  `cmd/thv-operator/pkg/controllerutil/podtemplatespec_builder.go:61-85`).

What already exists and is **reused, not rebuilt**: upstream consent flow with PKCE
(`pkg/authserver/server/handlers/authorize.go`, `callback.go`), the per-session upstream token
store keyed `{prefix}upstream:{sessionID}:{provider}` (`pkg/authserver/storage/redis_keys.go`),
`tsid` minted into the ToolHive JWT (`pkg/authserver/server/session/session.go`), per-request
loading of upstream tokens into `Identity` (`pkg/auth/token.go:1169-1192`), refresh-on-read
(`pkg/auth/upstreamtoken/service.go`), and inbound `StripAuthMiddleware`
(`pkg/transport/middleware/strip_auth.go`).

### The two structural blockers that forced the design

Verified during plan scrutiny (plan §8.2):

- **Blocker A — per-call dispatch IDs cannot reach the server's egress call.** vMCP can attach a
  value to the vMCP→backend hop (headers, MCP `_meta`), but no MCP mechanism — by spec or
  convention — makes a server copy an inbound value into the outbound HTTPS request *it* sends to
  `api.github.com`. An untrusted server cannot be relied on to forward it faithfully, to the
  intended host only, or without exfiltrating it. Any design that requires server cooperation is
  void as a security boundary for unmodified servers.
- **Blocker B — backend pods are multi-tenant, so egress cannot be attributed to a user.** A single
  MCPServer is one shared replica-based StatefulSet (`buildStatefulSetSpec`,
  `pkg/container/kubernetes/client.go:557`); vMCP routes by capability to a shared Service URL with
  no principal in the addressing. When a shared pod dials out, nothing on the egress path says
  which user's tool call triggered it.

Together: **for an unmodified untrusted server in a shared pod there is no reliable per-call
credential carriage and no reliable per-user egress attribution.** One of the two constraints had
to break. We break tenancy, not the "unmodified server" constraint — breaking the latter defeats
the point of fronting arbitrary third-party servers.

---

## 2. Decisions (ratified — plan §9.2)

| # | Decision | One-line rationale |
|---|----------|--------------------|
| D1 | **No per-call dispatch records** (retired) | Token-selection input never crosses the untrusted server in any form |
| D2 | **Single-tenant backends**: one backend pod per `(user, MCPServer)` | Pod identity *is* user identity; attribution needs no server cooperation |
| D3 | **Data plane = Envoy sidecar per backend pod + Go `ext_authz` injector** | Sidecar identity == pod identity; ext_authz header mutation is exactly the injection job |
| D4 | **Trusted identity-aware front mandatory (vMCP), stated as a requirement not a brand** | The requirement is per-session lifecycle ownership + per-user routing; vMCP is the natural home |
| D5 | **Credential-to-destination binding is a hard invariant** | Provider P's credential is emitted only for hosts in P's allowlist, checked before any header is written |
| D6 | **Response-side leakage control** | No redirect following; v1 allowlist restricted to known API hosts; token-value response scanning in Wave 5 |
| D7 | **Broker-side lockdown** | Sidecar egress NetworkPolicy-restricted to allowlisted destinations; per-dial resolved-IP validation (DNS-rebinding protection) |
| D8 | **Consent-on-demand in both subsystems** | Proxy path and vMCP path are independent patches; both gain a consent URL |
| D9 | **CA trust distribution is a compatibility matrix** | Bump CA via ConfigMap volume + runtime-specific env (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, …) |
| D10 | **stdio servers that ignore proxy env fail closed** | NetworkPolicy blocks direct egress; compatibility limit, documented — not a security gap |
| D11 | **Audit cardinality discipline** | User sub / pod name go to structured audit logs, never metrics labels |

### D1 — No per-call dispatch records (retired Option A)

**Decision.** The token-selection key (`tsid`) never travels in anything the untrusted server
emits. No `dispatch-id` header, `_meta` field, or tool-arg convention. The per-call dispatch-record
model (original plan D1/Option A) is removed from the primary design; a record-format sketch is
retained in the plan appendix solely as a future optimization for **first-party servers built with
a ToolHive SDK** that opt in to echoing a dispatch token upstream — and even then it is never the
security boundary.

**Rationale.** Blocker A: there is no way to make an unmodified server propagate a per-call
identifier into its own egress, and a malicious server that *can* see the identifier can replay or
exfiltrate it. This also aligns with `.claude/rules/security.md` ("Prefer Stateless Routing Over
Stored Routing"): the pod→user mapping is a stable fact established once at pod creation, not a
per-call record written to a shared control API.

**Alternatives considered.** Per-call dispatch records with a broker control API (finer-grained but
void for unmodified servers — Blocker A); per-session `(mcp-session → tsid)` push records (same
propagation problem, plus shared-state recovery burden).

### D2 — Single-tenant backends

**Decision.** Untrusted mode runs the backend MCP server **single-tenant: one backend pod per
`(user, MCPServer)` pair** — not per provider, not per tool. The pod→user binding is established
once at pod creation; the Envoy sidecar (D3) resolves identity locally from its own pod's binding.

**Rationale.** Blocker B: this is the only break that preserves unmodified-server compatibility.
With 1:1 pod↔user mapping, the broker attributes egress by the pod's stable network identity —
no server cooperation, no propagated identifiers. Per-`(user, MCPServer)` granularity (rather than
per-user shared across servers) matches the existing session model and avoids cross-server
coupling; revisit only if pod fan-out proves untenable (Wave 2 measures this).

**Alternatives considered.** Multi-tenant pods + per-call attribution (impossible, Blockers A+B);
per-user pods shared across MCPServers (rejected: unsupported by the session model, couples
unrelated servers' lifecycles).

**Cost (owned explicitly).** Untrusted mode needs a per-session backend lifecycle: spin-up on
first use, idle-TTL teardown, cold-start latency, pod fan-out with DoS controls. This is the
largest single work item in the program (Wave 2) and interacts with the per-pod session caps in
`docs/arch/13-vmcp-scalability.md`. Session-restore (`pkg/vmcp/session/factory.go:400`) and its
15 s bound (`pkg/vmcp/server/sessionmanager/session_manager.go:211`) must tolerate pod creation.

### D3 — Data plane: Envoy sidecar per backend pod + Go `ext_authz` service

**Decision.** Each single-tenant backend pod carries an **Envoy sidecar** that terminates and
re-originates TLS ("bump"). Credential injection lives in a small **Go service invoked by Envoy as
an `ext_authz` filter** — not `ext_proc`. The Go service resolves pod→user, calls
`GetAllUpstreamCredentials` (refresh-on-read already exists,
`pkg/auth/upstreamtoken/service.go:88`), enforces D5, and returns the header mutation. `ext_proc`
is added **only** if response-body inspection (D6c, Wave 5) is built — the two filters are not
interchangeable and "pick later" was explicitly rejected during scrutiny.

**Rationale.** ext_authz's job is exactly "mutate request headers on allow"; ext_proc is a
stream-processing filter whose cost and complexity are unjustified until response inspection
exists. The sidecar topology means the sidecar's identity *is* the user's identity — no
SPIFFE/mTLS or other workload-identity infrastructure is required, and TLS-bump stays
loopback-local to the pod. Envoy is also the standard egress-gateway substrate operators already
trust, unlike a bespoke Squid-bump image.

**Alternatives considered.**
- **Squid + ssl_bump (rejected for K8s)**: cannot run our Go injectors; Squid remains the
  Docker-mode egress reference only (`pkg/container/docker/squid.go` already enforces
  `OutboundNetworkPermissions` there — this ADR extends an already-shipping Docker pattern into
  K8s, where the same CRD fields are currently dead).
- **Shared Envoy egress gateway per namespace (rejected for v1)**: cheaper per-connection, but
  sound only with workload-identity infrastructure (SPIFFE/mTLS) to supply pod→user; the marginal
  cost of a sidecar inside an already-per-user pod is small. Recorded as a possible future
  optimization once workload identity exists; composes with static egress IP (Cilium
  EgressGateway / cloud NAT) for IdP-side allowlisting.
- **Go forward proxy with a goproxy-style core (rejected)**: a bespoke TLS-bump core is a
  high-value security component; Envoy's maturity and operator familiarity win.

### D4 — Trusted identity-aware front mandatory

**Decision.** Untrusted-mode workloads MUST be fronted by a trusted, identity-aware component that
owns the per-session backend lifecycle and per-user routing. **vMCP is that component**;
enforcement is CEL validation requiring untrusted MCPServers to be members of an MCPGroup fronted
by a VirtualMCPServer. The requirement is lifecycle ownership, not vMCP per se — the upstream-token
machinery is not vMCP-exclusive (`pkg/auth/upstreamswap` is proxy-runner middleware) — but vMCP is
the natural and only supported home in v1.

**Rationale.** Only a component holding per-request `Identity` and the `(iss,sub)` session binding
(`pkg/vmcp/session/internal/security/security.go:137-192`) can establish the pod→user binding at
pod creation and route subsequent calls to the right pod.

**Alternatives considered.** Proxy-runner-only untrusted mode (rejected: no session lifecycle
ownership, no per-user routing dimension).

### D5 — Credential-to-destination binding is a hard invariant

**Decision.** The injector emits provider P's credential **only** when the destination host is in
P's allowlist, evaluated **before any header is written**. The binding is encoded in the egress
policy CRD as `provider → {allowedHosts, allowedMethods, allowedPathPrefixes}` (extending the
currently K8s-dead `OutboundNetworkPermissions`,
`cmd/thv-operator/api/v1beta1/mcpserver_types.go:645-661`).

**Rationale.** Without it, a malicious server points its egress at `attacker.com` under TLS-bump
and the broker — having resolved pod→user→real GitHub token — injects that token into a request
to `attacker.com`. Pod resolution selects the token; only destination-binding stops it going to
the wrong place. This is a security invariant, not an ACL nicety.

### D6 — Response-side leakage control

**Decision.** The broker must never let an injected credential come back to the server:
(a) **never follow redirects** — return 3xx to the server untouched; (b) **v1: destination
allowlists are restricted to known API hosts** (config-level, e.g. `api.github.com` — not
arbitrary echo services); (c) **Wave 5: response scanning** for the injected token value with
redact/block (`ext_proc`).

**Rationale.** The canonical attack is an httpbin-style echo endpoint: server asks the broker to
call a host that reflects the `Authorization` header back in the response body. (a)+(b) close it
for v1 within the known-API-host constraint; (c) closes it generally.

### D7 — Broker-side lockdown

**Decision.** The sidecar's own egress is NetworkPolicy-restricted to allowlisted destination
hosts only, and resolved destination IPs are validated **per dial** against the allowlist —
the same pattern as `WithDialControl` (`pkg/vmcp/client/client.go:99-103`). The broker is never a
general SSRF proxy.

**Rationale.** A TLS-terminating proxy that will dial anything is an SSRF oracle into both the
cluster and the internet; per-dial IP validation closes DNS-rebinding (name resolves to an
allowlisted host at policy-check time, to a link-local/internal IP at dial time).

### D8 — Consent-on-demand in both subsystems

**Decision.** Missing/expired upstream consent produces an actionable response in **both**
independent subsystems (they must not be conflated):
- **Proxy path** (`pkg/auth/upstreamswap/middleware.go:112-119`): keep the 401 + RFC 6750
  `WWW-Authenticate` challenge, add the consent URL in `error_description` or a structured body.
- **vMCP path** (`pkg/vmcp/auth/strategies/upstream_inject.go:82` → `pkg/vmcp/client/client.go`):
  a structured MCP error carrying the authorize URL + provider name.

**Rationale.** The broker never talks to agents; consent must be driven by the trusted front.
Today the vMCP path surfaces a bare authentication failure with no remediation path.

### D9 — CA trust distribution is a compatibility matrix

**Decision.** The bump CA is delivered via ConfigMap volume mount through the `--k8s-pod-patch`
seam (`cmd/thv-operator/controllers/mcpserver_controller.go:1079` → `applyPodTemplatePatch`,
`pkg/container/kubernetes/client.go:1251-1292`) plus runtime-specific env: `SSL_CERT_FILE` (Go),
`NODE_EXTRA_CA_CERTS` (Node), `REQUESTS_CA_BUNDLE` (Python/requests), `CURL_CA_BUNDLE`. Distroless
images: volume mount only. Per-tenant CA where feasible; a cluster CA is acceptable for v1 with a
documented rotation procedure.

**Rationale.** The bump CA in the pod trust store is an intentional, scoped MITM; if any runtime
in the image doesn't trust it, TLS-bump fails closed (availability loss, not confidentiality).

### D10 — stdio servers fail closed, documented

**Decision.** A stdio server that ignores `HTTP_PROXY`/`HTTPS_PROXY` env makes direct egress
attempts → NetworkPolicy blocks them → the server cannot call upstreams. This is a compatibility
limit, not a security gap, and is documented in user-facing docs.

### D11 — Audit cardinality

**Decision.** Every injected call is audit-logged with workload, user sub, pod name, destination
host, method, path, and status — in **structured audit logs only**. Metrics carry injection
counts and ACL denials per workload+provider **only**; user sub, pod name, and dispatch details
never appear in metric labels (cardinality explosion + PII in metrics).

---

## 3. Rejected alternatives (recorded so they are not re-litigated)

1. **Mediated tool-call model.** ToolHive's trusted layer makes the upstream call itself; the
   server is reduced to a schema/transformer, or declares egress that the trusted layer executes.
   This is the *only* model that removes the server from directing the credential's authority
   (it would also close the §5 residual). **Rejected** because it breaks the
   arbitrary-third-party-server model that is the entire point of the feature.
2. **Per-call dispatch propagation for unmodified servers** (original Option A). Void: no MCP
   mechanism carries an inbound value into the server's own egress, and an untrusted server cannot
   be trusted to (Blocker A). Demoted to a first-party-SDK opt-in optimization, never the security
   boundary.
3. **Shared-gateway topology for v1.** Requires workload-identity infrastructure to attribute
   connections to users; sidecar-in-pod gets the same property for free in an already-per-user
   pod. Revisit once SPIFFE/mTLS exists.
4. **Squid data plane for Kubernetes.** Cannot host the per-user Go injection logic; bespoke
   bump images are a worse operational and security posture than Envoy. Squid stays Docker-only.
5. **DNS pinning as a load-bearing control.** With default-deny egress that permits only the
   sidecar, and the broker re-resolving names itself, the server's own resolver cannot help it
   reach a forbidden host. Kept only as optional defense-in-depth; no work is sequenced behind it.

---

## 4. Wave plan (delivery roadmap — plan §9.3)

Each wave is one dev-pipeline run; nothing is deferred out of a wave.

| Wave | Contents | Critical path? |
|------|----------|----------------|
| **W0 — ADR + hardening PRs** | This ADR. **0.1** read-side binding check (`ErrInvalidBinding` is documented at `pkg/authserver/storage/types.go:565` but never returned by `memory.go:791-823` or `redis.go:1199-1209` — documented-vs-actual drift, hard gate for untrusted mode). **0.2** reject `valueFrom.secretKeyRef` in backend container env for untrusted workloads. **0.3** encrypt upstream tokens at rest in Redis (plaintext JSON today, `pkg/authserver/storage/redis.go:843`). **0.4** operator RBAC for `networkpolicies`. | Independent, merge first |
| **W1 — CRD surface + sentinel machinery** | `MCPServer.spec.untrusted`, `EgressPolicy` (`provider → allowedHosts/methods/pathPrefixes`), CEL validation (`untrusted ⇒ group membership + single-tenant + no secretKeyRef + provider allowlist`), sentinel env injection for token-requiring servers. Spec: `.claude/scratch/wave0/wave1-handoff.md`. | |
| **W2 — Session-scoped single-tenant backend lifecycle** | Spin-up on first use, idle-TTL teardown, pod→(user, MCPServer) registry (Redis, TTL'd), session-restore integration (`factory.go:400`, 15 s `restoreSessionTimeout` at `session_manager.go:211` is insufficient for pod creation), mandatory session affinity for untrusted groups, DoS controls (per-user quota, creation rate limit, capacity admission vs the 1,000-session LRU cap, `docs/arch/13-vmcp-scalability.md`), backend-pod survival across vMCP restarts. | **CRITICAL PATH — largest wave** |
| **W3 — Envoy sidecar + ext_authz injector** | `pkg/egressbroker` Go ext_authz service (pod→user from W2 registry, D5 precondition, inject Authorization), Envoy sidecar config (TLS-bump, per-workload routes/ACLs, no-redirect D6a, per-dial IP validation D7), per-tenant bump CA + ConfigMap distribution, NetworkPolicy generation (greenfield — the `client.go:360` TODO becomes real), e2e proving the token never appears in pod env/dumps/responses. | |
| **W4 — Consent-on-demand** | Both subsystems per D8; client/CLI rendering + post-consent retry. | |
| **W5 — Hardening + ops** | Response-side token scanning (ext_proc, D6c), audit pipeline + dashboards + alerting on ACL denials/leakage hits (D11), sidecar HA + upgrade story + CA rotation runbook, docs (new broker arch doc; update 09, 05, 13; user-facing untrusted-mode doc stating §5 loudly), first-party-SDK dispatch-echo spike (appendix only). | |

---

## 5. The security boundary, stated loudly

**Untrusted mode defeats credential theft, replay, cross-user use, and post-hoc use.** The server
never holds a token it can carry off, replay later, or use as another user.

**It does NOT defeat:**

1. **Authority abuse within scope for the current call.** The server shapes every outbound request
   (path, method, body), so it directs the current user's credential within granted scopes for the
   current session. A compromised GitHub MCP server can still call `DELETE /repos/{user}/{repo}`
   as the user. **Mitigations (these are the actual boundary):** least-privilege upstream consent
   scopes; per-tool method+path ACLs enforced in Envoy route config and the injector; full audit of
   every injected call (D11) as the primary detection surface.
2. **Data exfiltration via MCP responses.** Anything the upstream API returns to the server can be
   embedded in tool responses to the agent. The credential never leaks; the data it fetches can.

Operators must be told both limits — here and in the Wave-5 user-facing doc — so "untrusted mode"
is not over-trusted.

## 6. Consequences

- **Positive**: unmodified third-party servers can call upstream APIs as the consenting user while
  never possessing a credential; the exposure at `upstream_inject.go:85` is interposed on; every
  credentialed call is ACL'd and audited at a point the server cannot reach.
- **Negative / costs**: per-session pod fan-out (capacity, cold start, DoS surface — Wave 2 owns
  this); Envoy sidecar per pod; TLS-bump means the broker sees plaintext of every upstream request
  *and* response — the sidecar is a high-value target and a data-privacy consideration;
  cert-pinning servers and proxy-ignoring stdio servers fail closed (compatibility limits);
  purely request-driven servers are the only kind that function (no background polling — enforced
  by default-deny egress, documented).
- **Operational**: bump-CA rotation runbook required; per-tenant CA preferred; NetworkPolicy
  support in the CNI becomes a hard dependency for untrusted mode.

## 7. Remaining open questions (owned by Wave gates, not blocking W0/W1)

1. Exact lifecycle-owner split for single-tenant backends (vMCP component vs new controller) —
   Wave 2 design gate.
2. Per-tenant vs cluster bump CA — default per-tenant where feasible (D9).
3. Idle-TTL default and cold-start SLO for session-scoped pods — Wave 2.
4. Non-HTTP upstreams (DBs, gRPC, SSH-git): deny by default; revisit per demand.
