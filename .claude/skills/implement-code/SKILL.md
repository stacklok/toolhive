---
name: implement-code
description: Executes a design document or implementation plan produced by /plan-design or /plan-changes. Uses TDD, parallel subagents, two-stage review, systematic debugging, and verification gates to produce high-quality, tested, documented code. Use when you have a plan and are ready to build.
---

# Implement Code

Executes an existing design document or implementation plan, stage by stage, using
test-driven development, subagent delegation, two-stage review, and verification
gates. Produces tested, reviewed, documented code ready for PR.

**This skill assumes a plan already exists.** If you don't have one, use
`/plan-design` or `/plan-changes` first. If `/plan-design` was just run in the
same conversation without producing a design file, summarize the agreed approach
(goal, affected files, key decisions) into a concise inline plan before
proceeding — do not rely on implicit conversation context surviving across phases.

## Arguments

The user provides a design document path, a GitHub issue with an attached plan,
or explicit instructions about what to implement.

```
/implement-code @design-mcpremoteproxy-telemetryconfigref.md
/implement-code @implementation-plan.md Stage 2
/implement-code #4620 (will look for a design doc or plan in the repo)
```

**Argument**: `$ARGUMENTS`

---

## Phase 0: Load the Plan

### 0.1 Locate the Plan

Based on the argument:

**If a file path is provided** (e.g., `@design-*.md`, `@implementation-plan.md`):
- Read the full document
- Extract: goal, stages, file change map, test strategy, pitfalls, PR strategy

**If a GitHub issue is provided**:
- Fetch the issue with `gh issue view <number> --repo stacklok/toolhive`
- Search for a design doc: `ls design-*.md implementation-plan.md 2>/dev/null`
- If no plan exists, ask the user: "No design doc found. Want me to run
  `/plan-design` first, or should I work directly from the issue?"

**If a stage is specified** (e.g., "Stage 2"):
- Read the full plan but only execute the specified stage(s)
- Verify that dependencies (earlier stages) are already implemented by checking
  the codebase for the types, functions, and files those stages produce

### 0.2 Assess Complexity and Choose Execution Path

After reading the plan, assess the total scope:

