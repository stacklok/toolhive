## thv config otel set-endpoint

Set the OpenTelemetry endpoint URL

### Synopsis

Set the OpenTelemetry OTLP endpoint URL for tracing and metrics.
This endpoint will be used by default when running MCP servers unless overridden by the --otel-endpoint flag.

Example:
  thv config otel set-endpoint https://api.honeycomb.io

```
thv config otel set-endpoint <endpoint> [flags]
```

### Options

```
  -h, --help   help for set-endpoint
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv config otel](thv_config_otel.md)	 - Manage OpenTelemetry configuration

