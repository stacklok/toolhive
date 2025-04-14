# Kubernetes Deployment

This directory contains Kubernetes manifests for deploying toolhive in a Kubernetes cluster.

Note that this support is experimental as of now since we're still working on more thorough support.
If you're interested in contributing to this or want to see more extensive Kubernetes support, please reach out to us on our [Discord](https://discord.gg/stacklok).
We'd love to hear your feedback and suggestions!

## Files

- `thv.yaml`: Contains the StatefulSet and Service for the toolhive application
- `ingress.yaml`: Contains an Ingress resource for routing HTTP traffic to toolhive
- `rbac.yaml`: Contains RBAC resources (ServiceAccount, Role, RoleBinding) for namespace-scoped deployments
- `namespace.yaml`: Contains a Namespace resource for deploying toolhive in a dedicated namespace

## Deployment Options

### Default Namespace Deployment

For deploying toolhive in the default namespace:

```bash
kubectl apply -f rbac.yaml
kubectl apply -f thv.yaml
kubectl apply -f ingress.yaml  # Optional, if you need ingress
```

### Dedicated Namespace Deployment

For deploying toolhive in a dedicated namespace:

```bash
kubectl apply -f namespace.yaml
kubectl apply -f rbac.yaml -n toolhive-deployment
kubectl apply -f thv.yaml -n toolhive-deployment
kubectl apply -f ingress.yaml -n toolhive-deployment  # Optional, if you need ingress
```

## Customization

You may need to customize these manifests based on your specific requirements:

1. Update the image reference in `thv.yaml` if you're using a custom image
2. Modify resource limits and requests in `thv.yaml` based on your workload
3. Adjust the RBAC permissions in `rbac.yaml` if needed
4. Configure the Ingress resource in `ingress.yaml` to match your ingress controller and domain