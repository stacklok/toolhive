# OpenTelemetry Observability Stack

ToolHive provides comprehensive observability with metrics, distributed tracing, and logging through OpenTelemetry. This stack includes:

- **Metrics**: Prometheus for metrics collection and storage
- **Tracing**: Jaeger for distributed trace collection and analysis
- **Visualization**: Grafana with pre-configured dashboards
- **Collection**: OpenTelemetry Collector for telemetry aggregation

ToolHive will push OTEL telemetry data to a collector as well as expose a Prometheus `/metrics` endpoint when enabled. This document describes how to install and configure the complete observability stack for testing and development.

## ToolHive Tracing Configuration

**IMPORTANT**: By default, ToolHive has tracing **disabled** even when an observability stack is running. You must explicitly enable tracing using CLI flags or configuration files.

### Key Configuration Flags

The following flags control ToolHive's telemetry behavior:

- `--otel-tracing-enabled`: **Enable distributed tracing** (default: false)
- `--otel-metrics-enabled`: **Enable OTLP metrics export** (default: false)  
- `--otel-endpoint`: **OTLP endpoint URL** (required for OTLP export)
- `--otel-sampling-rate`: **Trace sampling rate** (default: 0.1, range: 0.0-1.0)
- `--otel-service-name`: **Service name for telemetry** (default: "toolhive-mcp-proxy")
- `--otel-headers`: **OTLP headers** in key=value format (e.g., x-honeycomb-team=your-api-key)
- `--otel-insecure`: **Use HTTP instead of HTTPS** for OTLP endpoint (default: false)
- `--otel-enable-prometheus-metrics-path`: **Enable /metrics endpoint** (default: false)

### Example Commands for Different Scenarios

#### 1. Development: Full Telemetry with Local Stack
Use this configuration when running the complete observability stack locally:

```bash
# Start an MCP server with full telemetry enabled
thv run github-pr-summary \
  --otel-tracing-enabled \
  --otel-metrics-enabled \
  --otel-endpoint http://otel-collector.monitoring.svc.cluster.local:4318 \
  --otel-sampling-rate 1.0 \
  --otel-enable-prometheus-metrics-path \
  --otel-service-name "toolhive-github-dev"
```

#### 2. Development: Local Testing with Jaeger Direct
Send traces directly to Jaeger for quick testing:

```bash
# Direct to Jaeger (requires port-forward: kubectl port-forward -n monitoring svc/jaeger-all-in-one-jaeger 14268:14268)
thv run fetch-content \
  --otel-tracing-enabled \
  --otel-endpoint http://localhost:14268/v1/traces \
  --otel-sampling-rate 1.0 \
  --otel-insecure
```

#### 3. Production: External Observability Platform
Send telemetry to external platforms like Honeycomb, DataDog, or New Relic:

```bash
# Example: Honeycomb
thv run document-processor \
  --otel-tracing-enabled \
  --otel-metrics-enabled \
  --otel-endpoint https://api.honeycomb.io \
  --otel-headers "x-honeycomb-team=your-api-key" \
  --otel-sampling-rate 0.1 \
  --otel-service-name "toolhive-prod-docs"

# Example: DataDog
thv run data-analyzer \
  --otel-tracing-enabled \
  --otel-metrics-enabled \
  --otel-endpoint https://trace-agent.datadoghq.com \
  --otel-headers "DD-API-KEY=your-api-key" \
  --otel-sampling-rate 0.05 \
  --otel-service-name "toolhive-prod-analytics"
```

#### 4. Metrics Only: Prometheus Scraping
Enable only Prometheus metrics without distributed tracing:

```bash
# Expose /metrics endpoint for Prometheus scraping
thv run log-analyzer \
  --otel-enable-prometheus-metrics-path \
  --proxy-port 8080

# Access metrics at: http://localhost:8080/metrics
```

#### 5. Configuration File Approach
You can also set these values in your ToolHive configuration file (`~/.toolhive/config.yaml`):

```yaml
otel:
  endpoint: "http://otel-collector.monitoring.svc.cluster.local:4318"
  tracingEnabled: true
  metricsEnabled: true
  samplingRate: 0.1
  serviceName: "toolhive-mcp-proxy"
  headers:
    x-custom-header: "value"
  insecure: false
  enablePrometheusMetricsPath: false
```

## Quick Setup Guide

To install the complete observability stack in the correct order:

### Prerequisites

Add the required Helm repositories:

```bash
# Add OpenTelemetry Helm repository
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts

# Add Prometheus community Helm repository  
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts

# Add Jaeger Helm repository
helm repo add jaegertracing https://jaegertracing.github.io/helm-charts

# Update repositories
helm repo update
```

### 1. Install Jaeger Tracing Backend

