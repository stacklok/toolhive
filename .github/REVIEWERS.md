# Reviewers (natural-language ownership)

This file is the **source of truth for reviewer routing**. On every `pull_request`,
an automated GitHub Action feeds the PR diff plus the ownership rules below to an
LLM, which requests the appropriate reviewers.

A few things to understand before editing:

- **This routing is ADVISORY.** Reviewers picked here are *requested*, but they do
  not by themselves block merge. The hard merge **gate** is deterministic and lives
  in `.github/CODEOWNERS`.
- **The CONDITION sentences are the whole point.** They let the LLM be more precise
  than a path glob — scoping reviewers to a sub-feature, or skipping reviewers for
  low-risk churn (generated code, doc regen, comment- or test-only edits). Prefer
  adding conditions over adding people.
- **Edits to this file are themselves gated** by `.github/CODEOWNERS` (it lives under
  `.github/`), so routing rules cannot be changed without a gating review.

Handles are case-sensitive and must match GitHub exactly (including the leading `@`).
The repo does not use GitHub teams, so all owners are individuals.

> **Scope (v1):** routing is intentionally limited to Kubernetes changes for now.
> All other paths fall through to the Default owner. New areas will be added once
> this has proven out.

---

## Default

Covers everything not matched by a more specific section below.

- **Required:** @JAORMX

---

## Kubernetes (operator, proxyrunner, charts)

Paths: `cmd/thv-operator/`, `cmd/thv-proxyrunner/`, `pkg/operator/`,
`deploy/charts/operator/`, `deploy/charts/operator-crds/`, `config/webhook/`,
`pkg/webhook/`, `pkg/k8s/`, `test/e2e/chainsaw/operator/`, `test/e2e/thv-operator/`,
`docs/operator/`

- **Required:** @ChrisJBurns @JAORMX @jerm-dro @jhrozek @tgrunnagle @rdimitrov @reyortiz3 @blkt

Conditions to keep review load sane on this large area:

- **Controller / reconcile-logic changes** (controllers under `cmd/thv-operator/`,
  `pkg/operator/`) **must always include @ChrisJBurns** as a required reviewer.
- If the PR is **only** regenerated CRD reference docs (`docs/operator/`, output of
  `task crdref-gen`) or regenerated CRD manifests / deepcopy with no controller-logic
  change, require just @ChrisJBurns @jerm-dro and skip the rest.
- For **chart-only** changes under `deploy/charts/` (templates/values, no Go), require
  @ChrisJBurns @jerm-dro @blkt. Do NOT request review on `Chart.yaml` version bumps —
  the release process owns those.
- For **webhook** changes (`config/webhook/`, `pkg/webhook/`) that affect
  admission/validation behavior, keep @ChrisJBurns @jhrozek @tgrunnagle.
- CRD-schema (API) changes require the full set.
- Comment-only or generated-mock-only (`task gen`) edits can drop to @ChrisJBurns @JAORMX.
