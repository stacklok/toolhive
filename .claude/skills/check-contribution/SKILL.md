---
name: check-contribution
description: Validates operator chart contribution practices (helm template, ct lint, docs generation) before committing changes.
allowed-tools: [Bash, Read]
---

# Check Operator Chart Contribution Practices

Verify that all contribution guidelines from `deploy/charts/operator/CONTRIBUTING.md` are followed before committing Helm chart changes. Do not make any edits to files.

## Checks

### 1. Helm Template Validation
```bash
cd "$(git rev-parse --show-toplevel)"/deploy/charts/operator && helm template test .
```
Verify the output contains valid Kubernetes YAML without errors.

### 2. Chart Linting
```bash
ct lint
```
Report any linting errors or warnings.

### 3. Documentation Generation
```bash
helm-docs --dry-run
```
Verify that `values.yaml` variables are documented and the generated README.md matches.

## Output Format

```
✅ or ❌ Helm template renders successfully
✅ or ❌ Chart linting passes
✅ or ❌ Documentation up-to-date

Note: Chart version bumps are automated by the release bot. Feature PRs should NOT modify Chart.yaml.
```

Include specific errors for any failing checks with actionable remediation commands.
