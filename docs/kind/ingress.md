## Setting up Ingress in a Local Kind Cluster

This document walks through setting up Ingress in a local Kind cluster. There are many examples of how to do this online but the intention of this document is so that when writing future ToolHive content, we can refer back to this guide when needing to setup Ingress in a local Kind cluster without polluting future content with the additional steps.

### Prerequisites

- A [kind](https://kind.sigs.k8s.io/) cluster running locally. Follow our [Setup a Local Kind Cluster](./setup-kind-cluster.md) to do this.
- Optional: [Task](https://taskfile.dev/installation/) to run automated steps with a cloned copy of the ToolHive repository
  (`git clone https://github.com/stacklok/toolhive`)

### Install Nginx Ingress Controller

To install the Nginx Ingress Controller, run the following:

```bash
$ kubectl apply -f https://kind.sigs.k8s.io/examples/ingress/deploy-ingress-nginx.yaml
```

There are [known issues](https://github.com/kubernetes/ingress-nginx/issues/5968#issuecomment-849772666) around inconsistencies between the secret and the webhook `caBundle` resulting in the Nginx Ingress Controller not being fully running and operational.

To fix these inconsistencies run:

```bash
$ CA=$(kubectl -n ingress-nginx get secret ingress-nginx-admission -ojsonpath='{.data.ca}')
$ kubectl patch validatingwebhookconfigurations ingress-nginx-admission --type='json' --patch='[{"op":"add","path":"/webhooks/0/clientConfig/caBundle","value":"'$CA'"}]'
```

We should now be able to confirm that the Nginx Ingress Controller is running and healthy by running:

```bash
$ kubectl get --namespace=ingress-nginx pod --selector=app.kubernetes.io/instance=ingress-nginx,app.kubernetes.io/component=controller
NAME                                       READY   STATUS    RESTARTS   AGE
ingress-nginx-controller-76666fb69-5bshr   1/1     Running   0          2m41s
```

Now, although the Nginx Ingress Controller is running, we need to hook with an IP so we can access it from our local terminal. Automatically, this won't be possible by default, as there is nothing to provide an ExternalIP.

To confirm there is no IP, run:

```bash
$ kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

Follow the next section to learn how to assign an ExternalIP to an Ingress Controller in Kind.

### Run a Local Kind Load Balancer

When running local Kind cluster, the issue is normally being able to run a Load Balancer that assigns an IP to an Ingress Controllers. In the Cloud, this functionality is provided by the Cloud Load Balancers (AWS LB etc). However, the Kind authors have been kind enough (pun intended) to provide a local Kind Load Balancer called [`cloud-provider-kind`](https://github.com/kubernetes-sigs/cloud-provider-kind). This acts as a small LoadBalancer to assign IPs to Ingress Controllers in a Kind cluster - it essentially mimics the functionality of a Cloud provider's Load Balancer.

To install and run, follow the [install documentation](https://github.com/kubernetes-sigs/cloud-provider-kind?tab=readme-ov-file#install) found on the Github repository for your preferred method of installation.

After following the documentation, it should now be installed and running and quickly detect that it needs to provide an IP address to our pending Ingress Controllers in our local Kind cluster.

To confirm that it has provided an IP address, you should now see an IP returned when you run:

```bash
$ kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

### Test Nginx Ingress Controller and Kind Load Balancer Setup

After following the two previous sections, we should now be able to confirm if we can connect to the Ingress Controller with our local terminal. Inside of a local terminal run:

```bash
$ LB_IP=$(kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
$ curl -I $LB_IP/healthz
HTTP/1.1 200 OK
Date: Wed, 30 Apr 2025 12:34:43 GMT
Content-Type: text/html
Content-Length: 0
Connection: keep-alive
```

If you receive an `OK` response, then you have successfully confirmed that you have an Ingress setup working for your cluster. 

To add Ingress for your applications, this can be done using the standard `Ingress` resource.

We won't be applying it as its beyond the scope of this document, but the below is an example:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: mcp-fetch-ingress
  namespace: toolhive-system
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  ingressClassName: nginx
  rules:
  - http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: mcp-fetch-proxy
            port:
              number: 8080
```

### Ingress with a Local Hostname

If you prefer to use a friendly hostname instead of an IP address, modify your `/etc/hosts` file to include a mapping for the load balancer IP.

This example creates the hostname `my-kind-cluster.dev`:

```bash
$ LB_IP=$(kubectl get svc/ingress-nginx-controller -n ingress-nginx -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
$ sudo sh -c "echo '$LB_IP my-kind-cluster.dev' >> /etc/hosts"
```

Now, when you curl that endpoint, it should connect as it did with the IP:

```bash
$ curl -I my-kind-cluster.dev/healthz
HTTP/1.1 200 OK
Date: Wed, 30 Apr 2025 12:37:16 GMT
Content-Type: text/html
Content-Length: 0
Connection: keep-alive
```

### Optional: Setup Ingress with Task

We have also automated the installation of the Nginx Ingress Controller using a Task. 

To use, run:

```bash
$ task kind-ingress-setup
```

It will install the Nginx Ingress Controller and fix the secret inconsistencies. It does nothing with the `cloud-provider-kind` Load Balancer, so you will still need to run that yourself. But by the end of the task run, the controller will be waiting for an assigned IP.