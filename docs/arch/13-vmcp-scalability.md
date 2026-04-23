# vMCP Scalability Limits and Constraints

> **Audience**: operators scaling vMCP beyond a single replica. For the
> architectural overview, see
> [Virtual MCP Server Architecture](10-virtual-mcp-architecture.md).

This document describes the known capacity limits, configuration-driven
constraints, and operational considerations for Virtual MCP Server (vMCP)
deployments. Review this before scaling beyond a single replica.

## Per-pod session cache

Each vMCP pod maintains a **node-local LRU cache** capped at **1,000 concurrent
`MultiSession` entries** (source:
`pkg/vmcp/server/sessionmanager/factory.go:defaultCacheCapacity`).

When the cache is full, the least-recently-used session is evicted via the
`onEvict` callback, which calls `sess.Close()` to tear down its backend
connections. Any request in flight at that moment fails. Subsequent requests
for the same session ID trigger a cache miss: the session manager calls
`factory.RestoreSession()`, which reconstructs the `MultiSession` from stored
metadata and re-establishes backend connections transparently. The client does
not need to reconnect unless the metadata itself has also expired.

The cap exists to prevent unbounded memory growth: omitting `CacheCapacity`
from a `FactoryConfig` silently defaults to 1,000 rather than unbounded growth.
`CacheCapacity` is currently an internal field and is not exposed via the
VirtualMCPServer CRD.

**Implication:** A single vMCP pod can serve at most ~1,000 simultaneous MCP
sessions. To handle more, add replicas and configure Redis session storage so
that session metadata is persisted and any pod can reconstruct the live session
(including its routing table) via `RestoreSession()` on demand.

## Session TTL

### vMCP server TTL (30 minutes)

The vMCP server defaults to a **30-minute session TTL**
(`pkg/vmcp/server/server.go:defaultSessionTTL`). The TTL controls the lifetime
of **session metadata** in the storage layer, not the in-process `MultiSession`
runtime objects:

- **Local storage (single replica):** session metadata is removed from
  `LocalSessionDataStorage` after the TTL elapses with no access. The
  corresponding in-process `MultiSession` (with its live backend connections)
  remains in the node-local LRU cache until it is evicted by cache pressure or
  explicit termination.
