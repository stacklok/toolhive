## Summary

<!--
REQUIRED. You MUST explain:
1. WHY this change is needed (the problem or motivation)
2. WHAT changed (concise bullet points)

The diff shows the code — your summary must provide the context a reviewer
needs to understand the purpose without reading the diff first.
-->

-

<!--
Link related issues. Use "Closes" or "Fixes" to auto-close on merge.
Remove this line if there is no related issue.
-->

Fixes #

## Type of change

<!-- REQUIRED. Check exactly one. -->

- [ ] Bug fix
- [ ] New feature
- [ ] Refactoring (no behavior change)
- [ ] Dependency update
- [ ] Documentation
- [ ] Other (describe):

## Test plan

<!--
REQUIRED. Check every verification step you actually ran.
You MUST check at least one item. If you only did manual testing,
describe exactly what you tested below the checkbox.
-->

- [ ] Unit tests (`task test`)
- [ ] E2E tests (`task test-e2e`)
- [ ] Linting (`task lint-fix`)
- [ ] Manual testing (describe below)

## API Compatibility

<!--
The CRD Schema Compatibility check guards the v1beta1 operator API.
If the check flags this PR as Incompatible and the break is intentional,
apply the `api-break-allowed` label and describe below:

1. Which fields, types, or CRDs are changing.
2. Why the break is unavoidable.
3. The user-facing migration path (what cluster admins need to do).

See CONTRIBUTING.md → "API Stability" for the full rubric. Coordinate
with maintainers before applying the label.

Remove this section entirely if the PR does not touch operator API surface.
-->

- [ ] This PR does not break the `v1beta1` API, OR the `api-break-allowed` label is applied and the migration guidance is described above.

## Changes

<!--
Optional — include for PRs touching more than a few files to help
reviewers navigate the diff. Remove this entire section for small PRs.
-->

| File | Change |
|------|--------|
|      |        |

## Does this introduce a user-facing change?

<!--
If yes, describe the change from the user's perspective. This helps with release notes.
If no, write "No".
Remove this section entirely if not applicable.
-->

## Implementation plan

<!--
Optional — include when this PR was planned with an AI assistant (Claude Code, etc.).
Paste the approved plan inside the <details> block so reviewers can see the intended
design without cluttering the main PR description. Remove this section entirely
for PRs that were not AI-planned.
-->

<details>
<summary>Approved implementation plan</summary>

<!-- Paste the plan here -->

</details>

## Special notes for reviewers

<!--
Optional — call out anything non-obvious: tricky logic, known limitations,
areas where you'd like extra scrutiny, or follow-up work planned.
Remove this section if not needed.
-->
