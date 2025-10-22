# Custom OpenTelemetry Attributes Feature

## Overview

This document demonstrates the new custom OpenTelemetry attributes feature added to ToolHive. This feature allows you to add custom resource attributes to all telemetry signals (traces and metrics) via command-line flags or environment variables.

## Usage Examples

### 1. Using Command-Line Flag

Add custom attributes using the `--otel-custom-attributes` flag:

```bash
thv run my-server \
  --otel-endpoint https://api.honeycomb.io \
  --otel-headers "x-honeycomb-team=YOUR_API_KEY" \
  --otel-custom-attributes "server_type=production,region=us-east-1,team=platform,deployment_id=abc123"
```

### 2. Using Environment Variable

The standard OpenTelemetry environment variable `OTEL_RESOURCE_ATTRIBUTES` is also supported:

```bash
export OTEL_RESOURCE_ATTRIBUTES="deployment.environment=staging,service.namespace=backend,cloud.provider=aws"
thv run my-server --otel-endpoint https://api.honeycomb.io
```

### 3. Combining Both Methods

You can use both CLI flags and environment variables together. Both sets of attributes will be included:

```bash
export OTEL_RESOURCE_ATTRIBUTES="deployment.environment=production"
thv run my-server \
  --otel-endpoint https://api.honeycomb.io \
  --otel-custom-attributes "region=us-west-2,team=infrastructure"
```

## Attribute Format

- **Format**: `key=value` pairs separated by commas
- **Key**: Can contain letters, numbers, dots (.), underscores (_), and hyphens (-)
- **Value**: Can contain any characters including special characters and URLs
- **Examples**:
  - Simple: `environment=production`
  - With dots: `service.name=my-service`
  - With URL: `endpoint=https://api.example.com/v1`
  - Multiple: `env=prod,region=us-east-1,team=platform`

## Common Use Cases

### 1. Environment Identification

```bash
--otel-custom-attributes "environment=production,datacenter=us-east-1a"
```

### 2. Service Metadata

```bash
--otel-custom-attributes "service.team=platform,service.owner=devops@example.com,service.tier=critical"
```

### 3. Deployment Tracking

```bash
--otel-custom-attributes "deployment.id=v1.2.3-abc123,deployment.timestamp=2024-01-15T10:30:00Z"
```

### 4. Cloud Provider Information

```bash
--otel-custom-attributes "cloud.provider=aws,cloud.region=us-west-2,cloud.zone=us-west-2a,cloud.account.id=123456789"
```

### 5. Kubernetes Labels

```bash
--otel-custom-attributes "k8s.cluster.name=production,k8s.namespace=default,k8s.pod.name=my-pod-xyz"
```

## How It Works

1. **CLI Parsing**: The `--otel-custom-attributes` flag accepts a comma-separated string of key=value pairs
2. **Attribute Creation**: These are parsed into OpenTelemetry `attribute.KeyValue` structures
3. **Resource Creation**: Custom attributes are added to the OpenTelemetry Resource along with standard attributes (service.name, service.version)
4. **Environment Support**: The standard `OTEL_RESOURCE_ATTRIBUTES` environment variable is automatically read via `resource.WithFromEnv()`
5. **Global Application**: These resource attributes are attached to ALL telemetry signals:
   - Every trace span will include these attributes
   - Every metric data point will include these attributes

## Benefits

1. **Better Observability**: Easily filter and query telemetry data by custom dimensions
2. **Multi-tenancy**: Distinguish between different environments, teams, or customers
3. **Debugging**: Track specific deployments, versions, or configurations
4. **Compliance**: Add required metadata for audit and compliance purposes
5. **Cost Attribution**: Track resource usage by team, project, or cost center

## Error Handling

Invalid attribute formats will log a warning but won't prevent the application from starting:

```bash
# This will log a warning about the invalid format
thv run my-server --otel-custom-attributes "invalid_format"
# Warning: Failed to parse custom attributes: invalid attribute format 'invalid_format': expected key=value
```

## Integration with Observability Platforms

These custom attributes will appear in your observability platform (Honeycomb, Datadog, New Relic, etc.) and can be used for:

- **Filtering**: Filter traces and metrics by any custom attribute
- **Grouping**: Group and aggregate metrics by custom dimensions
- **Alerting**: Create alerts based on specific attribute values
- **Dashboards**: Build dashboards segmented by custom attributes
- **Service Maps**: Enhance service dependency maps with custom metadata

## Testing

Run the unit tests to verify the implementation:

```bash
go test -v ./pkg/telemetry -run TestParseCustomAttributes
```

Run integration tests (requires build tag):

```bash
go test -v ./pkg/telemetry -tags=integration -run TestCustomAttributesIntegration
```

## Implementation Details

### Files Modified

1. **pkg/telemetry/attributes.go** - New file with attribute parsing functions
2. **pkg/telemetry/config.go** - Added CustomAttributes field to Config struct
3. **pkg/telemetry/providers/providers.go** - Updated to include custom attributes in resource creation
4. **cmd/thv/app/run_flags.go** - Added --otel-custom-attributes CLI flag
5. **pkg/telemetry/attributes_test.go** - Comprehensive unit tests
6. **pkg/telemetry/config_integration_test.go** - Integration tests

### Key Functions

- `ParseCustomAttributes(input string)`: Parses a comma-separated string of key=value pairs
- `ParseMultipleCustomAttributes(inputs []string)`: Parses multiple attribute strings
- `resource.WithFromEnv()`: Automatically reads OTEL_RESOURCE_ATTRIBUTES environment variable

## Future Enhancements

Potential future improvements could include:

1. Support for typed attributes (int, float, bool) via type hints
2. Validation of attribute keys against OpenTelemetry semantic conventions
3. Configuration file support for persistent custom attributes
4. Dynamic attribute updates without restart
5. Attribute templates for common scenarios