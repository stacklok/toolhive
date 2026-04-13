---
paths:
  - "*_test.go"
  - "test/**"
---

# Testing Rules

Applies to test files and test directories.

## Testing Strategy

- **`pkg/` packages**: Thorough unit test coverage (business logic lives here)
- **`cmd/thv/app/`**: Minimal unit tests (only output formatting, flag validation helpers)
- **CLI commands**: Tested primarily with E2E tests (`test/e2e/`), not unit tests
- **Integration tests**: Ginkgo/Gomega in package test files
- **Operator tests**: Chainsaw tests in `test/e2e/chainsaw/operator/`

## Mock Generation

- Use `go.uber.org/mock` (gomock) framework — never hand-write mocks
- Generate mocks with `mockgen` and place in `mocks/` subdirectories
- Generate with: `task gen`

## Assertions

- Prefer `require.NoError(t, err)` (from `github.com/stretchr/testify`) instead of `t.Fatal`

## Test Quality

1. **Structure**: Prefer table-driven (declarative) tests over imperative tests
2. **Redundancy**: Avoid overlapping test cases exercising the same code path
3. **Value**: Every test must add meaningful coverage — remove tests that don't
4. **Consolidation**: Consolidate small test functions into a single table-driven test when they test the same function
5. **Naming**: Test names must match what they actually assert — if the assertion changes, update the name too.
6. **Boilerplate**: Minimize setup code; extract shared setup into helpers with `t.Helper()`

## Running Operator E2E Tests

Operator E2E tests live in `test/e2e/thv-operator/` and require a Kind cluster. All tasks are defined in `cmd/thv-operator/Taskfile.yml` and must be run from the repo root with `task -d cmd/thv-operator <task>` (or `cd cmd/thv-operator && task <task>`).

**Full automated run** (creates cluster, deploys, tests, destroys on exit):
```
task -d cmd/thv-operator thv-operator-e2e-test
```

**Iterative manual workflow** (keep the cluster alive between test runs):
```
task -d cmd/thv-operator kind-setup-e2e       # Kind cluster with NodePort mappings
task -d cmd/thv-operator operator-install-crds
task -d cmd/thv-operator operator-deploy-local # builds & loads local images via ko
task -d cmd/thv-operator thv-operator-e2e-test-run  # re-run as many times as needed
task -d cmd/thv-operator kind-destroy          # when done
```

**Cluster variants:**
- `kind-setup` — plain cluster, no port mappings (general use)
- `kind-setup-e2e` — cluster with NodePort mappings required by Ginkgo E2E tests

**Chainsaw (operator unit-level E2E):**
```
task -d cmd/thv-operator operator-e2e-test
```
Runs `chainsaw` against `test/e2e/chainsaw/operator/` scenarios. Installs `chainsaw` automatically if missing.

The Ginkgo suite runs with `--procs=8` and uses `kconfig.yaml` (written to repo root by the kind-setup tasks) as its `KUBECONFIG`.

## E2E Test Coverage

E2E tests must verify functional behavior, not just infrastructure state. Confirming that pods are ready or that counts are correct is not sufficient — the test must also exercise the actual code path (send traffic, trigger the feature) to prove it works end-to-end.

## Test Scope

Tests must only test code in the package under test. Do NOT test behavior of dependencies, external packages, or transitive functionality.

## Temp Directories

When tests need a temp directory that must pass validation rejecting symlinks, use a resolved temp dir:
```go
dir := t.TempDir()
resolved, _ := filepath.EvalSymlinks(dir)
```
On macOS, `t.TempDir()` often returns paths through `/var/folders/...` which is a symlink. See `pkg/skills/project_root_test.go` for a `resolvedTempDir(t)` helper.

## Environment Variables

Write tests isolated from other tests that may set the same env vars. Use `t.Setenv()` which auto-restores.

## Port Numbers

Use random ports (e.g., `net.Listen("tcp", ":0")`) to let the OS assign a free port. Do not use hardcoded port numbers — even large ones can clash with running services.

## Test Hooks in Production Structs

Avoid adding test-only hook fields (nil-checked `func()` fields) to production structs. A field documented as "nil in production" signals the concern belongs outside the production type. Preferred alternatives:

- **Interface seam**: Replace the internal component with an interface; tests inject a wrapper that adds the needed synchronization or observation.
- **Functional constructor options**: Expose hook injection only through a constructor option so the production call site stays clean.
- **Test at the observable boundary**: Control timing through the mock/stub's own behavior rather than hooking into production internals.

Existing instances in the codebase are legacy — do not expand them. When touching a struct that already has hook fields, consider extracting them as part of the change.

## Use `t.Cleanup` for Resource Teardown in Parallel Tests

In tests using `t.Parallel()`, always register resource teardown (stopping servers, closing connections, cancelling contexts) with `t.Cleanup`, not just `defer`.

A `require.*` failure or panic aborts the test function immediately, skipping all deferred calls. `t.Cleanup` handlers run regardless of how the test exits, preventing leaked goroutines, ports, and connections when tests fail early.

```go
// Good
server := httptest.NewServer(handler)
t.Cleanup(server.Close)

// Avoid — skipped if test aborts early via require.*
defer server.Close()
```

Make stop/close functions idempotent (`sync.Once`) when registering with both `t.Cleanup` and an explicit mid-test shutdown.

## Concurrent Tests: Always Add Timeouts to Blocking Barriers

Blocking operations in tests (`WaitGroup.Wait()`, channel receives, `sync.Cond.Wait()`) must have a timeout/fail-fast path. Without one, a panicking goroutine or regression in synchronization logic causes the test to hang until the global `go test` timeout.

```go
// Good: fail fast with a clear message
done := make(chan struct{})
go func() { wg.Wait(); close(done) }()
select {
case <-done:
case <-time.After(5 * time.Second):
    t.Fatal("timeout waiting for goroutines to synchronize")
}

// Avoid: hangs indefinitely on deadlock
wg.Wait()
```
