# PR A1 — Fix metrics assertions that cannot fail

- **Depends on**: nothing. Start immediately.
- **Blocks**: nothing, but ship first — it is the safety net that makes A2, A3, and B1 reviewable.
- **Size**: test-only, ~255 lines changed across 3 files.
- **Type of change**: Bug fix (test correctness).
- **Source**: commit `70302f0a5` on `gautam/metrics-std-toolhive`.

## Why

Four assertions in the telemetry test suite are structurally incapable of
failing. They give false confidence: the metrics work in PR #5956 shows green CI
on tests that assert nothing. Fixing them **before** any metric changes land
means the later PRs are landing against tests that can actually catch a
regression.

One of the four masks a real bug (see Task 1) — the test claims to validate a
metric that was genuinely absent from the scrape it inspected.

This PR is **test-only**. It changes no production code and no metric names, so
it is safe to merge against `main` regardless of how the dual-emit question is
resolved.

## Scope guard

This PR must contain **no production-code changes** and must not rename any
metric. Written against `main`'s current metric names (`toolhive_mcp_*`), not the
renamed `stacklok.*` names from the source branch.

```bash
# Must list only _test.go files
git diff --name-only main...HEAD
```

## The four defects

### Task 1 — Tautological guard hiding a genuinely absent metric

**File**: `pkg/telemetry/integration_test.go` (~line 163 on `main`)

`main` currently has:

```go
// If we have custom metrics, verify them
if strings.Contains(metricsBody, "toolhive_mcp") {
    assert.Contains(t, metricsBody, "toolhive_mcp_requests")
    assert.Contains(t, metricsBody, "toolhive_mcp_request_duration")
    assert.Contains(t, metricsBody, "toolhive_mcp_active_connections")
}
```

The guard string `"toolhive_mcp"` is a **prefix of every string it guards**.
If the metrics are present the guard passes and the assertions are redundant; if
they are absent the guard fails and the body is skipped. Either way **the
assertions can never fail**. If the middleware emitted nothing at all, the block
is skipped and the test still passes.

**Fix**: delete the `if` and assert unconditionally. The test already drove a
real request through the middleware, so the series must exist.

```go
// Asserted unconditionally: the test already drove a real request through the
// middleware, so these series must exist. Guarding on a prefix of the metric
// names would make these assertions unable to fail.
assert.Contains(t, metricsBody, "toolhive_mcp_requests")
assert.Contains(t, metricsBody, "toolhive_mcp_request_duration")
assert.Contains(t, metricsBody, "toolhive_mcp_active_connections")
```

**⚠️ Expect this to fail at first, and do not paper over it.** On the source
branch, removing this guard exposed a real bug: the request-driving subtests used
`t.Parallel()`, so they had **not run** when the parent scraped `/metrics`. The
metric was legitimately missing.

The fix is to make the requests happen inline (before the scrape) rather than in
parallel subtests. Investigate the enclosing test, confirm the ordering problem,
and serve the requests synchronously before the `/metrics` scrape.

Also consider whether the outer `if metricsRec.Code == http.StatusOK { ... } else
{ t.Logf(...) }` should become `require.Equal(t, http.StatusOK, metricsRec.Code)`
— a non-200 currently logs and passes. Tighten it if it does not destabilise
the test.

### Task 2 — Substring `NotContains` that cannot fail

**File**: `pkg/vmcp/server/telemetry_integration_test.go` (~line 343 on `main`)

On `main` this is a positive assertion:

```go
assert.Contains(t, metrics, `server="telemetry-vmcp"`,
```

The defect appears when such an assertion is inverted to `NotContains` to check
that an old label name is gone — because `server="telemetry-vmcp"` **is a
substring of** `mcp_server="telemetry-vmcp"`. A `NotContains` on the bare form
therefore fails even when the label was correctly renamed, and conversely a
naive check can never distinguish the two.

**Fix**: any assertion distinguishing `server=` from `mcp_server=` must anchor on
the delimiter. Use a regex requiring `{` or `,` immediately before the key:

