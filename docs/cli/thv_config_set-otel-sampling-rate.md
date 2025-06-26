## thv config set-otel-sampling-rate

Set the OpenTelemetry sampling rate

### Synopsis

Set the OpenTelemetry trace sampling rate (between 0.0 and 1.0).
This sampling rate will be used by default when running MCP servers unless overridden by the --otel-sampling-rate flag.

Example:
  thv config set-otel-sampling-rate 0.1

```
thv config set-otel-sampling-rate <rate> [flags]
```

### Options

```
  -h, --help   help for set-otel-sampling-rate
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv config](thv_config.md)	 - Manage application configuration

