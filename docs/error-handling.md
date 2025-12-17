# Error Handling

This document describes ToolHive's error handling strategy for both the CLI and API to ensure consistent, user-friendly error messages that help users diagnose and resolve issues.

## Core Principles

1. **Errors are returned by default** - Never silently swallow errors. If an operation fails, the error should propagate up to where it can be handled appropriately.

2. **Ignored errors must be documented** - When an error is intentionally ignored, add a comment explaining why. Typically, ignored errors should still be logged (unless the log would be exceptionally noisy).

3. **No sensitive information in errors** - Avoid putting potentially sensitive information in error messages (API keys, credentials, tokens, passwords). Errors may be returned to users or logged elsewhere.

4. **Use `errors.Is()` or `errors.As()` for error inspection** - Always use these functions for inspecting errors, since they properly unwrap all types of Go errors.


## Constructing Errors

There are two acceptable ways to construct errors in ToolHive:
- **Common Errors** - If you have a common type of error (e.g. workload not found), then it may already exist in our error package. See the section below.
- **Unstructured Errors** - If an error type is not common enough to motivate inclusion in the error package, using `fmt.Errorf` or `errors.New` is acceptable. Today, we don't construct errors with additional structured data, so any explanatory string will do.

## Error Package

ToolHive provides a typed error system for common error scenarios. Each error type has an associated HTTP status code for API responses.

Error types are defined in:
- `pkg/errors/errors.go` - Core application errors (CLI, proxy, etc.)
- `pkg/vmcp/errors.go` - Virtual MCP Server domain errors


## Error Wrapping Guidelines

### Use `%w` for Preserving Error Chains with fmt.Errorf

When wrapping errors using `fmt.Errorf`, use `%w` to preserve the error chain for `errors.Is()` and `errors.As()`:

```go
// Good - preserves error chain
return fmt.Errorf("failed to start container: %w", err)

// Good - allows errors.Is(err, runtime.ErrWorkloadNotFound)
return fmt.Errorf("workload %s not accessible: %w", name, runtime.ErrWorkloadNotFound)
```

Don't use `errors.Wrap` (from github.com/pkg/error) unless you really want a stack trace. Excessively capturing stack traces can result in challenging-to-read errors and unnecessary memory use if errors occur frequently.

### When should I wrap an error?

It is NOT necessary to wrap all errors, and it's best if we don't. Wrapping errors excessively
can lead to hard to understand errors. Typically, one would wrap an error to better indicate 
which particular step is failing. Consider using `errors.WithStack` or `errors.Wrap` if you find yourself needing to wrap errors many times in order to debug.



## API Error Handling

### Response Format

API errors are returned as plain text using `http.Error()`:

Response codes are derived from unwrapping the error and this happens in a common middleware layer.

See pkg/api/errors/ for more details. 
TODO: implement common middleware for error and panic handling.
TODO: integrate handler into setupDefaultRoutes.
TODO: update documentation on APIs.


### Error Response Behavior

1. **First matching error code wins** - Check specific errors first, then fall back to generic internal server error.
2. **Hide internal details** - For 500 errors, log the full error but return a generic message to the user
3. **Include context for client errors** - For 400/404 errors, include helpful context in the message


## CLI Error Handling

### Error Propagation

CLI errors bubble up to the outermost command where they are logged once. Do not log errors at every level of the call stack.

```go
// In a helper function - return the error, don't log it
func doSomething() error {
    if err := someOperation(); err != nil {
        return fmt.Errorf("failed to do something: %w", err)
    }
    return nil
}

// In the command handler - the error will be handled by Cobra
func runCommand(cmd *cobra.Command, args []string) error {
    if err := doSomething(); err != nil {
        return err  // Cobra will display this to the user
    }
    return nil
}
```

### Log Levels for Errors

| Level | When to Use |
|-------|-------------|
| `logger.Errorf` | Errors that stop execution and will be returned |
| `logger.Warnf` | Non-fatal issues where operation continues |
| `logger.Debugf` | Informational errors for troubleshooting |

```go
// Error - operation failed and program/request aborts
logger.Errorf("Failed to start container: %v", err)
os.Exit(1)

// Warning - degraded but continuing
if err := cleanup(); err != nil {
    logger.Warnf("Failed to cleanup temporary files: %v", err)
    // Continue execution
}

// Debug - expected failure path
if err := checkOptionalFeature(); err != nil {
    logger.Debugf("Optional feature not available: %v", err)
}
```

## When to Return vs Ignore Errors

Most errors should be returned by default. When an error is intentionally ignored, add a comment explaining why and typically log it.

### Examples of Ignored Errors

```go
// Good - commented and logged
if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopping, ""); err != nil {
    // Non-fatal: status update failure shouldn't prevent stopping the workload
    logger.Debugf("Failed to set workload %s status to stopping: %v", name, err)
}

// Good - idempotent operation
if errors.Is(err, rt.ErrWorkloadNotFound) {
    // Workload already gone - this is fine for a delete operation
    logger.Warnf("Workload %s not found, may have already been deleted", name)
    return nil
}
```

## Panic Recovery

Use `recover()` sparingly. It should only be used at well-defined boundaries to prevent crashes and provide meaningful errors. 

### Where to Use recover()

1. **Top level of the API server** - Prevent a single request from crashing the entire server
2. **Top level of the CLI** - Ensure users see a meaningful error message instead of a stack trace


### When NOT to Use recover()

- Do not use `recover()` to hide programming errors - fix them instead
- Do not use `recover()` deep in the call stack - let panics propagate to the top-level handlers
- Do not use `recover()` for expected error conditions - use normal error handling

