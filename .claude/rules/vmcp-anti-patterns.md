---
description: Checks vMCP code changes against known anti-patterns
globs:
  - "pkg/vmcp/**/*.go"
  - "cmd/vmcp/**/*.go"
---

# vMCP Anti-Pattern Rule

When reviewing or writing code in `pkg/vmcp/` or `cmd/vmcp/`, check changes against the anti-pattern catalog in `docs/vmcp-anti-patterns.md`.

Flag any code that introduces or expands these patterns:

1. **Context Variable Coupling** — using `context.WithValue`/`ctx.Value` for domain data
2. **Repeated Request Body Read/Restore** — multiple middleware calling `io.ReadAll(r.Body)` then restoring
3. **God Object: Server Struct** — single struct owning too many concerns (10+ fields spanning domains)
4. **Middleware Overuse** — business logic in middleware instead of domain objects or direct calls
5. **String-Based Error Classification** — `strings.Contains(err.Error(), ...)` instead of `errors.Is`/`errors.As`
6. **Silent Error Swallowing** — logging errors but not returning them to callers
7. **SDK Coupling Leaking Through Abstractions** — SDK-specific patterns escaping adapter boundaries
8. **Configuration Object Passed Everywhere** — threading a large `Config` struct through code that only needs a subset
9. **Mutable Shared State Through Context** — modifying structs retrieved from context in place

Refer to `docs/vmcp-anti-patterns.md` for full details, detection guidance, and recommended alternatives for each pattern.
