# Deploying ToolHive Kubernetes Operator

The [ToolHive Kubernetes Operator](../../cmd/thv-operator/README.md) manages MCP (Model Context Protocol) servers in Kubernetes clusters. It allows you to define MCP servers as Kubernetes resources and automates their deployment and management.

## Prerequisites

- kubectl configured to communicate with your cluster
- Kind installed
- Optional: [Task](https://taskfile.dev/installation/) to run automated steps with a cloned copy of the ToolHive repository
  (`git clone https://github.com/stacklok/toolhive`)


## TL;DR

To setup a kind cluster and deploy the Operator, we have created a Task so that you can do this with one command. You will need to clone this repository to run the command.

Run:
```bash
task kind-with-toolhive-operator
```

This will create the kind cluster, install an nginx ingress controller and then install the latest built ToolHive Operator image.

## Installation

## Installing the Operator Into a New Kind Cluster

### Automated via Task

To setup a kind cluster and deploy the Operator, we have created a Task so that you can do this with one command.

Run:
```bash
task kind-with-toolhive-operator
```

### Manually

Follow the [Kind Cluster setup](./setup-kind-cluster.md#manual-setup-setup--destroy-a-local-kind-cluster) guide.

Once the cluster is running, follow these steps:

1. Install the CRD:

```bash
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds
```

2. Deploy the operator:

```bash
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator -n toolhive-system --create-namespace
```

## Installing the Operator Into an Existing Kind Cluster

### Automated via Task

We have a dedicated Task that installs the operator into a cluster.

Run:

```bash
# If you want to install the latest built operator image from Github (recommended)
task operator-deploy-latest

# If you want to built the operator image locally and deploy it (only recommended if you're doing development around the Operator)
task operator-deploy-local
```

### Manually

1. Install the CRD:

```bash
helm upgrade -i toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds
```

2. Deploy the operator:

```bash
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator -n toolhive-system --create-namespace
```