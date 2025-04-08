# Running ToolHive Inside a Local Kubernetes Kind Cluster With Ingress

## Prerequisites:
- Have a local Kind Cluster running (`kind create cluster`)
- Have [`ko`](https://ko.build/install/) installed (for building the ToolHive image locally)
- Have [Taskfile](https://taskfile.dev/installation/) installed (to run automated steps)
- Git Clone the ToolHive repository

## Deploy an MCP Server into Kind Cluster

To make the deployment of an MCP server easier, we have setup the `test-k8s-apply` task. It will go through and perform the following actions:
- Builds the ToolHive container image
- Outputs the local `kind` cluster config into a local file
- Loads ToolHive image into Kind
- Applies example [Kubernetes manifests](../deploy/k8s/thv.yaml) of a Fetch MCP Server
- Creates a RoleBinding for the ToolHive container to be able to create resources
- Applies and configures the Nginx Controller manifests for Ingress

To run this task, in the root of this repository, run `task test-k8s-apply`. Once it has finished, you should have a local Kind cluster with a deployed MCP server. To access it, you can follow the next section of setting up a simple Ingress for it.

## Setting Up Ingress

There should now be a local Kind cluster with an MCP server running and a ToolHive proxy running, in addition to an Nginx Ingress controller running and pending an ExternalIP.

To give the ingress controller an IP, we will run a local Kind Go binary that acts as a small LoadBalancer that gives IPs to ingress controllers inside of the Kind Cluster. This binary mimicks Cloud Providers LoadBalancers functionality for local Kind setups.

```shell
go install sigs.k8s.io/cloud-provider-kind@latest
sudo ~/go/bin/cloud-provider-kind
```

After a few moments, the ingress controller should be running and the service should have an external IP set for it. Run the below to get the IP and store it in a variable, and then curl the MCP server endpoint to see a connection.

```shell
$ LB_IP=$(kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
$ curl $LB_IP/sse
event: endpoint
data: http://172.20.0.3/messages?session_id=637d766e-354a-45b6-bc91-e153a35bc49f
```

### Ingress with Local Hostname

In order to avoid using of the IP you can use a hostname instead of preferred. This can be achieved by modifying the `/etc/hosts` file to include a mapping for the load balancer IP to a friendly hostname.

```shell
sudo sh -c "echo '$LB_IP mcp-server.dev' >> /etc/hosts"
```

Now when you curl that endpoint, it should connect as it did with the IP

```shell
$ curl mcp-server.dev/sse
event: endpoint
data: http://mcp-server.dev/messages?session_id=337e4d34-5fb0-4ccc-9959-fc382d5b4800
```
