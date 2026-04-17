---
name: release-notes
description: Generates polished GitHub release notes for a ToolHive release by analyzing every merged PR, cross-referencing linked issues, dispatching expert agents to assess breaking changes, and producing a formatted release body. Use when the user provides a GitHub release URL, tag name, or says "release notes".
---

# Release Notes Generator

Produces publication-ready GitHub release notes by deeply analyzing every PR
merged between two version tags.

## Arguments

```
/release-notes https://github.com/stacklok/toolhive/releases/tag/v0.18.0
/release-notes v0.18.0
```

**Input**: `$ARGUMENTS` — a GitHub release URL or a tag name.

---

## Phase 1: Gather Raw Data

### Step 1: Resolve the release and prior tag

```bash
# If given a URL, extract the tag from the path
# Then find the immediately preceding release tag
gh release view <tag> --json tagName,name,body,publishedAt
git tag --sort=-v:refname | grep -A1 "^<tag>$" | tail -1
```

Store:
- `CURRENT_TAG` (e.g., `v0.18.0`)
- `PREVIOUS_TAG` (e.g., `v0.17.0`)
- `PUBLISHED_AT` date

### Step 2: Get the auto-generated changelog

Fetch the existing release body. GitHub's auto-generated "What's Changed" block
(PR title by @author with links) will be preserved verbatim as the commit log
at the bottom of the final output. Save it as `AUTO_CHANGELOG`.

### Step 3: List all PRs between tags

```bash
gh api repos/stacklok/toolhive/compare/{PREVIOUS_TAG}...{CURRENT_TAG} \
  --jq '.commits[] | "\(.sha[0:8]) \(.commit.message | split("\n")[0])"'
```

Extract every PR number from commit messages (look for `(#NNNN)` suffixes).
Exclude the release PR itself (e.g., "Release vX.Y.Z").

### Step 3b: Separate dependency PRs

Filter out PRs authored by `renovate[bot]`, `dependabot[bot]`, or with labels
containing `dependencies`. These go directly into the **Dependencies** section —
they do not need expert review or further classification. Record them separately.

### Step 4: Fetch PR details

For each PR, fetch:
- Title, labels, body
- Whether the "Breaking change" checkbox is checked in the body
- Linked issues (look for `Closes #N`, `Fixes #N`, `Part of #N`, `Resolves #N`)
- Migration guide content (if present in the PR body)

```bash
gh pr view <number> --json title,labels,body
```

### Step 5: Fetch linked issue details

For each unique linked issue number, fetch title and labels:

```bash
gh issue view <number> --json title,labels
```

### Step 6: Identify new contributors

Check the auto-generated changelog for the "New Contributors" section. Extract
author handles.

---

## Phase 2: Classify Changes

### Step 1: Initial triage

Dependency PRs (from Step 3b) are already separated — skip them here.

Categorize each remaining PR into one of the categories below. Check the
signals **in this priority order** — earlier signals are more reliable:

1. **Linked issue labels** — if the linked issue has a `breaking-change` label,
   classify as Breaking regardless of whether the PR checkbox is checked.
2. **PR body content** — look for explicit "breaking" mentions, removal of
   fields/APIs, or JSON tag renames. Note: a migration guide alone does NOT
   mean breaking — deprecations often include migration guides too. The key
   question is whether the old behavior/field/API **still works**. If yes,
   it's a deprecation. If no, it's breaking.
3. **PR labels** — `breaking`, `enhancement`, `bug`, etc.
4. **Breaking change checkbox** — least reliable; often unchecked even on
   genuinely breaking PRs.

| Category | Criteria |
|----------|----------|
| **Breaking** | Old behavior/field/API **no longer works** — linked issue labeled `breaking-change`, OR "Breaking change" checkbox checked, OR PR labels contain `breaking`, OR PR removes fields/endpoints/flags without backwards compatibility |
| **Deprecation** | PR introduces new deprecation warnings or marks fields as deprecated |
| **New Feature** | Labels contain `enhancement`/`feature`, OR PR adds new user-facing capability |
| **Bug Fix** | Labels contain `bug`, OR PR title/body indicates a fix |
| **Misc** | Everything else — refactors, test improvements, CI, docs, internal cleanup |

**Overlap rule:** If a PR belongs to multiple categories (e.g., both a new
feature AND a breaking change), always classify it in the **most urgent**
category. The priority order is: Breaking > Deprecation > Bug Fix > New Feature > Misc.
The PR can still be mentioned in a secondary section (e.g., a breaking API
change can also appear under New Features for its positive user impact), but its
primary home is always the most urgent category.

### Step 2: Identify ambiguous PRs

