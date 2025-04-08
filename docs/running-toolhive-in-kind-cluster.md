# Run ToolHive in Kubernetes using kind

This guide walks you through deploying ToolHive in a local kind cluster with an
example MCP server.

## Prerequisites

- A [kind](https://kind.sigs.k8s.io/) cluster running locally
  (`kind create cluster`)
- [`ko`](https://ko.build/install/) to build the ToolHive image
- [Task](https://taskfile.dev/installation/) to run automated steps
- A copy of the ToolHive repository
  (`git clone https://github.com/StacklokLabs/toolhive`)

## Deploy an MCP server into a kind cluster

To simplify the deployment of an MCP server, you can use the `test-k8s-apply`
task. This task performs the following actions:

- Builds the ToolHive container image
- Outputs the local `kind` cluster configuration to a local file
- Loads the ToolHive image into the kind cluster
- Applies example [Kubernetes manifests](../deploy/k8s/thv.yaml) for the
  [Fetch MCP server](https://github.com/modelcontextprotocol/servers/tree/main/src/fetch)
- Creates a RoleBinding for the ToolHive container, allowing it to create
  resources
- Applies and configures the Nginx Ingress controller manifests

To run this task, navigate to the root of this repository and execute
`task test-k8s-apply`. Once it completes, you should have a local kind cluster
with a deployed MCP server. To access it, follow the next section to set up a
simple ingress controller.

## Ingress configuration

You now have a local kind cluster with an MCP server, ToolHive proxy, and an
Nginx ingress controller awaiting an ExternalIP.

To assign an IP to the ingress controller, run
[`cloud-provider-kind`](https://github.com/kubernetes-sigs/cloud-provider-kind),
a tool that acts as a small LoadBalancer to assign IPs to ingress controllers in
the kind cluster. This binary mimics the functionality of a cloud provider's
load balancer capabilities for local kind setups.

In a new terminal, run the following commands and keep the terminal open:

```shell
# Linux / macOS with Go installed:
go install sigs.k8s.io/cloud-provider-kind@latest
sudo ~/go/bin/cloud-provider-kind

# macOS with Homebrew:
brew install cloud-provider-kind
sudo cloud-provider-kind
```

After a few moments, the ingress controller should be running, and the service
should have an external IP assigned. Run the following command to retrieve the
IP and store it in a variable. Then, curl the MCP server endpoint to verify the
connection:

```shell
$ LB_IP=$(kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
$ curl $LB_IP/sse
event: endpoint
data: http://172.20.0.3/messages?session_id=637d766e-354a-45b6-bc91-e153a35bc49f
```

### Ingress with a local hostname

If you prefer to use a friendly hostname instead of an IP address, modify your
`/etc/hosts` file to include a mapping for the load balancer IP. This example
creates the hostname `mcp-server.dev`:

```shell
sudo sh -c "echo '$LB_IP mcp-server.dev' >> /etc/hosts"
```

Now, when you curl that endpoint, it should connect as it did with the IP:

```shell
$ curl mcp-server.dev/sse
event: endpoint
data: http://mcp-server.dev/messages?session_id=337e4d34-5fb0-4ccc-9959-fc382d5b4800
```
