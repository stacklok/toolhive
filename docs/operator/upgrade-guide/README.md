# Upgrade walkthrough — v1alpha1 → v1beta1 with StorageVersionMigrator

End-to-end manual walkthrough that replays a real user upgrade against a local kind cluster: install the previous v0.21.0 release, create a CR of each of the 12 toolhive CRD kinds that graduated from `v1alpha1` to `v1beta1`, upgrade to the new multi-version chart, deploy this branch's operator with the migrator **disabled**, re-apply the CRs at `v1beta1`, and confirm `status.storedVersions` is stuck at `[v1alpha1, v1beta1]` on every graduated CRD. Then enable the `StorageVersionMigrator` and confirm it converges every graduated CRD to `[v1beta1]`.

> **Scope note**: the chart actually ships **13** labeled toolhive CRDs, but the 13th — `mcpwebhookconfigs` — never graduated (its only version is `v1alpha1`, which is also its storage version). The migrator's `isMigrationNeeded` check is a no-op on it. This walkthrough scopes every verification loop to the 12 graduated CRDs and excludes `mcpwebhookconfigs`; we'll note where this matters.

Total run time: ~30 minutes. The slow part is the first `ko build` of the operator + proxyrunner + vmcp images (~3 min); subsequent runs use the build cache and finish in ~30s.

This guide is the canonical reproducible verification for the migrator. Companion reading:

