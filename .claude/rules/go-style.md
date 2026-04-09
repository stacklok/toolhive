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

## Logging

- **Silent success** — no output at INFO or above for successful operations
- **DEBUG** for diagnostics (runtime detection, state transitions, config values)
- **INFO** sparingly — only for long-running operations like image pulls
- **WARN** for non-fatal issues (deprecations, fallback behavior, cleanup failures)
- **Never log** credentials, tokens, API keys, or passwords
