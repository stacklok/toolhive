# Claude.md

This document will contain vital pieces of information for Claude to better understand how to do things around the Operator Helm Chart in the codebase.

## Bumping Operator Chart
When you are asked to bump the Operator Helm chart, you will need to do the following:
- Change the Chart Version in the Chart.yaml to the version you've been asked to bump it to
- Also make this change to the version in the README.md (the Chart version is also in a badge)
- Bump the `appVersion` in the Chart.yaml to the version that the Operator Image is being bumped too
- Run `pre-commit run --all-files` to auto-generate the docs with the new updated versions 