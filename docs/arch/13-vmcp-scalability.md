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
  stays in the node-local LRU cache until the background cache sweep reclaims
  it, cache pressure evicts it, or it is explicitly terminated.
- **Redis storage (multi-replica):** see [Redis sliding-window TTL](#redis-sliding-window-ttl) below.

When metadata expires, any subsequent request that references that session ID
will fail to restore the session (`RestoreSession()` finds no stored metadata)
and the client must reinitialize. Backend connections held by the cached
`MultiSession` are released when the background cache sweep finds the
metadata gone, when the LRU cache evicts the entry, or when the session is
explicitly terminated, whichever comes first. A DELETE cancels the session's
open Legacy calls only on the replica that receives it.

The sweep runs every `FactoryConfig.SweepInterval` (1 minute by default; a
negative value disables it). It reads each cached session's metadata with
`LoadMetadata`, which does not refresh the TTL, and evicts the sessions whose
record is gone or marked terminated. This covers sessions a client abandoned
without sending DELETE and sessions terminated on another replica. The sweep
also drops the session's health-resync registration, so that cleanup does not
wait for backend activity.

The Streamable HTTP transport also reaps its own pod-local copy of a session
(the go-sdk session and its per-session server) after one TTL with no POST
request. The session ownership middleware answers 404 for absent or terminated
sessions before the transport runs, so the transport's usual cleanup on that path
no longer happens. This releases only the pod-local copy, never the stored
metadata, so a client that returns within the stored TTL is rehydrated.

The TTL is configurable via `server.Config.SessionTTL` but is not currently
exposed through the operator CRD.

### MCPServer proxy TTL (2 hours)

The MCPServer proxy runner uses a separate, longer TTL of **2 hours**
(`pkg/transport/session/proxy_session.go:DefaultSessionTTL`). This applies to
the underlying SSE/streamable transport sessions, not the vMCP-level session
aggregation.

### Redis sliding-window TTL

When Redis session storage is enabled, every `Load` call issues a `GETEX` that
resets the key's TTL atomically (see `pkg/transport/session/storage_redis.go`,
`NewRedisStorage` and the `Load` function docstring). This means:

- Active sessions are preserved indefinitely as long as they receive at least
  one request per TTL window.
- Idle sessions expire automatically after the full TTL elapses with no access.
- There is no absolute maximum session lifetime enforced by Redis storage.

For the pod-local copy described above, an open standalone GET stream alone
does not count as activity, so a client that only listens must send a POST (for
example a ping) within `SessionTTL` to keep the copy on that pod. Otherwise the stream is closed,
and a reconnecting GET is rehydrated if the stored record is still valid and
answered 404 if not.

### Session garbage collection

| Trigger | Mechanism |
| ------- | --------- |
| Explicit termination (client disconnect, auth failure) | `DEL` issued immediately to Redis |
| Inactivity beyond TTL | Redis TTL expiry (automatic, no application-side action needed) |
| Background cache sweep (every `SweepInterval`, default 1 minute) | Closes backend connections of cached sessions whose metadata is gone or terminated, without refreshing the TTL of live ones |
| Pod-local cache eviction (LRU) | `onEvict` callback closes backend connections only; the Redis metadata key is **not** deleted and expires via TTL |

### Identity-binding storage and Redis access control

Each vMCP session carries an identity binding stored in session metadata under the
key `vmcp.identity.binding`. The canonical format is defined in
`pkg/auth/sessionbinding`. Authenticated bindings use NUL-separated fields:

- **Standard (2 fields):** `iss + "\x00" + sub` for interactive and unspecified routes.
- **Delegated (4 or 6 fields):** `iss`, `sub`, `delegated`, `client_id`, followed
  by `act.iss` and `act.sub` when the current actor is present.

Sessions without an auth identity use the literal string `"unauthenticated"`.

