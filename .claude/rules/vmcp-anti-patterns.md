---
paths:
  - "pkg/vmcp/**/*.go"
  - "cmd/vmcp/**/*.go"
---

# vMCP Anti-Pattern Rule

When reviewing or writing code in `pkg/vmcp/` or `cmd/vmcp/`, check changes against these anti-patterns. Flag any code that introduces or expands them.

## 1. Context Variable Coupling

Using `context.WithValue`/`ctx.Value` to pass domain data between middleware or from middleware to handlers. Creates invisible producer-consumer dependencies, ordering fragility, and silent degradation when values are missing.

**Detect**: `context.WithValue` in middleware setting domain data; `ctx.Value(someKey)` reads in handlers/routers/business logic; functions whose behavior depends on specific context values.

**Instead**: Push data onto `MultiSession` (handlers already have access); pass domain data as explicit function parameters; reserve context for trace IDs, cancellation, and deadlines only.

## 2. Repeated Request Body Read/Restore

Multiple middleware calling `io.ReadAll(r.Body)` then restoring with `io.NopCloser(bytes.NewReader(...))`. Fragile implicit contract — if any middleware forgets to restore, downstream handlers silently get an empty body.

**Detect**: `io.ReadAll(r.Body)` followed by `r.Body = io.NopCloser(bytes.NewReader(...))` in middleware; multiple middleware in the same chain parsing JSON from the request body.

**Instead**: Parse body once early in the pipeline; extend `ParsedMCPRequest` so all downstream consumers use the parsed representation; cache raw bytes alongside parsed form if needed for audit.

## 3. God Object: Server Struct

A single struct owning too many concerns (10+ fields spanning domains). Causes cognitive overload, makes subsystems untestable in isolation, and amplifies change risk.

**Detect**: Structs with 10+ fields spanning different domains; constructors >50 lines or with `nolint:gocyclo`; files >500 lines handling multiple unrelated concerns; multiple mutex fields protecting different state subsets.

**Instead**: Extract each concern into a self-contained module with its own `New()`/`Start()`/`Stop()`. Server struct should be a thin orchestrator composing pre-built subsystems.

## 4. Middleware Overuse

Business logic in HTTP middleware when behavior is specific to certain request types or belongs on a domain object. Adds cognitive load (10+ layer chains), wastes work on irrelevant requests, and creates invisible mutations.

**Detect**: Middleware that checks request method/type and returns early for most cases; middleware whose sole purpose is context stuffing (see #1); middleware that wraps `ResponseWriter` or reads request body (see #2).

**Instead**: Reserve middleware for truly cross-cutting concerns (recovery, telemetry, auth). Push behavior onto domain objects — e.g., annotation lookup as a method on `MultiSession` instead of middleware.

## 5. SDK Coupling Leaking Through Abstractions

SDK-specific patterns (e.g., mcp-go's two-phase session creation) escaping the adapter boundary and shaping internal architecture.

**Detect**: Code outside `adapter/` referencing SDK-specific concepts (hooks, placeholders, two-phase creation); session management with "re-check"/"double-check" patterns from SDK lifecycle race windows.

**Instead**: Keep the adapter layer thin and isolated. Internal session management should present a clean `CreateSession() -> (Session, error)` API. The two-phase dance should be invisible to callers.

## 6. Configuration Object Passed Everywhere

Threading a large `Config` struct (13+ fields) through constructors when each consumer only needs a small subset. Obscures dependencies, invites nil pointer panics, and bloats test setup.

**Detect**: Constructors accepting `*config.Config` but only accessing a few fields; nil checks on config sub-fields in business logic; test setup building large config structs with mostly zero/nil fields.

**Instead**: Each subsystem accepts only the config it needs via small, focused config types. Decompose the top-level config at the composition root before passing to constructors.

## 7. Mutable Shared State Through Context

Storing a mutable struct in context and having multiple middleware modify it in place. Violates the immutability convention, creates hidden mutation coupling, and risks data races in concurrent scenarios.

**Detect**: Middleware mutating fields on structs retrieved from context; structs stored in context with exported mutable fields; multiple middleware reading and writing the same context value.

**Instead**: Treat context values as immutable; create new values with `context.WithValue` if downstream needs to add info. Better yet, pass data explicitly (see #1).
