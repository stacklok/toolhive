# Setting up Ingress on a Local Kind Cluster

> Disclaimer: ChrisB to refine when back home.

Prereqs:
- have a local kind cluster running and working, and running an MCP server with the proxy workload fronting it

Run the local Kind Go binary that acts as a small LoadBalancer that gives IPs to ingress controllers inside of the cluster. This mimicks the behaviour of Cloud LBs

```
go install sigs.k8s.io/cloud-provider-kind@latest
sudo ~/go/bin/cloud-provider-kind
```

Install the Nginx controller
```
kubectl apply -f https://kind.sigs.k8s.io/examples/ingress/deploy-ingress-nginx.yaml
```

When the ingress controller is running, you should have an external IP set for it. Take not of this IP.

Add the following Ingress yaml that points to your vibetool service:
```yaml
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
spec:
  ingressClassName: nginx
  rules:
  - http:
      paths:
      - pathType: Prefix
        path: /sse
        backend:
          service:
            name: vibetool
            port:
              number: 8080
      - pathType: Prefix
        path: /messages
        backend:
          service:
            name: vibetool
            port:
              number: 8080
```

Now, you _should_ be able to curl the endpoint via `curl http://$EXTERNAL_IP/sse` and see a connection.

## Chris To Do
- adds the docs for the addition of a host name to make it look more ingressy, this avoids the IP requirement.g