# Manual verification — `MCPServerEntry.spec.headerForward` on Kind

This guide walks through the end-to-end verification I ran for issue
[#4996](https://github.com/stacklok/toolhive/issues/4996): an
`MCPServerEntry` declaring `spec.headerForward` is consumed as a static
backend of a `VirtualMCPServer`, and the configured headers (plaintext +
secret-backed) actually arrive at the remote backend on every outbound
request.

The setup deploys a tiny in-cluster echo server that records every request
header it receives, points an `MCPServerEntry` at it via a CoreDNS rewrite
(so the operator's SSRF blocklist is happy), and then checks the
captured headers via the echo's `/headers` endpoint.

Total runtime: ~5 minutes after the prerequisites are in place.

## Prerequisites

| Tool | Version I used |
|------|----------------|
| `kind` | latest |
| `kubectl` | matches your cluster |
| `helm` | v3 |
| `ko` | latest |
| `python3` | for the CoreDNS patch (only if you want to script the JSON edit) |

You also need a checkout of this branch
(`cburns/headerforward-envvar`) at the repo root.

## 1. Create the Kind cluster

```bash
cd <repo-root>
kind create cluster --name toolhive --config test/e2e/thv-operator/kind-config.yaml
kind get kubeconfig --name toolhive > kconfig.yaml
export KUBECONFIG=$(pwd)/kconfig.yaml
```

If you already have a cluster named `toolhive`, skip the create and just
re-export the kubeconfig.

## 2. Install the toolhive CRDs

```bash
helm upgrade --install toolhive-operator-crds deploy/charts/operator-crds
```

## 3. Build and load the operator + vMCP images

```bash
OPERATOR_IMAGE=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/thv-operator    | tail -1)
TOOLHIVE_IMAGE=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/thv-proxyrunner | tail -1)
VMCP_IMAGE=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/vmcp                | tail -1)

kind load docker-image --name toolhive "$OPERATOR_IMAGE" "$TOOLHIVE_IMAGE" "$VMCP_IMAGE"
```

## 4. Deploy the operator

```bash
helm upgrade --install toolhive-operator deploy/charts/operator \
  --namespace toolhive-system --create-namespace \
  --set operator.image="$OPERATOR_IMAGE" \
  --set operator.toolhiveRunnerImage="$TOOLHIVE_IMAGE" \
  --set operator.vmcpImage="$VMCP_IMAGE" \
  --set operator.features.experimental=true

kubectl -n toolhive-system rollout status deploy/toolhive-operator
```

## 5. Add a CoreDNS rewrite for the test hostname

The operator rejects any `remoteUrl` whose hostname matches an internal
SSRF blocklist (including `*.cluster.local`, RFC1918 ranges, etc.). To
keep the test in-cluster, we map a public-looking hostname
(`echo-public.test`) to the echo service's ClusterIP via a CoreDNS hosts
block.

Apply the test manifest first so the Service is created:

```bash
kubectl apply -f docs/manual-verification/headerforward-kind/manifest.yaml
```

Get the echo service's ClusterIP:

```bash
ECHO_IP=$(kubectl -n hf-test get svc echo-mcp -o jsonpath='{.spec.clusterIP}')
echo "echo-mcp ClusterIP: $ECHO_IP"
```

Patch CoreDNS:

```bash
kubectl get configmap -n kube-system coredns -o yaml | python3 -c "
import yaml, sys, os
cm = yaml.safe_load(sys.stdin)
ip = os.environ['ECHO_IP']
new_block = f'''
    echo-public.test:53 {{
        hosts {{
            {ip} echo-public.test
            fallthrough
        }}
    }}
'''
if 'echo-public.test:53' not in cm['data']['Corefile']:
    cm['data']['Corefile'] += new_block
print(yaml.safe_dump(cm, sort_keys=False))
" | kubectl apply -f -

kubectl rollout restart -n kube-system deploy/coredns
kubectl rollout status   -n kube-system deploy/coredns
```

After CoreDNS restarts, give the operator a moment to re-validate the
`MCPServerEntry`. If it was still rejected from a previous run, force a
re-reconcile by patching the spec back to the public hostname:

```bash
kubectl -n hf-test patch mcpserverentry github-copilot-fake \
  --type=merge -p '{"spec":{"remoteUrl":"http://echo-public.test/"}}'
```

## 6. Wait for the vMCP pod to come up

```bash
kubectl -n hf-test rollout status deploy/vmcp-headerfwd
kubectl -n hf-test get pods
```

Expected:

```
NAME                              READY   STATUS    RESTARTS   AGE
echo-mcp-...                      1/1     Running   0          1m
vmcp-headerfwd-...                1/1     Running   0          30s
```

## 7. Inspect the env vars the operator emitted on the vMCP pod

```bash
VMCP_POD=$(kubectl -n hf-test get pod -l app.kubernetes.io/name=virtualmcpserver -o jsonpath='{.items[0].metadata.name}')
kubectl -n hf-test describe pod "$VMCP_POD" | grep -A 1 "TOOLHIVE_HEADER_FORWARD\|TOOLHIVE_SECRET_HEADER"
```

Expected — one JSON manifest env var (literal value) plus one
`secretKeyRef` env var per secret-backed header:

```
TOOLHIVE_HEADER_FORWARD_GITHUB_COPILOT_FAKE:                  {"addPlaintextHeaders":{"X-MCP-Toolsets":"projects,issues,pull_requests","X-Trace-Id":"kind-test"},"addHeadersFromSecret":{"X-Api-Key":"HEADER_FORWARD_X_API_KEY_GITHUB_COPILOT_FAKE"}}
TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_GITHUB_COPILOT_FAKE: <set to the key 'token' in secret 'test-secret'>  Optional: false
```

The plaintext header values are visible in `kubectl describe pod` output
(this is documented in the PR description as a deliberate trade-off —
truly sensitive values still ride `valueFrom.secretKeyRef`).

## 8. Verify the headers actually arrive at the backend

The vMCP runs a 30-second health check loop against every static backend.
That loop alone exercises the full transport chain (the round tripper, the
`secrets.EnvironmentProvider` resolution, etc.), so the echo server should
see the configured headers without you sending a single tool call.

Wait for the next health check (≤30 s after pod start) then dump the
captured headers:

```bash
kubectl run --rm -i --restart=Never \
  --image=curlimages/curl:latest -n hf-test header-check \
  -- -s http://echo-mcp/headers
```

Expected output (a JSON array, one entry per recorded request):

```json
[
  {
    "Host": "echo-public.test",
    "User-Agent": "Go-http-client/1.1",
    ...
    "X-Api-Key": "secret-from-k8s-secret",
    "X-Mcp-Toolsets": "projects,issues,pull_requests",
    "X-Trace-Id": "kind-test",
    ...
  },
  ...
]
```

What the three header values prove:

| Header | Value | Proves |
|---|---|---|
| `X-Mcp-Toolsets` | `projects,issues,pull_requests` | Plaintext header from `addPlaintextHeaders` arrives with original casing/punctuation |
| `X-Trace-Id` | `kind-test` | Same — second plaintext header, scoped per-entry |
| `X-Api-Key` | `secret-from-k8s-secret` | Secret-backed header from `addHeadersFromSecret` is resolved via `valueFrom.secretKeyRef` and injected by the round tripper at request time |

If you want to send a real tool call (the health check is identical from
the round tripper's perspective, but you may want to verify against the
`/mcp` endpoint as well), port-forward the vMCP and use any MCP-compatible
client.

## 9. Tear-down

```bash
kubectl delete -f docs/manual-verification/headerforward-kind/manifest.yaml
helm -n toolhive-system uninstall toolhive-operator
helm uninstall toolhive-operator-crds
# kind delete cluster --name toolhive   # if you want to nuke the cluster
```

Reverting the CoreDNS rewrite is optional — the `echo-public.test`
hostname only resolves while the cluster has the `hf-test` Service
running.

## What this verifies vs. doesn't

- **Verifies**: end-to-end data path from `MCPServerEntry.spec.headerForward`
  → operator-emitted env vars on the vMCP pod → `readHeaderForwardFromEnv`
  → `Backend.HeaderForward` → round tripper → outbound request headers,
  for both plaintext and secret-backed entries, in static (inline) mode.
- **Verifies**: original header casing / punctuation is preserved
  end-to-end (the JSON manifest env var carries the literal user-authored
  names, not the env-var-normalized form).
- **Verifies**: secret values never enter the operator's view of the world
  (Secret value lands in the pod via `valueFrom.secretKeyRef`, the
  operator only handles the identifier).
- **Doesn't verify**: dynamic mode (when `spec.outgoingAuth.source` is
  `discovered`). That path reads `headerForward` directly from the
  `MCPServerEntry` CRD via `pkg/vmcp/workloads/k8s.go::mcpServerEntryToBackend`
  and is unchanged by this PR. If you want to test dynamic mode, omit
  `spec.outgoingAuth.source: inline` in the `VirtualMCPServer` and add a
  proper `discovered` block; the same headers should arrive at the echo
  backend.
- **Doesn't verify**: the per-backend `MCPServerEntry` validation
  condition (`HeaderSecretRefsValidated`). The CRD spec test
  (`TestValidateHeaderForwardSecretRefs` in
  `mcpserverentry_controller_test.go`) covers it.
