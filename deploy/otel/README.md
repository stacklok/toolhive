# OpenTelemetry

ToolHive currently has OTEL and Prometheus telemetry support. This means, ToolHive will push OTEL telemetry data to a collector as well as exposing a Prometheus `/metrics` endpoint - if enabled. However, in order to test these integrations with running instances of the tools you'll need to first install them into your local cluster.

## Installation of OTEL Collector
To install the OpenTelemetry Collector with the desired settings run:

> Note: The `otel-values.yaml` can be found in this directory.

```bash
helm upgrade -i otel-collector open-telemetry/opentelemetry-collector -f otel-values.yaml -n monitoring --kubeconfig kconfig.yaml --create-namespace
```

The current values file has the following setup:
- [Kubestats](https://opentelemetry.io/docs/platforms/kubernetes/collector/components/#kubeletstats-receiver) present enabled to recieve pod/container metrics from the kube API
- exports to prometheus via remote-write
- exports to prometheus allow for prometheus to scrape metrics from the OTEL collector (this is recommended over the remote-write)

## Installation of a Prometheus/Grafana Stack

To install the Prometheus/Grafana stack into a local cluster with the desired settings, run:

> Note: The `prometheus-stack-values.yaml` can be found in this directory.

```bash
helm upgrade -i kube-prometheus-stack prometheus-community/kube-prometheus-stack -f prometheus-stack-values.yaml -n monitoring --kubeconfig kconfig.yaml --create-namespace
```

The current values file has the following setup:
- Enable the remote-write receieve (to allow for OTEL to directly push metrics to prometheus rather than prometheus scraping them from the OTEL collector)
- Scrape config that scrapes the metrics from the OTEL collector
- Grafana is enabled
- kube state metrics enabled

## Grafana Dashboards

In the [grafana-dashboards](./grafana-dashboards/) folder there are some basic dashboards that will allow you to visualise some of the basic metrics regarding the ToolHive Proxy and MCP server. They are currently both the same, however they are separate because if you are adding more visualisations then its likely the two will diverge. One supports the scenario where you scrape metrics directly from the OTEL Collector via Prometheus. The other is one where the metrics are pushed from the OTEL Collector into Prometheus using the remote-write capability. Because of the slightly naming convention differences, the more visualisations and metrics you use, the more likely it is that the two dashboard files will differ.