```go
assert.NotRegexp(t, `[{,]server="telemetry-vmcp"`, metrics,
    "bare server= label must not appear; it was renamed to mcp_server=")
```

On `main` (pre-rename) the correct action is to **keep the positive assertion**
and add the anchored form as a helper or comment for the later rename PR. The
durable deliverable here: make sure no test in this file uses an unanchored
substring check to distinguish `server` from `mcp_server`. Audit the file for
that pattern and anchor every instance.

### Task 3 — Zero-match regex silently skips the assertion

**File**: `test/e2e/telemetry_metrics_validation_e2e_test.go` (~line 360)

Pattern to find:

```go
matches := re.FindStringSubmatch(metricsContent)
if len(matches) > 0 {
    // ... parse and Expect(totalRequests).To(BeNumerically(">", 0))
}
```

If the regex matches nothing, the entire body — including the value assertion —
is skipped and the helper passes silently.

**Fix**: assert the match exists before using it.

```go
matches := re.FindStringSubmatch(metricsContent)
Expect(matches).ToNot(BeEmpty(),
    "expected a request-count series in the scrape; found none")
// ... then parse unconditionally
```

Keep the server-name and transport pinning in the regex if `main`'s version has
it — do not weaken the pattern.

### Task 4 — Narrowed scan with no match counter

**File**: `test/e2e/telemetry_metrics_validation_e2e_test.go`, func
`validateNoEmptyLabels` (~line 330)

The loop scans lines for a metric and asserts label correctness inside the `if`.
If no line matches, the body never executes and the helper passes without
asserting anything.

**Fix**: count the lines actually checked and assert the count is non-zero.

```go
checkedLines := 0
for _, line := range lines {
    if strings.Contains(line, "<metric name>") && !strings.HasPrefix(line, "#") {
        if strings.Contains(line, "{") {
            checkedLines++
            // ... existing label assertions
        }
    }
}
Expect(checkedLines).To(BeNumerically(">", 0),
    "expected at least one labelled series in the scrape")
```

Reference implementation (already written on the source branch):

```bash
git show gautam/metrics-std-toolhive:test/e2e/telemetry_metrics_validation_e2e_test.go
```

## General rule to apply while you are in here

While auditing these files, apply the same lens to neighbouring assertions:

1. **Guard that is a prefix/substring of its own assertion** → delete the guard.
2. **`if len(x) > 0 { ...assert... }`** → assert non-empty first.
3. **Loop with assertions only inside a conditional** → add a match counter.
4. **`NotContains` on a string that is a substring of the valid new form** →
   anchor with a delimiter-aware regex.
5. **`require.Contains(body, "metric_name")`** → anchor at line start
   (`(?m)^metric_name\{`) so a longer name (e.g. a `_ratio` or `_total` suffix)
   cannot satisfy it accidentally.

Fix any instance you find in these three files. Do **not** expand into unrelated
test files — note them in the PR description instead.

## Files to change

| File | Change |
|---|---|
| `pkg/telemetry/integration_test.go` | Remove tautological guard; fix the parallel-subtest ordering it exposes |
| `pkg/vmcp/server/telemetry_integration_test.go` | Anchor label assertions so `server=` vs `mcp_server=` is distinguishable |
| `test/e2e/telemetry_metrics_validation_e2e_test.go` | Add non-empty match assertion + `checkedLines` counter |

## Verification

```bash
task lint-fix
task test
```

Then prove the assertions can now fail — this is the whole point of the PR, so
do it explicitly and record it in the PR description:

```bash
# 1. Break the metric name in pkg/telemetry/middleware.go (e.g. rename
#    toolhive_mcp_requests to toolhive_mcp_requests_XXX)
# 2. task test  -> the integration test MUST now fail
# 3. Revert the deliberate break
```

For the e2e helpers, temporarily change the searched metric name to a
nonexistent string and confirm the helper fails rather than passing vacuously.

E2E run (needs a build first):

```bash
task build
task test-e2e
```

## Acceptance criteria

- [ ] No production (non-`_test.go`) file appears in the diff.
- [ ] The `strings.Contains(metricsBody, "toolhive_mcp")` guard is gone and its
      assertions run unconditionally.
- [ ] The parallel-subtest ordering problem exposed by removing that guard is
      fixed, with requests served before the `/metrics` scrape.
- [ ] `validateNoEmptyLabels` fails when no matching series is present.
- [ ] The request-count helper fails when its regex matches nothing.
- [ ] No assertion distinguishing `server=` from `mcp_server=` relies on an
      unanchored substring match.
- [ ] Each fixed assertion has been manually demonstrated to fail against a
      deliberately broken metric name, and that demonstration is described in
      the PR body.
- [ ] `task lint-fix` and `task test` pass (excluding the two known-flaky tests
      listed in the [README](README.md#verification)).

## PR description skeleton

```markdown
## Summary

Four assertions in the telemetry test suite could not fail, so the metrics
tests reported green while verifying nothing. Fixing them before the metrics
standardization work lands means those changes are reviewed against tests that
can actually catch a regression.

- A guard string that is a prefix of the metric names it guards made three
  assertions unreachable-or-redundant. Removing it exposed a real bug: the
  request-driving subtests ran under `t.Parallel()`, so the metric was
  genuinely absent from the scrape the test claimed to validate.
- Two e2e helpers skipped their assertion bodies entirely on zero matches.
- Label assertions used unanchored substring checks, so `server="x"` could not
  be distinguished from `mcp_server="x"`.

Test-only; no production code or metric names change.

## Type of change
- [x] Bug fix (non-breaking change which fixes an issue)

## Test plan
- [x] Unit tests pass (`task test`)
- [x] Manual verification: each fixed assertion was demonstrated to fail
      against a deliberately broken metric name, then reverted.
```
