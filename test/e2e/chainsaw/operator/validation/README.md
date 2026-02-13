# Validation Tests

This directory contains validation tests for VirtualMCPServer and MCPExternalAuthConfig CRDs.

## Test Strategy

We use a **hybrid validation approach** with two layers of testing:

### 1. CEL Validation Tests (Chainsaw)
Tests that invalid manifests are **rejected immediately** by the API server using CEL validation rules.

**Location:** `test/e2e/chainsaw/operator/validation/`

**What they test:**
- Simple field validation (required fields, enum values, type-specific requirements)
- Immediate API-level rejection (before controller reconciliation)
- Validation happens at resource creation/update time

**Examples:**
- OIDC type without `oidcConfig`
- TokenExchange type without `tokenExchange` config
- Unauthenticated type with config (should have none)

### 2. Controller-Side Validation Tests (Unit Tests)
Tests that complex validation logic is executed during reconciliation and reported in status conditions.

**Location:**
- `cmd/thv-operator/controllers/virtualmcpserver_controller_test.go`
- `cmd/thv-operator/controllers/mcpexternalauthconfig_controller_test.go`
- `cmd/thv-operator/api/v1alpha1/*_test.go` (validation method tests)

**What they test:**
- Complex business logic validation (aggregation, composite tools, auth configs)
- Status conditions are set correctly (`Valid=False` with error message)
- Controller doesn't requeue on validation errors
- Resources that pass CEL but fail controller validation

**Examples:**
- Invalid backend auth configuration (missing type)
- Multiple upstream providers in embeddedAuthServer (not supported)
- Invalid aggregation conflict resolution
- Invalid composite tool definitions

## Running the Tests

### Run Chainsaw Tests (CEL Validation)

```bash
# Run all validation chainsaw tests
task operator-e2e-test-chainsaw

# Or run specific validation tests
chainsaw test test/e2e/chainsaw/operator/validation/virtualmcpserver/
chainsaw test test/e2e/chainsaw/operator/validation/mcpexternalauthconfig/
```

### Run Unit Tests (Controller Validation)

```bash
# Run all operator unit tests (includes validation tests)
cd cmd/thv-operator
go test ./... -v

# Run specific controller tests
go test ./controllers/... -v

# Run specific validation method tests
go test ./api/v1alpha1/... -v

# Run with coverage
go test ./... -cover
```

## Test Coverage

### VirtualMCPServer

**CEL Validation (API-level):**
- ✅ `spec.incomingAuth.oidcConfig` required when type is `oidc`

**Controller Validation (reconciliation):**
- ✅ `spec.config.group` is required (CEL can't validate embedded types)
- ✅ Invalid backend auth configuration
- ✅ Invalid aggregation conflict resolution
- ✅ Invalid composite tool definitions
- ✅ Missing references to external resources

### MCPExternalAuthConfig

**CEL Validation (API-level):**
- ✅ Type-specific config must match type
  - `tokenExchange` → requires `tokenExchange` config
  - `headerInjection` → requires `headerInjection` config
  - `bearerToken` → requires `bearerToken` config
  - `embeddedAuthServer` → requires `embeddedAuthServer` config
  - `unauthenticated` → no config allowed

**Controller Validation (reconciliation):**
- ✅ EmbeddedAuthServer with multiple providers (not supported)
- ✅ Invalid upstream provider configuration
- ✅ Complex auth flow validation

## How It Works

### Before (Webhooks - didn't work)
```
User applies manifest
    ↓
❌ Webhook validation (never functional - no cert-manager, no webhook service)
    ↓
API Server accepts manifest
    ↓
Controller reconciles
```

### After (Hybrid Approach)
```
User applies manifest
    ↓
✅ CEL validation (immediate API-level rejection for simple rules)
    ↓
API Server accepts manifest
    ↓
✅ Controller validation (complex business logic, updates status.conditions)
    ↓
User sees validation errors in status
```

## Benefits

1. **Faster feedback** - CEL validation rejects invalid manifests immediately
2. **Better UX** - Clear error messages at `kubectl apply` time
3. **Rich status** - Complex validation errors visible in `status.conditions`
4. **Simpler infrastructure** - No webhooks, no cert-manager needed
5. **Better testability** - Can test both layers independently

## Validation Error Example

```yaml
# Invalid manifest (missing group)
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: test
spec:
  incomingAuth:
    type: anonymous
  config:
    group: ""  # Empty - fails CEL validation
```

```bash
$ kubectl apply -f invalid.yaml
The VirtualMCPServer "test" is invalid: spec: Invalid value: "object":
spec.config.group is required
```

## Adding New Validation Tests

### For CEL Validation (Chainsaw):
1. Add invalid manifest YAML that should be rejected
2. Add test case in `chainsaw-test.yaml` expecting API rejection
3. Verify error message matches CEL validation message

### For Controller Validation (Unit Tests):
1. Add test case in `cmd/thv-operator/controllers/*_test.go` or `api/v1alpha1/*_test.go`
2. Create resource with configuration that passes CEL but fails controller logic
3. Mock the Kubernetes client interactions
4. Verify `Valid=False` condition is set in status with correct error message
5. Verify controller doesn't requeue on validation errors

## See Also

- [CLAUDE.md](../../../../../cmd/thv-operator/CLAUDE.md) - Operator development guide
- [CLI Best Practices](../../../../../docs/cli-best-practices.md) - Testing philosophy
