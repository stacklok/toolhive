# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Project Overview

ToolHive is a lightweight, secure manager for MCP (Model Context Protocol: https://modelcontextprotocol.io) servers written in Go. It provides a CLI (`thv`), a Kubernetes operator (`thv-operator`), and a proxy runner (`thv-proxyrunner`) for container-based MCP server isolation.

## Build and Development Commands

```bash
task build            # Build the main binary
task install          # Install binary to GOPATH/bin
task lint             # Run linting
task lint-fix         # Fix linting issues (preferred over lint)
task test             # Unit tests (excluding e2e)
task test-e2e         # E2E tests (requires build first)
task test-all         # All tests (unit + e2e)
task test-coverage    # Tests with coverage analysis
task gen              # Generate mocks
task docs             # Generate CLI documentation
task build-image      # Build container image
task build-all-images # Build all container images
```

**Testing**: Ginkgo/Gomega for BDD-style tests. Unit tests for `pkg/` business logic; E2E tests for CLI commands.

## Available Subagents

Agents are in `.claude/agents/` and MUST be invoked for tasks matching their expertise:

### Core Development
- **toolhive-expert**: Architecture, codebase navigation, implementation guidance
- **golang-code-writer**: Writing new Go code (functions, structs, interfaces, packages)
- **unit-test-writer**: Writing comprehensive unit tests
- **code-reviewer**: Code review for best practices, security, conventions
- **tech-lead-orchestrator**: Architectural oversight, task delegation, complex features

### Specialized Domains
- **kubernetes-expert**: Operator patterns, CRDs, controllers, cloud-native architecture
- **mcp-protocol-expert**: MCP spec compliance, transport protocols, JSON-RPC
- **oauth-expert**: OAuth 2.0, OIDC, token exchange, authentication flows
- **site-reliability-engineer**: Observability, OpenTelemetry, monitoring

### Support
- **documentation-writer**: Documentation updates, CLI docs
- **security-advisor**: Security guidance, code review, threat modeling

### When to Use Subagents
- Writing new code: golang-code-writer
- Creating tests: unit-test-writer
- Orchestrating multi-component work: tech-lead-orchestrator
- Reviewing code: code-reviewer
- Domain expertise: kubernetes-expert, oauth-expert, mcp-protocol-expert, site-reliability-engineer

## Key Conventions

Detailed rules are in `.claude/rules/` (loaded automatically when matching files are read):
- **Go style, errors, logging, SPDX headers**: `.claude/rules/go-style.md`
- **CLI architecture**: `.claude/rules/cli-commands.md`
- **Testing**: `.claude/rules/testing.md`
- **Operator/CRDs**: `.claude/rules/operator.md`
- **PR creation**: `.claude/rules/pr-creation.md`

## Commit Guidelines

- Imperative mood, capitalize subject, no trailing period
- 50-char subject line limit
- Explain what and why, not how
- Do NOT use Conventional Commits (`feat:`, `fix:`, `chore:`, etc.)
- See `CONTRIBUTING.md` for full guidelines

## Pull Request Guidelines

- Follow `.claude/rules/pr-creation.md` and `.github/pull_request_template.md`
- Max **400 lines** of code changes, **10 files** changed (excluding tests/docs/generated)
- Each PR = one logical change (one feature, one bug fix, or one refactoring)
- If changes exceed limits, use `/split-pr` skill to propose a split strategy
- Large PRs acceptable for: generated code, dependency updates, docs-only, test-only changes (with user confirmation)

## Architecture Documentation

When making changes that affect architecture, update relevant docs in `docs/arch/`. See `docs/arch/README.md` for structure.

## Evolving Conventions

When a developer states a preference, convention, or correction during conversation (e.g., "we should use X instead of Y", "don't do Z", "always prefer A over B"), you MUST:

1. **Apply it immediately** in the current conversation
2. **Suggest codifying it** — identify which `.claude/rules/` file or `.claude/agents/` file it belongs in and propose the edit
3. **Offer to apply** with a one-line confirmation (e.g., "Want me to add this to `.claude/rules/go-style.md`?")

Use the `/add-rule` skill to formalize conventions. This ensures tribal knowledge gets captured in version-controlled config, not lost in chat history.

**Personal vs team conventions**: Personal preferences (e.g., "I like verbose output") belong in `~/.claude/` personal memory. Team-wide conventions (e.g., "always use `errors.Is()` for error checks") belong in `.claude/rules/` so all team members benefit.
