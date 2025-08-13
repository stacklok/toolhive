# ToolHive MCP Registry Consumption Plan

## Executive Summary

This plan outlines how ToolHive will consume and run MCP servers from the official Model Context Protocol registry format. The goal is to enable ToolHive users to access the growing ecosystem of MCP servers while maintaining ToolHive's security-first approach and operational simplicity.

## Background & Motivation

### Current State
- **ToolHive Registry**: Contains ~100 curated MCP servers with security profiles, provenance data, and Docker-first packaging
- **MCP Registry**: Growing ecosystem with standardized `server.json` format, supporting multiple package managers (npm, PyPI, Docker, NuGet)
- **Gap**: ToolHive users cannot access MCP registry servers, limiting their options

### Why This Matters
1. **Ecosystem Growth**: MCP registry is becoming the standard, with more servers published there
2. **User Demand**: ToolHive users want access to all available MCP servers
3. **Avoid Fragmentation**: Supporting both formats prevents ecosystem split
4. **Maintain Differentiation**: ToolHive's security features remain valuable regardless of registry source

## Core Design Principles

### 1. Security First
Every MCP server, regardless of source, must run with ToolHive's security controls:
- Network isolation by default
- Filesystem access restrictions
- Controlled secret management
- No privilege escalation

### 2. Transparency
Users should understand:
- Which registry a server comes from
- What security profile is applied
- How the server will be executed

### 3. Seamless Experience
Running an MCP server should work the same way regardless of source:
```bash
thv run github           # From ToolHive registry
thv run some-new-server  # From MCP registry
```

## Architecture Overview

```
┌─────────────────┐     ┌─────────────────┐
│ ToolHive        │     │ MCP Official    │
│ Registry        │     │ Registry        │
│ (Current)       │     │ (server.json)   │
└────────┬────────┘     └────────┬────────┘
         │                       │
         └───────────┬───────────┘
                     │
            ┌────────▼────────┐
            │ Registry        │
            │ Abstraction     │
            │ Layer           │
            └────────┬────────┘
                     │
            ┌────────▼────────┐
            │ Package         │
            │ Resolver        │
            └────────┬────────┘
                     │
            ┌────────▼────────┐
            │ Security        │
            │ Profile         │
            │ Assignment      │
            └────────┬────────┘
                     │
            ┌────────▼────────┐
            │ ToolHive        │
            │ Runtime         │
            │ (Docker)        │
            └─────────────────┘
```

## Key Components

### 1. Registry Abstraction Layer

**Purpose**: Provide a unified interface for accessing servers from multiple registries.

**How it works**:
- Maintains a list of registry sources (ToolHive, MCP official, custom)
- Searches registries in priority order when user requests a server
- Caches registry data to improve performance
- Handles format differences transparently

**User Impact**:
- Can configure which registries to use
- Can add private/enterprise registries
- Server names are resolved automatically

### 2. Package Resolver

**Purpose**: Convert MCP package definitions into executable formats that ToolHive can run.

**Strategy by Package Type**:

| MCP Package Type | ToolHive Execution Method | Reasoning |
|------------------|---------------------------|-----------|
| Docker | Direct Docker run | Already containerized, best security |
| npm (npx) | Build Docker via npx:// | Leverage existing ToolHive feature |
| PyPI (uvx) | Build Docker via uvx:// | Leverage existing ToolHive feature |
| NuGet (dnx) | Build Docker via dnx:// | May need new support |
| Remote (SSE/HTTP) | Proxy connection | No local execution needed |

**Docker Package Handling - Key Differences**:

When MCP specifies `runtime_hint: "docker"`, ToolHive needs to handle the differences:

| MCP Docker Spec | ToolHive Approach | Why |
|-----------------|-------------------|-----|
| `runtime_arguments` with Docker flags | **Ignore most Docker flags** | ToolHive controls container security |
| `--publish` port mappings | **Auto-handle based on transport** | ToolHive manages ports automatically |
| `--volume` mounts | **Require explicit user approval** | Security: no automatic filesystem access |
| `--env` variables | **Pass through after validation** | Support, but check for secrets |
| Custom Docker networks | **Use ToolHive's network isolation** | Consistent security model |
| Privileged mode | **Never allow** | Security boundary |

**Example Translation**:
```yaml
# MCP server.json snippet:
packages:
  - registry_name: docker
    name: mcp/filesystem
    runtime_hint: docker
    runtime_arguments:
      - type: named
        name: "--publish"
        value: "8080:8080"
      - type: named
        name: "--volume"
        value: "/data:/data"

# ToolHive execution:
# 1. Ignores --publish (auto-assigns port if needed)
# 2. Prompts user: "Server requests /data mount. Allow? [y/N]"
# 3. Runs with ToolHive's network isolation
# 4. Never passes raw Docker flags directly
```

**Why Docker-centric**:
- Provides strongest isolation
- Consistent runtime environment
- ToolHive's security model is built around containers
- Avoids installing language runtimes on host

