# Reviewers (natural-language ownership)

This file is the **source of truth for reviewer routing**. On every `pull_request`,
an automated GitHub Action feeds the PR diff plus the rules below to an LLM, which
requests the reviewers those rules call for.

The whole point of this file is to be **smarter and smaller than `CODEOWNERS`**.
`CODEOWNERS` notifies everyone who *could* own a path; this file names only the
people who *actually need to look* at a specific kind of change. It exists to stop
the spam that broad `CODEOWNERS` globs create.

How it works:

- **There is no blanket reviewer list.** A reviewer is requested **only** when a
  specific instruction below matches the change. If nothing matches, the action
  requests **no one**.
- **This routing is ADVISORY.** Reviewers are *requested*, not required. The hard
  merge **gate** still lives in `.github/CODEOWNERS`, which catches anything this
  file does not route.
- **Write specific instructions, not lists.** Each rule should describe a *kind of
  change* and name the minimal set of people for it. Prefer narrow, targeted rules
  over adding more names.
- **Edits to this file are themselves gated** by `.github/CODEOWNERS` (it lives
  under `.github/`).

Handles are case-sensitive and must match GitHub exactly (including the leading `@`).
The repo does not use GitHub teams, so all owners are individuals.

> **Scope (v1):** routing is intentionally limited to Kubernetes changes for now.
> Any change that matches none of the rules below is left to `CODEOWNERS`.

---

## Kubernetes (operator, proxyrunner, charts)

Applies to changes under: `cmd/thv-operator/`, `cmd/thv-proxyrunner/`,
`pkg/operator/`, `deploy/charts/operator/`, `deploy/charts/operator-crds/`,
`config/webhook/`, `pkg/webhook/`, `pkg/k8s/`, `test/e2e/chainsaw/operator/`,
`test/e2e/thv-operator/`, `docs/operator/`.

Request a reviewer ONLY when one of these rules matches. If a Kubernetes change
matches none of them, request no one.

- **Controller or reconcile-logic change** — any change to controller logic under
  `cmd/thv-operator/` or `pkg/operator/` (reconcilers, watches, predicates,
  finalizers): request **@ChrisJBurns** and **@JAORMX**.
- **CRD API change** — changes to CRD types (`*_types.go`), `api/`, or generated
  CRD manifests/deepcopy that alter the API surface: request **@ChrisJBurns**.

Explicitly request **no reviewer** for:

- Pure CRD-reference doc regeneration (`docs/operator/`, the output of
  `task crdref-gen`) with no controller-logic change.
- `Chart.yaml` version bumps (the release process owns those).
- Generated-mock-only changes (`task gen`) with no hand-written logic change.
