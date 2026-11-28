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

### Creating Errors with HTTP Status Codes

Use `errors.WithCode()` to associate HTTP status codes with errors:

```go
import (
    "errors"
    "net/http"
    
    "github.com/stacklok/toolhive-core/httperr"
)

// Define an error with a status code
var ErrWorkloadNotFound = httperr.WithCode(
    errors.New("workload not found"),
    http.StatusNotFound,
)

// Create a new error inline with a status code
return httperr.WithCode(
    fmt.Errorf("invalid request: %w", err),
    http.StatusBadRequest,
)
```

### Extracting Status Codes

Use `errors.Code()` to extract the HTTP status code from an error:

```go
code := httperr.Code(err)  // Returns 500 if no code is found
```

### Error Definitions

Error types with HTTP status codes are defined in:
- [`toolhive-core/httperr`](https://github.com/stacklok/toolhive-core/tree/main/httperr) - Core error utilities (`WithCode`, `Code`, `CodedError`)
- `pkg/groups/errors.go` - Group-related errors
- `pkg/container/runtime/types.go` - Runtime errors (`ErrWorkloadNotFound`)
- `pkg/workloads/types/validate.go` - Workload validation errors
- `pkg/secrets/factory.go` - Secrets provider errors
- `pkg/transport/session/errors.go` - Transport session errors
- `pkg/vmcp/errors.go` - Virtual MCP Server domain errors

In general, define errors near the code that produces the error.

## vMCP delegated credential failures

An outgoing authentication strategy can fail to read a user's delegated
credential for an upstream provider in two ways, and vMCP answers each one
differently:

- **The user must authorize the provider.** `tools/call` returns an `isError`
  tool result that names the provider, for example
  `provider "github" requires upstream authorization`, or
  `upstream authorization required` if no provider is named.
- **The credential is unavailable.** `tools/call` returns JSON-RPC `-32603`
  with the fixed message `upstream credential unavailable`. Causes include a
  missing reader, a reader that does not read user-keyed storage, and storage
  or refresh failures.

Both revisions (Legacy 2025-11-25 and Modern 2026-07-28) answer the same way,
for a direct call, through the Legacy optimizer's `call_tool`, and for a
composite tool whose step fails on the credential. The response never carries
the backend address, transport errors, or the workflow and step names.

`resources/read` and `prompts/get` have no `isError` result. For them, an
unavailable credential returns `-32603` with the same fixed message on both
revisions. When the user must authorize the provider, they return a JSON-RPC
error whose message is the same text that names the provider: `-32603` on
Modern, and the SDK's generic error code on Legacy.

### Precedence

When one `tools/call` failure carries several causes, vMCP answers with the
first one that applies:

1. An authorization denial by vMCP's own policy, which keeps its existing
   response.
2. A URL elicitation answer, which keeps its existing rendering (see
   [Answering a call with a URL elicitation](arch/10-virtual-mcp-architecture.md#answering-a-call-with-a-url-elicitation)).
3. The user must authorize the provider.
4. The credential is unavailable.

A URL elicitation answer outranks the authorization requirement because it can
carry a link the user follows to connect the provider. The plain `isError`
text is the answer when no such link exists, and it must not hide one.

`resources/read` and `prompts/get` do not follow this list. They have no
URL elicitation answer, so an unavailable credential there renders the fixed
`-32603` message even when a URL elicitation answer is also on the error chain.

If a `forEach` iteration in a composite tool times out, the call reports the
workflow timeout, because `forEach` keeps the iteration's error as the cause
instead of flattening it to text.

### Retries and fallbacks

Composite workflow steps do not retry these failures. If a revision probe
fails on the credential, vMCP does not fall back to Legacy and read the
credential again; the call returns the failure. The same holds when vMCP
re-probes a backend whose cached revision turned out to be wrong.

### Limitations

These errors appear only when the backend's capabilities were listed
successfully and the credential read failed on a later call. If the credential
read fails while vMCP lists a backend's capabilities, vMCP leaves that backend
out of the tool list, so a call to one of its tools reports that the tool was
not found. On a Modern backend, when the read fails on an optional list
(resources, resource templates, or prompts) after the tool list succeeded,
that list is empty and vMCP logs a warning that names the credential failure.

A never-connected user who calls an omitted tool gets an unknown-tool error
(JSON-RPC `-32602`: `unknown tool` on Modern, `tool "<name>" not found` on
Legacy), not a result naming the provider. If no backend returns
capabilities, discovery itself fails instead of producing a tool list.

After the user connects the provider, Modern requests can still see the old
view until the capability cache TTL expires (30 seconds by default). On Legacy,
the reliable way to pick up newly visible tools is a fresh session started
after the cache expires. A `list_changed` resync triggered by a notification
from an eligible backend can also update a live session, but connecting a
provider does not trigger one.

In code mode, a script's tool call (`execute_tool_script`) does not go through
these renderers. The script receives the error text, which is the same text
named above and carries no backend or transport detail, and the outer call
answers with an ordinary tool result. A URL elicitation answer cannot reach the
client through a script.

These rules do not change how a missing token on the non-delegated path is
classified, and do not enable delegated credential reads by themselves.

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

### Handler Pattern

API handlers return errors instead of calling `http.Error()` directly. The `ErrorHandler` decorator in `pkg/api/errors/handler.go` converts errors to HTTP responses:

```go
// Define a handler that returns an error
func (s *Routes) getWorkload(w http.ResponseWriter, r *http.Request) error {
    workload, err := s.manager.GetWorkload(ctx, name)
    if err != nil {
        return err  // ErrWorkloadNotFound already has 404 status code
    }
    
    // For errors without a status code, wrap with WithCode
    if someCondition {
        return httperr.WithCode(
            fmt.Errorf("invalid input"),
            http.StatusBadRequest,
        )
    }
    
    // Success case - write response
    return json.NewEncoder(w).Encode(workload)
}

// Wire up with the ErrorHandler decorator
r.Get("/{name}", apierrors.ErrorHandler(routes.getWorkload))
```

### Error Response Behavior

1. **Status codes from errors** - The `ErrorHandler` extracts status codes using `errors.Code()`. Errors without codes default to 500.
2. **Hide internal details** - For 5xx errors, the full error is logged but only a generic message is returned to the user.
3. **Include context for client errors** - For 4xx errors, the error message is returned to the client.

See `pkg/api/errors/handler.go` for implementation details.


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

## Sentry Error Reporting

The API server supports optional [Sentry](https://sentry.io) integration for error and panic capture. When enabled (via `--sentry-dsn`), the following are automatically reported:

### What Gets Reported

1. **Panics** - The recovery middleware in `pkg/recovery/recovery.go` reports recovered panics to Sentry via `sentrypkg.RecoverPanic()` before returning a 500 response.

2. **5xx errors** - The error handler in `pkg/api/errors/handler.go` captures server errors to Sentry via `sentrypkg.CaptureException()`. This provides visibility into internal errors without requiring panics.

### How It Works

The Sentry integration is implemented in `pkg/sentry/sentry.go` and wired into two places:

- **Recovery middleware** catches panics and reports them to Sentry using `RecoverPanic()`.
- **Error handler** captures 5xx errors to Sentry using `CaptureException()`.

For distributed tracing, `thv serve` uses **OTEL `otelhttp` middleware** (not `sentryhttp`) to extract W3C `traceparent` headers. When a Sentry DSN is configured alongside an OTEL endpoint, the `pkg/sentry.SpanProcessor()` is registered with the OTEL SDK so spans are exported to **both** the configured OTLP backend and Sentry simultaneously.

### When Sentry Is Disabled

When no DSN is configured, all Sentry operations are no-ops. `sentrypkg.CaptureException()`, `sentrypkg.RecoverPanic()`, and `sentrypkg.SpanProcessor()` all check an atomic boolean and return immediately, adding no overhead.

### Configuration

See [Deployment Modes - Observability](arch/01-deployment-modes.md#observability-otel-distributed-tracing-and-sentry-error-reporting) for CLI flags, environment variables, and OTEL configuration.

