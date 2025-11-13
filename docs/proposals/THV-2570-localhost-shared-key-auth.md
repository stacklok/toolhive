# Shared Key Authentication for Local MCP Servers

## Problem Statement

Local ToolHive deployments bind MCP servers to localhost (`127.0.0.1`) for network-level security, but have no authentication layer. While localhost binding prevents remote access, customers want defense-in-depth protection against:

- Malicious local processes connecting to open localhost ports
- Port forwarding or SSH tunneling accidentally exposing endpoints
- Privilege escalation attacks targeting unauthenticated local services
- Compliance requirements mandating authentication even for localhost

Current authentication options (OIDC, local user auth) require external identity providers or manual password management, making them unsuitable for single-user local deployments.

## Goals

- Provide lightweight authentication for localhost MCP server deployments
- Require zero or minimal configuration from users
- Leverage existing ToolHive infrastructure (OS keychain, middleware, stdio bridge)
- Work transparently with the `thv proxy stdio` bridge used by MCP clients
- Maintain backward compatibility (opt-in feature)

## Non-Goals

- Replace network isolation or container-based security
- Support multi-user local deployments (use OIDC instead)
- Protect against root/administrator access or OS keychain compromise
- Store credentials in configuration files

## Proposed Solution

Implement a shared key authentication middleware that generates unique cryptographic keys per MCP server workload, stores them in the OS keychain, and validates them on incoming requests.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     MCP Client                               │
│                 (Claude Desktop, etc.)                       │
└──────────────────────┬──────────────────────────────────────┘
                       │ stdio
                       ↓
┌─────────────────────────────────────────────────────────────┐
│              thv proxy stdio Bridge                          │
│  • Reads shared key from OS keychain                         │
│  • Injects X-ToolHive-Auth header in HTTP requests          │
└──────────────────────┬──────────────────────────────────────┘
                       │ HTTP + X-ToolHive-Auth header
                       ↓
┌─────────────────────────────────────────────────────────────┐
│              ToolHive HTTP Proxy                             │
│  • Shared Key Auth Middleware validates header              │
│  • Constant-time comparison with env variable               │
│  • Other middleware (Parser, Authz, Audit)                  │
└──────────────────────┬──────────────────────────────────────┘
                       │ Forwarded request
                       ↓
┌─────────────────────────────────────────────────────────────┐
│              MCP Server Container                            │
│  • Receives TOOLHIVE_SHARED_KEY env variable                 │
└─────────────────────────────────────────────────────────────┘
```

### Key Components

**1. Key Generation and Storage** (`pkg/auth/sharedkey/`)
- Generate cryptographically-secure 32-byte keys using `crypto/rand`
- Store in OS keychain via existing encrypted secrets provider
- Key naming: `workload:<workload-name>:shared-key`
- Lifecycle: Generate on deployment, delete on workload removal

**2. Shared Key Authentication Middleware** (`pkg/auth/sharedkey/middleware.go`)
- Implements standard `types.Middleware` interface
- Validates `X-ToolHive-Auth` header against `TOOLHIVE_SHARED_KEY` environment variable
- Uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks
- Returns `401 Unauthorized` for missing/invalid keys
- Adds identity to context for audit trail

**3. Workload Integration** (`pkg/workloads/manager.go`)
- Generate and store shared key when `SharedKeyAuth.Enabled` is set in RunConfig
- Inject key into container and proxy via `TOOLHIVE_SHARED_KEY` environment variable
- Configure middleware chain automatically
- Clean up key on workload deletion

**4. Stdio Bridge Enhancement** (`cmd/thv/app/proxy_stdio.go`)
- Retrieve shared key from secrets provider on startup
- Inject `X-ToolHive-Auth` header in all HTTP requests via custom transport wrapper
- Handle 401 responses with informative error messages

**5. RunConfig Extension** (`pkg/runner/config.go`)

```go
type SharedKeyAuthConfig struct {
    Enabled bool `json:"enabled"`
    KeyRotationDays int `json:"keyRotationDays,omitempty"` // 0 = disabled
}

