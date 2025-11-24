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

- Unit tests: `task test`
- E2E tests: `task test-e2e` (requires build first)
- All tests: `task test-all`
- With coverage: `task test-coverage`

The test framework uses Ginkgo and Gomega for BDD-style testing.

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
- **Integration tests**: In individual package test files
- **End-to-end tests**: Located in `test/e2e/`
- **Operator tests**: Chainsaw tests in `test/e2e/chainsaw/operator/`

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

## Development Guidelines

### Code Organization

- Follow Go standard project layout
- Use interfaces for testability and runtime abstraction
- Separate business logic from transport/protocol concerns
- Keep packages focused on single responsibilities
- In Go codefiles, keep public methods to the top half of the file, and private
  methods to the bottom half of the file.

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

1. Create command file in `cmd/thv/app/`
2. Add command to `NewRootCmd()` in `commands.go`
3. Update CLI documentation with `task docs`

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

- **Linting**:
  - Prefer `lint-fix` to `lint` since `lint-fix` will fix problems automatically.
- **Commit messages and PR titles**:
  - Refer to the `CONTRIBUTING.md` file for guidelines on commit message format
    conventions.
  - Do not use "Conventional Commits", e.g. starting with `feat`, `fix`, `chore`, etc.
  - Use mockgen for creating mocks instead of generating mocks by hand.
