# Usage Analytics

ToolHive includes privacy-first usage analytics to help improve the product while protecting user privacy. This document explains what data is collected, how it's used, and how to control or disable this feature.

## Overview

ToolHive collects anonymous usage analytics to understand how the MCP servers are being used in aggregate. This helps the Stacklok team prioritize development efforts and identify potential issues.

**Key Privacy Principles:**
- **Anonymous**: No personally identifiable information is collected
- **Minimal**: Only essential metrics are collected
- **Transparent**: This documentation explains exactly what is collected
- **Opt-out**: Usage analytics can be easily disabled
- **Dual-endpoint**: User's own telemetry configuration is preserved and unaffected

## What Data is Collected

Usage analytics collect only the following anonymous metrics:

### Tool Call Counts
- **Metric**: `toolhive_usage_tool_calls_total`
- **Data**: Count of MCP tool calls with success/error status
- **Attributes**: Only `status` (success or error)

### What is NOT Collected
- Server names or identifiers
- Tool names or tool types
- Command arguments or parameters
- File paths or content
- User identifiers or client information
- Request/response payloads
- Environment variables
- Host information
- IP addresses

## How Analytics Work

ToolHive uses a dual-endpoint telemetry architecture:

1. **User Telemetry**: Your configured OTEL endpoint (if any) receives full telemetry with all details for your observability needs
2. **Usage Analytics**: Stacklok's analytics collector receives only anonymous tool call counts

This ensures that enabling usage analytics doesn't interfere with your existing observability setup.

## Configuration

### Default Behavior
- **Usage analytics are enabled by default**
- Anonymous metrics are sent to `https://analytics.toolhive.stacklok.dev/v1/traces`
- Your existing telemetry configuration remains unaffected

### CLI Configuration

#### Disable Usage Analytics
To disable usage analytics, update your configuration file (`~/.toolhive/config.yaml`):

```yaml
otel:
  usage-analytics-enabled: false
```

#### Check Current Setting
```bash
# View current configuration
thv config otel get usage-analytics-enabled
```

### Kubernetes Operator Configuration

For the ToolHive operator, you can disable usage analytics per MCPServer:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: example-server
spec:
  image: example/mcp-server:latest
  telemetry:
    openTelemetry:
      enabled: true
      endpoint: "otel-collector:4317"  # Your own telemetry
      usageAnalyticsEnabled: false     # Disable analytics for this server
```

## Data Retention and Usage

- **Retention**: Analytics data is retained for up to 2 years for trend analysis
- **Purpose**: Data is used solely for product development and improvement
- **Access**: Only authorized Stacklok personnel have access to aggregated analytics data
- **Sharing**: Analytics data is never shared with third parties

## Privacy Compliance

ToolHive's usage analytics are designed to comply with privacy regulations:
- **GDPR**: No personal data is collected; anonymous usage metrics fall outside GDPR scope
- **CCPA**: No personal information is collected or sold
- **SOC2**: Analytics infrastructure follows Stacklok's security and privacy controls

## Technical Implementation

### Architecture
- Separate OTLP endpoint for analytics (`https://analytics.toolhive.stacklok.dev/v1/traces`)
- Dedicated `AnalyticsMeterProvider` that only records tool call counts
- Middleware filters out all sensitive information before recording analytics metrics

### Security
- HTTPS/TLS encryption for all analytics data transmission
- No authentication headers needed (anonymous metrics)
- Separate from user's telemetry configuration to prevent cross-contamination

## Frequently Asked Questions

### Q: Can I see what analytics data is being sent?
A: Yes, you can enable debug logging to see the minimal metrics being sent. The only metric is `toolhive_usage_tool_calls_total` with a `status` attribute.

### Q: Will this affect my existing telemetry?
A: No. Usage analytics use a completely separate telemetry pipeline. Your existing OTEL configuration for traces, metrics, and logs remains unchanged.

### Q: How do I know if analytics are enabled?
A: Check your configuration with `thv config otel get usage-analytics-enabled` or look for `usage-analytics-enabled: true` in your config file.

### Q: What happens if I disable analytics?
A: When disabled, no usage analytics are collected or sent. Only your regular telemetry (if configured) continues to work.

### Q: Can I use custom analytics endpoints?
A: The analytics endpoint is set by default and not user-configurable. This ensures data goes to Stacklok's analytics infrastructure for product improvement.

## Support

If you have questions about usage analytics or privacy concerns:
- **Documentation**: https://docs.toolhive.dev
- **Issues**: https://github.com/stacklok/toolhive/issues
- **Email**: toolhive-support@stacklok.com

## Changes to This Policy

This documentation may be updated to reflect changes in the usage analytics system. Changes will be:
- Documented in the ToolHive release notes
- Committed to the repository with version control history
- Made available at https://docs.toolhive.dev/usage-analytics