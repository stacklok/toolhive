# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Available Subagents

ToolHive uses specialized AI subagents for different aspects of development. These agents are configured in `.claude/agents/` and MUST be invoked when you need to perform tasks that come under their expertise:

### Core Development Agents

- **toolhive-expert**: Deep expert on ToolHive architecture, codebase structure, design patterns, and implementation guidance. Use for general ToolHive questions, architecture decisions, and navigating the codebase.

- **golang-code-writer**: Expert Go developer for writing clean, idiomatic Go code. Use when creating new functions, structs, interfaces, or complete packages.

- **unit-test-writer**: Specialized in writing comprehensive unit tests for Go code. Use when you need thorough test coverage for functions, methods, or components.

- **code-reviewer**: Reviews code for ToolHive best practices, security patterns, Go conventions, and architectural consistency. Use after significant code changes.

- **tech-lead-orchestrator**: Provides architectural oversight, task delegation, and technical leadership for code development projects. Use for complex features or architectural decisions.

### Specialized Domain Agents

- **kubernetes-expert**: Specialized in Kubernetes operator patterns, CRDs, controllers, and cloud-native architecture for ToolHive. Use for operator-specific questions or K8s resources.

- **mcp-protocol-expert**: Specialized in MCP (Model Context Protocol) specification, transport implementations, and protocol compliance. Use when working with MCP transports or protocol details.

- **oauth-expert**: Specialized in OAuth 2.0, OIDC, token exchange, and authentication flows for ToolHive. Use for auth/authz implementation.

- **site-reliability-engineer**: Expert on observability and monitoring (logging, metrics, tracing) including OpenTelemetry instrumentation. Use for telemetry and monitoring setup.

### Support Agents

- **documentation-writer**: Maintains consistent documentation, updates CLI docs, and ensures documentation matches code behavior. Use when updating docs.

- **security-advisor**: Provides security guidance for coding tasks, including code reviews, architecture decisions, and secure implementation patterns.

### When to Use Subagents

Invoke specialized agents when:
- You need expertise in their specific domain (e.g., Kubernetes, OAuth, MCP protocol)
- Writing new code (use golang-code-writer)
- Creating tests (use unit-test-writer)
- Orchestrating the different tasks to other subagents that are required for completing work asked by the user (use tech-lead-orchestrator)
- Reviewing code that is written (use code-reviewer)
- Working with observability/monitoring (use site-reliability-engineer)

The agents work together - for example, tech-lead-orchestrator might delegate to golang-code-writer for implementation and then to code-reviewer for validation.

## Project Overview

