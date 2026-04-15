---
name: plan-design
description: Designs features through interactive exploration, producing a persistent design document optimized for Claude Code to implement in a future conversation. Use when you need to think through a feature before implementing it — especially for work that spans multiple stories, has unclear scope, or requires architectural decisions.
---

# Plan & Design

Creates a design document for a feature through interactive exploration with the
user. The output is a persistent file optimized for a future Claude Code session
to pick up and implement without needing the original conversation context.

**This is NOT an implementation skill.** It produces a design — not code, not PRs,
not commits. Use `/implement-story` or `/plan-changes` when you're ready to build.

## When to use this vs other skills

| Skill | Use when |
|-------|----------|
| `/plan-design` | You need to think through a feature before building it. Scope is unclear, multiple approaches exist, or the work spans multiple stories. |
| `/plan-changes` | You have a well-defined issue and want a PR-level execution plan with agent consensus. |
| `/implement-story` | You have a clear story with AC and want to go straight to code. |

## Arguments

The user provides a GitHub issue, a text description, or nothing (interactive mode).

```
/plan-design #4550
/plan-design "add credential rotation for backend MCP servers"
/plan-design
```

**Argument**: `$ARGUMENTS`

---

## Phase 1: Understand the Problem

### 1.1 Capture the Starting Point

Based on the argument:

**If a GitHub issue is provided**:
- Fetch the issue with `gh issue view <number> --repo stacklok/toolhive`
- Extract: title, body, labels, acceptance criteria, linked issues
- Check for linked RFCs (`THV-XXXX` references or links to `toolhive-rfcs`)

**If a text description is provided**:
- Use it as the seed requirement
- Search for related issues:
  ```bash
  gh search issues "<keywords>" --repo stacklok/toolhive --state open --limit 5
  ```

**If no argument is provided**:
- Ask: "What feature or change are you thinking about?"

### 1.2 Ask Clarifying Questions

Before researching anything, ask the user **one round** of clarifying questions.
Focus on things that change the design direction, not implementation details:

- What problem does this solve? (if not clear from the issue)
- Who is the user/persona? (CLI user, operator admin, MCP client, API consumer)
- Are there hard constraints? (backward compat, performance targets, must use X)
- What is explicitly out of scope?
- Is there prior art or a reference implementation to learn from?

Ask **3-5 questions max** in a single message. Wait for answers before proceeding.

If the issue is well-specified with clear AC, you may skip this step — tell the
user you have enough context and move on.

---

## Phase 2: Research the Codebase

### 2.1 Identify Affected Domains

Classify the feature into one or more domains:

1. **MCP Protocol/Transport** — `pkg/transport/`, `pkg/runner/`
2. **Kubernetes Operator** — `cmd/thv-operator/`, `api/v1alpha1/`, `controllers/`
3. **Security & Auth** — `pkg/auth/`, `pkg/authz/`, `pkg/secrets/`
4. **Container Runtime** — `pkg/container/`
5. **API/CLI** — `pkg/api/`, `cmd/thv/`
6. **Virtual MCP** — `pkg/vmcp/`
7. **Observability** — `pkg/telemetry/`
8. **Configuration** — `pkg/config/`, `pkg/registry/`

### 2.2 Deep Exploration

Launch **parallel** Explore agents scoped to each affected domain. Each agent
should answer:

1. What existing code is relevant? (specific files, functions, interfaces)
2. What patterns does similar functionality follow? (find analogues)
3. What extension points exist? (interfaces, registries, middleware chains)
4. What constraints does the existing code impose? (type signatures, config shapes)
5. Are there tests to use as a template?

For domain-specific questions, also launch the appropriate specialist agent
(`kubernetes-expert`, `mcp-protocol-expert`, `oauth-expert`, etc.) to assess
feasibility and flag protocol/spec constraints.

**Read `.claude/rules/` files** for every affected domain before proceeding —
the design must conform to existing conventions.

### 2.3 Verify Agent Findings

Before synthesizing, spot-check agent claims. For each agent's response:

- If it names a file: verify the file exists (`Glob` or `Read`)
- If it names a function or type: `Grep` for it
- If it describes a pattern: read the file and confirm the pattern is there

Agents can hallucinate paths or mischaracterize code. One wrong file reference
in the design wastes the implementer's time. Verify before trusting.

