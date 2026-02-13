# Redis Sentinel Storage for Auth Server

This guide explains how to configure Redis Sentinel as the storage backend for ToolHive's embedded authorization server, enabling horizontal scaling across multiple auth server replicas.

## Overview

By default, ToolHive's embedded auth server uses in-memory storage. This works well for single-instance deployments but does not support horizontal scaling since each replica has its own isolated state. Redis Sentinel provides a shared, highly available storage backend that enables multiple auth server replicas to share OAuth 2.0 state (tokens, authorization codes, clients, and user data).

**Key design decisions:**

- **Sentinel-only**: Only Redis Sentinel deployments are supported (not standalone or cluster mode). Sentinel provides automatic failover and high availability without the complexity of Redis Cluster.
- **ACL user authentication**: Only Redis ACL user authentication is supported. This is the modern Redis authentication mechanism (Redis 6+) that provides fine-grained access control.
- **Multi-tenancy via key prefixes**: Each auth server instance uses a unique key prefix (`thv:auth:{namespace:name}:`) to isolate its data, allowing multiple auth servers to share the same Redis deployment.

## Prerequisites

- A running Redis Sentinel deployment (Redis 6+ for ACL support)
- Redis ACL user configured with appropriate permissions
- For Kubernetes: Secrets containing Redis credentials

## Configuration

### Kubernetes (MCPExternalAuthConfig CRD)

When using the ToolHive operator, Redis storage is configured through the `storage` field in the embedded auth server section of `MCPExternalAuthConfig`.

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPExternalAuthConfig
metadata:
  name: my-auth-config
  namespace: default
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    # ... other auth server config ...

    storage:
      type: redis
      redis:
        sentinelConfig:
          masterName: mymaster
          # Option 1: Direct Sentinel addresses
          sentinelAddrs:
            - "redis-sentinel-0.redis-sentinel:26379"
            - "redis-sentinel-1.redis-sentinel:26379"
            - "redis-sentinel-2.redis-sentinel:26379"
          db: 0

        aclUserConfig:
          usernameSecretRef:
            name: redis-credentials
            key: username
          passwordSecretRef:
            name: redis-credentials
            key: password

        # Optional timeouts (shown with defaults)
        dialTimeout: "5s"
        readTimeout: "3s"
        writeTimeout: "3s"
```

#### Sentinel Service Discovery

Instead of listing Sentinel addresses directly, you can reference a Kubernetes Service. The operator resolves the Service's Endpoints to discover Sentinel instances automatically.

```yaml
storage:
  type: redis
  redis:
    sentinelConfig:
      masterName: mymaster
      # Option 2: Kubernetes Service discovery
      sentinelService:
        name: rfs-redis-sentinel
        namespace: redis    # defaults to same namespace if omitted
        port: 26379         # defaults to 26379 if omitted
      db: 0

    aclUserConfig:
      usernameSecretRef:
        name: redis-credentials
        key: username
      passwordSecretRef:
        name: redis-credentials
        key: password
```

> **Note:** `sentinelAddrs` and `sentinelService` are mutually exclusive. Specify one or the other.

#### Redis Credentials Secret

Create a Kubernetes Secret containing the Redis ACL username and password:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: redis-credentials
  namespace: default
type: Opaque
stringData:
  username: toolhive-auth
  password: "<your-secure-password>"
```

### RunConfig (Process Boundary Configuration)

When the auth server configuration is serialized for passing across process boundaries (e.g., from operator to proxy-runner), it uses the `RunConfig` format:

```json
{
  "type": "redis",
  "redisConfig": {
    "sentinelConfig": {
      "masterName": "mymaster",
      "sentinelAddrs": [
        "redis-sentinel-0:26379",
        "redis-sentinel-1:26379",
        "redis-sentinel-2:26379"
      ],
      "db": 0
    },
    "authType": "aclUser",
    "aclUserConfig": {
      "usernameEnvVar": "TOOLHIVE_AS_REDIS_USERNAME",
      "passwordEnvVar": "TOOLHIVE_AS_REDIS_PASSWORD"
    },
    "keyPrefix": "thv:auth:{default:my-auth-config}:",
    "dialTimeout": "5s",
    "readTimeout": "3s",
    "writeTimeout": "3s"
  }
}
```

In RunConfig format, credentials are referenced via environment variables rather than Kubernetes Secrets. The operator handles the translation from Secret references to environment variables when constructing the proxy-runner pod.

## Deploying Redis with the Spotahome Redis Operator