ToolHive is a lightweight, secure manager for MCP (Model Context Protocol: https://modelcontextprotocol.io) servers written in Go. It provides three main components:

- **CLI (`thv`)**: Main command-line interface for managing MCP servers locally
- **Kubernetes Operator (`thv-operator`)**: Manages MCP servers in Kubernetes clusters
- **Proxy Runner (`thv-proxyrunner`)**: Handles proxy functionality for MCP server communication

The application acts as a thin client for Docker/Podman/Colima Unix socket API, providing container-based isolation for running MCP servers securely. It also builds on top of the MCP Specification: https://modelcontextprotocol.io/specification.

## Build and Development Commands

### Essential Commands

```bash
# Build the main binary
task build

# Install binary to GOPATH/bin
task install

# Run linting
task lint

# Fix linting issues automatically
task lint-fix

# Run unit tests (excluding e2e tests)
task test

# Run tests with coverage analysis
task test-coverage

# Run end-to-end tests
task test-e2e

# Run all tests (unit and e2e)
task test-all

# Generate mocks
task gen

# Generate CLI documentation
task docs

# Build container images
task build-image
task build-egress-proxy
task build-all-images
```

### Running Tests

- Unit tests: `task test` (primarily for `pkg/` business logic)
- E2E tests: `task test-e2e` (requires build first, primary testing for CLI)
- All tests: `task test-all`
- With coverage: `task test-coverage`

The test framework uses Ginkgo and Gomega for BDD-style testing.

**When adding CLI features**: Always run E2E tests (`task test-e2e`) to verify the full user experience. Unit tests should primarily cover business logic in `pkg/` packages.

## Architecture Overview

### Core Components

1. **CLI Application (`cmd/thv/`)**
   - Main entry point in `main.go`
   - Command definitions in `app/` directory
   - Uses Cobra for CLI framework
   - Key commands: run, list, stop, rm, proxy, restart, serve, version, logs, secret, inspector, mcp

2. **Kubernetes Operator (`cmd/thv-operator/`)**
   - CRD definitions in `api/v1alpha1/`
   - Controller logic in `controllers/`
   - Follows standard Kubernetes operator patterns

3. **Core Packages (`pkg/`)**
   - `api/`: REST API server and handlers
   - `auth/`: Authentication (anonymous, local, OAuth/OIDC)
   - `authz/`: Authorization using Cedar policy language
   - `client/`: Client configuration and management
   - `container/`: Container runtime abstraction (Docker/Kubernetes)
   - `registry/`: MCP server registry management
   - `runner/`: MCP server execution logic
   - `transport/`: Communication protocols (HTTP, SSE, stdio, streamable)
   - `workloads/`: Workload lifecycle management

4. **Virtual MCP Server (`cmd/vmcp/`)**
   - Core packages in `pkg/vmcp/`:
     - `aggregator/`: Backend discovery (CLI/K8s), capability querying, conflict resolution (prefix/priority/manual)
     - `router/`: Routes MCP requests (tools/resources/prompts) to backends
     - `auth/`: Two-boundary auth model (incoming: clients→vMCP; outgoing: vMCP→backends)
     - `composer/`: Multi-step workflow execution with DAG-based parallel/sequential support
     - `config/`: Platform-agnostic config model (works for CLI YAML and K8s CRDs)
     - `server/`: HTTP server implementation with session management and auth middleware
     - `client/`: Backend MCP client using mark3labs/mcp-go SDK
     - `cache/`: Token caching with pluggable backends
   - K8s CRDs in `cmd/thv-operator/api/v1alpha1/`:
     - `VirtualMCPServer`: Main vMCP server resource (GroupRef, auth configs, aggregation, composite tools)
     - `VirtualMCPCompositeToolDefinition`: Reusable composite tool workflows
     - `MCPGroup`: Defines backend workload collections
     - `MCPServer`, `MCPRegistry`, `MCPRemoteProxy`: Backend workload types
     - `MCPExternalAuthConfig`, `ToolConfig`: Supporting config resources

### Key Design Patterns

- **Factory Pattern**: Used extensively for creating runtime-specific implementations (Docker/Colima/Podman vs Kubernetes)
- **Interface Segregation**: Clean abstractions for container runtimes, transports, and storage
- **Middleware Pattern**: HTTP middleware for auth, authz, telemetry
- **Observer Pattern**: Event system for audit logging

### Transport Types

ToolHive supports multiple MCP transport protocols (https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
- **stdio**: Standard input/output
- **HTTP**: Traditional HTTP requests
- **SSE**: Server-Sent Events for streaming
- **streamable**: Custom streamable HTTP protocol

### Security Model

- Container-based isolation for all MCP servers
- Cedar-based authorization policies
- Secret management with multiple backends (1Password, encrypted storage)
- Certificate validation for container images
- OIDC/OAuth2 authentication support

## Testing Strategy

### Test Organization

- **Unit tests**: Located alongside source files (`*_test.go`)
  - **`pkg/` packages**: Thorough unit test coverage (this is where business logic lives)
  - **`cmd/thv/app/`**: Minimal unit tests (only for output formatting, flag validation helpers)
- **Integration tests**: In individual package test files
- **End-to-end tests**: Located in `test/e2e/`
  - **Primary testing strategy for CLI commands**
  - Tests the compiled binary with real user workflows
  - Covers flag parsing, business logic execution, and output formatting in one test
- **Operator tests**: Chainsaw tests in `test/e2e/chainsaw/operator/`

### Testing Practices

- When generating tests for code which touches the state files on disk, ensure
  that they write state into isolated temporary directories to prevent clashes
  between different tests.
- Also take care of tests which set env vars. Try and write the tests in a way
  that the tests are isolated from other tests which may set the same env vars.

### Test Quality Guidelines

When writing or reviewing tests:

1. **Structure**: Prefer table-driven (declarative) tests over imperative tests for better readability and maintainability.
2. **Redundancy**: Avoid overlapping test cases that exercise the same code path.
3. **Value**: Every test must add meaningful coverage. Remove tests that don't.
4. **Consolidation**: Consolidate multiple small test functions into a single table-driven test when they test the same function or behavior.
5. **Naming**: Use clear, descriptive test names that convey the scenario being tested.
6. **Boilerplate**: Minimize setup code. Extract shared setup into helpers or use `t.Helper()` functions.

#### Critical Requirements

- **Test scope**: Tests must only test the code in the package under test. Do not test behavior of dependencies, external packages, or transitive functionality. If a test is exercising code outside the package, flag it for removal or refactoring.
- **Mocking**: Use mocks for external dependencies. Mocks must use the `go.uber.org/mock` (gomock) framework, consistent with ToolHive conventions. Generate mocks with `mockgen` and place them in `mocks/` subdirectories.
- **Assertions**: Prefer `require.NoError(t, err)` instead of `t.Fatal` for error checking. The `require` package (from `github.com/stretchr/testify`) provides better error messages and consistency.

### CLI Testing Philosophy

**CLI commands should be tested primarily with E2E tests, not unit tests.**

- Business logic in `pkg/` packages → Unit tests with mocks
- CLI commands in `cmd/thv/app/` → E2E tests with compiled binary
- Only write CLI unit tests for output formatting or validation helper functions

Why?
- CLI is a thin layer (just glue code calling `pkg/`)
- E2E tests verify real user experience
- Better coverage with less maintenance burden
- Catches integration issues that unit tests miss

See `docs/cli-best-practices.md` for detailed testing guidance.

### Mock Generation

The project uses `go.uber.org/mock` for generating mocks. Mock files are located in `mocks/` subdirectories within each package.

## Configuration

- Uses Viper for configuration management
- Configuration files and state stored in platform-appropriate directories
- Supports environment variable overrides
- Client configuration stored in `~/.toolhive/` or equivalent

### Container Runtime Configuration

ToolHive automatically detects available container runtimes in the following order: Podman, Colima, Docker. You can override the default socket paths using environment variables:

- `TOOLHIVE_PODMAN_SOCKET`: Custom Podman socket path
- `TOOLHIVE_COLIMA_SOCKET`: Custom Colima socket path (default: `~/.colima/default/docker.sock`)
- `TOOLHIVE_DOCKER_SOCKET`: Custom Docker socket path

**Colima Support**: Colima is fully supported as a Docker-compatible runtime. ToolHive will automatically detect Colima installations on macOS and Linux systems.

### Remote Workload Health Checks

ToolHive performs health checks on workloads to verify they are running and responding correctly. The behavior differs based on workload type:

- **Local workloads**: Health checks are always enabled
- **Remote workloads**: Health checks are disabled by default (to avoid unnecessary network traffic), but can be enabled with:
  - `TOOLHIVE_REMOTE_HEALTHCHECKS`: Set to `true` or `1` to enable health checks for remote MCP server proxies

Health check implementation: `pkg/transport/http.go:shouldEnableHealthCheck`

## Development Guidelines

### Code Organization

- Follow Go standard project layout
- Use interfaces for testability and runtime abstraction
- Separate business logic from transport/protocol concerns
- Keep packages focused on single responsibilities
- In Go codefiles, keep public methods to the top half of the file, and private
  methods to the bottom half of the file.

### CLI Architecture Principle

**CRITICAL**: CLI commands in `cmd/thv/app/` must be thin wrappers that delegate to business logic in `pkg/`.

The CLI layer is responsible ONLY for:
- Parsing flags and arguments (using Cobra)
- Calling business logic functions from `pkg/` packages
- Formatting output (text tables or JSON)
- Displaying errors to users

Business logic MUST live in `pkg/` packages:
- `pkg/workloads/`: Workload lifecycle management
- `pkg/registry/`: Registry operations
- `pkg/groups/`: Group management
- `pkg/runner/`: Server execution logic
- etc.

**Why this matters**:
- Business logic in `pkg/` is thoroughly unit tested
- CLI layer is tested with E2E tests (testing real user workflows)
- Separation makes code reusable (API can use same `pkg/` logic)
- Easier to maintain and refactor

**Example**: See `cmd/thv/app/list.go` which delegates to `pkg/workloads.Manager.ListWorkloads()`

### Operator Development

When working on the Kubernetes operator:
- CRD attributes are used for business logic that affects operator behavior
- PodTemplateSpec is used for infrastructure concerns (node selection, resources)
- See `cmd/thv-operator/DESIGN.md` for detailed decision guidelines

### Dependencies

- Primary container runtime: Docker API
- Web framework: Chi router
- CLI framework: Cobra
- Configuration: Viper
- Testing: Ginkgo/Gomega
- Kubernetes: controller-runtime
- Telemetry: OpenTelemetry

## Common Development Tasks

### Adding New Commands

When adding new CLI commands, follow the comprehensive guide in `docs/cli-best-practices.md` which covers:
- Command structure and design patterns
- Flag naming conventions and common patterns
- Output formatting (JSON/text)
- Error messages with actionable hints
- Shell completion
- Testing requirements

Quick steps:
1. **Put business logic in `pkg/` first** - Create or use existing packages for the actual work
2. Create command file in `cmd/thv/app/` as a thin wrapper
3. Follow patterns from existing commands (e.g., `list.go`, `run.go`, `status.go`)
4. Add command to `NewRootCmd()` in `commands.go`
5. Add flags using helper functions (`AddFormatFlag`, `AddGroupFlag`, etc.)
6. Implement validation in `PreRunE`
7. CLI should only: parse flags, call `pkg/` functions, format output
8. Support both text and JSON output formats
9. **Write E2E tests** (primary testing strategy)
10. Write minimal unit tests only for output formatting or validation helpers
11. Update CLI documentation with `task docs`

**CRITICAL PRINCIPLES**:
- **Business logic MUST be in `pkg/` packages, NOT in CLI code**
- CLI commands are thin wrappers (flag parsing → call pkg/ → format output)
- Test business logic with unit tests in `pkg/`
- Test CLI with E2E tests using the compiled binary
- Minimal unit tests for CLI layer (only output formatting/validation)

**Usability requirements** (see `docs/cli-best-practices.md`):
- Silent success (no output unless there's an error or `--debug` is used)
- Actionable error messages with hints pointing to relevant commands
- Consistent flag names across commands
- Support for `--format json` and `--format text`

### Adding New Transport

1. Implement transport interface in `pkg/transport/`
2. Add factory registration
3. Update runner configuration
4. Add comprehensive tests

### Working with Containers

The container abstraction supports Docker, Colima, Podman, and Kubernetes runtimes. When adding container functionality:
- Implement the interface in `pkg/container/runtime/types.go`
- Add runtime-specific implementations in appropriate subdirectories
- Use factory pattern for runtime selection

## Commit Guidelines

Follow conventional commit format:
- Separate subject from body with blank line
- Limit subject line to 50 characters
- Use imperative mood in subject line
- Capitalize subject line
- Do not end subject line with period
- Use body to explain what and why vs. how

## Pull Request Guidelines

### PR Size and Scope

**CRITICAL**: Before creating a pull request, Claude MUST evaluate the size and scope of changes to ensure they are reviewable.

#### Size Limits

Maximum recommended changes per PR:
- **400 lines of code changes** (excluding tests, generated code, and documentation)
- **10 files changed** (excluding test files, generated code, and documentation)

These limits ensure PRs are:
- Reviewable in a reasonable time (under 1 hour)
- Focused on a single logical change
- Less likely to introduce bugs
- Easier to revert if needed

#### When Changes Exceed Limits

When changes exceed these limits, Claude MUST:
1. **Stop and inform the user** that the PR would be too large for effective review
2. **Analyze the changes** and identify logical boundaries for splitting
3. **Propose a split strategy** with multiple smaller PRs
4. **Ask the user** which part they want to create first
5. **Create a tracking issue or checklist** for the remaining work

Use the `/split-pr` skill to help with this analysis.

#### Check PR Size Before Creation

Before creating a PR, run these commands to check size:

```bash
# Count files changed (excluding generated files and docs)
git diff main...HEAD --name-only | grep -v 'vendor/' | grep -v '\.pb\.go$' | grep -v 'zz_generated' | grep -v '^docs/' | wc -l

# Count lines changed (excluding generated files, vendor, docs)
git diff main...HEAD --stat -- . ':(exclude)vendor/*' ':(exclude)*.pb.go' ':(exclude)zz_generated*' ':(exclude)docs/*' | tail -1
```

If the numbers exceed limits, propose splitting before creating the PR.

#### Atomic PRs

Each PR should represent **one logical change**:
- ✅ One feature at a time
- ✅ One bug fix at a time
- ✅ One refactoring at a time
- ✅ Tests included with their related code changes
- ❌ Multiple unrelated changes bundled together
- ❌ Refactoring mixed with new features
- ❌ Bug fixes mixed with feature work

#### Exception Cases

Large PRs are acceptable for:
- **Generated code updates** (proto files, CRDs, mocks) - but confirm with the user first
- **Dependency updates** (go.mod changes, vendor updates)
- **Large refactorings that cannot be safely split** (with explicit user approval)
- **Documentation-only updates** (changes to `docs/` directory)
- **Test-only changes** (adding missing test coverage)

For these cases, still inform the user about the size and ask for confirmation before creating the PR.

#### PR Split Strategy

When splitting large changes:

1. **Identify dependencies**: Which changes depend on others?
2. **Create foundation PRs first**: Refactoring, new interfaces, shared utilities
3. **Build features on top**: Feature PRs that use the foundation
4. **Keep each PR independently testable**: Each PR should pass all tests
5. **Use feature flags if needed**: For incomplete features that span multiple PRs

**Example split**:
- PR 1: Add new interface and base implementation (foundation)
- PR 2: Migrate existing code to use new interface (refactoring)
- PR 3: Add new feature using the interface (feature)

### PR Creation Workflow

When a user asks to create a PR:

1. **Check size first** using the commands above
2. **If too large**: Propose split strategy, wait for user decision
3. **If reasonable size**: Proceed with PR creation following standard git workflow
4. **Include context**: Ensure PR description explains what/why, links to issues
5. **Verify tests pass**: Run relevant tests before creating PR

## Architecture Documentation

ToolHive maintains comprehensive architecture documentation in `docs/arch/`. When making changes that affect architecture, you MUST update the relevant documentation to keep it in sync with the code.

### When to Update Documentation

Update architecture docs when you:
- Add or modify core components (workloads, transports, middleware, permissions, registry, groups)
- Change system design patterns, abstractions, or interfaces
- Modify the RunConfig schema or permission profiles
- Update the Kubernetes operator or CRDs
- Add new architectural concepts or change how components interact

### Which Documentation to Update

| Code Changes | Documentation Files |
|--------------|---------------------|
| CLI commands (`cmd/thv/app/`) | Follow `docs/cli-best-practices.md`; update generated docs with `task docs` |
| Core packages (`pkg/workloads/`, `pkg/transport/`, `pkg/permissions/`, `pkg/registry/`, `pkg/groups/`) | `docs/arch/02-core-concepts.md` + topic-specific doc (e.g., `08-workloads-lifecycle.md`, `06-registry-system.md`) |
| RunConfig schema (`pkg/runner/config.go`) | `docs/arch/05-runconfig-and-permissions.md` |
| Middleware (`pkg/*/middleware.go`) | `docs/middleware.md` and `docs/arch/02-core-concepts.md` |
| Operator or CRDs (`cmd/thv-operator/`, `api/`) | `docs/arch/09-operator-architecture.md` |
| Major architectural changes | `docs/arch/00-overview.md` and `docs/arch/02-core-concepts.md` |

### Documentation Guidelines

- Include code references using `file_path` format (without line numbers since they change frequently)
- Update Mermaid diagrams when component interactions change
- Keep terminology consistent with definitions in `docs/arch/02-core-concepts.md`
- Cross-reference related documentation
- Explain design decisions and the "why" behind changes

For the complete documentation structure and navigation, see `docs/arch/README.md`.

## Development Best Practices

- **CLI Commands**:
  - **MUST** follow the guidelines in `docs/cli-best-practices.md` for all CLI command development
  - Ensure silent success (no output on successful operations unless `--debug` is used)
  - Provide actionable error messages with hints
  - Support both `--format json` and `--format text` output
  - Use helper functions for common flags (`AddFormatFlag`, `AddGroupFlag`, `AddAllFlag`)
  - Include shell completion with `ValidArgsFunction`
- **Linting**:
  - Prefer `lint-fix` to `lint` since `lint-fix` will fix problems automatically.
- **SPDX License Headers**:
  - All Go files require SPDX headers at the top:
    ```go
    // SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
    // SPDX-License-Identifier: Apache-2.0
    ```
  - Use `task license-check` to verify headers, `task license-fix` to add them automatically.
  - CI enforces this on all PRs (ignores generated files: mocks, testdata, vendor, *.pb.go, zz_generated*.go).
- **Commit messages and PR titles**:
  - Refer to the `CONTRIBUTING.md` file for guidelines on commit message format
    conventions.
  - Do not use "Conventional Commits", e.g. starting with `feat`, `fix`, `chore`, etc.
  - Use mockgen for creating mocks instead of generating mocks by hand.
- Use very large numbers for unit tests which need port numbers.
  - Using common port ranges leads to a likelihood that you will clash with a
    port which is already in use. Pick math.MaxInt16 as a sample port to reduce
    the changes of hitting this problem.

### Go Coding Style

- **Prefer immutable variable assignment with anonymous functions**:
  When you need to assign a variable based on complex conditional logic, prefer using an immediately-invoked anonymous function instead of mutating the variable across multiple branches:

  ```go
  // ✅ Good: Immutable assignment with anonymous function
  phase := func() PhaseType {
      if someCondition {
          return PhaseA
      }
      if anotherCondition {
          return PhaseB
      }
      return PhaseDefault
  }()

  // ❌ Avoid: Mutable variable across branches
  var phase PhaseType
  if someCondition {
      phase = PhaseA
  } else if anotherCondition {
      phase = PhaseB
  } else {
      phase = PhaseDefault
  }
  ```

  **Benefits**:
  - The variable is immutable after assignment, reducing bugs from accidental modification
  - All decision logic is in one place with explicit returns
  - Clearer logic flow and easier to understand
  - Reduces cognitive load from tracking which branch sets which value

## Error Handling Guidelines

See `docs/error-handling.md` for comprehensive documentation.

- **Return errors by default** - Never silently swallow errors
- **Comment ignored errors** - Explain why and typically log them
- **No sensitive data in errors** - No API keys, credentials, tokens, or passwords
- **Use `errors.Is()` or `errors.As()`** - For all error inspection (they properly unwrap errors)
- **Use `fmt.Errorf` with `%w`** - To preserve error chains; don't wrap excessively
- **Use `recover()` sparingly** - Only at top-level API/CLI boundaries

## Logging Guidelines

See `docs/logging.md` for comprehensive documentation.

- **Silent success** - Do not log at INFO or above for successful operations; users should only see output when something requires attention or when using `--debug`
- **DEBUG for diagnostics** - Use for detailed troubleshooting info (runtime detection, state transitions, config values)
- **INFO sparingly** - Only for long-running operations like image pulls that benefit from progress indication
- **WARN for non-fatal issues** - Deprecations, fallback behavior, cleanup failures
- **No sensitive data** - Never log credentials, tokens, API keys, or passwords