### 2.4 Synthesize Findings

After verification, present findings to the user in this exact format:

```markdown
## Codebase Research Summary

### Key Files
| File | Role | Why It Matters |
|------|------|---------------|
| `path/to/file.go` | What this file does | How the feature interacts with it |

### Patterns to Follow
| Pattern | Example File | How to Apply |
|---------|-------------|-------------|
| e.g., "config ref handler" | `controllers/mcpremoteproxy_controller.go:handleToolConfig()` | Mirror this for the new config ref |

### Constraints
- <constraint with source — e.g., "CEL rules must go on the struct comment, not the field (kubebuilder convention)">

### Gaps
- <thing that doesn't exist yet but needs to — e.g., "no GetTelemetryConfigForMCPRemoteProxy() helper yet">
```

Present this summary to the user before proposing approaches. This ensures the
user can correct any misunderstandings about the codebase before you design on
top of them.

### 2.5 Scope Check

After presenting findings, assess whether the feature is larger than initially
expected:

- **5+ PRs estimated** or **4+ affected domains**: Tell the user: "This is
  larger than a single design doc can cover well. I recommend using `/plan-changes`
  for a PR-level execution plan with multi-agent consensus, or splitting this
  into smaller features first."
- **Cross-cutting concerns** (e.g., requires coordinated changes to CRD types,
  transport layer, AND CLI simultaneously): Flag it — these need more structured
  coordination than a design doc provides.

This is a recommendation, not a hard stop. If the user wants to proceed with
`/plan-design` anyway, continue — but note the complexity risk in the design
document's Risks table.

---

## Phase 3: Explore Approaches

### 3.0 Complexity Gate

Before proposing approaches, assess complexity based on what you learned in Phase 2:

- **Single obvious approach** (existing pattern to mirror, no meaningful design
  choices): Skip 3.1. Present one approach in 3.2 and ask "Does this direction
  make sense?" Do NOT invent artificial alternatives just to fill a template.
- **Genuine design choices exist** (multiple valid patterns, tradeoffs between
  approaches): Proceed with 3.1.

The test: if you cannot articulate a *real* reason someone would pick Approach B
over Approach A, there is only one approach. Move on.

### 3.1 Propose 2-3 Approaches

For features with **genuine design choices**, propose 2-3 approaches. For each:

```markdown
### Approach [A/B/C]: [name]

**How it works**: 1-3 sentences describing the approach.

**Key files affected**:
- `path/to/file.go` — what changes and why
- `path/to/new.go` — new file, purpose

**Follows pattern from**: `path/to/existing_analogue.go` (describe similarity)

**Tradeoffs**:
- [+] advantage
- [-] disadvantage

**Risk**: [LOW/MED/HIGH] — one sentence explaining the risk
```

### 3.2 Make a Recommendation

State which approach you recommend and why. Frame it as:

