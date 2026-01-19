---
name: helm-chart-bump
description: Ensures that all necessary tasks have been performed for a Helm Chart bump.
---

# Bumping Helm Chart

## Instructions

1. Ensure the chart version in [deploy/charts/operator/Chart.yaml](deploy/charts/operator/Chart.yaml) is updated based on what you're told
2. Ensure the same version change to the badge [deploy/charts/operator/README.md](deploy/charts/operator/README.md)
3. Ensure the appVersion in [deploy/charts/operator/Chart.yaml](deploy/charts/operator/Chart.yaml) matches the version of the image that is being bumped to
4. Go to the `.pre-commit-config.yaml` in the root of this repo and run the `helm-docs` hook command specifically with the args found in the file, nothing else. Do not run the `pre-commit` command as you will not have access to the binary. You do have access to the `helm-docs` binary so only run the `helm-docs` command with the args found in the `./pre-commit.yaml` file.
5. Please make sure you do not format the files at all before you commit, just run the `helm-docs` command and commit what is output.

## Best practices

- Use present tense
- Explain what and why, not how
