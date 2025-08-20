# GitHub Copilot Instructions for ToolHive

This file provides GitHub Copilot with context about the ToolHive project to help generate better pull request reviews and suggestions.

## Project Overview

ToolHive is a lightweight, secure manager for Model Context Protocol (MCP) servers written in Go. It provides:

- **CLI (`thv`)**: Main command-line interface for managing MCP servers locally
- **Kubernetes Operator (`thv-operator`)**: Manages MCP servers in Kubernetes clusters  
- **Proxy Runner (`thv-proxyrunner`)**: Handles proxy functionality for MCP server communication

## Key Architecture Principles

- **Container-based isolation**: All MCP servers run in Docker/Podman containers
- **Security first**: Cedar-based authorization, secret management, certificate validation
- **Runtime abstraction**: Support for both Docker and Kubernetes via factory pattern
- **Multiple transport protocols**: stdio, HTTP, SSE, streamable MCP transports
- **Interface segregation**: Clean abstractions for testability and runtime flexibility

## Code Review Focus Areas

### Go Code Standards
- Follow Go standard project layout conventions
- Use interfaces for testability and runtime abstraction
- Keep public methods in top half of files, private methods in bottom half
- Separate business logic from transport/protocol concerns
- Keep packages focused on single responsibilities

### Security Considerations
- Never expose or log secrets and keys
- Validate all container images and certificates
- Ensure proper isolation between MCP servers
- Review Cedar authorization policies carefully
- Check for proper input validation and sanitization

### Testing Requirements
- Unit tests alongside source files (`*_test.go`)
- Integration tests within packages
- End-to-end tests in `test/e2e/`
- Use Ginkgo/Gomega for BDD-style testing
- Mock generation using `go.uber.org/mock`

### Architecture Patterns
- **Factory Pattern**: Used for runtime-specific implementations (Docker vs Kubernetes)
- **Middleware Pattern**: HTTP middleware for auth, authz, telemetry
- **Observer Pattern**: Event system for audit logging
- Implement interfaces defined in `pkg/container/runtime/types.go`

### Development Workflow
- Check that `task lint` and `task lint-fix` pass
- Ensure `task test` (unit tests) and `task test-e2e` pass
- Use `task build` to verify successful compilation
- Follow commit message guidelines from CONTRIBUTING.md

### Key Dependencies
- Docker API for container runtime
- Chi router for web framework
- Cobra for CLI framework
- Viper for configuration management
- controller-runtime for Kubernetes operations
- OpenTelemetry for observability

## Common Anti-Patterns to Flag

- Using concrete types instead of interfaces for testability
- Mixing business logic with transport/protocol code
- Hardcoding container runtime specifics instead of using abstraction
- Missing error handling, especially for container operations
- Inadequate input validation for MCP server configurations
- Security vulnerabilities in secret handling or container permissions
- Missing tests for new functionality
- Not following the project's commit message format

## Project-Specific Guidelines

### Operator Development
- CRD attributes for business logic that affects operator behavior
- PodTemplateSpec for infrastructure concerns (node selection, resources)
- Refer to `cmd/thv-operator/DESIGN.md` for detailed decisions

### Transport Implementation
- Implement transport interface in `pkg/transport/`
- Add factory registration for new transports
- Update runner configuration appropriately
- Add comprehensive tests for new transport types

### Container Runtime
- Support both Docker and Kubernetes via abstraction
- Use factory pattern for runtime selection
- Implement interfaces consistently across runtimes

## Configuration Management
- Uses Viper with environment variable overrides
- Client configuration in `~/.toolhive/` or platform equivalent
- Support for multiple secret backends (1Password, encrypted storage)

When reviewing PRs, focus on these areas to ensure code quality, security, and adherence to the project's architectural principles.