- [`docs/operator/storage-version-migration.md`](../storage-version-migration.md) — reference docs for the controller itself (label contract, opt-in model, mechanism).
- [Issue #4969](https://github.com/stacklok/toolhive/issues/4969) — the motivating problem.

> **⚠ Upgrade break for namespace-scoped installs.** `operator.features.storageVersionMigrator` now defaults to `true`. The chart hard-fails the render when the migrator is enabled with `operator.rbac.scope=namespace`, because the controller requires cluster-wide CRD access that a namespace-scoped operator does not have. If you run the operator namespace-scoped and never explicitly set this flag, **`helm upgrade` will fail** with:
>
> ```
> operator.features.storageVersionMigrator requires operator.rbac.scope=cluster
> ```
>
> **Before upgrading**, add `operator.features.storageVersionMigrator=false` to your Helm values (or `--set` flags) for any namespace-scoped install. Cluster-scoped installs (the default) are unaffected.

## Prerequisites

- `kind`, `kubectl`, `helm`, `ko`, `task` on PATH (`go install github.com/google/ko@latest` for ko)
- Working directory: the repo root (`task operator-deploy-local` and the relative chart paths assume this).
- Cluster name is `toolhive`. If you already have a cluster with that name, delete it first or run from a different worktree.

The CR fixtures used below ship alongside this doc:

- [`crs-v1alpha1.yaml`](./crs-v1alpha1.yaml) — one CR of each of the 12 graduated kinds at `v1alpha1`
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

## 2 · Create one CR of each of the 12 graduated kinds at v1alpha1

```bash
# The 12 graduated CRDs. Excludes mcpwebhookconfigs (single-version v1alpha1).
# Used by every verification loop in steps 8–11 so the walkthrough doesn't drift
# if a future PR adds a 14th CRD.
GRADUATED_CRDS=(
  embeddingservers.toolhive.stacklok.dev
  mcpexternalauthconfigs.toolhive.stacklok.dev
  mcpgroups.toolhive.stacklok.dev
  mcpoidcconfigs.toolhive.stacklok.dev
  mcpregistries.toolhive.stacklok.dev
  mcpremoteproxies.toolhive.stacklok.dev
  mcpservers.toolhive.stacklok.dev
  mcpserverentries.toolhive.stacklok.dev
  mcptelemetryconfigs.toolhive.stacklok.dev
  mcptoolconfigs.toolhive.stacklok.dev
  virtualmcpcompositetooldefinitions.toolhive.stacklok.dev
  virtualmcpservers.toolhive.stacklok.dev
)

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

# Confirm flag took effect — read off the Deployment spec directly, not a pod.
# A pod-based check races with the old pod's Terminating state and can return
# the pre-upgrade env value, which looks like a feature-flag bug but isn't.
kubectl get deploy -n toolhive-system toolhive-operator \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR")].value}'
# expected: false

# The "disabled" log is emitted at V(1); at default verbosity it is not
# visible. Setting LOG_LEVEL=DEBUG on the operator would surface:
#   "StorageVersionMigrator disabled" envVar="TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR"
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

## 8 · Confirm storedVersions is stuck at `[v1alpha1, v1beta1]` on the 12 graduated CRDs (migrator is OFF)

```bash
echo "=== storedVersions on the 12 graduated CRDs (migrator OFF) ==="
for crd in "${GRADUATED_CRDS[@]}"; do
  stored=$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')
  printf "  %-55s %s\n" "$crd" "$stored"
done
```

Expected: every line ends with `["v1alpha1","v1beta1"]`. This is the "post-graduation, pre-migration" state every cluster lands in after the v0.21.0 → multi-version upgrade. (The 13th labeled CRD, `mcpwebhookconfigs`, is excluded from this loop because it's single-version and reads `["v1alpha1"]` — see the scope note at the top.) **Without the migrator, this state is permanent** — any future operator release that drops `v1alpha1` from `spec.versions` would fail with:

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
  --set operator.features.storageVersionMigrator=true \
  --namespace toolhive-system

kubectl rollout status deployment -n toolhive-system --timeout=180s

# Confirm flag is now true — read off the Deployment spec directly (see step 5
# for why a pod-based check races with the old pod's Terminating state).
# NOTE: this is set explicitly here only because step 5 explicitly disabled it
# (and `helm upgrade` does not reuse the previous release's --set values). The
# chart default is storageVersionMigrator=true, so a normal install/upgrade that
# omits the flag entirely already runs the migrator.
kubectl get deploy -n toolhive-system toolhive-operator \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR")].value}'
# expected: true
```

Wait for convergence:

```bash
expected="${#GRADUATED_CRDS[@]}"   # 12 today; the loop tracks the array, so a future PR that adds a CRD won't drift.
echo "=== waiting up to 60s for ${expected} graduated CRDs to converge ==="
for i in $(seq 1 60); do
  count=0
  for c in "${GRADUATED_CRDS[@]}"; do
    if [ "$(kubectl get crd "$c" -o jsonpath='{.status.storedVersions}')" = '["v1beta1"]' ]; then
      count=$((count + 1))
    fi
  done
  if [ "$count" = "$expected" ]; then
    echo "All ${expected} graduated CRDs converged after ${i}s"
    break
  fi
  sleep 1
done

# If the loop exited without converging, surface what's outstanding so the
# reader has something actionable to investigate (operator logs, *Failed
# events, admission-policy rejections).
if [ "$count" != "$expected" ]; then
  echo "TIMEOUT: only ${count}/${expected} converged. Check operator logs and"
  echo "        StorageVersionMigrationFailed events on the CRDs."
fi
```

In practice this completes in ~1-2 seconds once the new pod is ready.

Verify:

```bash
echo "=== storedVersions on the ${expected} graduated CRDs (migrator ON, post-converge) ==="
for crd in "${GRADUATED_CRDS[@]}"; do
  stored=$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')
  printf "  %-55s %s\n" "$crd" "$stored"
done
# expected: every line ends with ["v1beta1"]

echo "=== StorageVersionMigrationSucceeded events ==="
kubectl get events -A --field-selector reason=StorageVersionMigrationSucceeded --no-headers | wc -l
# expected: ${expected} — one event per graduated CRD. The 13th (mcpwebhookconfigs)
# is a no-op for the migrator (storedVersions already == [storageVersion]) so it
# emits no event.

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

Pod-selection is needed here (logs aren't readable off a Deployment), but filter to Running pods so a still-Terminating old pod from the helm upgrade can't be picked up:

```bash
NEW_POD=$(kubectl get pods -n toolhive-system \
  --field-selector=status.phase=Running --no-headers \
  | grep "toolhive-operator-" | awk '{print $1}' | head -1)
kubectl logs "$NEW_POD" -n toolhive-system | grep "storage version migration complete" | wc -l
# expected: ${expected} lines — one per graduated CRD
```

**If this prints 0**, the migration may have happened in a previous container instance (the operator pod can restart for unrelated reasons in a kind cluster — leases, OOM, etc.). Try the previous container's logs:

```bash
kubectl logs "$NEW_POD" -n toolhive-system --previous | grep "storage version migration complete" | wc -l
```

The migration is complete in either case — the events on the CRDs in step 9 are the authoritative signal.

## 11 · (Optional) Simulate the next release that drops v1alpha1

Once `storedVersions` is `[v1beta1]` everywhere, the apiserver will accept removal of `v1alpha1` from `spec.versions` — the safety interlock the migrator exists to satisfy. To demonstrate:

```bash
# Direct apiserver patch — the same end state a future "drop v1alpha1" chart
# would produce. Scoped to the 12 graduated CRDs: mcpwebhookconfigs is
# single-version v1alpha1, so removing v1alpha1 from its spec.versions would
# leave it with zero versions and the apiserver would reject the patch.
for crd in "${GRADUATED_CRDS[@]}"; do
  newversions=$(kubectl get crd "$crd" -o json | jq '.spec.versions | map(select(.name != "v1alpha1"))')
  patch=$(jq -n --argjson v "$newversions" '{spec:{versions:$v}}')
  printf "  %-55s " "$crd"
  kubectl patch crd "$crd" --type=merge -p "$patch" 2>&1 | tail -1
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
| Operator startup logs show migrator skipped when disabled | The `setupStorageVersionMigrator` branch in `app/app.go` honours `TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR=false` | 5 |
| Zero-downtime upgrade across operator chart upgrade | The PR's operator changes don't recreate any managed workload | 6 |
| `storedVersions` is `[v1alpha1, v1beta1]` after re-apply with migrator OFF | Migrator did not run; baseline state is correctly preserved | 8 |
| Helm-upgrade flip enables the migrator | Feature flag round-trips correctly | 9 |
| `storedVersions` converges to `[v1beta1]` on all 12 graduated CRDs | The actual migration logic works against real ToolHive CRDs | 9 |
| 12 `StorageVersionMigrationSucceeded` events on the graduated CRDs | The Recorder is correctly wired and per-CRD migrations are observable | 9 |
| 0 `StorageVersionMigrationFailed` events | No CRD's per-CR Update loop returned a non-retriable error | 9 |
| All 12 CRs still healthy after migration | Validating admission webhooks (MCPServer / MCPGroup / VirtualMCPServer) tolerate the per-CR Updates | 9 |
| RBAC permits storedVersions trim | The ClusterRole has the correct verbs for `customresourcedefinitions/status` and `*.toolhive.stacklok.dev/*` | implicit — trim succeeds in step 9 |
| Apiserver permits `v1alpha1` removal from `spec.versions` once `storedVersions` is clean | The deprecation chain end-to-end works | 11 |

## What this does NOT cover

- **Mid-migration crash recovery**: no test forces the operator to crash mid-loop. Envtest covers the conflict-handling and re-encode-failure paths separately.
- **Pagination at real-cluster scale**: 12 CRs is well below the 500-default page size. The envtest suite explicitly tests the continue-token loop with 7 CRs at `PageSize=3`.
- **Operator restart resilience under load**: kind clusters are resource-limited and the operator pod sometimes restarts during tests for unrelated reasons (the `--previous` log fallback in step 10 covers this).

These are covered by the envtest suite at `cmd/thv-operator/test-integration/storageversionmigrator/`. This walkthrough covers what envtest can't: helm chart wiring, real ToolHive CRD schemas with their actual webhooks, and the full upgrade sequence a real user would run.