First, install Jaeger to collect and store distributed traces:

```bash
helm upgrade -i jaeger-all-in-one jaegertracing/jaeger -f jaeger-values.yaml -n monitoring --create-namespace
```

### 2. Install Prometheus/Grafana Stack

Install the monitoring stack with Jaeger pre-configured as a data source:

```bash
helm upgrade -i kube-prometheus-stack prometheus-community/kube-prometheus-stack -f prometheus-stack-values.yaml -n monitoring
```

### 3. Install OpenTelemetry Collector

Finally, install the OTEL collector to aggregate and forward telemetry data:

```bash
helm upgrade -i otel-collector open-telemetry/opentelemetry-collector -f otel-values.yaml -n monitoring
```

## Component Details

### OpenTelemetry Collector Configuration

The `otel-values.yaml` file configures the collector with:
- **Receivers**: OTLP (gRPC/HTTP) and Kubernetes stats
- **Processors**: Batch processing for efficiency
- **Exporters**: 
  - Jaeger for traces
  - Prometheus for metrics (both scraping and remote-write)
  - Debug output for troubleshooting

Key features:
- [Kubestats](https://opentelemetry.io/docs/platforms/kubernetes/collector/components/#kubeletstats-receiver) receiver enabled to collect pod/container metrics from the Kube API
- Exports traces to Jaeger via OTLP
- Exports metrics to Prometheus via both remote-write and scrape endpoint
- Batch processing to optimize telemetry data transmission

### Prometheus/Grafana Stack Configuration

The `prometheus-stack-values.yaml` file configures:
- **Prometheus**: Remote-write receiver enabled for OTLP metrics
- **Grafana**: Pre-configured with Prometheus and Jaeger data sources
- **Node Exporter**: System-level metrics collection
- **Kube State Metrics**: Kubernetes cluster state metrics

### Jaeger Tracing Backend Configuration  

The `jaeger-values.yaml` file configures Jaeger All-in-One deployment with:
- **In-memory storage**: Suitable for development (50,000 traces max)
- **OTLP support**: Native OpenTelemetry protocol receivers
- **Multi-protocol support**: Jaeger, Zipkin, and OTLP endpoints
- **Resource limits**: Configured for development workloads

## Accessing the Observability Stack

After installation, you can access the components:

### Grafana Dashboard
```bash
# Port-forward to access Grafana locally
kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3000:80

# Access at: http://localhost:3000
# Username: admin
# Password: admin (configured in values file)
```

### Jaeger UI
```bash
# Port-forward to access Jaeger UI locally  
kubectl port-forward -n monitoring svc/jaeger-all-in-one-jaeger 16686:16686

# Access at: http://localhost:16686
```

### Prometheus UI
```bash
# Port-forward to access Prometheus locally
kubectl port-forward -n monitoring svc/kube-prometheus-stack-prometheus 9090:9090

# Access at: http://localhost:9090
```

## Grafana Dashboards

In the [grafana-dashboards](./grafana-dashboards/) folder are pre-built dashboards for visualizing ToolHive metrics:

- `toolhive-mcp-grafana-dashboard-otel-scrape.json`: For Prometheus scraping setup
- `toolhive-mcp-grafana-dashboard-otel-remotewrite.json`: For Prometheus remote-write setup

### Importing Dashboards

You can import these dashboards through:
1. Grafana UI: Configuration → Data Sources → Import
2. Automatic sidecar discovery (if enabled)
3. Grafana provisioning configuration

## ToolHive Trace Verification and Debugging

### Verifying the Complete Trace Flow

To ensure traces flow from ToolHive → OpenTelemetry Collector → Jaeger, follow these validation steps:

#### 1. Verify ToolHive Telemetry Configuration

Check that ToolHive is configured to emit traces:

```bash
# Confirm telemetry flags are set correctly
thv run your-server --help | grep otel

# Example: Check if tracing is enabled in a running command
thv run test-server \
  --otel-tracing-enabled \
  --otel-endpoint http://otel-collector.monitoring.svc.cluster.local:4318 \
  --otel-sampling-rate 1.0 \
  --foreground  # Run in foreground to see immediate output
```

**Expected output**: ToolHive should log telemetry provider creation:
```
INFO[0001] Creating OTLP tracer provider for endpoint: http://otel-collector.monitoring.svc.cluster.local:4318 with sampling rate: 1.00
```

#### 2. Test OTLP Endpoint Connectivity

Verify ToolHive can reach the OTLP collector:

```bash
# From within ToolHive pod (or locally if testing locally)
curl -v http://otel-collector.monitoring.svc.cluster.local:4318/v1/traces

# Expected response: HTTP 405 (Method Not Allowed) is normal for GET requests
# HTTP connection refused or timeout indicates connectivity issues
```

#### 3. Verify OpenTelemetry Collector Health

Check that the OTLP collector is receiving and forwarding traces:

```bash
# Check collector pod status
kubectl get pods -n monitoring -l app.kubernetes.io/name=opentelemetry-collector

# Check collector logs for trace processing
kubectl logs -n monitoring deployment/otel-collector-opentelemetry-collector -f

# Look for lines like:
# "Trace received" or "TracesExporter" entries
```

#### 4. Test Jaeger Endpoint Accessibility  

Ensure the collector can forward traces to Jaeger:

```bash
# Test Jaeger OTLP endpoint from collector pod
kubectl exec -n monitoring deployment/otel-collector-opentelemetry-collector -- \
  curl -v http://jaeger-all-in-one-jaeger:14268/v1/traces

# Check Jaeger logs for incoming traces
kubectl logs -n monitoring deployment/jaeger-all-in-one-jaeger -f | grep -i trace
```

#### 5. Generate Test Traces

Create predictable trace activity to verify the pipeline:

```bash
# Start ToolHive with 100% sampling and simple server
thv run simple-test-server \
  --otel-tracing-enabled \
  --otel-endpoint http://otel-collector.monitoring.svc.cluster.local:4318 \
  --otel-sampling-rate 1.0 \
  --otel-service-name "toolhive-trace-test"

# Make several requests to generate traces
curl http://localhost:8080/health
curl http://localhost:8080/some-endpoint
```

**Expected behavior**: Within 30 seconds, traces should appear in Jaeger UI under service "toolhive-trace-test".

## Common Issues: "No Traces Appearing in Jaeger"

### Issue 1: Tracing Not Enabled

**Problem**: ToolHive tracing flags are not set correctly.

**Symptoms**: 
- No telemetry provider logs in ToolHive output
- ToolHive runs normally but no traces generated

**Solution**: 
```bash
# Ensure BOTH flags are set:
thv run your-server \
  --otel-tracing-enabled \          # This enables tracing
  --otel-endpoint http://...         # This configures where to send traces
```

**Validation**: Look for this log message:
```
INFO Creating OTLP tracer provider for endpoint: <your-endpoint>
```

### Issue 2: Wrong OTLP Endpoint URL

**Problem**: ToolHive is sending traces to wrong endpoint or using wrong protocol.

**Common mistakes**:
- Using gRPC port (4317) with HTTP endpoint configuration
- Using Jaeger UI port (16686) instead of OTLP port
- Missing `/v1/traces` path for direct Jaeger HTTP

**Solution**:
```bash
# For OTLP Collector (recommended)
--otel-endpoint http://otel-collector.monitoring.svc.cluster.local:4318

# For direct Jaeger HTTP (development only)  
--otel-endpoint http://jaeger-all-in-one-jaeger.monitoring.svc.cluster.local:14268/v1/traces --otel-insecure

# For direct Jaeger gRPC (development only)
--otel-endpoint http://jaeger-all-in-one-jaeger.monitoring.svc.cluster.local:14250 --otel-insecure
```

### Issue 3: Network Connectivity Problems

**Problem**: ToolHive cannot reach the OTLP endpoint due to network issues.

**Symptoms**:
- Connection refused errors in ToolHive logs  
- ToolHive timeouts on trace export

**Debug steps**:
```bash
# 1. Test basic connectivity from ToolHive pod
kubectl exec -it <toolhive-pod> -- nc -zv otel-collector.monitoring.svc.cluster.local 4318

# 2. Check if services exist and have endpoints
kubectl get svc -n monitoring | grep otel
kubectl get endpoints -n monitoring otel-collector-opentelemetry-collector

# 3. Verify collector is listening on correct port
kubectl exec -n monitoring deployment/otel-collector-opentelemetry-collector -- netstat -tlnp | grep 4318
```

### Issue 4: Low Sampling Rate

**Problem**: Traces are being sampled out due to low sampling rate.

**Symptoms**:
- Occasional traces appear but not consistently
- High-traffic applications show fewer traces than expected

**Solution**:
```bash
# Set sampling to 100% for debugging
--otel-sampling-rate 1.0

# For production, use appropriate sampling (5-10%)
--otel-sampling-rate 0.05
```

### Issue 5: Collector Configuration Issues  

**Problem**: OTLP collector is not properly configured to receive or forward traces.

**Debug steps**:
```bash
# 1. Check collector configuration
kubectl get configmap -n monitoring otel-collector-opentelemetry-collector -o yaml

# 2. Verify receivers are configured for OTLP
# Should see: otlp: protocols: grpc: endpoint: 0.0.0.0:4317, http: endpoint: 0.0.0.0:4318

# 3. Verify exporters include Jaeger
# Should see: jaeger: endpoint: http://jaeger-all-in-one-jaeger:14250

# 4. Check service pipeline connects receivers to exporters
# Should see: traces: receivers: [otlp], exporters: [jaeger]
```

### Issue 6: Jaeger Memory Limits  

**Problem**: Jaeger in-memory storage is full and dropping traces.

**Symptoms**:
- Older traces disappear
- Jaeger logs show "memory store full" messages

**Solution**:
```bash
# Increase memory trace limit
helm upgrade jaeger-all-in-one jaegertracing/jaeger -n monitoring \
  --set allInOne.args[0]="--memory.max-traces=100000"

# Or configure persistent storage for production
```

### Issue 7: Authentication/Headers Issues

**Problem**: OTLP endpoint requires authentication headers that are missing or incorrect.

**Solution**:
```bash
# For platforms requiring API keys (like Honeycomb)
thv run server \
  --otel-tracing-enabled \
  --otel-endpoint https://api.honeycomb.io \
  --otel-headers "x-honeycomb-team=your-api-key"

# Multiple headers
--otel-headers "Authorization=Bearer token" \
--otel-headers "X-Custom-Header=value"
```

## Troubleshooting

### Common Jaeger Installation Issues

#### Image Pull Secrets Template Error

**Error:** `template: jaeger/templates/allinone-deploy.yaml:33:10: executing "jaeger/templates/allinone-deploy.yaml" at <include "allInOne.imagePullSecrets" .>: error calling include: template: jaeger/templates/_helpers.tpl:576:4: executing "allInOne.imagePullSecrets" at <include "common.images.renderPullSecrets" (dict "images" (list .Values.allInOne.image) "context" $)>: error calling include: template: jaeger/charts/common/templates/_images.tpl:86:14: executing "common.images.renderPullSecrets" at <.pullSecrets>: can't evaluate field pullSecrets in type interface {}`

**Root Cause:** The Jaeger Helm chart expects the `allInOne.image` configuration to be structured as an object with specific fields including `pullSecrets`, but it was configured as a simple string.

**Solution:** Use the proper image configuration structure in your `jaeger-values.yaml`:

```yaml
allInOne:
  image:
    registry: ""
    repository: jaegertracing/all-in-one  
    tag: "1.61.0"
    digest: ""
    pullPolicy: IfNotPresent
    pullSecrets: []
```

Instead of the incorrect format:
```yaml
allInOne:
  image: jaegertracing/all-in-one:1.61.0  # This causes the template error
```

#### Memory Storage Limitations

**Issue:** Traces disappearing after reaching memory limits in development setup.

**Solution:** Increase the memory trace limit or configure persistent storage:

```yaml
allInOne:
  args:
    - --memory.max-traces=100000  # Increase from default 50000
```

For production environments, configure Elasticsearch or Cassandra storage instead of in-memory storage.

#### Port Conflicts

**Issue:** Service port conflicts when multiple observability tools are installed.

**Solution:** Verify port availability and adjust service configuration if needed:

```bash
# Check if ports are already in use
kubectl get svc -A | grep -E "16686|14250|14268"

# Modify ports in values if conflicts exist
```

#### OTLP Receiver Not Working

**Issue:** ToolHive cannot send traces to Jaeger OTLP endpoint.

**Solution:** Verify OTLP configuration and network connectivity:

1. Check if OTLP receivers are enabled:
```yaml
allInOne:
  args:
    - --collector.otlp.enabled=true
    - --collector.otlp.grpc.host-port=0.0.0.0:14250
    - --collector.otlp.http.host-port=0.0.0.0:14268
```

2. Test connectivity from ToolHive pod:
```bash
# Test OTLP gRPC endpoint
kubectl exec -it <toolhive-pod> -- nc -zv jaeger-all-in-one-jaeger.monitoring.svc.cluster.local 14250

# Test OTLP HTTP endpoint  
kubectl exec -it <toolhive-pod> -- curl -v http://jaeger-all-in-one-jaeger.monitoring.svc.cluster.local:14268/v1/traces
```

### General Troubleshooting Steps

1. **Check Helm installation status:**
```bash
helm list -n monitoring
helm status jaeger-all-in-one -n monitoring
```

2. **Verify pod health:**
```bash
kubectl get pods -n monitoring -l app.kubernetes.io/name=jaeger
kubectl describe pod <jaeger-pod-name> -n monitoring
```

3. **Check service connectivity:**
```bash
kubectl get svc -n monitoring | grep jaeger
kubectl port-forward -n monitoring svc/jaeger-all-in-one-jaeger 16686:16686
```

4. **Review logs for errors:**
```bash
kubectl logs -n monitoring deployment/jaeger-all-in-one-jaeger -f
```
