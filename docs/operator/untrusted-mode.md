# Untrusted Mode Operator Guide

How to run a third-party MCP server whose code you do not trust while still
letting it call upstream APIs as the consenting user. Architecture, threat
model, and the full security boundary:
[Untrusted Mode architecture](../arch/16-untrusted-mode.md) and
[ADR-0001](../arch/adr/0001-untrusted-mcp-egress-broker.md).

> **Read the security boundary first.** Untrusted mode stops the server from
> *stealing* credentials. It does not stop the server from *using* the current
> user's credential within granted scopes, and it does not stop data fetched
> with that credential from leaving through MCP tool responses.

## Prerequisites

- **Untrusted mode is opt-in and disabled by default.** Set the operator helm
  value `operator.features.untrustedMode: true` (this sets
  `TOOLHIVE_ENABLE_UNTRUSTED_MODE=true` on the operator Deployment, which the
  operator forwards to every vMCP Deployment it creates — one value controls
  both processes). Enable it only if you accept the cost: untrusted mode runs
  one backend pod per (user, session, untrusted MCPServer) plus Envoy/broker
  sidecars per pod.
  - When the flag is off, an `MCPServer` with `spec.untrusted: true` is
    reconciled as a normal **trusted** workload: no per-session pods, no bump
    CA, no egress-lockdown NetworkPolicy, no egress broker, and the
    secretKeyRef backend-env gate does not apply. The operator surfaces this
    on the `UntrustedMode` status condition (`False` /
    `UntrustedModeDisabled`) and emits a one-shot Warning event.
  - The CRD fields (`spec.untrusted`, `spec.egressPolicy`), CEL validation,
    and the trusted-mode egress `NetworkPolicy` from `spec.permissionProfile`
    work regardless of the flag.
- A `VirtualMCPServer` fronting an `MCPGroup` (untrusted workloads must be
  group members — enforced by CEL).
- The vMCP's embedded auth server with the upstream provider(s) configured and
  **Redis storage** (standalone/cluster — Sentinel-managed Redis is not
  supported for untrusted egress).
- A CNI that enforces `NetworkPolicy` (hard dependency).
- Session affinity `ClientIP` on the MCPServer (required by CEL).

## Marking a server untrusted

Set `spec.untrusted: true` and declare an `egressPolicy`:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPServer
metadata:
  name: github-community
  namespace: default
spec:
  image: ghcr.io/example/github-mcp:latest
  transport: streamable-http
  port: 8080
  groupRef: research-group          # required: an MCPGroup fronted by a VirtualMCPServer
  sessionAffinity: ClientIP         # required
  untrusted: true
  egressPolicy:
    providers:
    - provider: github              # must match the auth-server upstream provider name
      allowedHosts:
      - api.github.com
      - "*.githubusercontent.com"   # one-label wildcards allowed
      allowedMethods: [GET, POST]   # omit for read-only (GET/HEAD/OPTIONS only)
      allowedPathPrefixes: ["/repos/", "/user"]  # omit for all paths on the allowed hosts
      credentialEnvName: GITHUB_TOKEN            # the env var the server reads its "token" from
