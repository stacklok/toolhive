---
paths:
  - "**/go.mod"
---

# Go Module Conventions

Applies to all `go.mod` files in the project.

## Go Directive Version Format

- **Never specify a patch version in the `go` directive** — use `go 1.23` not `go 1.23.4`. The patch version is irrelevant to module compatibility and creates unnecessary churn in diffs when toolchains update.
- If a toolchain directive is needed, it goes on a separate `toolchain` line, not embedded in the `go` line.
