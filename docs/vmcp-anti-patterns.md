# vMCP Anti-Pattern Catalog

This catalog defines anti-patterns specific to the vMCP codebase. Each entry explains what the pattern is, why it's destructive, how to detect it, and what authors should do instead.

---

## 1. Context Variable Coupling

### Definition

Using `context.WithValue` / `context.Value` to pass domain data between middleware layers or from middleware to handlers, creating invisible producer-consumer dependencies.

### Why It's Destructive

- **Invisible dependencies**: You cannot see from a function signature that it requires specific context values to be present. The compiler can't help you.
- **Ordering fragility**: Producers must run before consumers, but nothing enforces this. Reordering middleware silently breaks the system.
- **Silent degradation**: Consumers typically check `ok` and silently degrade (return nil, skip logic) when the value is missing, rather than failing loudly.
- **Testing burden**: Every test must manually construct context with the right values, and forgetting one produces confusing test failures.

### How to Detect

Look for:
- `context.WithValue` calls in middleware that set domain data (not request-scoped infrastructure like trace IDs)
- `ctx.Value(someKey)` reads in handlers, routers, or business logic
- Functions that accept `context.Context` but whose behavior depends on specific values being present in that context
- Multiple packages importing a shared context key package

### Known Instances

- `DiscoveredCapabilities`: Set in `pkg/vmcp/discovery/middleware.go`, read in 5 locations across `router/`, `server/`, `composer/`
- `HealthCheckMarker`: Set in `pkg/vmcp/health/`, read in `pkg/vmcp/auth/strategies/` to skip authentication — a behavioral side-channel through context
- `BackendInfo`: Set by audit middleware, then **mutated in place** by `server/backend_enrichment.go` — context values should be immutable

### What to Do Instead

- **Push data onto the `MultiSession` object.** The session is a well-typed, 1:1-with-request domain object that is decoupled from middleware and protocol concerns. Instead of stuffing discovered capabilities into context for 5 downstream readers, make them a field or method on `MultiSession`. Handlers already have access to the session — let them ask it for what they need. Data that is immutable and known early (e.g., discovered capabilities for the session) can be provided at construction time. Data that varies per-request can be set on the session at request time.
- **Pass domain data as explicit function/method parameters** when the session isn't the right home. If a handler needs something, accept it as a parameter.
- **For the health check marker specifically**: Pass a flag or option to the auth strategy rather than smuggling it through context. Authentication behavior should not depend on invisible context state.
- **Context is appropriate for**: trace IDs, cancellation, deadlines, and request-scoped infrastructure that is truly cross-cutting. It is not appropriate for domain data that specific code paths depend on.

---

## 2. Repeated Request Body Read/Restore

### Definition

Multiple middleware layers independently call `io.ReadAll(r.Body)`, parse the JSON, then restore the body with `io.NopCloser(bytes.NewReader(...))` for the next layer in the chain.

### Why It's Destructive

- **Fragile implicit contract**: If any middleware forgets to restore the body (or restores it incorrectly), all downstream handlers get an empty body with no error signal.
- **Redundant work**: The same JSON is parsed independently 3+ times per request (parser middleware, audit middleware, backend enrichment middleware).
- **Error-prone**: Each read/restore is a place where bugs can hide — partial reads, buffer misuse, or failure to restore on error paths.
- **Invisible coupling**: Adding a new middleware that reads the body requires understanding that this pattern exists and that restore is mandatory.

### How to Detect

Look for:
- `io.ReadAll(r.Body)` followed by `r.Body = io.NopCloser(bytes.NewReader(...))` in middleware
- Multiple middleware in the same chain that each parse JSON from the request body
- Comments like "restore body for next handler"

### Known Instances

- `pkg/mcp/parser.go:85` — reads body, parses JSON-RPC, restores
- `pkg/audit/auditor.go:182` — reads body for logging, restores
- `pkg/vmcp/server/backend_enrichment.go:26` — reads body to look up backend name, restores
- `pkg/mcp/tool_filter.go:229` — reads body, filters tools, restores (sometimes with modified content)

### What to Do Instead

