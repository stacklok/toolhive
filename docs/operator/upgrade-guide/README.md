# Upgrade walkthrough — v1alpha1 → v1beta1 with StorageVersionMigrator

End-to-end manual walkthrough that replays a real user upgrade against a local kind cluster: install the previous v0.21.0 release, create a CR of each of the 12 toolhive CRD kinds at `v1alpha1`, upgrade to the new multi-version chart, deploy this branch's operator with the migrator **disabled**, re-apply the CRs at `v1beta1`, and confirm `status.storedVersions` is stuck at `[v1alpha1, v1beta1]` on every CRD. Then enable the `StorageVersionMigrator` and confirm it converges every CRD to `[v1beta1]`.

Total run time: ~30 minutes. The slow part is the first `ko build` of the operator + proxyrunner + vmcp images (~3 min); subsequent runs use the build cache and finish in ~30s.

This guide is the canonical reproducible verification for the migrator. Companion reading:

- [`docs/operator/storage-version-migration.md`](../storage-version-migration.md) — reference docs for the controller itself (label contract, opt-out, mechanism).
- [Issue #4969](https://github.com/stacklok/toolhive/issues/4969) — the motivating problem.

## Prerequisites

- `kind`, `kubectl`, `helm`, `ko`, `task` on PATH (`go install github.com/google/ko@latest` for ko)
- Working directory: the repo root (`task operator-deploy-local` and the relative chart paths assume this).
- Cluster name is `toolhive`. If you already have a cluster with that name, delete it first or run from a different worktree.

The CR fixtures used below ship alongside this doc:

- [`crs-v1alpha1.yaml`](./crs-v1alpha1.yaml) — one CR of each of the 12 kinds at `v1alpha1`
- [`crs-v1beta1.yaml`](./crs-v1beta1.yaml) — byte-identical to the v1alpha1 file except for `apiVersion`, simulating what `sed -i 's/v1alpha1/v1beta1/g'` would produce

---

## 0 · Set up the cluster

```bash
# If you already have a "toolhive" kind cluster from a previous run, delete it
kind delete cluster --name toolhive 2>/dev/null

kind create cluster --name toolhive --wait 60s
kind get kubeconfig --name toolhive > kconfig.yaml
export KUBECONFIG=$(pwd)/kconfig.yaml
```

## 1 · Install v0.21.0 (the last v1alpha1-only release)

```bash
helm install toolhive-operator-crds \
  oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds \
  --version 0.21.0 --wait

helm install toolhive-operator \
  oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  --version 0.21.0 \
  --namespace toolhive-system --create-namespace --wait

kubectl get crd mcpservers.toolhive.stacklok.dev -o jsonpath='{.spec.versions[*].name}'
# expected: v1alpha1   (only one version)

kubectl wait --for=condition=Available deployment -n toolhive-system --all --timeout=180s
```

## 2 · Create one CR of each of the 12 kinds at v1alpha1

```bash
kubectl create namespace upgrade-test
kubectl apply -f docs/operator/upgrade-guide/crs-v1alpha1.yaml

# Confirm all 12 landed
kubectl get \
  mcpservers,mcpremoteproxies,mcptoolconfigs,mcpgroups,embeddingservers,mcpregistries,virtualmcpservers,virtualmcpcompositetooldefinitions,mcpoidcconfigs,mcptelemetryconfigs,mcpexternalauthconfigs,mcpserverentries \
  -n upgrade-test --no-headers | wc -l
# expected: 12
```

## 3 · Let the old operator reconcile + capture the baseline

```bash
sleep 60
kubectl get deployments -n upgrade-test
# expected: up to 5 Deployments — test-server, test-remote-proxy, test-virtual-server,
# test-registry-api, and (sometimes) test-embedding shows as a StatefulSet not a Deployment

# Snapshot the UIDs for later comparison
kubectl get deployments -n upgrade-test \
  -o jsonpath='{range .items[*]}{.metadata.name}={.metadata.uid}{"\n"}{end}' \
  | sort > /tmp/before-upgrade.txt
cat /tmp/before-upgrade.txt
```

These UIDs are the canary. If they change after the operator upgrade, a workload was recreated → downtime.

## 4 · Upgrade the CRDs chart to multi-version

```bash
helm upgrade toolhive-operator-crds deploy/charts/operator-crds --wait --timeout 120s

kubectl get crd mcpservers.toolhive.stacklok.dev -o jsonpath='{.spec.versions[*].name}'
# expected: v1alpha1 v1beta1

kubectl get crd mcpservers.toolhive.stacklok.dev -o jsonpath='{.spec.versions[?(@.storage==true)].name}'
# expected: v1beta1
```

## 5 · Build the new operator + deploy with the migrator DISABLED

This is the key deviation from `task operator-deploy-local` — we want to control the feature flag, so we run ko + helm manually rather than using the task.

```bash
# ~3 minutes on first run, ~30s with build cache
OP=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/thv-operator | tail -1)
PR=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/thv-proxyrunner | tail -1)
VM=$(KO_DOCKER_REPO=kind.local ko build --local -B ./cmd/vmcp | tail -1)

# Load all three into the kind cluster
kind load docker-image --name toolhive "$OP"
kind load docker-image --name toolhive "$PR"
kind load docker-image --name toolhive "$VM"

# Persist for later steps
echo "$OP" > /tmp/img-op
echo "$PR" > /tmp/img-pr
echo "$VM" > /tmp/img-vm

# Helm upgrade with migrator EXPLICITLY DISABLED
helm upgrade toolhive-operator deploy/charts/operator \
  --set operator.image="$OP" \
  --set operator.toolhiveRunnerImage="$PR" \
  --set operator.vmcpImage="$VM" \
  --set operator.features.storageVersionMigrator=false \
  --namespace toolhive-system

kubectl rollout status deployment -n toolhive-system --timeout=180s

# Confirm flag took effect
NEW_POD=$(kubectl get pods -n toolhive-system --no-headers | grep "toolhive-operator-" | awk '{print $1}' | head -1)
kubectl get pod "$NEW_POD" -n toolhive-system -o yaml | grep -A1 ENABLE_STORAGE_VERSION_MIGRATOR
# expected: value: "false"

kubectl logs "$NEW_POD" -n toolhive-system | grep "ENABLE_STORAGE_VERSION_MIGRATOR is disabled"
# expected: one line — "ENABLE_STORAGE_VERSION_MIGRATOR is disabled, skipping StorageVersionMigrator controller"
```

## 6 · Verify zero downtime — Deployment UIDs unchanged

```bash
kubectl get deployments -n upgrade-test \
  -o jsonpath='{range .items[*]}{.metadata.name}={.metadata.uid}{"\n"}{end}' \
  | sort > /tmp/after-upgrade.txt

diff /tmp/before-upgrade.txt /tmp/after-upgrade.txt && echo "All Deployment UIDs unchanged"
```

## 7 · Re-apply all 12 CRs at v1beta1 (the user migration step)

```bash
kubectl apply -f docs/operator/upgrade-guide/crs-v1beta1.yaml
# expected: 12 "configured" lines (not "created")

# Wait for the operator to observe each update
sleep 10
```

## 8 · Confirm storedVersions is stuck at `[v1alpha1, v1beta1]` on all 12 CRDs (migrator is OFF)

```bash
echo "=== storedVersions on ALL 12 CRDs (migrator OFF) ==="
for crd in $(kubectl get crd -o name | grep toolhive.stacklok.dev | sort); do
  name=$(echo $crd | sed 's|.*/||')
  stored=$(kubectl get $crd -o jsonpath='{.status.storedVersions}')
  printf "  %-55s %s\n" "$name" "$stored"
done
```

Expected: every line ends with `["v1alpha1","v1beta1"]`. This is the "post-graduation, pre-migration" state every cluster lands in after the v0.21.0 → multi-version upgrade. **Without the migrator, this state is permanent** — any future operator release that drops `v1alpha1` from `spec.versions` would fail with:

```
status.storedVersions[0]: Invalid value: "v1alpha1": must appear in spec.versions
```

## 9 · Enable the migrator + watch it converge

Helm upgrade to flip the feature flag:

```bash
OP=$(cat /tmp/img-op)
PR=$(cat /tmp/img-pr)
VM=$(cat /tmp/img-vm)

helm upgrade toolhive-operator deploy/charts/operator \
  --set operator.image="$OP" \
  --set operator.toolhiveRunnerImage="$PR" \
  --set operator.vmcpImage="$VM" \
  --namespace toolhive-system

kubectl rollout status deployment -n toolhive-system --timeout=180s

# Confirm flag is now true
NEW_POD=$(kubectl get pods -n toolhive-system --no-headers | grep "toolhive-operator-" | awk '{print $1}' | head -1)
kubectl get pod "$NEW_POD" -n toolhive-system -o yaml | grep -A1 ENABLE_STORAGE_VERSION_MIGRATOR
# expected: value: "true"
```

Wait for convergence:

```bash
echo "=== waiting up to 60s for all 12 CRDs to converge ==="
for i in $(seq 1 60); do
  count=$(for c in $(kubectl get crd -o name | grep toolhive.stacklok.dev); do
    kubectl get $c -o jsonpath='{.status.storedVersions}'
    echo
  done | grep -c '^\["v1beta1"\]$')
  if [ "$count" = "12" ]; then
    echo "All 12 CRDs converged after ${i}s"
    break
  fi
  sleep 1
done
```

In practice this completes in ~1-2 seconds once the new pod is ready.

Verify:

```bash
echo "=== storedVersions on ALL 12 CRDs (migrator ON, post-converge) ==="
for crd in $(kubectl get crd -o name | grep toolhive.stacklok.dev | sort); do
  name=$(echo $crd | sed 's|.*/||')
  stored=$(kubectl get $crd -o jsonpath='{.status.storedVersions}')
  printf "  %-55s %s\n" "$name" "$stored"
done
# expected: every line ends with ["v1beta1"]

echo "=== StorageVersionMigrationSucceeded events ==="
kubectl get events -A --field-selector reason=StorageVersionMigrationSucceeded --no-headers | wc -l
# expected: 12 — one event per CRD

echo "=== StorageVersionMigrationFailed events (should be 0) ==="
kubectl get events -A --field-selector reason=StorageVersionMigrationFailed --no-headers | wc -l
# expected: 0
```

Confirm CRs still healthy (admission webhooks on MCPServer / MCPGroup / VirtualMCPServer all accepted the per-CR Updates):

```bash
kubectl get \
  mcpservers,mcpremoteproxies,mcptoolconfigs,mcpgroups,embeddingservers,mcpregistries,virtualmcpservers,virtualmcpcompositetooldefinitions,mcpoidcconfigs,mcptelemetryconfigs,mcpexternalauthconfigs,mcpserverentries \
  -n upgrade-test --no-headers | wc -l
# expected: 12
```

## 10 · (Optional) Inspect migrator logs

```bash
NEW_POD=$(kubectl get pods -n toolhive-system --no-headers | grep "toolhive-operator-" | awk '{print $1}' | head -1)
kubectl logs "$NEW_POD" -n toolhive-system | grep "storage version migration complete" | wc -l
# expected: 12 lines — one per CRD
```

**If this prints 0**, the migration may have happened in a previous container instance (the operator pod can restart for unrelated reasons in a kind cluster — leases, OOM, etc.). Try the previous container's logs:

```bash
kubectl logs "$NEW_POD" -n toolhive-system --previous | grep "storage version migration complete" | wc -l
```

The migration is complete in either case — the events on the CRDs in step 9 are the authoritative signal.

## 11 · (Optional) Simulate the next release that drops v1alpha1

Once `storedVersions` is `[v1beta1]` everywhere, the apiserver will accept removal of `v1alpha1` from `spec.versions` — the safety interlock the migrator exists to satisfy. To demonstrate:

```bash
# Direct apiserver patch — the same end state a future "drop v1alpha1" chart would produce.
for crd in $(kubectl get crd -o name | grep toolhive.stacklok.dev); do
  name=$(echo $crd | sed 's|.*/||')
  newversions=$(kubectl get $crd -o json | jq '.spec.versions | map(select(.name != "v1alpha1"))')
  patch=$(jq -n --argjson v "$newversions" '{spec:{versions:$v}}')
  printf "  %-55s " "$name"
  kubectl patch $crd --type=merge -p "$patch" 2>&1 | tail -1
done
```

Every line should say `... patched`. Verify v1alpha1 access now fails:

```bash
kubectl get mcpgroups.v1alpha1.toolhive.stacklok.dev test-group -n upgrade-test
# expected: error — the server doesn't have a resource type "mcpgroups"

kubectl get mcpgroups.v1beta1.toolhive.stacklok.dev test-group -n upgrade-test
# expected: the resource
```

**Negative test**: if you skip step 9 (the migrator pass) and jump straight to step 11, every `kubectl patch` will fail with:

```
Invalid value: "v1alpha1": must appear in spec.versions
```

That's the apiserver wall the migrator exists to clear.

## 12 · Cleanup

```bash
kind delete cluster --name toolhive
rm -f kconfig.yaml /tmp/before-upgrade.txt /tmp/after-upgrade.txt \
      /tmp/img-op /tmp/img-pr /tmp/img-vm
```

---

## Summary of what's verified

| Check | Validates | Step |
|---|---|---|
| Operator startup logs show migrator skipped when disabled | The `setupStorageVersionMigrator` branch in `app/app.go` honours `ENABLE_STORAGE_VERSION_MIGRATOR=false` | 5 |
| Zero-downtime upgrade across operator chart upgrade | The PR's operator changes don't recreate any managed workload | 6 |
| `storedVersions` is `[v1alpha1, v1beta1]` after re-apply with migrator OFF | Migrator did not run; baseline state is correctly preserved | 8 |
| Helm-upgrade flip enables the migrator | Feature flag round-trips correctly | 9 |
| `storedVersions` converges to `[v1beta1]` on all 12 CRDs | The actual migration logic works against real ToolHive CRDs | 9 |
| 12 `StorageVersionMigrationSucceeded` events on the CRDs | The Recorder is correctly wired and per-CRD migrations are observable | 9 |
| 0 `StorageVersionMigrationFailed` events | No CRD's per-CR Update loop returned a non-retriable error | 9 |
| All 12 CRs still healthy after migration | Validating admission webhooks (MCPServer / MCPGroup / VirtualMCPServer) tolerate the per-CR Updates | 9 |
| RBAC permits storedVersions trim | The ClusterRole has the correct verbs for `customresourcedefinitions/status` and `*.toolhive.stacklok.dev/*` | implicit — trim succeeds in step 9 |
| Apiserver permits `v1alpha1` removal from `spec.versions` once `storedVersions` is clean | The deprecation chain end-to-end works | 11 |

## What this does NOT cover

- **Mid-migration crash recovery**: no test forces the operator to crash mid-loop. Envtest covers the conflict-handling and re-encode-failure paths separately.
- **Pagination at real-cluster scale**: 12 CRs is well below the 500-default page size. The envtest suite explicitly tests the continue-token loop with 7 CRs at `PageSize=3`.
- **Operator restart resilience under load**: kind clusters are resource-limited and the operator pod sometimes restarts during tests for unrelated reasons (the `--previous` log fallback in step 10 covers this).

These are covered by the envtest suite at `cmd/thv-operator/test-integration/storageversionmigrator/`. This walkthrough covers what envtest can't: helm chart wiring, real ToolHive CRD schemas with their actual webhooks, and the full upgrade sequence a real user would run.
