# Auth Server Design — Review Concerns

This document captures concerns raised during review of the [vMCP Auth Server Integration design](README.md), along with proposed solutions and open questions.

---

## 1. Scale-Out (RFC 47 Compatibility)

**Problem:** The design presents memory vs Redis storage as a configuration choice. But when `replicas > 1`, a session can be resumed on any pod. Upstream tokens stored in-memory are invisible to other pods. Fosite's OAuth state (auth codes, pending authorizations, refresh tokens) is also in-memory by default — and with eager auth chaining, the multi-leg redirect flow can hit different pods at each callback.

When a session is restored on a new pod, the design assumes backend connections are created through the full `initOneBackend` path — which runs MCP `Initialize` and capability queries against each backend. RFC 47 requires session resumption without re-initialization: the new pod should reconnect to backends using their persisted MCP session IDs, not start fresh. The auth plumbing (outgoing auth strategies, upstream token injection) must work for restored connections that skip the initialization handshake.

Additionally, when upstream token refresh is eventually implemented, multiple pods could simultaneously try to refresh the same expired token. With rotating refresh tokens (e.g., GitHub), the second refresh would invalidate the first pod's new token.

**Resolution:** Depends on ordering. If this work lands before RFC 47, the design should document that `storage.type: memory` is single-replica only, and RFC 47 should be updated to require Redis storage for the auth server alongside session metadata. If RFC 47 lands first, this design should treat Redis as a hard requirement when `replicas > 1`. The auth design's outgoing auth strategies are stateless per-request and don't inherently depend on initialization — but the design should acknowledge that session restore will need a separate code path that wires up the auth round-tripper chain without calling `Initialize`. The token refresh race condition should be noted as a known limitation to address when token refresh and multi-replica support are both present.

## 2. Token Plumbing Without Context Dependency

**Problem:** The design introduces an `UpstreamTokenSource` interface whose adapter extracts the TSID from the request context to look up upstream tokens at call time. The v2 session management system is intentionally moving away from context-based dependency plumbing — passing explicit parameters makes the code easier to test, dependencies clearer, and the flow easier to follow. The `UpstreamTokenSource` pattern reintroduces implicit context coupling into a layer that has been designed to avoid it.

**Solution:** Load upstream tokens from Redis into the Identity struct at the middleware layer, before the session layer sees it. Add a field like `UpstreamTokens map[string]string` to Identity. The middleware reads `Claims["tsid"]`, fetches tokens from Redis, and populates the map. Outgoing auth strategies read `identity.UpstreamTokens["github"]` directly — no interface indirection, no context extraction, no hidden Redis calls deep in the call stack. The tokens flow through the same explicit parameter chain as the rest of the Identity.

## 3. Session Hijack Prevention With Token Refresh

**Problem:** The `HijackPreventionDecorator` binds sessions to `HMAC-SHA256(identity.Token, secret, salt)` — the full JWT string. When the TH-JWT is refreshed (new signature, new `exp`), the hash changes and the session rejects the new token. The design (UC-04 §5.2) treats this as intentional — the client creates a new session.

**Current state:** Token refresh is not wired up today. The upstream providers implement `RefreshTokens()`, but no consumer calls it. The `upstreamswap` middleware and all outgoing auth strategies use stored tokens as-is — expired tokens cause request failures, not refresh. The `HijackPreventionDecorator`'s full-token binding doesn't break in practice because JWT refresh doesn't happen mid-session yet.

**Future concern:** When token refresh is implemented, the full-token binding will force session recreation on every JWT refresh. With RFC 47 (session resumption without re-initialization), this becomes unacceptable. At that point, the binding should change from `HMAC-SHA256(identity.Token, secret, salt)` to `HMAC-SHA256(tsid + subject, secret, salt)`. Both `tsid` and `sub` are preserved across JWT refresh by fosite, and are sufficient to prevent cross-user and cross-session hijacking. The code change is minimal — `HashToken` and `validateCaller` in `pkg/vmcp/session/internal/security/security.go` hash different inputs; everything else (salt generation, constant-time comparison, metadata persistence, anonymous handling) is unchanged. For non-AS deployments where there is no TSID, the current full-token binding remains correct since those deployments have no JWT refresh.