### 3. Security Profile Assignment

**Purpose**: Ensure every MCP server runs with appropriate security controls.

**Default Profiles**:

```yaml
Unknown Server (Default):
  network:
    outbound:
      allow_host: []  # No network by default
      allow_port: []
  filesystem:
    read: []   # No filesystem access
    write: []

Known Categories:
  web-scraper:
    network:
      outbound:
        allow_port: [443, 80]
        insecure_allow_all: true  # Needs to access any website
  
  database-client:
    network:
      outbound:
        allow_port: [5432, 3306, 27017]  # Common DB ports
```

**Override Mechanism**:
1. Check if server has ToolHive metadata in `custom_metadata`
2. Apply category-based defaults based on tags/description
3. Allow user overrides via CLI flags
4. Prompt for confirmation if requesting elevated permissions

### 4. Metadata Enrichment

**Purpose**: Enhance MCP servers with ToolHive-specific features.

**What gets added**:
- **Popularity metrics**: Track usage within ToolHive ecosystem
- **Security audit**: Automated scanning of requested permissions
- **User ratings**: Community feedback on server quality
- **Verified status**: For servers that pass ToolHive review

**Storage approach**:
- Keep enrichment data separate from registry data
- Store in local database or cloud service
- Merge at runtime for display

## User Experience

### Discovery Flow

```
$ thv search "github"

Found in multiple registries:

[ToolHive Registry] ⭐ Verified
  github - GitHub API integration
  Security: Network restricted to *.github.com
  Popularity: ★★★★★ (1,234 users)

[MCP Official]
  github-mcp-server - Official GitHub MCP server
  Security: Default profile will be applied
  Package: npm (@modelcontextprotocol/github)

$ thv run github --source mcp-official
```

### Running Servers

**Scenario 1: Docker Package**
```
User: thv run github
System: 
  1. Finds server in MCP registry
  2. Sees Docker package available
  3. Pulls Docker image
  4. Applies default security profile
  5. Runs with network isolation
```

**Scenario 2: NPM Package**
```
User: thv run typescript-analyzer
System:
  1. Finds server in MCP registry
  2. Sees only npm package available
  3. Converts to npx://package-name@version
  4. Builds Docker image (cached for reuse)
  5. Runs with filesystem access to current directory
```

### Security Prompts

```
$ thv run web-scraper

⚠️  Security Notice:
This server requests:
  • Unrestricted internet access
  • Read access to /tmp

Source: MCP Official Registry (unverified)

Allow these permissions? [y/N]
```

## Implementation Phases

### Phase 1: Foundation
**Goal**: Understand and parse MCP format.

- Research MCP `server.json` schema thoroughly
- Build parser for MCP registry format
- Create test suite with sample MCP servers
- Document format differences

**Success Criteria**: Can read and validate MCP registry files

### Phase 2: Integration Design
**Goal**: Design how MCP servers map to ToolHive execution.

- Map each package type to execution strategy
- Design security profile system
- Create unified registry interface
- Plan backward compatibility

**Success Criteria**: Clear documentation of all edge cases

### Phase 3: Core Implementation
**Goal**: Build the consumption pipeline.

- Registry abstraction layer
- Package resolver for each type
- Security profile assignment
- CLI integration

**Success Criteria**: Can run basic MCP servers

### Phase 4: Security Hardening
**Goal**: Ensure security model is robust.

- Audit default profiles
- Add permission prompting
- Implement sandbox testing
- Security documentation

**Success Criteria**: Security review passed

### Phase 5: User Experience
**Goal**: Polish the experience.

- Improve discovery (search, filter)
- Add progress indicators
- Better error messages
- Performance optimization

**Success Criteria**: User testing feedback positive

### Phase 6: Launch
**Goal**: Release to users.

- Documentation
- Migration guides
- Community announcement
- Monitor adoption

**Success Criteria**: 100+ MCP servers accessible

## Risk Analysis

### Technical Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| MCP schema changes | High | Version detection, graceful degradation |
| Package execution failures | Medium | Fallback strategies, clear errors |
| Performance with large registries | Low | Caching, pagination, CDN usage |
| Security vulnerabilities | High | Conservative defaults, sandboxing |

### User Experience Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Confusion about sources | Medium | Clear labeling, source indicators |
| Security fatigue | High | Smart defaults, remember choices |
| Breaking changes | High | Careful migration, compatibility mode |
| Discovery difficulties | Medium | Good search, categories, recommendations |

## Conclusion

By implementing MCP registry consumption, ToolHive becomes the most secure and user-friendly way to run any MCP server. Users gain access to the entire MCP ecosystem while benefiting from ToolHive's security-first approach. This positions ToolHive as the runtime of choice for security-conscious organizations and developers.

The phased approach ensures we maintain stability while expanding capabilities, and the security-first design means users can confidently run any MCP server without compromising their systems.