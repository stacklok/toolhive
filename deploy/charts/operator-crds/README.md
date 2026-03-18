# ToolHive Operator CRDs Helm Chart

![Version: 0.12.2](https://img.shields.io/badge/Version-0.12.2-informational?style=flat-square)
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

A Helm chart for installing the ToolHive Operator CRDs into Kubernetes.

---

ToolHive Operator CRDs

## TL;DR

```console
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds
```

## Prerequisites

- Kubernetes 1.25+
- Helm 3.10+ minimum, 3.14+ recommended

## Usage

### Installing from the Chart

Install one of the available versions:

```shell
helm upgrade -i <release_name> oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds --version=<version>
```

> **Tip**: List all releases using `helm list`

### Uninstalling the Chart

To uninstall/delete the `toolhive-operator-crds` deployment:

```console
helm uninstall <release_name>
```

## Why CRDs in templates/?

Helm does not upgrade CRDs placed in the `crds/` directory during `helm upgrade` operations. This is a [known Helm limitation](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/#some-caveats-and-explanations) to prevent accidental data loss. As a result, users running `helm upgrade` would silently have stale CRDs.

To ensure CRDs are upgraded alongside the chart, this chart places CRDs in `templates/` with Helm conditionals. This follows the pattern used by several popular projects.

However, placing CRDs in `templates/` means they would be deleted when the Helm release is uninstalled, which could result in data loss. To prevent this, CRDs are annotated with `helm.sh/resource-policy: keep` by default (controlled by `crds.keep`). This ensures CRDs persist even after uninstalling the chart.

## Important: Namespace Consistency

When installing this chart, Helm stamps all CRDs with a `meta.helm.sh/release-namespace` annotation set to the namespace used at install time. This annotation **cannot be changed** by subsequent `helm upgrade` commands targeting a different namespace.

You are free to install this chart in any namespace, but you **must use the same namespace consistently** for all future upgrades. If you plan to install the operator chart in `toolhive-system`, install the CRD chart there too:

```shell
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds -n toolhive-system --create-namespace
```

### Migrating from a Different Namespace

If you previously installed the CRD chart without specifying a namespace (defaulting to `default`) and now want to upgrade using a different namespace, you will see an error like:

```
Error: invalid ownership metadata; annotation validation error:
key "meta.helm.sh/release-namespace" must equal "toolhive-system": current value is "default"
```

To fix this, patch the ownership annotations on all CRDs to match your desired namespace:

```shell
for crd in $(kubectl get crd -o name | grep toolhive.stacklok.dev); do
  kubectl annotate "$crd" meta.helm.sh/release-namespace=<target-namespace> --overwrite
done
```

This is a one-time operation. After patching, future upgrades will work as long as the same namespace is used consistently.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| crds | object | `{"install":{"registry":true,"server":true,"virtualMcp":true},"keep":true}` | CRD installation configuration |
| crds.install | object | `{"registry":true,"server":true,"virtualMcp":true}` | Feature flags for CRD groups |
| crds.install.registry | bool | `true` | Install Registry CRDs (mcpregistries) |
| crds.install.server | bool | `true` | Install Server CRDs (mcpservers, mcpremoteproxies, mcptoolconfigs, mcpgroups) |
| crds.install.virtualMcp | bool | `true` | Install VirtualMCP CRDs (virtualmcpservers, virtualmcpcompositetooldefinitions) |
| crds.keep | bool | `true` | Whether to add the "helm.sh/resource-policy: keep" annotation to CRDs When true, CRDs will not be deleted when the Helm release is uninstalled |

