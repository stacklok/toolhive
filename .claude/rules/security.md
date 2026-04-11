---
paths:
  - "**/*.go"
---

# Security Rules

Applies to all Go files in the project.

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

## All Requests Must Pass Through the Proxy Runner

Every request to a managed container (MCP server or tool) must flow through the proxy runner (`pkg/runner/proxy`). Bypassing it is a vulnerability, not an optimization.

The proxy runner is the single enforcement point for:
- Authentication and authorization checks
- Secret injection and credential management
- Network policy and egress controls
- Audit logging

Any code that constructs a direct connection to a container — by using a raw host:port, reaching past the proxy interface, or type-asserting to an underlying transport — skips these controls entirely.

**If you find a code path that contacts a container without going through the proxy runner, treat it as a security bug and fix it.**