- **Parse the request body once**, early in the pipeline. The `ParsedMCPRequest` in context already partially does this — extend it so that all downstream consumers use the parsed representation instead of re-reading raw bytes.
- **If raw bytes are needed** (e.g., for audit logging), cache them alongside the parsed form in the same early-parse step.
- **New middleware must never re-read the body.** If a middleware needs request data, it should consume the already-parsed representation.

---

## 3. God Object: Server Struct

### Definition

A single struct that owns and orchestrates too many distinct concerns, becoming the central point through which all logic flows. In vMCP, `Server` in `pkg/vmcp/server/server.go` manages HTTP lifecycle, session management, health monitoring, status reporting, capability aggregation, composite tools, MCP SDK integration, telemetry, audit, auth/authz, optimization, and graceful shutdown.

### Why It's Destructive

- **Cognitive overload**: 1,265 lines, 21 struct fields, multiple mutexes. Understanding any one concern requires reading the entire file.
- **Untestable in isolation**: You cannot instantiate the health subsystem without wiring up telemetry, sessions, discovery, and the MCP SDK.
- **Initialization complexity**: The 183-line `New()` constructor has a `nolint:gocyclo` suppression. `Start()` has implicit ordering dependencies between subsystem initialization steps.
- **Change amplification**: Modifying one concern (e.g., health monitoring) risks breaking unrelated concerns because they share the same struct and initialization path.

### How to Detect

Look for:
- Structs with 10+ fields spanning different domains
- Constructors longer than 50 lines or with `nolint:gocyclo`
- Files longer than 500 lines that handle multiple unrelated concerns
- Multiple `sync.Mutex` / `sync.RWMutex` fields protecting different subsets of state
- A `Start()` or `Run()` method that initializes many unrelated subsystems in sequence

### Known Instances

- `pkg/vmcp/server/server.go` — the primary instance. 14+ concerns, 21 fields, 183-line constructor, 137-line `Start()` method.

### What to Do Instead

- **Extract each concern into a self-contained module** that owns its own construction, initialization, and lifecycle. For example: `HealthSubsystem`, `SessionSubsystem`, `AuditSubsystem`, each with their own `New()` and `Start()`/`Stop()`.
- **Construct comprehensive units outside of server.go.** Each module should hide its internal complexity. Server.go should compose pre-built units, not orchestrate step-by-step construction of everything.
- **The Server struct should be a thin orchestrator**: receive pre-built subsystems, wire them together, and manage lifecycle. It should not know the internal details of how any subsystem works.
- **Use a builder or staged initialization pattern** where each stage's inputs and outputs are typed, making ordering constraints explicit and compiler-enforced.

---

## 4. Middleware Overuse for Non-Cross-Cutting Concerns

### Definition

Implementing business logic as HTTP middleware when the behavior is specific to certain request types, could be a direct method call, or belongs on a domain object.

### Why It's Destructive

- **Cognitive load**: A 10+ layer middleware chain means understanding request processing requires mentally tracing through all layers in order, including the wrapping-order vs. execution-order inversion.
- **Wasted work**: Method-specific middleware (e.g., annotation enrichment only applies to `tools/call`) runs on every request and bails early for irrelevant methods.
- **Invisible mutations**: Middleware that modify the request (body reads, context injection, response wrapping) create side effects that downstream code silently depends on.
- **Wrong home for the logic**: When middleware enriches or transforms data, it's often doing work that belongs on the domain objects themselves.

### How to Detect