Any PR that touches CRD types, API surfaces, wire formats, authentication flows,
or MCP protocol behavior but is NOT already classified as breaking needs expert
review. Flag these for Phase 3.

Heuristics for flagging:
- Modifies files in `cmd/thv-operator/api/` or CRD manifests
- Changes JSON/YAML struct tags (especially renames — these cause silent etcd
  data loss on existing resources)
- Removes CRD fields, API fields, CLI flags, or enum values
- Alters authentication, token handling, or middleware wiring
- Changes MCP message formats or transport behavior
- Renames or removes public Go types/methods consumed by external packages
- Changes default values, config semantics, or HTTP status codes

For flagged PRs, always fetch the diff summary so agents have concrete data:

```bash
gh pr diff <number> --stat
```

---

## Phase 3: Expert Breaking-Change Assessment

### Step 1: Map PRs to expert agents

For flagged PRs and confirmed breaking PRs, dispatch the appropriate expert
agent to assess impact and write migration guidance.

| Change Area | Agent | What to ask |
|-------------|-------|-------------|
| CRD types, operator, Helm | `kubernetes-expert` | Is this a breaking CRD change? What manifests break? What's the migration path? |
| MCP transport, protocol messages | `mcp-protocol-expert` | Does this break MCP clients or change wire behavior? |
| Auth flows, OIDC, tokens, Cedar | `oauth-expert` | Does this break existing auth configurations? |
| API endpoints, CLI commands | `toolhive-expert` | Does this break CLI users or API consumers? |
| Observability, metrics, tracing | `site-reliability-engineer` | Does this change metric names, trace attributes, or dashboard contracts? |

### Step 2: Launch agents in parallel

For each flagged PR, include in the agent prompt:
- The PR title, number, and full body
- The linked issue title and body (if any)
- The diff summary (`gh pr diff <number> --stat`)
- The question: "Is this a breaking change? If yes, who is affected and what is
  the migration path? If no, explain why it's safe."

**When a PR has no labels, no checkbox, no migration guide, and no issue
references** — the agent MUST read the actual code changes to make a
determination. Tell the agent to examine the PR diff and the affected source
files directly rather than relying on metadata. This is the fallback for
under-documented PRs.

**Launch all agents in a single message** so they run in parallel.

### Step 3: Collect verdicts

Each agent returns one of:
- **Breaking** — with affected audience, impact description, and migration steps
- **Deprecation** — with timeline and recommended replacement
- **Not breaking** — with rationale for why it's safe

Update the classification from Phase 2 with agent verdicts. If an agent
overrides the initial classification (e.g., flags something as breaking that
wasn't initially caught), trust the domain expert.

---

## Phase 4: Compose Release Notes

Read the template at [TEMPLATE.md](TEMPLATE.md) and use it to assemble the
final release body. **Omit any section that has zero entries** — do not include
empty headers.

---

## Phase 5: Present and Publish

### Step 1: Present the draft

Show the complete release notes to the user. Highlight:
- How many breaking changes were found (and which agents confirmed them)
- Any PRs where the breaking-change assessment was uncertain
- Any PRs with no linked issues (less context available)

### Step 2: Wait for approval

Ask:

> "Ready to publish these release notes?
> 1. **Publish** — update the GitHub release with these notes
> 2. **Revise** — tell me what to change
> 3. **Export** — save to a file instead of publishing"

### Step 3: Save to file

Always write the final release notes to `release-notes-<tag>.md` in the repo
root (e.g., `release-notes-v0.19.0.md`). This gives the user a reviewable
artifact before anything is published.

### Step 4: Publish (if approved)

If the user chose "Publish", push the notes to the GitHub release:

```bash
gh release edit <CURRENT_TAG> --notes-file release-notes-<tag>.md
```

---

## Important Notes

- **Read every PR body** — do not skip PRs or rely only on titles. The breaking
  change checkbox, migration guides, and linked issues are in the body.
- **Cross-reference issues** — issue labels and descriptions often contain
  context that the PR body lacks (e.g., an issue labeled `breaking` when the PR
  isn't).
- **Trust expert agents** for domain-specific breaking-change assessments. If
  the kubernetes-expert says a CRD change is breaking, it is breaking.
- **When in doubt, flag it** — it's better to ask the user about a potentially
  breaking change than to miss it. Present the evidence and let them decide.
- **Preserve the auto-generated changelog verbatim** — do not reformat, reorder,
  or edit the GitHub "What's Changed" block. It's the raw record.
- **Omit empty sections** — if there are no breaking changes, no deprecations,
  or no new contributors, leave those sections out entirely. Do not include
  headers with no content beneath them.

## Usage Examples

```
/release-notes https://github.com/stacklok/toolhive/releases/tag/v0.18.0
/release-notes v0.18.0
/release-notes v0.15.0
```