```

CEL validation enforces, when `untrusted: true`:

- `egressPolicy` with at least one provider (required);
- `groupRef` set (the server must be fronted by a vMCP);
- `spec.secrets` forbidden — declare providers in `egressPolicy` and let the
  broker inject; no Secret/ConfigMap-sourced env or volumes on the backend
  either (runtime gate, terminal `SecretEnvRejected` condition);
- `podTemplateSpec` and `backendReplicas` forbidden — the untrusted session
  lifecycle owns the pod shape and the replica count (one pod per
  (user, MCPServer) pair).

The same provider name must appear in the vMCP's embedded auth server upstream
chain; the broker looks the user's token up by it.

## How the pieces fit at runtime

1. The operator reconciles the untrusted MCPServer into: a per-server
   **bump CA** (generation-named Secret + public bundle ConfigMap), an
   **egress-policy ConfigMap** the sidecar compiles, and a
   **NetworkPolicy** confining session-pod egress to loopback, cluster DNS,
   and the CIDRs your `allowedHosts` resolve to.
2. On a user's first call, vMCP clones a backend pod from the server template
   and adds three containers: a CA-seed init container, an Envoy sidecar, and
   the credential broker. The pod is stamped with the user's identity.
3. The server's egress goes through the sidecar; the broker injects the user's
   consented token only for policy-allowlisted destinations and scans responses
   for credential echoes.

Idle pods are torn down after 30 minutes (`THV_UNTRUSTED_IDLE_TTL`); cold start
is bounded at 120s (`THV_UNTRUSTED_READINESS_TIMEOUT`). Capacity caps default to
10 pods/user, 200 pods/server, 0.8× session-cache globally — see the
[configuration reference](../arch/16-untrusted-mode.md#7-configuration-reference)
for the `THV_UNTRUSTED_*` tunables and image overrides.

## Sentinel semantics

The env var named by `credentialEnvName` contains the literal
`thv-untrusted-sentinel:<provider>` — a boot-compatibility shim, **not** a
token. The server must make its upstream call anyway; the broker replaces the
placeholder at the sidecar. Servers that ignore `HTTP_PROXY`/`HTTPS_PROXY`, pin
certificates, or poll in the background will not work (see
[limitations](../arch/16-untrusted-mode.md#known-limitations)).

## kube-dns limitation

The generated NetworkPolicy permits DNS only to pods labeled `k8s-app:
kube-dns` in namespace `kube-system` (kubeadm/kind/GKE/EKS defaults). If your
cluster DNS lives elsewhere, backend name resolution fails loudly — patch the
`<mcpserver-name>-egress` NetworkPolicy's DNS peer to match your DNS pods.

## Consent UX for end users

When a user who has not consented the upstream provider calls a tool that needs
it, the tool result is an error whose text is:

```
UPSTREAM_CONSENT_REQUIRED {"provider":"github","authorize_url":"https://thv.example.com/oauth/authorize"}
```

The `UPSTREAM_CONSENT_REQUIRED` marker is machine-detectable; MCP clients
surface it as a consent prompt. The `authorize_url` is only an endpoint — the
client merges its own `client_id`, `redirect_uri`, and PKCE parameters, drives
the OAuth flow in a browser, and retries the tool call. The URL comes from the
vMCP's auth-server issuer configuration; when unset, the payload carries just
the provider name and the user must be directed to consent out of band.

## Token encryption at rest (recommended)

Encrypt upstream tokens in Redis with AES-256-GCM envelope encryption:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: thv-token-keks
stringData:
  kek-2026-07: <base64-encoded 32 random bytes>   # data keys are key IDs
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: VirtualMCPServer
spec:
  authServerConfig:
    storage:
      type: redis
      redis:
        addr: redis:6379
        aclUserConfig:
          passwordSecretRef: { name: redis-acl, key: password }
      tokenEncryption:
        activeKeyId: kek-2026-07
        keySecretRef: { name: thv-token-keks }
```

Rotation: add a new key to the Secret, flip `activeKeyId`, keep the old key
until all rows are re-sealed or expired — both the auth server and the broker
sidecars receive the full key set, so retired IDs keep decrypting. Key IDs must
match `[A-Za-z0-9_-]+`. Requires standalone/cluster Redis; Sentinel storage
emits a `TokenEncryptionNotSupportedForUntrusted` Warning and sidecars cannot
decrypt.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Server boots, upstream calls fail with 403 | destination/method/path not in `egressPolicy` for that provider |
| Connection refused to a host | host not in `allowedHosts` (no route at the proxy) |
| Tool result says `UPSTREAM_CONSENT_REQUIRED` | user has not consented; drive the authorize URL flow |
| Denials with `credential-unavailable` | consent expired/revoked, or token-store Redis unreachable from sidecars |
| Pods created then deleted ~2 min later | cold start exceeded the readiness timeout (image pull, broker health) — check the broker `/healthz` (Redis reachable, policy loaded, CA valid) |
| All egress broken, DNS timeouts | cluster DNS not in `kube-system`/`k8s-app=kube-dns` — see kube-dns limitation |
| Warning event `TokenEncryptionNotSupportedForUntrusted` | `tokenEncryption` set with Sentinel Redis storage |
