# Setup a Local Kind Cluster

This document walks through setting up a local Kind cluster. There are many examples of how to do this online but the intention of this document is so that when writing future ToolHive content, we can refer back to this guide when needing to setup a local Kind cluster without polluting future content with the additional steps.

## Prerequisites

- Local container runtime is installed ([Docker](https://www.docker.com/), [Podman](https://podman.io/) etc)
- Optional: [Task](https://taskfile.dev/installation/) to run automated steps with a cloned copy of the ToolHive repository
  (`git clone https://github.com/StacklokLabs/toolhive`)

## Installing Kind

You can install the Kind binary in a variety of ways. The official [install documentation](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) has more detail on how to do this.

### Setup a Local Kind Cluster

For testing ToolHive on a local Kind Cluster, you can get by with just a single node cluster. Kind will create a single node cluster (control plane node only) by default by utilising the container runtime to create the containers.

You can setup the cluster using two methods:
- [Manual](#manual-setup-setup--destroy-a-local-kind-cluster)
- [Automated](#automatic-setup-setup--destroy-a-local-kind-cluster) (requires [Task](https://taskfile.dev/installation/) to be installed)

### Manual Setup: Setup & Destroy a Local Kind Cluster

You can perform Kind operations manually by following the sections below.

#### Setup

To setup a Local Kind Cluster manually, run:

```shell
$ kind create cluster --name kind
```

#### Getting Kind Config

We recommend having a dedicated kubeconfig file to keep things isolated from your other cluster configs (even though Kind adds it to `~/.kube/config` automatically).

To do this, run:

```shell
$ kind get kubeconfig > kconfig.yaml
```

This will output the kind cluster config to a file called `kconfig.yaml` in the directory of which the command is ran in. This file is added to the `.gitignore` of this repository, so there is no worry about checking it in.

#### Destroy

To destroy a local Kind cluster, run:

```shell
$ kind delete clusters kind
```

### Automatic Setup: Setup & Destroy a Local Kind Cluster

To automate the creation/destruction of a local Kind cluster, we have added a Task into the [`Taskfile.yml`](https://github.com/StacklokLabs/toolhive/blob/main/Taskfile.yml) in the root of the ToolHive repository.

#### Setup

To setup a Local Kind Cluster using Task, run:

```shell
$ task kind-setup
```

This will create a single node Kind cluster and it will output the kubeconfig into the `kconfig.yaml` file. This file is added to the `.gitignore` of this repository, so there is no worry about checking it in.

#### Destroy

To destroy a local Kind cluster using Task, run:

```shell
$ task kind-destroy
```

This will destroy the Kind cluster, as well as removing the `kconfig.yaml` kubeconfig file.
