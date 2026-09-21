---
paths:
  - "**/*.go"
---

# Security Rules

Applies to all Go files in the project.

## Session Ownership

Reuse the common issuer+subject session binding and ownership middleware across proxy families. Bind at creation before atomic publication; validate every session-bearing request after authentication and before routing, restoration, or other side effects. Reject foreign or unowned sessions without disclosing metadata or destroying the owner's session; malformed identities must never downgrade to unauthenticated access.

## Don't Store Internal Addressing in Shared State

Never persist internal infrastructure addresses (hostnames, IPs, service URLs, pod names) into shared or external state stores (databases, caches, config passed to clients).

Internal addresses stored externally:
- Leak topology to anyone who can read the store
- May allow callers to bypass security middleware by using the stored address directly
- Couple your routing logic to volatile infrastructure state that changes independently

**Instead**: derive routing from stable, non-sensitive inputs (e.g. a session ID, a content hash, a logical name). If you must store a target, store a logical identifier and resolve it at use time through a path that enforces security controls.

## Route Through Security-Enforcing Components

Always route traffic through the component responsible for auth, rate limiting, or policy enforcement — never optimize past it.

A direct path that skips middleware is a vulnerability, not a performance improvement. If you find yourself type-asserting, casting, or reaching into an internal field to get a "more direct" address, stop and ask whether the shortcut bypasses any security boundary.

When multiple routing options exist (e.g. a proxy vs. a raw address), choose the one where security controls are guaranteed to be in the critical path.

## Prefer Stateless Routing Over Stored Routing

When routing can be derived deterministically from stable request properties, compute it on every request rather than storing it.

Storing routing decisions:
- Creates state that must be recovered correctly after restarts
- Introduces a window where stored state is stale or wrong
- Expands the attack surface of the state store

If the same input always maps to the same destination (consistent hashing, modular arithmetic, content addressing), there is no need to store the mapping. Remove the stored state and eliminate the recovery problem entirely.

## Own the Fix for a Reported Security Advisory

Verify the reported bug yourself, then derive the fix from this codebase's existing
answer for the same shape. Read the advisory's "Recommended remediation" section
only afterwards, as a cross-check — never as the design input.

Reporters do not know our invariants, and two ToolHive advisories have shipped
unsafe remediation advice:

- **GHSA-8rgq-f53x-rcp6** advised preserving the full `completion/complete` params
  as authorization context. Doing so introduced a *new* bypass: Cedar namespaces
  arguments as `arg_*`, and the parser hands that method its entire params map, so
  a caller could forge `arg_env` and satisfy a policy condition the real
  `prompts/get` could never satisfy.
- **GHSA-5jfr-jf2r-pcfj** advised installing a private-IP deny on vmcp backends,
  which resolve to RFC 1918 pod IPs — it would have broken every in-cluster
  deployment on the first request.

When fixing one:

1. Reproduce the bug independently; do not rely on the reporter's PoC.
2. Find the codebase's precedent for the same shape and let that drive the design.
3. Check `git log` for a deliberate PR behind the claimed defect — intended,
   documented behavior is not a vulnerability.
4. Name the behavior the fix changes for existing deployments (requests that
   previously succeeded, forward-compatibility breaks, new amplification) and treat
   each as an explicit decision. Prefer fail-closed *with* an operator-visible
   signal — log request-shape rejections at WARN — over silent breakage.

## All Requests Must Pass Through the Proxy Runner

Every request to a managed container (MCP server or tool) must flow through the proxy runner (`pkg/runner/proxy`). Bypassing it is a vulnerability, not an optimization.

The proxy runner is the single enforcement point for:
- Authentication and authorization checks
- Secret injection and credential management
- Network policy and egress controls
- Audit logging

Any code that constructs a direct connection to a container — by using a raw host:port, reaching past the proxy interface, or type-asserting to an underlying transport — skips these controls entirely.

**If you find a code path that contacts a container without going through the proxy runner, treat it as a security bug and fix it.**