Every replica checks the stored binding against the authenticated caller on each
Legacy request that carries `Mcp-Session-Id`, reading it from the shared store
before the session is rehydrated locally. See
[Session Ownership](10-virtual-mcp-architecture.md#session-ownership) for the
matching rules and rolling-upgrade constraints.

The binding is stored as **plaintext** in the session store (Redis/Valkey). It is
not a credential — it identifies but does not authenticate a principal — but it
contains personally identifying information (the issuer URL and user subject).
Delegated bindings also store `client_id` and, when present, `act.iss` and
`act.sub` in plaintext; the actor subject may be a SPIFFE ID.

Operators are responsible for access-controlling the Redis/Valkey instance
equivalently to any other identity store. Concretely: enable Redis ACLs (Redis 6+)
or `requirepass`, restrict network reach with a Kubernetes `NetworkPolicy`, and
avoid sharing the cluster with untrusted workloads.

The session store prior to issue #5306 held an HMAC of the bearer token rather than
the raw `(iss, sub)` pair. That scheme reduced the value of a Redis dump at the cost
of breaking on every legitimate OAuth token refresh. The current scheme accepts
plaintext PII at rest as the price of correctness; operators who require additional
protection against a Redis compromise must layer Redis-side access controls as
described above.

### Transport security

`spec.sessionStorage.tls` (shared by `VirtualMCPServer`, `MCPServer` and
`MCPRemoteProxy`) enables TLS for the session-storage connection and for the rate
limiter's client to the same Redis. Standalone vMCP uses `sessionStorage.tls` in its
YAML (`caCertFile`, `insecureSkipVerify`), and proxy RunConfigs use
`scaling_config.session_redis.tls`; both map to `pkg/redisconfig.TLSConfig`.

- An empty `tls` object verifies the server certificate against the system roots.
- `caCertSecretRef` mounts a private CA at `/etc/toolhive/session-redis-tls/ca.crt`
  in the proxy runner or vMCP pod.
- `insecureSkipVerify` disables verification and is meant for testing only. The API
  rejects it together with `caCertSecretRef`, and rejects `tls` with a non-Redis
  provider.
- A failed handshake never falls back to plaintext.
- If the referenced CA Secret or key does not exist, the pod stays in
  `ContainerCreating`. This is fail-closed, as for the embedded auth server's Redis CA.
- The CA is mounted with `subPath`, so rotating the Secret's content requires a pod
  restart. Switching to a different Secret rolls the Deployment.
- On a `VirtualMCPServer`, the API rejects `spec.config.sessionStorage.tls`,
  because the operator derives the vMCP setting from `spec.sessionStorage.tls`.
- The API rejects a `caCertSecretRef` with an empty name or key.

When `tls` is omitted, the connection stays unencrypted for backward compatibility.
If TLS is omitted, or `insecureSkipVerify` is set, the session store constructor
logs one startup WARN carrying a `store` attribute, whether or not a password is
configured. vMCP additionally logs its existing no-authentication WARN when
`THV_SESSION_REDIS_PASSWORD` is unset; the two cover different gaps.

The operator-wide Redis fallback supports verified TLS through Helm:

```yaml
operator:
  defaultRedis:
    addr: redis.example.com:6380
    tls:
      enabled: true
      caCertSecretRef:
        name: redis-ca
        key: ca.crt
```

Omit `caCertSecretRef` to verify against system roots. The CA Secret must exist
in every workload namespace that uses the fallback, alongside any configured
password Secret. The operator passes references to the workloads; it does not
copy Secret contents between namespaces or connect to Redis itself.

If the global CA Secret or its configured key is missing, Kubernetes reports a
`FailedMount` event on each affected pod and leaves it in `ContainerCreating`.
Inspect `kubectl describe pod` in the workload namespace to identify the missing
reference. A missing global CA affects every workload using that fallback in the
namespace; it does not affect workloads with their own Redis configuration.

The chart sets `TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED` and, for a private CA,
`TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME` and
`TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY`. Invalid settings stop operator startup.
The operator reads and validates the defaults once at startup and passes that
snapshot to the controllers and vMCP converter. Configuration conversion and CA
projection do not read the environment during reconciliation.
CA projection, certificate validation, rate-limiter TLS, and restart requirements
are the same as for per-workload session storage. A change to operator defaults
is applied as workloads reconcile after the operator restarts.

Explicit per-workload Redis settings override the entire global connection,
including TLS; omitting TLS on an explicit Redis connection keeps that connection
plaintext. The default remains plaintext when global TLS is disabled. Global TLS
always verifies server certificates; it has no `insecureSkipVerify` option.

This change preserves existing provider selection: MCPRemoteProxy suppresses the
fallback for any explicit `sessionStorage`, while MCPServer and VirtualMCPServer
can still select global Redis when `provider: memory` is explicit. Their existing
password-selection behavior is unchanged. Configure an explicit Redis connection
when overriding global defaults, and avoid relying on this provider inconsistency.

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
| Dial timeout | 5 s (`DefaultDialTimeout`) | `github.com/stacklok/toolhive-core/redis/config.go` |
| Read timeout | 3 s (`DefaultReadTimeout`) | |
| Write timeout | 3 s (`DefaultWriteTimeout`) | |
| Key prefix | configurable | Must end with `:` to avoid collisions |

**Memory:** Session payloads include the routing table and tool metadata. Rough
estimate: 10–50 KB per session depending on backend count and tool count.
Maximum concurrent session count across the fleet is `replicas × 1,000`.

**Connection pools:** Each vMCP pod creates one go-redis client with its own
connection pool. Client construction is delegated to
`github.com/stacklok/toolhive-core/redis` and no explicit `PoolSize` is
configured (the `tcredis.Config` struct does not expose one), so go-redis
applies its default of `10 × GOMAXPROCS` connections per pool. Total Redis
connections therefore scale as `replicas × (10 × GOMAXPROCS)`. Size the Redis
`maxclients` setting accordingly.

The optimizer's shared embedding cache (see
[Optimizer embeddings](#optimizer-embeddings)) opens one more client per pod
against the same Redis, as rate limiting does when it is enabled. That client
is mostly idle: each optimizer build sends it at most two pipelines, so it uses
a few connections in bursts rather than a full pool.

**Eviction policy:** Use `allkeys-lru` so Redis can shed stale sessions under
memory pressure rather than returning errors on new writes.

**Persistence:** Redis persistence is not required for session storage. If the
Redis pod restarts, all active sessions are lost and MCP clients must reconnect.
For production deployments where session continuity is critical, use a
`StatefulSet` with a PVC and enable RDB/AOF persistence.

## Optimizer embeddings

Each replica keeps its optimizer index in its own in-memory SQLite database.
Building a session's optimizer upserts the session's tools, and a tool is
embedded only when no stored vector matches its content key. The key is a
SHA-256 over the embedded text, the configured provider, endpoint URL and
model, the model id read live from the backend, and a format version.

Two mechanisms keep the first sessions after a deploy from each embedding the
whole catalogue:

- **Within a replica**, with any session storage, builds that need new vectors
  take turns. A build that waited looks up the store again and reuses what the
  build ahead of it committed, so concurrent sessions on a cold replica embed
  the catalogue once. Builds whose tools are all stored never wait. A build
  waits at most 30 seconds for its turn and then embeds without it, so a build
  stuck on a hung embedding backend delays the others by that much. A session
  with one new tool can also wait that long behind a build that is embedding a
  whole catalogue.
- **Across replicas**, when session storage is Redis, every vector embedded
  under a verified model identity is also written to Redis, and a replica looks
  there for the vectors it lacks before embedding them. Vectors embedded while
  the model id could not be read stay local, as they are not reusable anywhere.

| Parameter | Value |
| --------- | ----- |
| Key | `<keyPrefix>optimizer:embedding:<content key>` |
| Value | Raw little-endian float32 vector, 4 bytes per dimension |
| TTL | Fixed 7 days from when a replica wrote the vector; reads do not extend it |

A key is written only when a replica embeds its vector. A replica that already
holds the vector never writes it again, so a vector in constant use still
expires 7 days after it was written, and the next cold replica re-embeds it.

Redis holds one vector per distinct tool text per model. The value is 4 bytes
per dimension: about 1.5 KB at 384 dimensions, and about 6 KB for a
1,536-dimension model such as `text-embedding-3-small`. With the 100-byte key
and Redis per-key overhead, a 384-dimension vector takes about 1.7 KB, so 1,000
tools take about 1.7 MB. The content key covers the endpoint URL, so replicas
that reach the embedding service through different URLs, such as a localhost
sidecar, never share vectors. Vectors for an old description, endpoint, or
model stay until their TTL expires.

The keys sit under the session key prefix, so an ACL scoped to `~<keyPrefix>*`
covers them, and a `SCAN <keyPrefix>*` that counts sessions counts them too. A
request whose `Mcp-Session-Id` names one of these keys gets the same 404 as a
missing session. Under `volatile-lru` (the ElastiCache default) or
`allkeys-lru`, Redis evicts these keys as it evicts sessions. Under
`noeviction` (the MemoryDB and self-managed default), they take memory that
session writes also need.

The cache only removes repeat work. Replicas do not coordinate the embedding
call, so two that miss the same vectors at the same moment both embed them. A
build reads Redis at most once, before it waits for its turn, and writes at
most once, after it commits. A slow Redis therefore delays only that build, by
the Redis timeouts on those calls. A Redis error after startup is logged at
WARN and costs reuse. A lookup reads at most 64 KiB of a value, so an
oversized value costs no more than that to reject.

Anyone who can write the keyspace can replace vectors and so change
`find_tool` ranking. Results stay limited to the tools the caller may see, and
`call_tool` is still authorized, but treat write access to the instance as you
would for the session data it already holds. A replaced vector that a replica
has already read is committed to its local SQLite under a valid key. It stays
in use on that replica after Redis is cleaned up, until the tool text or the
model changes or the pod restarts.

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
| Resource URI template-match cap | 2048 bytes | vmcp/types.go:MaxResourceURILength | No |
| vMCP session TTL | 30 minutes | `vmcp/server/server.go:defaultSessionTTL` | Via `server.Config.SessionTTL` (not CRD-exposed) |
| MCPServer proxy session TTL | 2 hours | `transport/session/proxy_session.go:DefaultSessionTTL` | No |
| Redis dial timeout | 5 s | `toolhive-core/redis/config.go:DefaultDialTimeout` | Via `tcredis.Config.DialTimeout` |
| Redis read timeout | 3 s | `toolhive-core/redis/config.go:DefaultReadTimeout` | Via `tcredis.Config.ReadTimeout` |
| Redis write timeout | 3 s | `toolhive-core/redis/config.go:DefaultWriteTimeout` | Via `tcredis.Config.WriteTimeout` |
| Optimizer embedding turn wait | 30 s | `vmcp/optimizer/internal/toolstore/sqlite_store.go:defaultEmbedTurnWait` | No |
| Shared optimizer embedding TTL | 7 days | `vmcp/optimizer/internal/toolstore/embedding_cache.go:sharedEmbeddingTTL` | No |
| Shared optimizer embedding read cap | 64 KiB | `vmcp/optimizer/internal/toolstore/embedding_cache.go:maxSharedEmbeddingBytes` | No |
| forEach max iterations | 1,000 | `vmcp/config/composite_validation.go:MaxForEachIterations` | Via `WorkflowStepConfig.MaxIterations` (capped at 1,000) |

## Related

- `pkg/vmcp/server/sessionmanager/factory.go` — LRU cache and `FactoryConfig`
- `pkg/vmcp/server/server.go` — `defaultSessionTTL`, `Config.SessionTTL`
- `pkg/transport/session/storage_redis.go` — sliding-window TTL via `GETEX`
- `github.com/stacklok/toolhive-core/redis/config.go` — Redis client config and timeout defaults
- `docs/arch/10-virtual-mcp-architecture.md` — overall vMCP architecture
- `docs/arch/11-auth-server-storage.md` — Redis Sentinel for auth server sessions
