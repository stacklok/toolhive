# Deploying MCP Server With Operator

The [ToolHive Kubernetes Operator](../../cmd/thv-operator/README.md) manages MCP (Model Context Protocol) servers in Kubernetes clusters. It allows you to define MCP servers as Kubernetes resources and automates their deployment and management.

## Prerequisites

- Kind cluster with the [ToolHive Operator installed](./deploying-toolhive-operator.md)

## Deploy MCP Server

With the ToolHive Operator running, you can deploy an MCP server into the cluster by running the following:

```bash
$ kubectl apply -f https://raw.githubusercontent.com/stacklok/toolhive/main/examples/operator/mcp-servers/mcpserver_fetch.yaml
```

You should now be able to see the MCP server pods being created/running:
```bash
$ kubectl get pods -n toolhive-system
```