> "I recommend **Approach A** because [reason]. The main tradeoff is [X], which I
> think is acceptable because [Y]. Approach B would be better if [condition that
> doesn't apply here]."

### 3.3 Get User Alignment

Ask the user to pick an approach or propose modifications. **Do not proceed to
Phase 4 until the user has confirmed the direction.**

If the feature is straightforward (single obvious approach), you may present one
approach and ask "Does this direction make sense?" instead of forcing alternatives.

---

## Phase 4: Design the Solution

With the chosen approach confirmed, produce the detailed design. This is the core
output — optimize it for a future Claude Code session that has zero context about
this conversation.

### 4.1 Interface Contracts

Define the key interfaces, structs, and function signatures. These are the
contracts that the implementation must satisfy. Include:

- New interfaces with method signatures and doc comments
- New structs with field types and json/yaml tags if relevant
- Key function signatures (not all functions — just the ones that define the API)
- How new types connect to existing ones (implements X, extends Y, registered in Z)

```go
// Example — show the actual Go signatures
type CredentialRotator interface {
    // Rotate replaces the credential for the given backend.
    // Returns the new credential expiry time.
    Rotate(ctx context.Context, backendID string) (time.Time, error)
}
```

**Do NOT write implementation bodies.** Signatures and contracts only.

### 4.2 Key Design Decisions

For each non-obvious decision made during the design, document it:

| Decision | Choice | Why | Alternatives Rejected |
|----------|--------|-----|-----------------------|
| Where to store rotation state | In-memory with CR status backup | Operator pattern precedent in `controllers/` | Database (overkill), ConfigMap (no typed schema) |

Include decisions the user made during Phase 3 — a future session won't have
that conversation context.

### 4.3 File Change Map

List every file that needs to change, what the change is, and what existing
pattern to follow:

| File | Action | What Changes | Pattern to Follow |
|------|--------|-------------|-------------------|
| `pkg/auth/rotation.go` | create | Credential rotation interface and types | `pkg/auth/provider.go` structure |
| `controllers/vmcp_controller.go` | modify | Add rotation reconciliation loop | Existing reconcile methods in same file |
| `api/v1alpha1/vmcp_types.go` | modify | Add RotationPolicy field to spec | Other spec fields in same file |

Use **function/type names** as anchors, not line numbers (line numbers shift).

### 4.4 Dependency Graph

Show the order in which work must happen:

```
Stage 1: Types & interfaces (no dependencies)
  - api/v1alpha1/vmcp_types.go — new CRD fields
  - pkg/auth/rotation.go — new interface

Stage 2: Core logic (depends on Stage 1)
  - pkg/auth/rotation_impl.go — implementation

Stage 3: Integration (depends on Stage 2)
  - controllers/vmcp_controller.go — wire into reconciler

Stage 4: Generated code (depends on Stages 1-3)
  - task operator-manifests operator-generate
  - task gen
```

Mark which stages are independent (can be parallelized) and which are sequential.

### 4.5 Test Strategy

For each stage, specify what to test:

| Stage | Test Type | What to Test | Test File |
|-------|-----------|-------------|-----------|
| 1 | Unit | Type validation, defaults | `api/v1alpha1/vmcp_types_test.go` |
| 2 | Unit | Rotation logic, error paths | `pkg/auth/rotation_test.go` |
| 3 | Integration | Controller reconciliation | `controllers/vmcp_controller_test.go` |

Name specific test patterns to follow from existing tests (e.g., "table-driven
like `pkg/auth/provider_test.go`").

### 4.6 Pitfalls and Landmines

List things that will trip up the implementer:

- **Non-obvious constraints**: "The reconciler must not call Rotate() if the
  backend is in a degraded state — check `status.conditions` first"
- **Ordering issues**: "CRD field additions must be in the same commit as
  `task operator-manifests operator-generate` output"
- **Test gotchas**: "Use `envtest` for controller tests, not mocks — see
  `.claude/rules/testing.md`"
- **Convention traps**: "Don't add `feat:` prefix to commit messages — this
  repo doesn't use conventional commits"

---

## Phase 5: Scope and Split

### 5.1 Estimate Size

Based on the file change map, estimate whether this fits in a single PR:

- Count files changed (excluding tests, generated code, docs)
- Estimate lines of production code

**If within limits** (< 10 files, < 400 lines production code): note "single PR".

**If over limits**: propose a split strategy with PR boundaries aligned to the
dependency graph stages. Each PR should be independently mergeable and testable.

### 5.2 Acceptance Criteria Mapping

Map every acceptance criterion to a stage and (if split) a PR:

| AC | Stage | PR | How Verified |
|----|-------|----|-------------|
| Backend credentials can be rotated | 2 | PR 1 | Unit test |
| Rotation is triggered by CRD field | 3 | PR 2 | Integration test |
| Failed rotation surfaces in status | 3 | PR 2 | Integration test |

**Every AC must appear in this table.** If an AC cannot be mapped, flag it as
needing clarification.

---

## Phase 6: Write the Design Document

### 6.1 Self-Review Checklist

Before writing the final document, **actively verify** each item — do not check
a box from memory. Run the commands.

- [ ] **File paths exist**: For every file in the change map marked "modify",
  run `Glob` or `Read` to confirm it exists. For "create" files, confirm the
  parent directory exists.
- [ ] **Types and functions exist**: For every type, interface, or function named
  in the design as existing code, `Grep` for it. If it was renamed or removed
  since Phase 2, update the design.
- [ ] **Patterns are real**: For every "pattern to follow" reference, `Read` the
  file and confirm the pattern is there (not just that the file exists).
- [ ] **AC coverage is complete**: Every acceptance criterion from the issue
  appears in the AC coverage table. Count them.
- [ ] **No placeholders**: Search the design text for TBD, TODO, FIXME, "...",
  or `<placeholder>`. Remove or resolve all of them.
- [ ] **Rules compliance**: Re-read `.claude/rules/` files for all affected
  domains and verify the design doesn't violate any convention.

If any check fails, fix the design before writing the document. A stale design
is worse than no design — it sends the implementer down the wrong path.

### 6.2 Write the Document

Write the design to a file. The format below is the contract — a future Claude
Code session will rely on this structure.

```bash
cat <<'EOF' > design-<feature-slug>.md
# Design: <feature title>

## Source
<issue reference or text description>
<date of design>

## Goal
<1-2 sentences: what and why>

## Scope
- **In**: <bullet list>
- **Out**: <bullet list>

## Key Decisions

| Decision | Choice | Why | Alternatives Rejected |
|----------|--------|-----|-----------------------|
| ... | ... | ... | ... |

## Interface Contracts

<Go code blocks with signatures, types, interfaces — no implementation bodies>

## Implementation Stages

### Stage N: <name>
- **Depends on**: <stage(s) or "none">
- **Files**:
  | File | Action | What Changes | Pattern to Follow |
  |------|--------|-------------|-------------------|
  | ... | ... | ... | ... |
- **Tests**:
  | Test Type | What to Test | Test File |
  |-----------|-------------|-----------|
  | ... | ... | ... |
- **Pitfalls**: <bullet list of things to avoid>

## PR Strategy
<single PR or split strategy with PR boundaries>

## AC Coverage

| AC | Stage | PR | How Verified |
|----|-------|----|-------------|
| ... | ... | ... | ... |

## Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| ... | ... | ... |

## Verification
```shell
task lint-fix
task test
task build
```
EOF
```

### 6.3 Present to User

Show the user the full design document inline. Then ask:

> "Design saved to `design-<feature-slug>.md`.
>
> To implement this later:
> - `@design-<feature-slug>.md implement Stage 1 of this design`
> - Or use `/implement-story` if there's a GitHub issue with AC
>
> Want to revise anything before we're done?"

If the user wants changes, apply them and re-present. Repeat until confirmed.

### 6.4 Clean Up

Remove any scratch files created during exploration:

```bash
rm -f plan-context.md plan-decisions.md
```

---

## Behavioral Guidelines

### Be interactive, not autonomous
This skill is a conversation, not a pipeline. Pause for user input at:
- End of Phase 1 (after clarifying questions)
- End of Phase 2 (after presenting codebase findings)
- End of Phase 3 (after proposing approaches)
- End of Phase 6 (after presenting the design)

Do NOT blast through all phases without stopping.

### Ground everything in code
Every claim about the codebase must reference a specific file or function that
you have actually read. Do not describe architecture from memory — verify it.
If an agent reports something surprising, verify it yourself before including
it in the design.

### Decide, don't defer
The design must contain decisions, not options. If you're unsure, ask the user
in Phase 3. By Phase 4, every decision should be resolved with a clear rationale.
A design full of "could do A or B" is useless to a future session.

### Right-size the process
- **Trivial change** (< 50 lines, single file): Skip Phases 2-3. Go straight
  from understanding the problem to writing a lightweight design.
- **Medium change** (1-3 files, clear approach): Do a quick Phase 2, use the
  complexity gate in Phase 3.0 to skip approach comparison, go straight to design.
- **Large change** (multiple packages, architectural decisions): Full process.
- **Very large change** (5+ PRs, 4+ domains): Flag in Phase 2.5 and recommend
  `/plan-changes` for structured execution planning.

Calibrate the depth of exploration to the complexity of the problem.

### Preserve the "why"
A future session will see the design document but not this conversation. Every
decision must include its rationale. "Use approach A" is insufficient.
"Use approach A because it follows the existing pattern in X and avoids the
backward-compat risk of approach B" gives the implementer enough context to
handle edge cases.

## Usage Examples

```
/plan-design #4550
/plan-design https://github.com/stacklok/toolhive/issues/4550
/plan-design "add credential rotation for backend MCP servers"
/plan-design "refactor transport layer to support WebSocket alongside SSE"
/plan-design
```