Look for:
- Middleware that checks the request method/type and returns early for most cases
- Middleware whose sole purpose is to look something up and stuff it into context (see anti-pattern #1)
- Middleware that wraps `ResponseWriter` to intercept and transform responses
- Middleware that reads the request body (see anti-pattern #2)

### Known Instances

- `AnnotationEnrichmentMiddleware` (`server/annotation_enrichment.go`) — only processes `tools/call` but runs on every request
- `backendEnrichmentMiddleware` (`server/backend_enrichment.go`) — re-parses the request body just to set a field for audit logging
- Authorization middleware response filtering (`pkg/authz/middleware.go`) — buffers entire responses to filter list operations at the HTTP layer

### What to Do Instead

- **Reserve middleware for truly cross-cutting concerns**: recovery, telemetry, authentication — things that apply uniformly to all requests.
- **Push behavior onto domain objects.** For example, instead of an annotation enrichment middleware that runs on every request, make annotation lookup a method on `MultiSession` or the capabilities object. The handler that processes `tools/call` can call it directly.
- **For response transformation** (e.g., authz filtering list results): do it at the data layer before the response is serialized, rather than intercepting and re-parsing HTTP responses.
- **For audit enrichment**: make it a step in the audit logging call, not a separate middleware that mutates shared context state.

---

## 5. String-Based Error Classification

### Definition

Categorizing errors by pattern-matching against their `.Error()` string representation (e.g., `strings.Contains(errLower, "401 unauthorized")`) instead of using typed errors or sentinel values.

### Why It's Destructive

- **Fragile**: Error message text is not a stable API. Any upstream library or refactor that changes wording breaks the classification silently.
- **False positives**: Substring matching can match unrelated errors that happen to contain the same words.
- **Undermines proper error types**: The codebase already defines sentinel errors, but the string-matching fallbacks invite bypassing them.

### How to Detect

Look for:
- `strings.Contains(err.Error(), ...)` or `strings.Contains(strings.ToLower(err.Error()), ...)`
- Functions named `Is*Error` that don't use `errors.Is()` or `errors.As()`
- Comments like "fallback mechanism" or "pattern matching" near error classification code

### Known Instances

- `pkg/vmcp/errors.go:71-178` — `IsAuthenticationError()`, `IsAuthorizationError()`, `IsNetworkError()`, `IsTimeoutError()` all use string matching

### What to Do Instead

- **Use `errors.Is()` and `errors.As()` exclusively** for error classification.
- **Wrap errors at the boundary** where they enter the codebase from external libraries: `fmt.Errorf("backend auth failed: %w", ErrAuthentication)`.
- **Remove string-matching classification functions.** If they exist as a "fallback," the real fix is ensuring all error producers wrap with the correct sentinel.

---

## 6. Silent Error Swallowing

### Definition

Logging an error but not returning it to the caller, allowing the operation to proceed as if it succeeded.

### Why It's Destructive

- **Invisible failures**: The caller cannot distinguish success from failure, so it continues operating on invalid state.
- **Debugging requires log inspection**: Instead of errors propagating to where they can be handled, they're buried in log output that may not be monitored.
- **Cascading failures**: A silently swallowed error early in a flow causes confusing failures later, far from the root cause.

### How to Detect

Look for:
- `slog.Error(...)` or `slog.Warn(...)` followed by `return ""`, `return nil`, or `continue` instead of `return err`
- Functions that return zero values on error paths without returning the error
- Functions whose return type doesn't include `error` but that can fail internally
- Comments like "astronomically unlikely" or "best effort" near error handling

### Known Instances

- `pkg/vmcp/server/sessionmanager/session_manager.go:99-106` — `Generate()` returns `""` on storage failure instead of an error
- `pkg/vmcp/composer/workflow_engine.go:151` — state persistence failure downgraded to a warning

### What to Do Instead

- **Return errors to callers.** If the function signature can't accommodate an error (e.g., it satisfies an external interface), either change the interface, use a wrapper, or use a different pattern.
- **If an error truly can be ignored**, document the specific invariant that makes it safe with a comment explaining *why*, not just that it's being ignored.
- **Never assume a failure is "astronomically unlikely."** If it can happen, handle it.

---

## 7. SDK Coupling Leaking Through Abstractions

### Definition

Patterns forced by an external SDK (e.g., mark3labs/mcp-go) that leak beyond the adapter boundary and shape internal architecture.

### Why It's Destructive

- **Viral complexity**: The two-phase session creation pattern (Generate placeholder, then CreateSession replaces it) exists because of the SDK's hook-based lifecycle. If this pattern leaks into session management, health checking, or routing, switching SDKs becomes prohibitively expensive.
- **Race windows**: The two-phase pattern creates a window between placeholder creation and session finalization where the session can be terminated, requiring fragile double-check logic.
- **Tight coupling**: Internal code that knows about placeholders, hooks, or SDK-specific lifecycle stages is coupled to the SDK's design decisions.

### How to Detect

Look for:
- Code outside `adapter/` or integration layers that references SDK-specific concepts (hooks, placeholders, two-phase creation)
- Session management code with "re-check" or "double-check" patterns that exist because of race windows introduced by the SDK's lifecycle
- Comments referencing specific SDK behavior or limitations

### Known Instances

- `pkg/vmcp/server/sessionmanager/session_manager.go` — the two-phase `Generate()` / `CreateSession()` pattern with double-check logic (lines 144-167)

### What to Do Instead

- **Accept the two-phase pattern as a necessary evil** at the SDK boundary, but keep it contained.
- **The adapter layer should be thin and isolated.** Internal session management should present a clean `CreateSession() -> (Session, error)` API. The two-phase dance should be invisible to callers.
- **Avoid adding new code that depends on SDK-specific lifecycle stages.** If you find yourself writing code that needs to know about placeholders or hooks, you're letting SDK coupling leak.
- **When evaluating SDK alternatives**, the adapter boundary should be the only code that changes.

---

## 8. Configuration Object Passed Everywhere

### Definition

A single large `Config` struct (13+ fields, each pointing to nested sub-configs) that gets threaded through constructors and methods across the codebase, even though each consumer only needs a small subset.

### Why It's Destructive

- **Unclear dependencies**: When a constructor accepts `*Config`, you can't tell which fields it actually uses without reading the implementation.
- **Nil pointer risk**: Every field is a pointer to a nested struct. Accessing `cfg.IncomingAuth.OIDC.ClientSecretEnv` requires nil checks at each level, and missing checks cause panics.
- **Change amplification**: Adding a new config field affects the entire config surface area, even though only one consumer needs it.
- **Testing burden**: Tests must construct the full config struct even though the code under test only reads 2-3 fields.

### How to Detect

Look for:
- Constructors or functions that accept `*config.Config` or `*server.Config` but only access a few fields
- Nil checks on config sub-fields scattered through business logic
- Test setup code that builds large config structs with mostly zero/nil fields
- Config structs with 10+ fields that span different domains

### Known Instances

- `pkg/vmcp/config/config.go:89-159` — 13 fields including auth, aggregation, telemetry, audit, optimizer
- `pkg/vmcp/server/server.go:83-165` — `server.Config` wraps the vmcp config and adds more fields

### What to Do Instead

- **Each subsystem should accept only the config it needs.** Define small config types close to their consumer: `NewHealthMonitor(cfg health.MonitorConfig)` not `NewHealthMonitor(cfg *vmcp.Config)`.
- **The top-level config is for parsing/loading only.** Decompose it into per-subsystem configs before passing to constructors.
- **Config decomposition should happen at the composition root** (currently server.go, ideally a dedicated wiring function). Each subsystem receives exactly what it needs and nothing more.

---

## 9. Mutable Shared State Through Context

### Definition

Storing a mutable struct in context and having multiple middleware modify it in place, rather than treating context values as immutable.

### Why It's Destructive

- **Violates context contract**: Context values are conventionally immutable. Code that reads a context value doesn't expect it to change after retrieval.
- **Hidden mutation**: One middleware mutates state that another middleware set, with no signal in the code that this coupling exists.
- **Concurrency risk**: If the same context is shared across goroutines (e.g., in streaming/SSE scenarios), in-place mutation of context values is a data race.

### How to Detect

Look for:
- Middleware that calls `someStruct.Field = value` on a struct retrieved from context (not replaced with `context.WithValue`)
- Structs stored in context with exported mutable fields
- Multiple middleware that both read and write the same context value

### Known Instances

- `pkg/vmcp/server/backend_enrichment.go:54` — retrieves `BackendInfo` from context (set by audit middleware) and mutates its `BackendName` field in place

### What to Do Instead

- **Treat context values as immutable.** If downstream code needs to add information, create a new value and set it with `context.WithValue`.
- **Better yet, don't use context for this at all** (see anti-pattern #1). Pass the data explicitly so that mutation points are visible in the code.
- **If shared state is truly needed**, use a dedicated request-scoped struct with clear ownership semantics and document which code is allowed to write to which fields.