// Add to RunConfig struct
SharedKeyAuth *SharedKeyAuthConfig `json:"sharedKeyAuth,omitempty"`
```

## High-Level Design

### Request Flow

1. **Workload Deployment**: User runs `thv run my-server --shared-key-auth`
2. **Key Generation**: Workload manager generates 32-byte random key
3. **Key Storage**: Key stored as `workload:my-server:shared-key` in OS keychain
4. **Key Injection**: Key injected via `TOOLHIVE_SHARED_KEY` environment variable to container and proxy
5. **Middleware Configuration**: Shared key auth middleware added to chain
6. **Client Configuration**: MCP client configured to use `thv proxy stdio my-server`

### Authentication Flow

1. **Client Request**: MCP client sends JSON-RPC over stdio to `thv proxy stdio`
2. **Key Retrieval**: Stdio bridge retrieves key from OS keychain
3. **Header Injection**: Bridge adds `X-ToolHive-Auth: <key>` to HTTP request
4. **Validation**: Middleware validates header against environment variable
5. **Forwarding**: On success, request forwarded to MCP server container
6. **Rejection**: On failure, return `401 Unauthorized`

### Security Properties

- **Key Strength**: 32 bytes (256 bits), base64-encoded
- **Key Generation**: Crypto-secure random via `crypto/rand`
- **Storage**: AES-256-GCM encrypted in OS keychain (existing provider)
- **Comparison**: Constant-time to prevent timing attacks
- **Uniqueness**: Independent key per workload
- **Lifecycle**: Keys deleted with workload

## Usage Examples

### Basic Usage

```bash
# Enable shared key authentication
thv run my-server --shared-key-auth

# ToolHive automatically:
# 1. Generates secure key
# 2. Stores in OS keychain
# 3. Configures middleware
# 4. Updates client config
```

### With Other Security Features

```bash
# Combine with authorization and audit
thv run my-server --shared-key-auth \
  --authz-config authz.yaml \
  --audit-config audit.yaml
```

### Debugging

```bash
# Verbose mode
thv proxy stdio my-server --verbose

# Inspect key
thv secret get workload:my-server:shared-key
```

## Operational Considerations

### Key Lifecycle

**Generation**: Automatic on deployment with `--shared-key-auth` flag
**Storage**: OS keychain via encrypted secrets provider
**Deletion**: Automatic on `thv rm my-server`

### Backward Compatibility

- Opt-in feature via flag or RunConfig
- Existing workloads continue working without changes
- No breaking changes to configurations
- Gradual migration: enable per-workload incrementally

### Multi-Workload Scenarios

- Each workload has unique key
- Keys identified by workload name
- Stdio bridge automatically selects correct key
- Group operations work transparently

## Security Considerations

### Threat Model

**Protected Against**:
- Unauthorized local process connections
- Port scanning attacks
- Accidental credential exposure in config files
- Replay attacks (keys are workload-scoped)

**Not Protected Against**:
- Root/administrator access to keychain
- Process memory inspection
- Physical machine access
- Compromised OS keychain

### Defense in Depth

Shared key authentication adds a layer to existing security:

1. Network isolation (localhost binding)
2. Container isolation
3. **Shared key authentication** ← New layer
4. Authorization (Cedar policies)
5. Audit logging

Each layer provides independent protection.

## Alternatives Considered

### Certificate-Based Authentication (mTLS)

**Pros**: Industry-standard, strong crypto
**Cons**: Complex (certificate generation, validation, expiry), TLS infrastructure overhead, overkill for localhost
**Decision**: Rejected due to complexity

### Unix Domain Sockets

**Pros**: Filesystem-based permissions, OS-level access control
**Cons**: Not cross-platform (Windows), breaks HTTP architecture, requires significant changes
**Decision**: Rejected due to platform limitations

### API Keys in Config Files

**Pros**: Simple implementation, easy debugging
**Cons**: Plaintext storage, git commit risk, no leverage of existing secrets infrastructure
**Decision**: Rejected due to poor security properties

### Time-Based One-Time Passwords (TOTP)

**Pros**: Strong authentication, industry-standard
**Cons**: Time synchronization complexity, poor UX for automated clients, overkill for localhost
**Decision**: Rejected due to complexity and poor UX

## Implementation Plan

### Phase 1: Core Infrastructure
- Create `pkg/auth/sharedkey/` package
- Implement key generation and storage
- Implement shared key authentication middleware
- Add middleware factory registration
- Extend RunConfig schema

### Phase 2: Workload Integration
- Modify workloads manager for key lifecycle
- Add CLI flag `--shared-key-auth`
- Configure middleware chain automatically
- Implement key cleanup on deletion

### Phase 3: Stdio Bridge Enhancement
- Enhance `thv proxy stdio` to retrieve keys
- Implement header injection in HTTP transport
- Add error handling for auth failures
- Update client configuration generation

### Phase 4: Testing and Documentation
- Unit tests for middleware and key management
- Integration tests for end-to-end flows
- Security testing (timing attacks, etc.)
- Update architecture documentation
- Create user guide

