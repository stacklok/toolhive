# VirtualMCPServer E2E Tests

This directory contains end-to-end tests for the VirtualMCPServer controller that run against a real Kubernetes cluster.

## Prerequisites

- A Kubernetes cluster with the ToolHive operator installed
- `kubectl` configured to access the cluster
- The VirtualMCPServer CRDs installed

Note: The Ginkgo CLI is automatically installed by the task commands when running tests.

## Running the Tests

### Using the Task Command (Recommended)

The easiest way to run the tests is using the task command from the operator directory, which will:
1. Create a Kind cluster
2. Install CRDs
3. Deploy the operator
4. Run all tests from test/e2e/thv-operator/virtualmcp (including setup)
5. Clean up the cluster

```bash
# Run from the project root
task thv-operator-e2e-test
```

### Manual Testing

The tests will:
- Use the kubeconfig from `$KUBECONFIG` or `~/.kube/config`
- Create all necessary resources (MCPGroup, MCPServers, VirtualMCPServer)
- Run comprehensive MCP protocol tests
- Clean up resources after completion

```bash
cd test/e2e/thv-operator/virtualmcp
ginkgo -v
```

### Customizing Test Parameters

You can customize the kubeconfig path using the `KUBECONFIG` environment variable:

```bash
export KUBECONFIG="/path/to/kubeconfig"
ginkgo -v
```

### Running Specific Tests

```bash
# Run only discovered mode tests
ginkgo -v --focus="Discovered Mode"

# Run tests and get verbose output
ginkgo -vv
```

## Test Structure

### Files

- `suite_test.go` - Ginkgo test suite setup with kubeconfig loading
- `virtualmcp_discovered_mode_test.go` - Tests VirtualMCPServer with discovered mode aggregation
- `helpers.go` - Common helper functions for interacting with Kubernetes resources
- `README.md` - This file

### Test Descriptions

#### Discovered Mode Tests (`virtualmcp_discovered_mode_test.go`)
Comprehensive E2E tests for VirtualMCPServer in discovered mode, which automatically discovers and aggregates tools from backend MCP servers in a group:
- Creates two backend MCPServers with different transports (SSE and streamable-http)
- Verifies individual backend connectivity and tool listing via MCP protocol
- Verifies VirtualMCPServer aggregates tools from all backends
- Tests tool calls through the VirtualMCPServer proxy
- Validates discovered mode configuration and backend discovery

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `KUBECONFIG` | Path to kubeconfig file | `~/.kube/config` |

## Adding New Tests

To add new test cases:

1. Create a new test file following the naming pattern `*_test.go`
2. Use the shared `k8sClient` and `ctx` variables from `suite_test.go`
3. Use helper functions from `helpers.go` when possible
4. Follow Ginkgo BDD style with `Describe`, `Context`, and `It` blocks

Example:

```go
var _ = Describe("VirtualMCPServer Feature", func() {
    Context("when testing feature X", func() {
        It("should behave as expected", func() {
            // Test implementation
        })
    })
})
```

## Troubleshooting

### Tests fail with "kubeconfig file should exist"

Ensure your `KUBECONFIG` environment variable points to a valid kubeconfig file, or that `~/.kube/config` exists.

### Tests fail with "VirtualMCPServer should exist"

Make sure:
1. The ToolHive operator is running in your cluster
2. The VirtualMCPServer CRDs are installed
3. The tests create their own VirtualMCPServer resources for testing

### Tests timeout waiting for resources

Check:
1. The operator is running: `kubectl get pods -n toolhive-system`
2. The operator logs for errors: `kubectl logs -n toolhive-system -l app.kubernetes.io/name=thv-operator`
3. The VirtualMCPServer status: `kubectl get virtualmcpserver -n <namespace> <name> -o yaml`