- **Estimated production code lines** (from the plan's PR strategy or file change map)
- **Whether all stages follow existing, documented patterns** (e.g., "copy from MCPServer")
- **Number of new abstractions** (new interfaces, new packages, new design patterns)

**Lightweight path** (< 200 lines production code, all stages follow existing
patterns, no new abstractions): Implement directly — write tests and code
yourself without dispatching subagents. Skip the two-stage review in Phase 1.5.
Still run `task lint-fix` and `task test` after each stage.

**Full path** (>= 200 lines, novel patterns, or new abstractions): Follow all
phases including subagent dispatch and two-stage review.

This gate prevents ceremony overhead from exceeding implementation time on
straightforward changes.

### 0.3 Read Project Rules

Before writing any code, read the `.claude/rules/` files relevant to the plan's
affected domains. These are auto-loaded when touching matching files, but read
them upfront so you can brief subagents correctly:

- `.claude/rules/go-style.md` — always
- `.claude/rules/testing.md` — always
- `.claude/rules/operator.md` — if touching CRDs, controllers, or `cmd/thv-operator/`
- `.claude/rules/cli-commands.md` — if touching CLI commands
- `.claude/rules/pr-creation.md` — for the final PR

### 0.4 Create Branch

Create a feature branch for the work:

```bash
git checkout -b <user>/<short-description>
```

### 0.5 Establish Clean Baseline

Before writing any code, verify the starting state is clean:

```bash
task build && task lint-fix && task test
```

**If baseline fails**: Determine whether the failure is **pre-existing** by
checking the same command on `main`. Pre-existing failures in unrelated packages
(e.g., mockgen version mismatch, missing integration test binaries) are
acceptable — note them and proceed. Only STOP if the failure is in packages you
are about to modify, since you won't be able to distinguish your bugs from
pre-existing ones.

**If a top-level task command fails but the failure is in unrelated packages**:
run the specific affected packages directly (e.g.,
`go test ./cmd/thv-operator/...`) and use that as your baseline instead. Record
which top-level commands fail and why so you can distinguish pre-existing failures
from regressions in Phase 4.

---

## Phase 1: Stage-by-Stage Execution

Execute each stage from the plan in order. For each stage, follow this cycle:

```
READ plan stage → WRITE failing tests → IMPLEMENT code → VERIFY green → REVIEW → COMMIT
```

### 1.1 Read the Stage

For the current stage, extract from the plan:
- Files to create or modify
- Patterns to follow (read those pattern files now)
- Interface contracts to satisfy
- Test strategy
- Pitfalls to avoid

Read every "pattern to follow" file referenced in the plan. If a referenced file
doesn't exist, STOP and flag it — the plan may be stale.

### 1.2 Write Failing Tests First (TDD)

**Iron rule: no production code without a failing test first.**

Dispatch the **unit-test-writer** agent with a focused prompt:

> "Write tests for [stage description] following the test strategy in the plan.
>
> **What to test**: [test cases from the plan's test strategy table]
> **Test file**: [path from plan]
> **Pattern to follow**: [read this existing test file for style and structure]
> **Conventions**: [paste relevant rules from .claude/rules/testing.md]
>
> Write the tests, then run them. They MUST fail — they are testing code that
> doesn't exist yet. Report which tests fail and what the failure messages are."

After the agent returns:

1. **Verify the tests fail**: Run them yourself. If any test passes immediately,
   it's not testing the right thing — fix or discard it.
2. **Verify the failures are correct**: Tests should fail because the feature is
   missing (e.g., undefined function, nil pointer), NOT because of syntax errors
   or import issues in the test itself.

### 1.3 Implement the Code

Dispatch the **golang-code-writer** agent with a focused prompt:

> "Implement [stage description] to make the failing tests pass.
>
> **Design contract**: [paste interface contracts and file change map for this stage]
> **Files to modify**: [list from plan]
> **Pattern to follow**: [paste the exact pattern file contents or key excerpts]
> **Pitfalls**: [paste pitfalls from the plan]
> **Conventions**: [paste relevant rules from .claude/rules/go-style.md]
>
> Your goal: make the tests in [test file] pass by writing the minimal correct
> implementation. Do not add features, refactor surrounding code, or change the
> test expectations. Run the tests after implementing to verify they pass."

**For domain-specific stages**, also dispatch or consult the appropriate specialist:

| Domain | Agent | When to Use | When to Skip |
|--------|-------|-------------|--------------|
| CRD types, controllers, reconcilers | `kubernetes-expert` | New patterns, novel reconciler logic, CRD design decisions | Copying an established pattern verbatim from another CRD type |
| MCP protocol, transport | `mcp-protocol-expert` | Protocol handling, message formats, spec compliance | No protocol-level changes (e.g., only adding a config ref) |
| OAuth, OIDC, token exchange | `oauth-expert` | Authentication flows, token handling | No auth logic changes |
| Metrics, tracing, logging | `site-reliability-engineer` | New instrumentation, new metrics, tracing architecture | Wiring existing telemetry config to a new consumer |

**Dispatch domain agents in parallel with the code writer** when they are providing
guidance (not writing code). Only one agent should write code at a time to avoid
conflicts. On the lightweight path, skip domain agents entirely — the pattern
files in the design doc provide sufficient guidance.

### 1.4 Verify Green

After the implementation agent returns (or after implementing directly on the
lightweight path):

1. **Run the stage's tests**:
   ```bash
   task test
   ```

2. **Verify ALL tests pass** — not just the new ones. If existing tests broke,
   the implementation has a regression.

3. **Run lint after every stage** — do not defer lint to Phase 4:
   ```bash
   task lint-fix
   ```
   Lint issues (ctx shadowing, cyclomatic complexity, line length) often require
   non-trivial refactoring. Catching them while the stage's context is fresh is
   far easier than debugging them after multiple stages have accumulated.

4. **Run build**:
   ```bash
   task build
   ```

**If tests fail — Systematic Debugging**:

Do NOT guess at fixes. Follow this process:

1. **Read the error carefully** — full stack trace, full error message
2. **Trace the data flow** — where does the bad value originate?
3. **Check recent changes** — what did the implementer agent change that could
   cause this?
4. **Form a hypothesis** — "I think X is the root cause because Y"
5. **Test minimally** — smallest possible change to verify the hypothesis
6. **If 3+ fix attempts fail**: STOP. Re-read the plan's pitfalls section. Consider
   whether the design itself has a flaw, and flag it to the user.

### 1.5 Two-Stage Review

**Skip this section on the lightweight path** (see Phase 0.2). Lint + tests
provide sufficient verification for pattern-following changes under 200 lines.

Once tests pass, run two sequential reviews.

**Stage A — Spec Compliance Review**:

Dispatch the **code-reviewer** agent:

> "Review the changes in this stage for spec compliance.
>
> **Design contract**: [paste the plan's interface contracts and AC for this stage]
> **Acceptance criteria covered**: [from plan's AC coverage table]
>
> Check:
> 1. Does the implementation satisfy every AC mapped to this stage?
> 2. Do the interface signatures match the design contract exactly?
> 3. Are the correct files modified (no unexpected changes)?
> 4. Are the pitfalls from the plan avoided?
>
> Report: PASS or list specific compliance gaps."

**If spec issues found**: Fix them, re-run tests, re-review. Do not proceed to
code quality review until spec compliance passes.

**Stage B — Code Quality Review**:

Dispatch the **code-reviewer** agent again (fresh context):

> "Review the changes in this stage for code quality.
>
> **Conventions**: .claude/rules/go-style.md, .claude/rules/testing.md
>
> Check:
> 1. Idiomatic Go (naming, error handling, logging)
> 2. No security issues (OWASP top 10, credential leaks, injection)
> 3. No resource leaks (goroutines, file handles, connections)
> 4. Test quality (meaningful assertions, not just coverage)
> 5. SPDX license headers on new files
>
> Report: PASS or list specific quality issues with file:line references."

**If quality issues found**: Fix them, re-run tests, re-review.

### 1.6 Commit

Once both reviews pass:

```bash
# Stage the specific files for this stage
git add <files from this stage>

# Commit with a descriptive message
git commit -m "$(cat <<'EOF'
<commit message from plan, or descriptive summary>

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

**Commit rules**:
- Each commit must be independently compilable (`task build` passes)
- Group related changes — don't mix concerns across commits
- Imperative mood, capitalize subject, no trailing period, no `feat:` prefix
- One commit per stage is typical, but split if the stage has distinct sub-steps

### 1.7 Repeat

Move to the next stage. If the next stage depends on the current one, verify the
dependency is satisfied before proceeding.

---

## Phase 2: Parallel Subagent Dispatch

When multiple stages are **independent** (no dependency between them), execute
them in parallel using separate agents.

### When to Parallelize

Check the plan's dependency graph:
- Stages with `Depends on: none` can run in parallel with each other
- Stages that depend on the same prior stage can run in parallel after that stage
- **Never parallelize stages that modify the same files**

### How to Parallelize

Dispatch multiple **golang-code-writer** agents in a single message, each with:
- Its own stage's scope, contracts, and test strategy
- Explicit file boundaries: "You may ONLY modify these files: [list]"
- A constraint: "Do NOT modify files outside your scope"

After all parallel agents return:
1. Review each agent's changes for conflicts
2. Run the full test suite to verify no interference
3. If conflicts exist, resolve them manually before committing

### When NOT to Parallelize

- Stages that share files (will create merge conflicts)
- Stages where one produces types/interfaces the other consumes
- When you're unsure about independence — sequential is safer

---

## Phase 3: Documentation

After all implementation stages are complete, update documentation.

### 3.1 Identify Doc Changes

Based on the plan's file change map and the actual changes made, determine what
documentation needs updating:

| Change Type | Doc Action | Command |
|-------------|-----------|---------|
| New/modified CLI commands | Regenerate CLI docs | `task docs` |
| CRD type changes | Regenerate CRD API docs | `task crdref-gen` (from `cmd/thv-operator/`) |
| New package or major feature | Update architecture docs | Edit `docs/arch/` |
| Changed user-facing behavior | Update relevant user docs | Edit `docs/` |
| New config fields or options | Update example configs | Edit examples |

### 3.2 Sweep for Stale Source Comments

After removing or changing behavior, grep production code for comments that
reference the old behavior. Common patterns to search for:

- Deprecation notices (`Deprecated:`, `deprecated in favor of`)
- Fallback descriptions (`falls back to`, `backwards compatibility`)
- "Takes precedence over" comments that reference removed alternatives
- Doc comments on types/functions that describe removed behavior

Update or remove these comments. A comment that contradicts the code is worse
than no comment — it actively misleads future readers.

### 3.3 Dispatch Documentation Writer (if needed)

For non-trivial doc changes, dispatch the **documentation-writer** agent:

> "Update documentation for the following changes:
>
> **What changed**: [summary of implementation]
> **User-facing impact**: [from plan's scope or AC]
> **Files to update**: [identified doc files]
>
> Conventions:
> - Architecture docs go in `docs/arch/`
> - CLI docs are auto-generated with `task docs` — don't edit manually
> - CRD docs are auto-generated with `task crdref-gen` — don't edit manually
> - Use clear, active voice with concrete examples
>
> After writing docs, run any generation commands needed."

### 3.4 Run Generation Tasks

Execute all generation tasks from the plan's verification section:

```bash
# Common generation tasks (run only those relevant to your changes)
task operator-generate     # After modifying CRD types
task operator-manifests    # After adding kubebuilder markers
task crdref-gen            # After CRD changes (run from cmd/thv-operator/)
task gen                   # After modifying mock interfaces
task docs                  # After CLI/API changes
task license-fix           # After any new Go files
```

Commit generated code separately from hand-written code:

```bash
git add <generated files>
git commit -m "$(cat <<'EOF'
Regenerate CRD manifests and deepcopy

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4: Final Verification

**Iron rule: no completion claims without fresh verification evidence.**

### 4.1 Full Verification Suite

Run the complete verification battery. Do not skip any step. Do not trust
previous runs — run them fresh now.

```bash
task lint-fix && task test && task build
```

### 4.2 Evidence Checklist

Before claiming the work is done, verify each item and record the evidence:

- [ ] `task build` exits 0 — paste the output
- [ ] `task lint-fix` reports no remaining issues — paste the output
- [ ] `task test` shows 0 failures — paste the summary line
- [ ] Every AC from the plan has a test that passes
- [ ] Every file in the plan's change map was actually modified
- [ ] No unintended files were changed (`git diff --stat` against base branch)
- [ ] Generated code is up to date — re-run all generation commands (`operator-generate`, `operator-manifests`, `crdref-gen`, etc.) **after all hand-written code is final**, then run `git diff` to confirm no new changes. If the generation produces a diff, the committed generated code is stale — commit the update.

### 4.3 AC Verification

Walk through the plan's AC coverage table. For each acceptance criterion:

1. Name the test that verifies it
2. Confirm that test passed in the latest run
3. If an AC has no test, flag it to the user

**Do NOT use words like "should pass", "looks good", or "I'm confident".** State
facts: "Test `TestHandleTelemetryConfig_ValidRef` passed — covers AC #3."

### 4.4 If Verification Fails

- **Test failure**: Go back to Phase 1.4 (systematic debugging). Do not push
  broken code.
- **Lint failure**: Run `task lint-fix` again. If it persists, fix manually.
- **Build failure**: Read the error. Likely a missing import or type mismatch
  from a generated code step that wasn't run.
- **AC gap**: Either implement the missing coverage or flag it to the user as
  a known gap that needs follow-up.

---

## Phase 5: Create PR

### 5.1 Pre-PR Checks

```bash
# Verify branch state
git status
git log --oneline main..HEAD
git diff --stat main..HEAD
```

Verify:
- All changes are committed (no unstaged changes)
- Commit history is clean and logical
- Total diff is within PR limits (< 400 lines production code, < 10 files
  excluding tests/generated/docs)

**If over PR limits**: Use `/split-pr` to propose a split strategy. Do not
submit an oversized PR.

### 5.2 Push and Create PR

```bash
git push -u origin HEAD
```

Create the PR following `.claude/rules/pr-creation.md` and
`.github/pull_request_template.md`:

```bash
gh pr create --title "<title>" --body "$(cat <<'EOF'
## Summary

<1-2 sentence motivation — why this change is needed>

<Issue reference: Closes #XXXX or Part of #XXXX>

<details>
<summary><strong>Medium level</strong></summary>

<3-5 bullet points covering the key changes at a component level: what was added,
what was modified, and how the pieces fit together. A reviewer scanning this should
understand the shape of the change without reading code.>

</details>

<details>
<summary><strong>Low level</strong></summary>

| File | Change |
|------|--------|
| `path/to/file.go` | What changed and why |

</details>

## Type of change

- [ ] Bug fix
- [x] New feature (or whichever applies)
- [ ] Breaking change
- [ ] Refactoring
- [ ] Documentation
- [ ] Other

## Test plan

- [x] `task lint-fix` passes
- [x] `task test` passes
- [x] `task build` passes
- [x] New unit tests added for [description]
- [x] Manually tested [if applicable]

## Special notes for reviewers

<Non-obvious decisions, known limitations, follow-up work>

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

The Summary section uses three levels of detail via GitHub's `<details>` collapsible
sections so reviewers can drill to the depth they need:

1. **High level** (always visible) — 1-2 sentences on *why* the change exists
2. **Medium level** (expandable) — component-level bullet points on *what* changed
3. **Low level** (expandable) — file-by-file table for deep inspection

### 5.3 Monitor CI

```bash
gh pr checks <pr-number> --repo stacklok/toolhive --watch
```

If CI fails:
1. Read the failure logs
2. Fix the issue using systematic debugging (Phase 1.4)
3. Push the fix as a **new commit** (don't amend)
4. Re-verify locally before pushing

---

## Subagent Reference

Quick reference for which agent to dispatch for each task:

| Task | Agent | Mode |
|------|-------|------|
| Write Go implementation code | `golang-code-writer` | `acceptEdits` — one at a time, never parallel |
| Write unit tests | `unit-test-writer` | `acceptEdits` — dispatch before implementation |
| Review for spec compliance | `code-reviewer` | Read-only — after tests pass |
| Review for code quality | `code-reviewer` | Read-only — after spec review passes |
| Update documentation | `documentation-writer` | `acceptEdits` — after all code stages |
| CRD/operator guidance | `kubernetes-expert` | Read-only — parallel with code writer |
| MCP protocol guidance | `mcp-protocol-expert` | Read-only — parallel with code writer |
| OAuth/auth guidance | `oauth-expert` | Read-only — parallel with code writer |
| Observability guidance | `site-reliability-engineer` | Read-only — parallel with code writer |
| Architecture questions | `toolhive-expert` | Read-only — when plan references unclear code |
| Security review | `security-advisor` | Read-only — for auth/secret/isolation changes |
| Multi-component orchestration | `tech-lead-orchestrator` | Read-only — when stages interact unexpectedly |

**Rules for subagent dispatch:**

1. **One writer at a time**: Never dispatch two agents that write to the same files
2. **Fresh context per task**: Each subagent gets a complete, self-contained prompt.
   Do not say "based on what we discussed" — the subagent has no conversation history
3. **Include conventions inline**: Paste relevant `.claude/rules/` content into
   the prompt. Don't tell the subagent to "read the rules" — give it the rules
4. **Specify output expectations**: Tell the subagent what to return (e.g., "report
   which tests fail and why" or "report PASS or list issues with file:line")
5. **Read-only agents can run in parallel**: Domain experts providing guidance
   (not editing code) can be dispatched alongside a code writer

---

## Behavioral Guidelines

### TDD is non-negotiable
Write the test. Watch it fail. Write the implementation. Watch it pass. If you
wrote code before the test, delete it and start over. Exceptions where TDD adds
no value (implement directly):
- Adding struct fields, constants, or type definitions (verified by compilation)
- CRD type stages that are pure schema changes with no logic
- Boilerplate that cannot meaningfully fail
- Removing deprecated code paths (constants, fallback logic, enum values) — the
  "test" is that existing tests pass with the deprecated path removed and new
  tests confirm the tightened behavior (e.g., validation now rejects what it
  previously accepted)

For these, compilation is the test.

### Verify before claiming
Never say "done", "complete", "fixed", or "passing" without fresh evidence from
the actual command output. "Should work" is not evidence. "All 47 tests passed
(output: PASS)" is evidence.

### Debug systematically
When something breaks, read the error. Trace the data flow. Form a hypothesis.
Test it minimally. If you've tried 3+ fixes without success, stop and re-examine
your assumptions — the design might need revision.

### Commit atomically
Each commit should compile, pass tests, and represent one logical step. Don't
batch unrelated changes. Don't commit broken intermediate states.

### Right-size the agents
Not every task needs a subagent. If the change is a 3-line field addition, just
write it directly. Subagents are for tasks that benefit from focused context and
a fresh perspective — typically 20+ lines of code or tests.

### Dispatch early for bulk test fixture updates
When a type contract change (new required field, removed fallback, tightened
validation) cascades into 10+ test fixtures across multiple files, dispatch a
**golang-code-writer** subagent for the mechanical update rather than editing
one-by-one. Brief it with: (1) the exact before/after pattern, (2) the list of
affected files (use `grep` to find them), (3) the verification command. This is
a high-value subagent use case — the work is mechanical but voluminous, and a
subagent can handle it in one pass while you continue planning the next stage.

### Preserve plan fidelity
The design document is a contract. If you discover during implementation that the
plan is wrong (file doesn't exist, interface changed, pattern doesn't apply),
STOP. Flag the discrepancy to the user. Do not silently deviate from the plan —
the plan exists so that design decisions don't get lost during implementation.

### Document as you go
Don't leave documentation for "later." If a stage introduces user-facing changes,
update the docs in the same commit or immediately after. The documentation-writer
agent exists for this purpose — use it.

---

## Red Flags — STOP Immediately

- **Tests pass immediately**: A new test that passes without implementation is
  testing nothing. Delete it and write a real test.
- **"Just skip the test for now"**: No. TDD means tests first. The test is how
  you know the code works.
- **Agent reports "success" without evidence**: Run verification yourself.
  Agent self-reports are not evidence.
- **3+ failed fix attempts**: You're guessing. Step back, re-read the error,
  trace the root cause.
- **Plan says X but code says Y**: The plan may be stale. Verify before
  implementing. If stale, flag to user.
- **Modifying files not in the plan**: Scope creep. Unless the plan is provably
  incomplete, stick to the plan.
- **"Quick fix, investigate later"**: Investigate now. Quick fixes become
  permanent debt.
- **Pushing code that doesn't compile**: Never. Each push must pass `task build`.

---

## Usage Examples

```
/implement-code @design-mcpremoteproxy-telemetryconfigref.md
/implement-code @design-mcpremoteproxy-telemetryconfigref.md Stage 1
/implement-code @implementation-plan.md
/implement-code #4620
/implement-code "implement the changes from the design doc, stages 2-4"
```
