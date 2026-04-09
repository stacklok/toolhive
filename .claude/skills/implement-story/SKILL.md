---
name: implement-story
description: Implements a GitHub user story from planning through PR creation, with research, codebase analysis, and structured commits.
---

# Implement User Story

Takes a GitHub user story issue and produces well-organized PR(s) that reliably meet the acceptance criteria.

## Arguments

The user provides a GitHub issue number or URL. Example:

```
/implement-story #4550
/implement-story https://github.com/stacklok/toolhive/issues/4550
```

---

## Phase 1: Gather Context

### 1.1 Read the Issue

Fetch the issue body using GitHub tools. Extract:

- **User story**: The "As a / I want / so that" statement
- **Acceptance criteria**: The checkbox list — this is the contract
- **Context links**: RFC links, related issues, dependencies
- **Out of scope**: What NOT to do

### 1.2 Fetch RFC Context

If the issue links to an RFC (look for `THV-XXXX` references or links to `toolhive-rfcs`):

1. Clone or locate the RFC repo locally (check `../toolhive-rfcs/` first)
2. Read the full RFC document
3. Extract design decisions relevant to this story — config shapes, algorithm details, error formats, key schemas, etc.

If no RFC is linked, skip this step.

### 1.3 Find Related Stories

Search for sibling stories that share context with this one. These inform how to factor the code for extensibility:

```bash
# Search by keywords from the issue title
gh search issues "<keywords>" --repo stacklok/toolhive --state open --limit 10

# Search for issues linking to the same RFC
gh search issues "THV-XXXX" --repo stacklok/toolhive --limit 10
```

For each related story, read its acceptance criteria. Ask:

- Will a future story need to extend a type, interface, or package I'm creating?
- Should I define an interface now that a sibling story will implement later?
- Are there naming conventions or patterns I should establish that siblings will follow?

**Do not implement sibling stories.** Design internal interfaces so they can be
extended without refactoring, but do not add config fields, CRD types, or
user-facing API surface for functionality that isn't implemented in this PR.
Unused config confuses users and reviewers.

### 1.4 Research the Codebase

Use the Explore agent or direct search to understand:

1. **Where does this change fit?** Identify the packages, files, and functions that need modification.
2. **What patterns exist?** Find analogous features already implemented. For example, if adding a new middleware, study how existing middleware (auth, mcp-parser, authz) is registered and wired.
3. **What gets generated?** Identify files that are auto-generated (CRD manifests, mocks, docs) so you know what to regenerate.
4. **What tests exist?** Find the test patterns used for similar features (table-driven tests, testcontainers, Chainsaw E2E).

Document your findings before writing any code.

---

## Phase 2: Plan the Work

### 2.1 Map AC to Changes

For each acceptance criterion, identify:

- Which files need to change
- Whether it's new code or a modification
- What tests verify it (unit, integration, or E2E)

### 2.2 Decide PR Strategy

Evaluate the total scope against the project's PR guidelines:

- **< 10 files changed** (excluding tests, generated code, docs)
- **< 400 lines of code changed** (excluding tests, generated code, docs)

If the story fits in one PR, use a single PR. If not, split into multiple PRs following these patterns:

1. **Foundation first**: New types, interfaces, packages
2. **Wiring second**: Integration into existing code (middleware chain, reconciler, CRD)
3. **Tests alongside**: Each PR includes its own tests
4. **Generated code with its trigger**: CRD type changes + `task operator-manifests operator-generate` output in the same PR

### 2.3 Present the High-Level Plan

First, show the user a high-level plan covering PR boundaries and what each PR delivers.
Do NOT include commit-level details yet — get alignment on the split first.

```markdown
## Implementation Plan

**Story**: #XXXX — [title]
**PRs**: [1 or N]

### PR 1: [title]
- [what this PR introduces and why]
- **AC covered**: [which acceptance criteria]

### PR 2: [title] (if needed)
- [what this PR introduces and why]
- **AC covered**: [which acceptance criteria]
```

Wait for user approval on the PR split. Adjust if the user has feedback.

### 2.4 Plan Each PR in Detail

