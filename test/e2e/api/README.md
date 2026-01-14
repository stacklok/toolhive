# API End-to-End Tests

This directory contains end-to-end tests for the ToolHive HTTP API.

## Overview

These tests validate the ToolHive HTTP API by starting a real API server instance and making HTTP requests to verify the endpoints work correctly. Unlike unit tests that mock dependencies, these e2e tests exercise the full API stack including:

- HTTP server and routing
- Middleware (auth, recovery, headers, etc.)
- Container runtime integration
- API handlers

## Structure

- `api_suite_test.go` - Ginkgo test suite setup
- `api_helpers.go` - Helper functions for starting API server and making requests
- `healthcheck_test.go` - Tests for the `/health` endpoint
- `run_tests.sh` - Test runner script

## Running Tests

### Prerequisites

- Go installed
- Ginkgo CLI installed: `go install github.com/onsi/ginkgo/v2/ginkgo@latest`
- Docker or Podman container runtime

### Run all API tests

```bash
cd test/e2e/api
./run_tests.sh
```

### Run tests with Ginkgo directly

```bash
cd test/e2e/api
ginkgo run --vv .
```

### Run specific tests by label

```bash
cd test/e2e/api
ginkgo run --label-filter="healthcheck" .
```

### Run from project root

```bash
cd test/e2e/api && ./run_tests.sh
```

## Test Labels

Tests are labeled to support parallelization in CI:

- `api` - All API tests
- `healthcheck` - Health check endpoint tests

## GitHub Actions Integration

The API tests run in parallel with CLI tests in the GitHub Actions workflow. The workflow:

1. Sets up Go and installs dependencies
2. Installs Ginkgo CLI
3. Ensures Docker is running
4. Runs tests with label filters for parallelization
5. Uploads test results as artifacts

See `.github/workflows/e2e-tests.yml` for the full configuration.

## Adding New Tests

To add new API endpoint tests:

1. Create a new test file (e.g., `workloads_test.go`)
2. Use the `api.StartAPIServer()` helper to start the server
3. Add appropriate labels for test categorization
4. Use the server's `Get()` or `GetWithHeaders()` methods to make requests
5. Update this README with the new test coverage

Example:

```go
var _ = Describe("Workloads API", Label("api", "workloads"), func() {
    var apiServer *api.Server

    BeforeEach(func() {
        config := api.NewServerConfig()
        apiServer = api.StartServer(config)
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

### Container runtime not available

Ensure Docker or Podman is running:
```bash
docker ps
# or
podman ps
```

### Port conflicts

The test server uses a random available port by default. If you encounter port binding issues, the system will automatically find an available port.

### Test timeouts

If tests timeout, increase the timeout:
```bash
TEST_TIMEOUT=20m ./run_tests.sh
```
