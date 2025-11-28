# ToolHive Operator CRDs Helm Chart

![Version: 0.0.70](https://img.shields.io/badge/Version-0.0.70-informational?style=flat-square)
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

### Skipping CRDs

By default, all CRDs are installed. You can selectively disable CRD groups based on your needs:

#### Skipping Server CRDs

To skip server-related CRDs (MCPServer, MCPExternalAuthConfig, MCPRemoteProxy, and ToolConfig):

```shell
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds \
  --set crds.install.server=false
```

**Important:** When server CRDs are not installed, you should also disable the server controllers in the operator:

```shell
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system --create-namespace \
  --set operator.features.server=false
```

#### Skipping Registry CRD

To skip the registry CRD (MCPRegistry):

```shell
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds \
  --set crds.install.registry=false
```

**Important:** When registry CRD is not installed, you should also disable the registry controller in the operator:

```shell
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system --create-namespace \
  --set operator.features.registry=false
```

#### Skipping Virtual MCP CRDs

To skip Virtual MCP CRDs (VirtualMCPServer, VirtualMCPCompositeToolDefinition, and MCPGroup):

```shell
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds \
  --set crds.install.virtualMCP=false
```

You can also combine this with disabling the registry CRD (see [Skipping Registry CRD](#skipping-registry-crd) section above) if you don't need registry features.

**Important:** When Virtual MCP CRDs are not installed, you should also disable the Virtual MCP controllers in the operator:

```shell
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system --create-namespace \
  --set operator.features.virtualMCP=false
```

If you also disabled the registry CRD, disable the registry controller as well (see the [Skipping Registry CRD](#skipping-registry-crd) section above).

This is useful for deployments that don't require Virtual MCP aggregation features. When `operator.features.virtualMCP=false`, the operator will skip setting up the VirtualMCPServer controller, MCPGroup controller, and their associated webhooks.

## Values

| Key | Type | Default | Description |
|-----|-------------|------|---------|
| crds.install.registry | bool | `true` | Install registry CRD (MCPRegistry). Users who only need server management without registry features can set this to false to skip installing the registry CRD. |
| crds.install.server | bool | `true` | Install server-related CRDs (MCPServer, MCPExternalAuthConfig, MCPRemoteProxy, and ToolConfig). Users who only need registry or aggregation features can set this to false to skip installing server management CRDs. |
| crds.install.virtualMCP | bool | `true` | Install Virtual MCP CRDs (VirtualMCPServer and VirtualMCPCompositeToolDefinition). Users who only need core MCP server management can set this to false to skip installing Virtual MCP aggregation features. |