Once the user approves the high-level split, enter plan mode for the first PR.
In plan mode, explore the codebase and design commit boundaries, file changes,
and test strategy. Present the detailed plan for user approval before writing code.

For subsequent PRs, enter plan mode again once CI is green for the previous PR.

---

## Phase 3: Implement

### 3.1 Create a Branch

```bash
git checkout -b <user>/<short-description> main
```

### 3.2 Write Code

Implement the changes from the plan. Follow these principles:

- **Match existing patterns**: Don't invent new conventions. Study the codebase and follow what's there.
- **Design for siblings**: If related stories will extend this code, use interfaces and clear extension points. But don't build speculative abstractions — just leave the door open.
- **Tests are not optional**: Every AC that says "Unit:" or "E2E:" must have a corresponding test. Write tests as you go, not at the end.
- **Core vs integration**: Core domain logic (algorithms, data structures, config
  parsing) can be introduced standalone — it's a testable unit of behavior.
  Integration concerns (protocol adapters, transport-specific formatting,
  middleware glue) should be introduced alongside the code that consumes them.
  If nothing in the PR calls a function, ask whether it belongs in a later PR.
- **Don't ship unused config surface**: If a story explicitly marks something
  as out of scope, do not add config fields, CRD attributes, or API surface
  for it. Design internal interfaces to be extensible, but only introduce
  user-facing configuration when the corresponding logic ships in the same PR.

### 3.3 Commit Per the Plan

Follow the commit boundaries from the plan. Each commit should:

- Be independently compilable (`go build ./...` passes)
- Have a clear, descriptive message
- Group related changes (e.g., don't mix CRD type changes with middleware logic)

### 3.4 Run Regeneration Tasks

After changes that affect generated artifacts, run the appropriate tasks:

| Change Type | Regeneration Command |
|-------------|---------------------|
| CRD type definitions (`api/v1alpha1/*_types.go`) | `task operator-manifests operator-generate` |
| Mock interfaces | `task gen` |
| CLI commands or API endpoints | `task docs` |
| Helm chart values | `task helm-docs` |
| Any Go file | `task license-fix` |

Run these **before committing** the related changes. Include the generated output in the same commit as the trigger.

---

## Phase 4: Create PR

### 4.1 Push and Create PR

Follow the PR template at `.github/pull_request_template.md` and the rules in `.claude/rules/pr-creation.md`:

- Title: under 70 chars, imperative mood, no conventional commit prefix
- Summary: why first, then what. Reference the issue with `Closes #XXXX`
- Type of change: check exactly one
- Test plan: check every verification step actually run

### 4.2 Verify AC Coverage

Before submitting, review each acceptance criterion from the issue:

- [ ] Is there code that implements it?
- [ ] Is there a test that verifies it?
- [ ] Has the test passed?

If any AC is not covered, either implement it or flag it to the user with a reason.

### 4.3 Babysit CI

After pushing, monitor CI status:

```bash
gh pr checks <pr-number> --repo stacklok/toolhive --watch
```

If CI fails:
1. Read the failure logs
2. Fix the issue
3. Push the fix as a new commit (don't amend — keep the history clean for review)
4. Re-check CI

### 4.4 Multi-PR Workflow

If the story spans multiple PRs:

1. Create the first PR targeting `main`
2. After merge, create subsequent PRs targeting `main`
3. Each PR references the story issue (`Part of #XXXX`)
4. The final PR uses `Closes #XXXX`

---

## Edge Cases

- **AC references another story**: If an acceptance criterion depends on work from another story (e.g., "STORY-001 core middleware exists"), check if that story is merged. If not, flag it to the user.
- **Generated code is large**: CRD manifest regeneration can produce hundreds of lines of diff. This is expected — note it in the PR description under "Special notes for reviewers."
- **Tests require infrastructure**: E2E tests may need a Kind cluster, Redis, or Keycloak. Document the setup in the test plan. Don't skip the test — write it even if the user will run it separately.
- **RFC is ambiguous**: If the RFC doesn't specify a detail needed for implementation, make a pragmatic choice, document it in a code comment, and flag it in the PR description.
