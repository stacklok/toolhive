## thv config set-otel-endpoint

Set the OpenTelemetry endpoint URL

### Synopsis

Set the OpenTelemetry OTLP endpoint URL for tracing and metrics.
This endpoint will be used by default when running MCP servers unless overridden by the --otel-endpoint flag.

Example:
  thv config set-otel-endpoint https://api.honeycomb.io

```
thv config set-otel-endpoint <endpoint> [flags]
```

### Options

```
  -h, --help   help for set-otel-endpoint
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv config](thv_config.md)	 - Manage application configuration

