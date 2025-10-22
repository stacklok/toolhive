---
description: Ensures operator chart contribution practices from CONTRIBUTING.md are followed
---

# Check Pre-Commit Practices for Operator Chart

You are verifying that all contribution guidelines from [deploy/charts/operator/CONTRIBUTING.md](deploy/charts/operator/CONTRIBUTING.md) are followed before committing changes to the Operator Chart. Do not make any edits to files.

## Practices to Check

Verify each of the following practices is completed:

### 1. Commit Signature Verification
- Check if commits are signed by running: `git log --show-signature -1`
- If not signed, remind the user to [sign their commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits)

### 2. Helm Template Validation
- Run `helm template` on the chart to ensure changes render correctly into Kubernetes manifests
- Command: `cd "$(git rev-parse --show-toplevel)"/deploy/charts/operator && helm template test .`
- Verify the output contains valid Kubernetes YAML without errors
- Check that changes are properly reflected in the rendered manifests

### 3. Chart Linting
- Run Chart Testing tool to lint the chart
- Command: `ct lint`
- Ensure all linting tests pass
- Report any linting errors or warnings found

### 4. Documentation Generation
- Verify that variables in `values.yaml` are properly documented with comments
- Run pre-commit hooks to generate README.md: `pre-commit run --all-files`
- Preview documentation with: `helm-docs --dry-run`
- Ensure the generated README.md matches the current values.yaml

### 5. Chart Version Bump
If changes are made to the chart, verify:
- Chart version follows [SemVer](https://semver.org/)
- Chart version is bumped in [deploy/charts/operator/Chart.yaml](deploy/charts/operator/Chart.yaml)
- Chart badge is updated in [deploy/charts/operator/README.md](deploy/charts/operator/README.md)
- Version changes are appropriate for the type of change (major/minor/patch)

## Execution Steps

1. Read the current git log to check commit signatures
2. Run helm template to validate manifest rendering
3. Run ct lint to check chart quality
4. Run pre-commit hooks to ensure documentation is up-to-date
5. Check if Chart.yaml version has been bumped appropriately
6. Provide a summary report of all checks with pass/fail status

## Output Format

Provide a checklist report:

```
 or L Commit signatures verified
 or L Helm template renders successfully
 or L Chart linting passes
 or L Documentation up-to-date (pre-commit)
 or L Chart version bumped appropriately
```

Include specific errors or warnings for any failing checks, and provide actionable remediation steps.

## Example Usage

User runs: `/check-pre-commit`

You should:
1. Execute all verification steps
2. Report findings with specific details
3. Provide commands to fix any issues found
4. Confirm when all practices are satisfied
