# Setup a Local Kind Cluster

This document walks through setting up a local Kind cluster. There are many examples of how to do this online but the intention of this document is so that when writing future ToolHive content, we can refer back to this guide when needing to setup a local Kind cluster without polluting future content with the additional steps.

## Prerequisites

- Local container runtime is installed ([Docker](https://www.docker.com/), [Podman](https://podman.io/) etc)
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) is installed
- Optional: [Task](https://taskfile.dev/installation/) to run automated steps with a cloned copy of the ToolHive repository
  (`git clone https://github.com/stacklok/toolhive`)

## TL;DR

To setup a local Kind Cluster using [Task](https://taskfile.dev/installation/), clone the ToolHive repo and run the below.

### Setup

```bash
task kind-setup
```

This will create a single node Kind cluster and it will output the kubeconfig into the `kconfig.yaml` file. This file is added to the `.gitignore` of this repository, so there is no worry about checking it in.

### Destroy

To destroy a local Kind cluster using Task, run:

```bash
task kind-destroy
```

This will destroy the Kind cluster, as well as removing the `kconfig.yaml` kubeconfig file.

## Manual Setup: Setup & Destroy a Local Kind Cluster

You can perform Kind operations manually by following the sections below.

### Setup

To setup a Local Kind Cluster manually, run:

```bash
kind create cluster --name toolhive
```

### Getting Kind Config

We recommend having a dedicated kubeconfig file to keep things isolated from your other cluster configs (even though Kind adds it to `~/.kube/config` automatically).

To do this, run:

```bash
kind get kubeconfig --name toolhive > kconfig.yaml
```

This will output the kind cluster config to a file called `kconfig.yaml` in the directory of which the command is ran in. This file is added to the `.gitignore` of this repository, so there is no worry about checking it in.

### Destroy

To destroy a local Kind cluster, run:

```bash
kind delete clusters toolhive
```
