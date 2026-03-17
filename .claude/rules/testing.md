---
paths:
  - "*_test.go"
  - "test/**"
---

# Testing Rules

Applies to test files and test directories.

## Testing Strategy

- **`pkg/` packages**: Thorough unit test coverage (business logic lives here)
- **`cmd/thv/app/`**: Minimal unit tests (only output formatting, flag validation helpers)
- **CLI commands**: Tested primarily with E2E tests (`test/e2e/`), not unit tests
- **Integration tests**: Ginkgo/Gomega in package test files
- **Operator tests**: Chainsaw tests in `test/e2e/chainsaw/operator/`

## Mock Generation

- Use `go.uber.org/mock` (gomock) framework — never hand-write mocks
- Generate mocks with `mockgen` and place in `mocks/` subdirectories
- Generate with: `task gen`

## Assertions

- Prefer `require.NoError(t, err)` (from `github.com/stretchr/testify`) instead of `t.Fatal`

## Test Quality

1. **Structure**: Prefer table-driven (declarative) tests over imperative tests
2. **Redundancy**: Avoid overlapping test cases exercising the same code path
3. **Value**: Every test must add meaningful coverage — remove tests that don't
4. **Consolidation**: Consolidate small test functions into a single table-driven test when they test the same function
5. **Naming**: Use clear, descriptive test names that convey the scenario
6. **Boilerplate**: Minimize setup code; extract shared setup into helpers with `t.Helper()`

## Test Scope

Tests must only test code in the package under test. Do NOT test behavior of dependencies, external packages, or transitive functionality.

## Temp Directories

When tests need a temp directory that must pass validation rejecting symlinks, use a resolved temp dir:
```go
dir := t.TempDir()
resolved, _ := filepath.EvalSymlinks(dir)
```
On macOS, `t.TempDir()` often returns paths through `/var/folders/...` which is a symlink. See `pkg/skills/project_root_test.go` for a `resolvedTempDir(t)` helper.

## Environment Variables

Write tests isolated from other tests that may set the same env vars. Use `t.Setenv()` which auto-restores.

## Port Numbers

Use random ports (e.g., `net.Listen("tcp", ":0")`) to let the OS assign a free port. Do not use hardcoded port numbers — even large ones can clash with running services.