The [Spotahome Redis Operator](https://github.com/spotahome/redis-operator) provides a Kubernetes-native way to deploy and manage Redis Sentinel clusters. This section walks through deploying a Redis Sentinel cluster suitable for ToolHive's auth server storage.

### Step 1: Install the Redis Operator

```bash
# Using Helm
helm repo add redis-operator https://spotahome.github.io/redis-operator
helm repo update

helm install redis-operator redis-operator/redis-operator \
  --namespace redis-operator \
  --create-namespace
```

### Step 2: Create the Redis Failover Resource

The `RedisFailover` CRD deploys a Redis master-replica set with Sentinel monitoring:

```yaml
apiVersion: databases.spotahome.com/v1
kind: RedisFailover
metadata:
  name: redis
  namespace: redis
spec:
  sentinel:
    replicas: 3
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 200m
        memory: 256Mi
  redis:
    replicas: 3
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: 512Mi
    customConfig:
      - "aclfile /data/users.acl"
    storage:
      persistentVolumeClaim:
        metadata:
          name: redis-data
        spec:
          accessModes:
            - ReadWriteOnce
          resources:
            requests:
              storage: 1Gi
```

### Step 3: Configure Redis ACL Users

Create a ConfigMap or init container to provision the ACL file. The ACL user needs permissions on the key prefix used by ToolHive:

```
# /data/users.acl
user toolhive-auth on ><your-secure-password> ~thv:auth:* &* +@all
```

This ACL entry:
- `on` — Enables the user
- `><your-secure-password>` — Sets the password
- `~thv:auth:*` — Allows access to all keys with the `thv:auth:` prefix
- `&*` — Allows access to all Pub/Sub channels (required for Sentinel)
- `+@all` — Allows all commands

> **Security note:** In production, restrict the allowed commands to the minimum required set. The auth server uses `GET`, `SET`, `DEL`, `EXISTS`, `EXPIRE`, `SADD`, `SREM`, `SMEMBERS`, `EVAL`, `MULTI`, `EXEC`, and `PING`.

### Step 4: Create the ToolHive Auth Config

With the Redis Sentinel cluster running, configure ToolHive to use it:

```yaml
# Redis credentials Secret
apiVersion: v1
kind: Secret
metadata:
  name: redis-credentials
  namespace: default
type: Opaque
stringData:
  username: toolhive-auth
  password: "<your-secure-password>"
---
# MCPExternalAuthConfig with Redis storage
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPExternalAuthConfig
metadata:
  name: my-auth-config
  namespace: default
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: "https://auth.example.com"
    upstreamProviders:
      - name: my-idp
        type: oidc
        oidcConfig:
          issuerUrl: https://accounts.google.com
          clientId: "my-client-id"
          clientSecretRef:
            name: idp-client-secret
            key: client-secret
    storage:
      type: redis
      redis:
        sentinelConfig:
          masterName: mymaster
          sentinelService:
            name: rfs-redis-sentinel
            namespace: redis
        aclUserConfig:
          usernameSecretRef:
            name: redis-credentials
            key: username
          passwordSecretRef:
            name: redis-credentials
            key: password
```

## Data Model

### Key Schema

All keys use the prefix `thv:auth:{namespace:name}:` where `{namespace:name}` is a Redis hash tag ensuring all keys for a single auth server land in the same hash slot.

| Key Pattern | Purpose | TTL |
|---|---|---|
| `{prefix}access:{signature}` | Access token data | 1 hour (default) |
| `{prefix}refresh:{signature}` | Refresh token data | 30 days (default) |
| `{prefix}authcode:{code}` | Authorization code | 10 minutes |
| `{prefix}pkce:{signature}` | PKCE challenge data | 10 minutes |
| `{prefix}client:{client_id}` | OAuth client registration | 30 days (public) / none (confidential) |
| `{prefix}user:{user_id}` | User account | None |
| `{prefix}provider:{len}:{provider_id}:{subject}` | Provider identity linkage | None |
| `{prefix}upstream:{session_id}` | Upstream IDP tokens | Matches token lifetime |
| `{prefix}pending:{state}` | In-flight authorization | 10 minutes |
| `{prefix}invalidated:{code}` | Replay detection for auth codes | 30 minutes |
| `{prefix}jwt:{jti}` | Client assertion JWT replay prevention | Matches JWT `exp` |

### Secondary Indexes

Redis Sets are used as secondary indexes for efficient lookups:

| Set Key Pattern | Purpose |
|---|---|
| `{prefix}reqid:access:{request_id}` | Request ID → access token signatures |
| `{prefix}reqid:refresh:{request_id}` | Request ID → refresh token signatures |
| `{prefix}user:upstream:{user_id}` | User → upstream token session IDs |
| `{prefix}user:providers:{user_id}` | User → provider identity keys |

These indexes enable grant-wide operations like token revocation (finding all tokens for a request ID) and user-scoped queries (finding all upstream tokens for a user).

### Atomicity and Consistency

The storage implementation uses different strategies depending on the consistency requirements of each operation:

- **Lua scripts** for strict atomicity: upstream token storage with user reverse-index cleanup, last-used timestamp updates
- **Pipelines** (`MULTI`/`EXEC`) for batched operations: authorization code invalidation, token session creation with secondary index updates
- **Individual commands** with best-effort cleanup: token revocation, refresh token rotation. These operations use `SMEMBERS` + individual `DEL` calls, meaning partial failures are possible but safe (orphaned keys expire via TTL)

Secondary index cleanup is best-effort: stale entries may remain temporarily but are cleaned up on the next write or by TTL expiration.

## Troubleshooting

### Connection Failures

**Symptom:** Auth server fails to start with Redis connection errors.

**Checks:**
1. Verify Sentinel addresses are reachable from the auth server pod:
   ```bash
   kubectl exec -it <pod> -- nc -zv <sentinel-host> 26379
   ```
2. Verify the master name matches the Sentinel configuration:
   ```bash
   redis-cli -h <sentinel-host> -p 26379 SENTINEL get-master-addr-by-name mymaster
   ```
3. Check that the ACL user credentials are correct:
   ```bash
   redis-cli -h <redis-host> -p 6379 --user toolhive-auth --pass <password> PING
   ```

### Authentication Errors

**Symptom:** `WRONGPASS` or `NOAUTH` errors in logs.

**Checks:**
1. Verify the Secret exists and contains the correct keys:
   ```bash
   kubectl get secret redis-credentials -o jsonpath='{.data.username}' | base64 -d
   kubectl get secret redis-credentials -o jsonpath='{.data.password}' | base64 -d
   ```
2. Verify the ACL user exists on Redis:
   ```bash
   redis-cli -h <redis-host> -p 6379 ACL LIST
   ```

### Key Permission Errors

**Symptom:** `NOPERM` errors when accessing keys.

**Checks:**
1. Verify the ACL user has the correct key pattern permissions:
   ```bash
   redis-cli -h <redis-host> -p 6379 ACL GETUSER toolhive-auth
   ```
2. Ensure the key pattern includes the `thv:auth:` prefix:
   ```
   user toolhive-auth on ><password> ~thv:auth:* &* +@all
   ```

### Failover Issues

**Symptom:** Requests fail during Redis master failover.

**Notes:**
- The Redis client library handles Sentinel failover automatically. During a failover (typically a few seconds), requests may briefly fail and retry.
- Ensure at least 3 Sentinel instances for quorum-based failover.
- Monitor Sentinel logs for failover events:
  ```bash
  kubectl logs <sentinel-pod> | grep "failover"
  ```

## Configuration Reference

### AuthServerStorageConfig (CRD)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `type` | `string` | No | `memory` | Storage backend type: `memory` or `redis` |
| `redis` | `RedisStorageConfig` | When type=redis | — | Redis configuration |

### RedisStorageConfig (CRD)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `sentinelConfig` | `RedisSentinelConfig` | Yes | — | Sentinel connection settings |
| `aclUserConfig` | `RedisACLUserConfig` | Yes | — | ACL user credentials |
| `dialTimeout` | `string` | No | `5s` | Connection establishment timeout |
| `readTimeout` | `string` | No | `3s` | Socket read timeout |
| `writeTimeout` | `string` | No | `3s` | Socket write timeout |

### RedisSentinelConfig (CRD)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `masterName` | `string` | Yes | — | Redis master name monitored by Sentinel |
| `sentinelAddrs` | `[]string` | One of addrs/service | — | Direct Sentinel host:port addresses |
| `sentinelService` | `SentinelServiceRef` | One of addrs/service | — | Kubernetes Service for Sentinel discovery |
| `db` | `int32` | No | `0` | Redis database number |

### SentinelServiceRef (CRD)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `name` | `string` | Yes | — | Name of the Kubernetes Service |
| `namespace` | `string` | No | Same namespace | Namespace of the Service |
| `port` | `int32` | No | `26379` | Port of the Sentinel service |

### RedisACLUserConfig (CRD)

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `usernameSecretRef` | `SecretKeyRef` | Yes | — | Secret reference for Redis username |
| `passwordSecretRef` | `SecretKeyRef` | Yes | — | Secret reference for Redis password |

## Related Documentation

- [Architecture Overview](arch/00-overview.md)
- [Operator Architecture](arch/09-operator-architecture.md)
- [Auth Server Storage Architecture](arch/11-auth-server-storage.md)
