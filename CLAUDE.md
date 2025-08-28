# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ToolHive is a lightweight, secure manager for MCP (Model Context Protocol: https://modelcontextprotocol.io) servers written in Go. It provides three main components:

- **CLI (`thv`)**: Main command-line interface for managing MCP servers locally
- **Kubernetes Operator (`thv-operator`)**: Manages MCP servers in Kubernetes clusters
- **Proxy Runner (`thv-proxyrunner`)**: Handles proxy functionality for MCP server communication

The application acts as a thin client for Docker/Podman Unix socket API, providing container-based isolation for running MCP servers securely. It also builds on top of the MCP Specification: https://modelcontextprotocol.io/specification.

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

### Key Design Patterns

- **Factory Pattern**: Used extensively for creating runtime-specific implementations (Docker vs Kubernetes)
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

The container abstraction supports both Docker and Kubernetes runtimes. When adding container functionality:
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

## Development Best Practices

- **Linting**:
  - Prefer `lint-fix` to `lint` since `lint-fix` will fix problems automatically.
- **Commit messages and PR titles**:
  - Refer to the `CONTRIBUTING.md` file for guidelines on commit message format
    conventions.
  - Do not use "Conventional Commits", e.g. starting with `feat`, `fix`, `chore`, etc.
  - Use mockgen for creating mocks instead of generating mocks by hand.

## Miscellaneous
Stop saying "You're absolutely right", please find other ways of saying this.