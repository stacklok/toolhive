# Logging Practices

This document describes ToolHive's logging strategy for both the CLI and server components to ensure consistent, user-friendly output that helps users and operators diagnose issues.

## Core Principles

1. **Successful operations are silent by default** - When an operation succeeds, do not emit logs at INFO level or above. Users should only see output when something requires their attention or when they explicitly request debug output.

2. **Not all failures are errors** - Just because something fails doesn't mean it should be logged as an error. Choose the appropriate log level based on impact:
   - **ERROR**: Fatal issues that prevent the operation from completing
   - **WARN**: Failures that provide context for potential hard errors, or issues where the operation continues with degraded functionality
   - **DEBUG**: Expected failures that are not essential for ToolHive to work (e.g., optional features, fallback scenarios)

3. **Logs serve their audience** - CLI logs serve end users who need actionable information. Server logs serve operators who need to debug and monitor systems.

4. **Structured logging for machines, human-readable for terminals** - Use structured (JSON) logging in production server environments and human-readable output for CLI interactions.

5. **Log the "why", not just the "what"** - Include context that helps diagnose issues, such as what was attempted and what state was expected.

6. **No sensitive information in logs** - Never log credentials, tokens, API keys, passwords, or other secrets.

## Log Levels

| Level | When to Use | Example |
|-------|-------------|---------|
| **DEBUG** | Detailed information for developers troubleshooting issues. Not shown unless `--debug` flag is used. | `"Attempting to connect to container runtime at socket /var/run/docker.sock"` |
| **INFO** | Significant events during long operations (image pulls, downloads). Use sparingly in CLI context. | `"Pulling image ghcr.io/toolhive/fetch:latest..."` |
| **WARN** | Non-fatal issues where the operation continues but something unexpected occurred. | `"Config file not found, using defaults"` |
| **ERROR** | Fatal issues that prevent the operation from completing. Should be followed by returning an error. | `"Failed to start container: permission denied"` |

## CLI Output Guidelines

### User-Facing Output vs Logs

Distinguish between:
- **User-facing output** - Information the user requested (use `fmt.Println`)
- **Operational logs** - Diagnostic information (use `logger`)

### Silent Success

Commands should produce minimal output on success. Show progress only for operations that take more than 2-3 seconds or pull external resources.

```bash
# Good - silent success
$ thv run fetch

# Avoid - verbose success messages
$ thv run fetch
INFO: Checking container runtime...
INFO: Container runtime found...
INFO: Starting container...
Server 'fetch' is now running!
```

## Configuration

- `--debug` flag enables DEBUG level logging
- `UNSTRUCTURED_LOGS=true` (default): Human-readable logs to stderr
- `UNSTRUCTURED_LOGS=false`: JSON-structured logs to stdout
