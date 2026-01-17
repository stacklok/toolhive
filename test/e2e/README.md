# End-to-End Tests

This directory contains end-to-end tests for ToolHive, including both CLI and HTTP API tests.

## Overview

These tests validate ToolHive functionality by exercising the full application stack:

- **CLI Tests**: Test command-line interface operations (run, list, stop, restart, etc.)
- **API Tests**: Test HTTP API endpoints with a real API server instance
- **Integration Tests**: Test interactions between different components

## Structure

### Test Files

- `*_test.go` - Individual test files organized by feature
- `e2e_suite_test.go` - Ginkgo test suite setup
- `api_helpers.go` - Helper functions for starting API server and making HTTP requests
- `helpers.go` - General helper functions for e2e tests
- `mcp_client_helpers.go` - MCP client helper utilities
- `oidc_mock.go` - Mock OIDC server for authentication tests
- `run_tests.sh` - Test runner script

### Test Categories

Tests are organized using Ginkgo labels for parallelization and filtering:

#### Core CLI Tests (Label: `core`)
- Client management (`client_test.go`)
- Group operations (`group_*.go`)
- Server restart (`restart_test.go`)
- Export functionality (`export_test.go`)
- THVIgnore support (`thvignore_test.go`)

#### MCP Protocol Tests (Label: `mcp`)
- MCP server operations (`osv_mcp_server_test.go`, `fetch_mcp_server_test.go`)
- Protocol builds (`protocol_builds_e2e_test.go`)
- Remote MCP servers (`remote_mcp_server_test.go`)
- Inspector functionality (`inspector_test.go`)

#### Proxy Tests (Label: `proxy`)
- Stdio proxy (`proxy_stdio_test.go`)
- OAuth authentication (`proxy_oauth_test.go`)
- Tunnel functionality (`proxy_tunnel_e2e_test.go`)
- Streamable HTTP proxy (`stdio_proxy_over_streamable_http_mcp_server_test.go`)

#### Middleware Tests (Label: `middleware`)
- Audit middleware (`audit_middleware_e2e_test.go`)
- Authorization (`osv_authz_test.go`)
- Telemetry (`telemetry_middleware_e2e_test.go`)

#### API Tests (Label: `api`)
- Health check endpoint (`api_healthcheck_test.go`)
- API server lifecycle and HTTP operations

#### Other Tests
- Network isolation (Label: `network`, `isolation`)
- Stability tests (Label: `stability`)
- Telemetry validation (Label: `telemetry`, `metrics`, `validation`)
- SSE endpoint rewriting (Label: `sse`, `endpoint-rewrite`)

## Running Tests

### Prerequisites

- Go installed
- Ginkgo CLI installed: `go install github.com/onsi/ginkgo/v2/ginkgo@latest`
- Docker, Podman, or Colima container runtime
- ToolHive binary built (for CLI tests): `task build`

### Run All Tests

```bash
cd test/e2e
./run_tests.sh
```

### Run Tests by Label

```bash
cd test/e2e

# Run only core CLI tests
E2E_LABEL_FILTER=core ./run_tests.sh

# Run only API tests
E2E_LABEL_FILTER=api ./run_tests.sh

# Run only MCP protocol tests
E2E_LABEL_FILTER=mcp ./run_tests.sh

# Run proxy and middleware tests
E2E_LABEL_FILTER='proxy || middleware' ./run_tests.sh
```

### Run with Ginkgo Directly

```bash
cd test/e2e

# Run all tests
ginkgo run --vv .

# Run specific label
ginkgo run --label-filter="api" .

# Run specific test file
ginkgo run --focus-file="api_healthcheck_test.go" .
```

### Run from Project Root

```bash
# Run all e2e tests
task test-e2e

# Run with custom label filter
E2E_LABEL_FILTER=api task test-e2e
```

## GitHub Actions Integration

The e2e tests run in parallel in GitHub Actions using label filters. The workflow:

1. Builds the ToolHive binary once and shares it across jobs
2. Runs tests in parallel using matrix strategy with label filters:
   - **core**: Core CLI functionality
   - **mcp**: MCP protocol tests
   - **proxy-mw**: Proxy, middleware, and stability tests
   - **api**: HTTP API tests
3. Uploads test results as artifacts

See `.github/workflows/e2e-tests.yml` for the full configuration.

## Writing Tests

### Adding New CLI Tests

1. Create a new test file (e.g., `feature_test.go`)
2. Add appropriate labels for categorization
3. Use existing helper functions from `helpers.go`
4. Follow the pattern of existing tests

Example:
```go
var _ = Describe("Feature Name", Label("core", "e2e"), func() {
    It("should do something", func() {
        // Test implementation
    })
})
```

### Adding New API Tests

1. Create a new test file (e.g., `api_workloads_test.go`)
2. Use the `api` label along with specific labels
3. Use `e2e.StartServer()` helper to start the API server
4. Make HTTP requests using the server's methods

Example:
```go
var _ = Describe("Workloads API", Label("api", "workloads"), func() {
    var apiServer *e2e.Server

    BeforeEach(func() {
        config := e2e.NewServerConfig()
        apiServer = e2e.StartServer(config)
    })

    It("should list workloads", func() {
        resp, err := apiServer.Get("/api/v1beta/workloads")
        Expect(err).ToNot(HaveOccurred())
        defer resp.Body.Close()
        Expect(resp.StatusCode).To(Equal(http.StatusOK))
    })
})
```

## Troubleshooting

### Container Runtime Not Available

Ensure Docker, Podman, or Colima is running:
```bash
docker ps
# or
podman ps
# or
colima status
```

### Binary Not Found (CLI Tests)

Build the ToolHive binary:
```bash
task build
# Binary will be at ./bin/thv
```

Set the binary path if needed:
```bash
export THV_BINARY=/path/to/thv
```

### Test Timeouts

Increase the timeout:
```bash
TEST_TIMEOUT=20m ./run_tests.sh
```

### Port Conflicts (API Tests)

API tests use random available ports by default. If you encounter port binding issues, the system will automatically find an available port.

## Test Best Practices

1. **Use descriptive labels** - Make it easy to filter and run related tests
2. **Clean up resources** - Use `DeferCleanup` or `AfterEach` to clean up
3. **Use unique names** - Use `GenerateUniqueServerName()` for server names
4. **Avoid hardcoded ports** - Use random ports for API tests
5. **Test isolation** - Ensure tests can run independently
6. **Meaningful assertions** - Add context messages to assertions
7. **Use Serial when needed** - Mark tests as `Serial` if they can't run in parallel
