---
name: toolhive-release
description: Creates ToolHive release PRs by analyzing commits since the last release, categorizing changes, recommending semantic version bump type (major/minor/patch), and triggering the release workflow. Use when cutting a release, preparing a new version, checking what changed since last release, or when the user mentions "release", "version bump", or "cut a release".
---

# ToolHive Release

Automates the ToolHive release process by analyzing changes and triggering the release PR workflow.

## When to Use

- When cutting a new ToolHive release
- When checking what's changed since the last release
- When deciding between patch, minor, or major version bump
- When the user says "release", "cut a release", "new version", or "version bump"

## Instructions

### Step 1: Find the Last Release

```bash
git tag --sort=-v:refname | head -1
```

This returns the most recent version tag (e.g., `v0.8.3`).

### Step 2: List Commits Since Last Release

```bash
git log <last-tag>..HEAD --oneline --no-merges
```

Count the commits:
```bash
git log <last-tag>..HEAD --oneline --no-merges | wc -l
```

### Step 3: Categorize Changes

Analyze each commit and categorize into:

| Category | Description | Version Impact |
|----------|-------------|----------------|
| **New Features** | New functionality, new commands, new APIs | Minor bump |
| **Bug Fixes** | Fixes to existing functionality | Patch bump |
| **Breaking Changes** | API changes, removed features, incompatible changes | Major bump |
| **Improvements** | Enhancements to existing features, refactoring | Patch or Minor |
| **Tests/CI** | Test additions, CI/CD changes | No impact |
| **Documentation** | Doc updates, README changes | No impact |
| **Dependencies** | Dependency updates (Renovate PRs) | Patch bump |

### Step 4: Recommend Version Bump

Based on the categorization:

- **Major** (`X.0.0`): Any breaking changes present
- **Minor** (`0.X.0`): New features without breaking changes
- **Patch** (`0.0.X`): Only bug fixes, dependency updates, improvements

Present the recommendation with justification to the user.

### Step 5: Trigger the Release Workflow

**IMPORTANT**: Present the analysis and recommendation to the user and WAIT for explicit confirmation before proceeding.

After user confirms the bump type, use the GitHub MCP tool to trigger the workflow:

```
mcp__github__run_workflow(
  owner: "stacklok",
  repo: "toolhive",
  workflow_id: "create-release-pr.yml",
  ref: "main",
  inputs: { "bump_type": "<patch|minor|major>" }
)
```

### Step 6: Monitor and Report

1. Get the workflow run status:
```
mcp__github__list_workflow_runs(
  owner: "stacklok",
  repo: "toolhive",
  workflow_id: "create-release-pr.yml",
  per_page: 1
)
```

2. Poll until completion (check the `status` field until it shows "completed"):
```
mcp__github__get_workflow_run(
  owner: "stacklok",
  repo: "toolhive",
  run_id: <run_id from step 1>
)
```

3. Find the created PR:
```
mcp__github__list_pull_requests(
  owner: "stacklok",
  repo: "toolhive",
  state: "open",
  sort: "created",
  direction: "desc",
  per_page: 5
)
```
Look for the PR with title matching "Release v<new-version>".

Report the PR URL to the user.

## Release Workflow Chain

For reference, here's what happens after the PR is merged:

1. **create-release-pr.yml** (manual) → Creates PR with version bumps
2. **create-release-tag.yml** (auto on VERSION change) → Creates git tag + GitHub Release
3. **releaser.yml** (auto on release publish) → Builds binaries, images, Helm charts

See [WORKFLOW-REFERENCE.md](references/WORKFLOW-REFERENCE.md) for detailed workflow documentation.

## Example Output

```
## Commits since v0.8.3 (24 commits)

### New Features
- OAuth Authorization Server (#3531, #3513, #3520, #3488)
- ExcludeAll for VirtualMCPServer (#3499)
- Generic PrefixHandlers (#3524)

### Bug Fixes
- OAuth token refresh context cancellation (#3539)
- Custom YAML unmarshalers for registry metadata (#3545)

### Improvements
- Logging updates (#3546, #3547)

### Tests/CI/Docs
- E2E tests for secrets management (#3485)
- Dependency updates

**Recommendation: Minor release (0.9.0)**
New features (OAuth auth server, ExcludeAll) warrant a minor version bump.
```

## Error Handling

- **No tags found**: Repository may not have any releases yet. Check `git tag` output.
- **Workflow trigger fails**: Ensure GitHub MCP server is configured and has proper permissions. The token needs `actions:write` scope.
- **PR not found**: The workflow may still be running. Poll `mcp__github__get_workflow_run` until status is "completed", then search for the PR.
- **Workflow run failed**: Use `mcp__github__get_workflow_run` to check the `conclusion` field. If "failure", use `mcp__github__get_job_logs` to investigate.
