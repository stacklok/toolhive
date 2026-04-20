---
name: deflake
description: Finds flaky tests on the main branch by analyzing GitHub Actions failures, ranks them by frequency, and enters parallel plan mode to design deflake strategies. Use when you want to find and fix the flakiest tests.
---

# Deflake Tests

Discovers, ranks, and plans fixes for flaky tests by analyzing GitHub Actions failures on `main`.

## Arguments

```
/deflake                    # Full analysis: discover, rank, and plan fixes
/deflake --report           # Report only: show flake rankings without planning fixes
/deflake --top N            # Analyze and plan fixes for the top N flakes (default: 3)
```

---

## Phase 1: Collect and Rank Flakes

Run the collection script. It handles all deterministic data collection and aggregation. If CI log formats change over time, update the script directly.

```bash
python3 .claude/skills/deflake/collect-flakes.py
```

The script outputs three sections:
1. **FLAKE REPORT** — overall stats (total runs, failure rate, date range)
2. **RANKED FAILURES** — table sorted by failure count with job, mode, and test name
3. **FAILURE DETAILS** — per-test breakdown with links to each failed run

### Phase 1 complete

Read the script output and use it directly for the report. The LLM's only job in this phase is to **categorize** each entry as a flake, real bug, or infra issue:

- **Flake**: Appears multiple times intermittently, interspersed with successful runs
- **Real bug**: Appeared after a specific commit and every run after that failed until a fix landed. Check `git log` for related fixes
- **Infra flake**: Entries tagged `[INFRA]` by the script, or failures with mode `connection refused` / `infra`

---

## Phase 2: Present the Report

Present the script output as a formatted report. Add categorization (flake / real bug / infra) to each entry. Example format:

```markdown
## Flake Report — main branch

**Period**: 2026-04-01 to 2026-04-10
**Runs analyzed**: 23 total, 8 failed (35% failure rate)

### Top Flaky Tests

| Rank | Test | Job | Failures | Failure Mode |
|------|------|-----|----------|--------------|
| 1 | Workload lifecycle ... [It] should track ... | E2E (api-workloads) | 5/23 | timeout (120s) |
| 2 | ... | ... | ... | ... |

### Real Bugs (not flakes)
- [Test name] — Introduced by [commit], fixed by [commit/PR]

### Infra Failures
- [N] runs failed due to [description]
```

If the user passed `--report`, stop here. Otherwise continue to Phase 3.

---

## Phase 3: Plan Deflake Fixes

### 3.1 Parallel Investigation

For the top N flakes (default 3), launch **parallel agents** to investigate each one simultaneously.

For each flake, spawn an Agent (subagent_type: `general-purpose`) that:

1. **Reads the test code**: Find the test file, understand what it does and what behavior it's verifying
2. **Reads the production code**: Read all the production code that the test exercises — handlers, services, middleware, etc. Understand the code path end-to-end
3. **Maps test coverage for this feature**: Search the entire repo for all tests that cover this same feature or code path. Don't assume test locations — grep for the feature name, function names, and related keywords across the whole codebase. Tests may live in `_test.go` files alongside prod code, in `e2e/`, in `acceptance_test` files, or elsewhere. For each test found, document what it covers, what level it operates at (unit/integration/E2E), and whether it's stable or also flaky
4. **Reads the failure logs**: Get 2-3 example failure logs from different runs
5. **Identifies the root cause**: Why does this test fail intermittently?
   - Timing-dependent (hardcoded sleeps, tight timeouts)?
   - Resource contention (port conflicts, shared state)?
   - Ordering dependency (relies on another test's side effects)?
   - External dependency (network call, container pull)?
   - Race condition (concurrent access, missing synchronization)?
6. **Proposes a fix strategy**: Following the deflake principles below, informed by the full picture of prod code and existing test coverage

**IMPORTANT**: Launch all agents in a single message so they run in parallel.

Wait for all agents to complete, then consolidate findings.

### 3.2 Present Deflake Plans

For each flake, present a high-level plan with alternatives considered:

```markdown
### Flake #N: [Test Name]

**Root cause**: [one-sentence explanation]
**Failure logs**: [links to 2-3 example runs]

**Options considered**:
1. [Option A] — [why it was rejected or chosen]
2. [Option B] — [why it was rejected or chosen]
3. [Option C] — [why it was rejected or chosen]

**Recommended approach**: [which option and why it's the best fit]
- [High-level description of the changes]

**Confidence**: High / Medium / Low
**Risk**: [What could go wrong with this approach]
```

Present all plans and wait for user feedback. The user may choose a different option, combine approaches, or ask for more investigation. Do NOT enter plan mode or start implementing until the user approves the approach for each flake.

### 3.3 Implement Approved Fixes

Once the user approves approaches, enter plan mode to design the detailed implementation. The plan should:

- Group related fixes (e.g., if multiple tests share the same root cause)
- Order by impact (fix the flake that fails most often first)
- Each fix should be its own commit for easy revert

---

## Deflake Principles

These principles guide all fix proposals. **Prefer simplifying code and tests over adding complexity.**

### Prefer removal over addition
- Delete flaky tests only if they're duplicative with other **stable tests at the same level**
- If multiple E2E tests cover fine-grained behavior for one feature, move the fine-grained cases to unit tests and keep a single E2E smoke test
- Never remove **all** E2E coverage for a feature — at least one smoke test must remain
- Remove unnecessary setup/teardown that introduces timing sensitivity

### Fix the test, not the production code
- If flakiness exposes a real bug, fix the production code
- Do NOT add complexity to production code just to make a flaky test pass (retry logic, test-only hooks, feature flags)
- Ask: what's the intention of this test? Can we capture it in a more reliable form?

### Fix options
- **Delete the test** if redundant (keeping at least one E2E smoke test per feature)
- **Rewrite as a unit test** if the behavior can be tested without integration
- **Refactor hard-to-test code** so the behavior under test can be easily isolated and reliably examined
- **Reduce scope** — test one thing instead of a full lifecycle
- **Use polling with short intervals** instead of fixed sleeps (e.g., `Eventually` with 1s poll interval)
- **Increase timeouts** — only as a last resort, and only for `Eventually`/`Consistently` matchers, not arbitrary `time.Sleep`

### Anti-patterns to avoid
- Adding `time.Sleep()` to "fix" timing issues
- Adding retry loops around flaky assertions
- Marking tests as `[Flaky]` or `Skip` without fixing them
- Adding production code complexity (feature flags, test modes) to make tests pass
- Increasing parallelism limits or resource requests as a band-aid
