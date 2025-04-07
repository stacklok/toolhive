# Setting up Ingress on a Local Kind Cluster

> Disclaimer: ChrisB to refine when back home.

Prerequisites:
- Have a local Kind Cluster running
- Have an MCP server with the proxy workload fronting it (this can be achieved by running the `test-k8s-apply` Taskfile task. `task test-k8s-apply`)

There should now be a local Kind cluster with an MCP server running and a ToolHive proxy running, in addition to an Nginx Ingress controller running and pending an ExternalIP.

To give the ingress controller an IP, we will run a local Kind Go binary that acts as a small LoadBalancer that gives IPs to ingress controllers inside of the Kind Cluster. This binary mimicks Cloud Providers LoadBalancers functionality for local Kind setups.

```
go install sigs.k8s.io/cloud-provider-kind@latest
sudo ~/go/bin/cloud-provider-kind
```

After a few moments, the ingress controller should be running and the service should have an external IP set for it. Run the below to get the IP and store it in a variable, and then curl the MCP server endpoint to see a connection.

```
$ LB_IP=$(kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
$ curl $LB_IP/sse
event: endpoint
data: http://172.20.0.3/messages?session_id=637d766e-354a-45b6-bc91-e153a35bc49f
```

## Ingress with Local Hostname

In order to avoid using of the IP you can use a hostname instead of preferred. This can be achieved by modifying the `/etc/hosts` file to include a mapping for the load balancer IP to a friendly hostname.

```
sudo sh -c "echo '$LB_IP mcp-server.dev' >> /etc/hosts"
```

Now when you curl that endpoint, it should connect as it did with the IP

```
$ curl mcp-server.dev/sse
event: endpoint
data: http://mcp-server.dev/messages?session_id=337e4d34-5fb0-4ccc-9959-fc382d5b4800
```
