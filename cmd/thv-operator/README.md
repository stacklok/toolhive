# ToolHive Kubernetes Operator

The ToolHive Kubernetes Operator manages MCP (Model Context Protocol) servers in Kubernetes clusters. It allows you to define MCP servers as Kubernetes resources and automates their deployment and management.

This operator is built using [Kubebuilder](https://book.kubebuilder.io/), a framework for building Kubernetes APIs using Custom Resource Definitions (CRDs).

## Overview

The operator introduces a new Custom Resource Definition (CRD) called `MCPServer` that represents an MCP server in Kubernetes. When you create an `MCPServer` resource, the operator automatically:

1. Creates a Deployment to run the MCP server
2. Sets up a Service to expose the MCP server
3. Configures the appropriate permissions and settings
4. Manages the lifecycle of the MCP server

## Installation

### Prerequisites

- Kubernetes cluster (v1.19+)
- kubectl configured to communicate with your cluster

### Installing the Operator

1. Install the CRD:

```bash
kubectl apply -f deploy/operator/crds/toolhive.stacklok.dev_mcpservers.yaml
```

2. Create the operator namespace:

```bash
kubectl apply -f deploy/operator/namespace.yaml
```

3. Set up RBAC:

```bash
kubectl apply -f deploy/operator/rbac.yaml
kubectl apply -f deploy/operator/toolhive_rbac.yaml
```

4. Deploy the operator:

```bash
kubectl apply -f deploy/operator/operator.yaml
```

## Usage

### Creating an MCP Server

To create an MCP server, define an `MCPServer` resource and apply it to your cluster:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: fetch
spec:
  image: docker.io/mcp/fetch
  transport: stdio
  port: 8080
  permissionProfile:
    type: builtin
    name: network
  resources:
    limits:
      cpu: "100m"
      memory: "128Mi"
    requests:
      cpu: "50m"
      memory: "64Mi"
```

Apply this resource:

```bash
kubectl apply -f your-mcpserver.yaml
```

### Using Secrets

For MCP servers that require authentication tokens or other secrets:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: github
spec:
  image: docker.io/mcp/github
  transport: stdio
  port: 8080
  permissionProfile:
    type: builtin
    name: network
  secrets:
    - name: github-token
      key: token
      targetEnvName: GITHUB_PERSONAL_ACCESS_TOKEN
```

First, create the secret:

```bash
kubectl create secret generic github-token --from-literal=token=your-token
```

Then apply the MCPServer resource.

The `secrets` field has the following parameters:
- `name`: The name of the Kubernetes secret (required)
- `key`: The key in the secret itself (required)
- `targetEnvName`: The environment variable to be used when setting up the secret in the MCP server (optional). If left unspecified, it defaults to the key.

### Checking MCP Server Status

To check the status of your MCP servers:

```bash
kubectl get mcpservers
```

This will show the status, URL, and age of each MCP server.

For more details about a specific MCP server:

```bash
kubectl describe mcpserver <name>
```

## Configuration Reference

### MCPServer Spec

| Field | Description | Required | Default |
|-------|-------------|----------|---------|
| `image` | Container image for the MCP server | Yes | - |
| `transport` | Transport method (stdio or sse) | No | stdio |
| `port` | Port to expose the MCP server on | No | 8080 |
| `args` | Additional arguments to pass to the MCP server | No | - |
| `env` | Environment variables to set in the container | No | - |
| `volumes` | Volumes to mount in the container | No | - |
| `resources` | Resource requirements for the container | No | - |
| `secrets` | References to secrets to mount in the container | No | - |
| `permissionProfile` | Permission profile configuration | No | - |

### Permission Profiles

Permission profiles can be configured in two ways:

1. Using a built-in profile:

```yaml
permissionProfile:
  type: builtin
  name: network  # or "none"
```

2. Using a ConfigMap:

```yaml
permissionProfile:
  type: configmap
  name: my-permission-profile
  key: profile.json
```

The ConfigMap should contain a JSON permission profile.

## Examples

See the `deploy/operator/samples/` directory for example MCPServer resources.

## Development

### Building the Operator

To build the operator:

```bash
go build -o bin/thv-operator cmd/thv-operator/main.go
```

### Running Locally

For development, you can run the operator locally:

```bash
go run cmd/thv-operator/main.go
```

This will use your current kubeconfig to connect to the cluster.

### Using Kubebuilder

This operator is scaffolded using Kubebuilder. If you want to make changes to the API or controller, you can use Kubebuilder commands to help you.

#### Prerequisites

- Install Kubebuilder: https://book.kubebuilder.io/quick-start.html#installation

#### Common Commands

Generate CRD manifests:
```bash
kubebuilder create api --group toolhive --version v1alpha1 --kind MCPServer
```

Update CRD manifests after changing API types:
```bash
make manifests
```

Run the controller locally:
```bash
make run
```

#### Project Structure

The Kubebuilder project structure is as follows:

- `api/v1alpha1/`: Contains the API definitions for the CRDs
- `controllers/`: Contains the reconciliation logic for the controllers
- `config/`: Contains the Kubernetes manifests for deploying the operator
- `PROJECT`: Kubebuilder project configuration file

For more information on Kubebuilder, see the [Kubebuilder Book](https://book.kubebuilder.io/).