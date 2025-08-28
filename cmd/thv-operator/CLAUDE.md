# Claude.md

This document will explain all necessary information for Claude to understand the ToolHive Operator.

## CustomResourceDefinitions

The CRD's are in `cmd/thv-operator/api/v1alpha1/`

After modifying the CRDs, the following needs to be run:
    - `task operator-generate`
    - `task operator-manifests`
    - `task crdref-gen` (it is important to run this command inside `cmd/thv-operator` as the current directory)

When committing a change that changes CRDs, it is important to bump the chart version as described in the [CLAUDE.md](../../deploy/charts/operator-crds/CLAUDE.md#bumping-crd-chart) doc for the CRD Helm Chart.

## OpenTelemetry (OTEL) Stack for Testing

When you have been asked to stand up an OTEL stack to test ToolHives integration inside of Kubernetes, you will need to perform the following tasks inside of the cluster that you have been instructed to use.

- Install the [`kube-prometheus-stack`](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) using Helm
- Install the [OTEL Collector](https://opentelemetry.io/docs/platforms/kubernetes/helm/collector/) using Helm

## Keycloak Development Setup

```bash
task keycloak:install-operator    # Install Keycloak operator
task keycloak:deploy-dev         # Deploy Keycloak and setup ToolHive realm  
task keycloak:get-admin-creds    # Get admin credentials
task keycloak:port-forward       # Access admin UI at http://localhost:8080
```
