# ToolHive Operator CRDs Helm Chart

![Version: 0.0.73](https://img.shields.io/badge/Version-0.0.73-informational?style=flat-square)
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

## Values

| Key | Type | Default | Description |
|-----|-------------|------|---------|
| crds | object | `{"enableRegistry":true,"enableServer":true,"enableVirtualMcp":true,"install":true,"keep":true}` | CRD installation configuration |
| crds.enableRegistry | bool | `true` | Whether to install Registry CRDs (mcpregistries) |
| crds.enableServer | bool | `true` | Whether to install Server CRDs (mcpservers, mcpexternalauthconfigs, mcpremoteproxies, mcptoolconfigs, mcpgroups) |
| crds.enableVirtualMcp | bool | `true` | Whether to install VirtualMCP CRDs (virtualmcpservers, virtualmcpcompositetooldefinitions) |
| crds.install | bool | `true` | Whether to install CRDs as part of the Helm release |
| crds.keep | bool | `true` | Whether to add the "helm.sh/resource-policy: keep" annotation to CRDs When true, CRDs will not be deleted when the Helm release is uninstalled |

