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

## Chris To Do
- adds the docs for the addition of a host name to make it look more ingressy, this avoids the IP requirement.