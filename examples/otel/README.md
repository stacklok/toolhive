# OpenTelemetry Observability Stack

ToolHive provides comprehensive observability with metrics, distributed tracing, and logging through OpenTelemetry. This stack includes:

- **Metrics**: Prometheus for metrics collection and storage
- **Tracing**: Jaeger for distributed trace collection and analysis
- **Visualization**: Grafana with pre-configured dashboards
- **Collection**: OpenTelemetry Collector for telemetry aggregation

ToolHive will push OTEL telemetry data to a collector as well as expose a Prometheus `/metrics` endpoint when enabled. This document describes how to install and configure the complete observability stack for testing and development.

> Note: ToolHive will be responsible for ensuring it emits the relevant telemetry data to OTel collectors and Prometheus `/metrics` endpoints. However, due to the fast pace in which the observability space moves, we cannot guarantee that the configuration that can be found below will work with the Charts forever. It will be maintained as a best effort but understand it is likely at somepoint that the Helm Charts will change rendering some of the configuration in this directory invalid. This directory was only to serve as a short-term example of provisioning an observability stack to demonstrate ToolHive telemetry capabilities.

## ToolHive Tracing Configuration

## Quick Setup Guide

To install the complete observability stack in order to test the ToolHive telemetry capability, follow the below:

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

## Grafana Dashboards

In the [grafana-dashboards](./grafana-dashboards/) folder are pre-built dashboards for visualizing ToolHive metrics:

- `toolhive-mcp-grafana-dashboard-otel-scrape.json`: For Prometheus scraping setup
- `toolhive-mcp-grafana-dashboard-otel-remotewrite.json`: For Prometheus remote-write setup

### Importing Dashboards

You can import these dashboards through:
1. Grafana UI: Configuration → Data Sources → Import
2. Automatic sidecar discovery (if enabled)
3. Grafana provisioning configuration