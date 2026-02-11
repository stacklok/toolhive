---
name: deploy-otel
description: Deploy the OpenTelemetry observability stack (Prometheus, Grafana, OTEL Collector) to a Kind cluster for testing toolhive telemetry. Use when you need to set up monitoring, metrics collection, or observability infrastructure.
allowed-tools: Bash, Read
---

# Deploy OTEL Observability Stack

Deploy a complete OpenTelemetry observability stack to a Kind cluster for testing ToolHives telemetry capabilities.

## Steps

### 1. Verify Prerequisites

Check that required tools are installed:

```bash
echo "Checking prerequisites..."
command -v kind >/dev/null 2>&1 || { echo "ERROR: kind is not installed"; exit 1; }
command -v helm >/dev/null 2>&1 || { echo "ERROR: helm is not installed"; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is not installed"; exit 1; }
echo "All prerequisites met."
```

### 2. Create Kind Cluster

Create the Kind cluster if it doesn't exist:

```bash
CLUSTER_NAME="toolhive"

if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "Kind cluster '${CLUSTER_NAME}' already exists"
else
  echo "Creating Kind cluster '${CLUSTER_NAME}'..."
  kind create cluster --name ${CLUSTER_NAME}
fi

# Export kubeconfig
kind get kubeconfig --name ${CLUSTER_NAME} > kconfig.yaml
echo "Kubeconfig written to kconfig.yaml"
```

### 3. Add Helm Repositories

```bash
echo "Adding Helm repositories..."
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
echo "Helm repositories updated."
```

### 4. Install Prometheus/Grafana Stack

```bash
echo "Installing kube-prometheus-stack..."
helm upgrade -i kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  -f examples/otel/prometheus-stack-values.yaml \
  -n monitoring --create-namespace \
  --kubeconfig kconfig.yaml \
  --wait --timeout 5m

echo "Prometheus/Grafana stack installed."
```

### 5. Install Tempo for Distributed Tracing

```bash
echo "Installing Grafana Tempo..."
helm upgrade -i tempo grafana/tempo \
  -f examples/otel/tempo-values.yaml \
  -n monitoring \
  --kubeconfig kconfig.yaml \
  --wait --timeout 3m

echo "Grafana Tempo installed."
```

### 6. Install OpenTelemetry Collector

```bash
echo "Installing OpenTelemetry Collector..."
helm upgrade -i otel-collector open-telemetry/opentelemetry-collector \
  -f examples/otel/otel-values.yaml \
  -n monitoring \
  --kubeconfig kconfig.yaml \
  --wait --timeout 3m

echo "OpenTelemetry Collector installed."
```

### 7. Verify Deployment

```bash
echo "Verifying deployment..."
kubectl get pods -n monitoring --kubeconfig kconfig.yaml
```

### 8. Display Access Instructions

```bash
cat <<'EOF'

=== OTEL Stack Deployment Complete ===

To access the UIs, run these port-forward commands:

  # Grafana (admin / admin)
  kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3000:3000 --kubeconfig kconfig.yaml

  # Prometheus
  kubectl port-forward -n monitoring svc/kube-prometheus-stack-prometheus 9090:9090 --kubeconfig kconfig.yaml

EOF
```

## Troubleshooting

If Helm installations fail due to incompatible values, it may be because the Helm charts have been updated and our `values.yaml` files are no longer compatible.

**Chart Documentation:**
- OpenTelemetry Collector: https://github.com/open-telemetry/opentelemetry-helm-charts/tree/main/charts/opentelemetry-collector
- Prometheus Stack: https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack
- Tempo: https://github.com/grafana/helm-charts/tree/main/charts/tempo

**If you encounter issues:**
1. Check the chart's `values.yaml` for schema changes in the versions of the Charts we are using
2. Compare with our values files in `examples/otel/`
3. Create an issue at: https://github.com/stacklok/toolhive/issues describing what the issue is and recommend a fix

## What This Deploys

| Component | Description |
|-----------|-------------|
| Prometheus | Metrics storage, scrapes OTEL collector on port 8889 |
| Grafana | Visualization dashboards (admin/admin) |
| Tempo | Distributed tracing backend, receives traces from OTEL Collector |
| OTEL Collector | Receives OTLP metrics/traces, exports to Prometheus and Tempo |

## Cleanup

To remove everything:

```bash
task kind-destroy
```

Or manually:

```bash
kind delete cluster --name toolhive
rm -f kconfig.yaml
```
