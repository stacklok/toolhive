## thv config set-otel-env-vars

Set the OpenTelemetry environment variables

### Synopsis

Set the list of environment variable names to include in OpenTelemetry spans.
These environment variables will be used by default when running MCP servers unless overridden by the --otel-env-vars flag.

Example:
  thv config set-otel-env-vars USER,HOME,PATH

```
thv config set-otel-env-vars <var1,var2,...> [flags]
```

### Options

```
  -h, --help   help for set-otel-env-vars
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv config](thv_config.md)	 - Manage application configuration