- **Redis storage (multi-replica):** see [Redis sliding-window TTL](#redis-sliding-window-ttl) below.

When metadata expires, any subsequent request that references that session ID
will fail to restore the session (`RestoreSession()` finds no stored metadata)
and the client must reinitialize. Backend connections held by the cached
`MultiSession` are only released when the LRU cache evicts the entry or the
session is explicitly terminated.

The TTL is configurable via `server.Config.SessionTTL` but is not currently
exposed through the operator CRD.

### MCPServer proxy TTL (2 hours)

The MCPServer proxy runner uses a separate, longer TTL of **2 hours**
(`pkg/transport/session/proxy_session.go:DefaultSessionTTL`). This applies to
the underlying SSE/streamable transport sessions, not the vMCP-level session
aggregation.

### Redis sliding-window TTL

When Redis session storage is enabled, every `Load` call issues a `GETEX` that
resets the key's TTL atomically
(`pkg/transport/session/storage_redis.go:NewRedisStorage` and the comment at
line 177). This means:

- Active sessions are preserved indefinitely as long as they receive at least
  one request per TTL window.
- Idle sessions expire automatically after the full TTL elapses with no access.
- There is no absolute maximum session lifetime enforced by Redis storage.

### Session garbage collection

| Trigger | Mechanism |
| ------- | --------- |
| Explicit termination (client disconnect, auth failure) | `DEL` issued immediately to Redis |
| Inactivity beyond TTL | Redis TTL expiry (automatic, no application-side action needed) |
| Pod-local cache eviction (LRU) | `onEvict` callback closes backend connections only; the Redis metadata key is **not** deleted and expires via TTL |

## File descriptor limits

Each open backend connection consumes one file descriptor on the vMCP pod. A
pod aggregating many MCP backends at high session concurrency can exhaust the
OS-level `nofile` limit before hitting the 1,000-session cache cap.

The default Linux per-process `nofile` soft limit is typically 1,024. When this
limit is reached, new `connect()` calls fail with `EMFILE` ("too many open
files"), which surfaces as backend connection errors.

**Estimate:** `concurrent_sessions × backends_per_session` file descriptors.
For example, 200 sessions each connecting to 3 backends requires ~600 fds,
plus fds for incoming client connections and internal pipes.

The issue has been identified but the exact threshold depends on pod
configuration and backend topology. Raise the limit in the container spec or at
the node level via the container runtime before deploying at scale.

## Redis sizing

Session data is written on every new session (`Store`) and read on every
request (`Load` + `GETEX`). Redis is on the hot path.

| Parameter | Default | Notes |
| --------- | ------- | ----- |
| Dial timeout | 5 s (`DefaultDialTimeout`) | `pkg/transport/session/redis_config.go` |
| Read timeout | 3 s (`DefaultReadTimeout`) | |
| Write timeout | 3 s (`DefaultWriteTimeout`) | |
| Key prefix | configurable | Must end with `:` to avoid collisions |

**Memory:** Session payloads include the routing table and tool metadata. Rough
estimate: 10–50 KB per session depending on backend count and tool count.
Maximum concurrent session count across the fleet is `replicas × 1,000`.

**Connection pools:** Each vMCP pod creates one go-redis client with its own
connection pool. No explicit `PoolSize` is configured
(`pkg/transport/session/storage_redis.go`), so go-redis applies its default of
`10 × GOMAXPROCS` connections per pool. Total Redis connections therefore scale
as `replicas × (10 × GOMAXPROCS)`. Size the Redis `maxclients` setting
accordingly, and tune `PoolSize` in `RedisConfig` if the default is too large
or too small for your workload.

**Eviction policy:** Use `allkeys-lru` so Redis can shed stale sessions under
memory pressure rather than returning errors on new writes.

**Persistence:** Redis persistence is not required for session storage. If the
Redis pod restarts, all active sessions are lost and MCP clients must reconnect.
For production deployments where session continuity is critical, use a
`StatefulSet` with a PVC and enable RDB/AOF persistence.

## Stateful backends and pod restarts

vMCP is a stateless proxy: it holds routing tables and tool aggregation state,
but the backend MCP servers own their own state (browser sessions, database
cursors, open files).

When a vMCP pod restarts or is evicted:

1. **Redis session storage is configured:** the routing table survives in Redis.
   Clients can reconnect and resume the MCP session. However, any backend-side
   state (Playwright browser context, open transaction, filesystem handle) is
   **not recovered** — the backend connection was torn down without a graceful
   MCP shutdown sequence.

2. **Local storage only:** both the routing table and the backend connections
   are lost. Clients must reinitialize completely.

In both cases, **in-flight tool calls are lost without a response** when a pod
dies. Callers should implement retry logic with idempotency guards for any tool
invocations that modify external state.

### Session affinity and multi-replica deployments

Stateful backends require that all requests within a session reach the same
backend pod. The `VirtualMCPServer` CRD exposes `sessionAffinity: ClientIP`
(default), which instructs kube-proxy to sticky-route connections by source IP.

This is unreliable when clients sit behind NAT, a corporate proxy, or a cloud
load balancer — all traffic appears to originate from the same IP, routing every
session to a single pod. For production stateful workloads, prefer vertical
scaling over horizontal scaling. See `docs/arch/10-virtual-mcp-architecture.md`
for session affinity design details.

## Hardcoded limits summary

| Limit | Value | Source | Tunable? |
| ----- | ----- | ------ | -------- |
| Per-pod session cache | 1,000 sessions | `sessionmanager/factory.go:defaultCacheCapacity` | No (internal field) |
| vMCP session TTL | 30 minutes | `vmcp/server/server.go:defaultSessionTTL` | Via `server.Config.SessionTTL` (not CRD-exposed) |
| MCPServer proxy session TTL | 2 hours | `transport/session/proxy_session.go:DefaultSessionTTL` | No |
| Redis dial timeout | 5 s | `transport/session/redis_config.go:DefaultDialTimeout` | Via `RedisConfig.DialTimeout` |
| Redis read timeout | 3 s | `transport/session/redis_config.go:DefaultReadTimeout` | Via `RedisConfig.ReadTimeout` |
| Redis write timeout | 3 s | `transport/session/redis_config.go:DefaultWriteTimeout` | Via `RedisConfig.WriteTimeout` |
| forEach max iterations | 1,000 | `vmcp/config/config.go:MaxForEachIterations` | Via `WorkflowStepConfig.MaxIterations` (capped at 1,000) |

## Related

- `pkg/vmcp/server/sessionmanager/factory.go` — LRU cache and `FactoryConfig`
- `pkg/vmcp/server/server.go` — `defaultSessionTTL`, `Config.SessionTTL`
- `pkg/transport/session/storage_redis.go` — sliding-window TTL via `GETEX`
- `pkg/transport/session/redis_config.go` — timeout defaults
- `docs/arch/10-virtual-mcp-architecture.md` — overall vMCP architecture
- `docs/arch/11-auth-server-storage.md` — Redis Sentinel for auth server sessions
