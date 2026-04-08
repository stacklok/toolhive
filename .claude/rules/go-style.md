---
paths:
  - "**/*.go"
---

# Go Style Rules

Applies to all Go files in the project.

## File Organization
- Public methods in the top half of files, private methods in the bottom half
- Use interfaces for testability and runtime abstraction
- Separate business logic from transport/protocol concerns
- Keep packages focused on single responsibilities

## Interface Design

Check these whenever adding a method to an interface or defining a new type:

- **Minimal surface**: Don't add interface methods that duplicate the semantics of existing ones. If an existing method already answers the question (possibly with a side effect), don't add a separate method for the same check.
- **No silent no-ops**: A no-op that silently breaks callers who depend on the method working is a sign the interface is too broad. Narrow the interface or use a separate capability interface. Benign no-ops (e.g., `Close()` on an in-memory store) are fine.
- **Option pattern must be compile-time safe**: Never define a local anonymous interface inside an option and type-assert against it to check capability — a silent no-op results if the target doesn't implement it. (Returning an explicit error from an option for input validation is fine.) Two typesafe approaches:
  - *Config struct field*: put the setting on the config struct (e.g., `types.Config.SessionStorage`) so all consumers see it at compile time.
  - *Typed functional option*: use `func(*ConcreteType)` so the option only compiles against the correct receiver.
  If you need to cast inside an option to check whether the target supports it, the option is on the wrong abstraction. See #4638.
- **Avoid parallel types that drift**: Don't define a separate config/data type that mirrors an existing one. Embed or reuse the original — two parallel structs require a conversion step and will diverge over time.

## Resource Leaks

Always pair resource acquisition with explicit release. Common patterns that leak:

- Goroutines with no exit condition or cancellation path
- Caches and maps that grow without a capacity limit or eviction policy
- Connections, files, or handles opened without a corresponding `Close()` (use `defer`)
- Tickers and timers whose `Stop()` is never called

When reviewing code that acquires a resource, ask: where does this get released, and what happens if the normal release path is never reached?

## Linting

All lint rules must be followed. Run `task lint-fix` before submitting. Do not suppress linter warnings with `//nolint` directives unless the violation is a confirmed false positive — fix the root cause instead.

## Validate Parsed Results

A successful parse (`err == nil`) only means the input was syntactically acceptable to the parser — not that it meets your requirements. Always validate the parsed result against what you actually need. Standard library parsers routinely accept more inputs than a given call site should allow.

## SPDX License Headers

All Go files require SPDX headers at the top:
```go
// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0
```

Use `task license-check` to verify, `task license-fix` to add automatically.

## Immutable Variable Assignment

Prefer immediately-invoked anonymous functions over mutable variables across branches:

```go
// Good: Immutable assignment
phase := func() PhaseType {
    if someCondition {
        return PhaseA
    }
    return PhaseDefault
}()

// Avoid: Mutable variable across branches
var phase PhaseType
if someCondition {
    phase = PhaseA
} else {
    phase = PhaseDefault
}
```

## Error Handling

- Return errors by default — never silently swallow errors
- Comment ignored errors — explain why and typically log them
- No sensitive data in errors (no API keys, credentials, tokens, passwords)
- Use `errors.Is()` or `errors.As()` for error inspection (they properly unwrap errors)
- Use `fmt.Errorf` with `%w` to preserve error chains; don't wrap excessively
- Use `recover()` sparingly — only at top-level API/CLI boundaries

## Package API Surface

- Packages expose interfaces, result types, and constructors
- Constructors accept dependencies (interfaces/functions), runtime information
  (identity, context), and config (in the caller's terms)
- Start without intermediate config types — introduce them when a concrete need
  arises (runtime shape meaningfully differs from input, multiple config sources,
  resolved secrets). Don't create a public type just to hold parsed values
  between two internal functions
- Use `internal/` subpackages for implementation details that callers should not
  depend on
- Public functions are a smell: if a function converts external types to internal
  state, ask whether it can be folded into a constructor or belongs in the
  caller's package

## Document Architectural Constraints on Exported Functions

When an exported function or constructor changes behavior based on injected infrastructure (storage backend, transport mode, external client), its doc comment must state what the injection does and does not solve. Callers cannot be expected to infer distributed-system constraints from the implementation.

Include at minimum:
- What the injected component enables (e.g., cross-replica metadata sharing).
- What it does *not* solve (e.g., cross-replica message delivery, fan-out).
- Any caller responsibility that follows (e.g., session affinity at the load balancer).

## Concurrency Comments

Keep comments about mutexes, locks, and concurrency accurate — they are easy to get wrong and mislead future readers:

- Only say a lock "must be held" or "is already held" if you have verified it at that call site.
- Do not claim an operation would deadlock without confirming that the lock in question would actually be re-acquired.
- When a comment describes a concurrency invariant (e.g., "called with mu held"), add it to the function's doc comment so it travels with the signature, not inline at the call site.

## Logging

- **Silent success** — no output at INFO or above for successful operations
- **DEBUG** for diagnostics (runtime detection, state transitions, config values)
- **INFO** sparingly — only for long-running operations like image pulls
- **WARN** for non-fatal issues (deprecations, fallback behavior, cleanup failures)
- **Never log** credentials, tokens, API keys, or passwords

## Prefer Existing Code and Packages Over From-Scratch Implementations

Before implementing any non-trivial functionality from scratch:

1. **Search the toolhive repo first** — check if an existing method, utility, or package already provides the functionality or something close enough to extend.
2. **Check the Go standard library** — the stdlib covers a wide surface area; prefer it over third-party packages when it fits.
3. **Look for existing Go packages** — search for well-maintained OSS libraries that solve the problem before writing custom implementations.

Implementing from scratch should be a last resort, justified by a specific gap no existing solution fills.
