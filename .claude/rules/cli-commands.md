---
paths:
  - "cmd/thv/app/**"
---

# CLI Command Rules

Applies to CLI command files in `cmd/thv/app/`.

## Thin Wrapper Principle

**CRITICAL**: CLI commands must be thin wrappers that delegate to business logic in `pkg/`.

The CLI layer is responsible ONLY for:
- Parsing flags and arguments (using Cobra)
- Calling business logic functions from `pkg/` packages
- Formatting output (text tables or JSON)
- Displaying errors to users

Business logic MUST live in `pkg/` packages (e.g., `pkg/workloads/`, `pkg/registry/`, `pkg/groups/`, `pkg/runner/`).

**Example**: `cmd/thv/app/list.go` delegates to `pkg/workloads.Manager.ListWorkloads()`

## Usability Requirements

- **Silent success**: No output on successful operations unless `--debug` is used
- **Actionable error messages**: Include hints pointing to relevant commands
- **Consistent flag names** across commands
- **Both output formats**: Support `--format json` and `--format text`
- **Helper functions**: Use `AddFormatFlag`, `AddGroupFlag`, `AddAllFlag` for common flags
- **Shell completion**: Include `ValidArgsFunction`

## Adding New Commands

1. Put business logic in `pkg/` first
2. Create command file in `cmd/thv/app/` as a thin wrapper
3. Follow patterns from existing commands (e.g., `list.go`, `run.go`, `status.go`)
4. Add command to `NewRootCmd()` in `commands.go`
5. Implement validation in `PreRunE`
6. Support both text and JSON output formats
7. Write E2E tests (primary testing strategy for CLI)
8. Update CLI documentation with `task docs`

## Testing

CLI commands are tested with **E2E tests** (`test/e2e/`), not unit tests. Only write CLI unit tests for output formatting or validation helper functions.
