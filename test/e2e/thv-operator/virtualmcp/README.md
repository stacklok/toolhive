# VirtualMCPServer E2E Tests

This directory contains end-to-end tests for the VirtualMCPServer controller that run against a real Kubernetes cluster.

## Prerequisites

- A Kubernetes cluster with the ToolHive operator installed
- `kubectl` configured to access the cluster
- The VirtualMCPServer CRDs installed
- Ginkgo CLI installed: `go install github.com/onsi/ginkgo/v2/ginkgo@latest`

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

#### With Resource Creation (Full E2E)

Run the setup test that creates VirtualMCPServer resources:

```bash
cd test/e2e/thv-operator/virtualmcp
ginkgo -v --focus="Setup and Lifecycle"
```

#### Against Existing Resources

By default, the tests will:
- Use the kubeconfig from `$KUBECONFIG` or `~/.kube/config`
- Look for a VirtualMCPServer named `test-vmcp-server` in the `default` namespace

```bash
cd test/e2e/thv-operator/virtualmcp
ginkgo -v
```

### Customizing Test Parameters

You can customize the test parameters using environment variables:

```bash
# Test against a specific VirtualMCPServer
export TEST_NAMESPACE="my-namespace"
export VMCP_SERVER_NAME="my-vmcp-server"
export KUBECONFIG="/path/to/kubeconfig"

ginkgo -v
```

### Running Specific Tests

```bash
# Run only deployment verification tests
ginkgo -v --focus="Deployment"

# Run tests and get verbose output
ginkgo -vv
```

## Test Structure

### Files

- `suite_test.go` - Ginkgo test suite setup with kubeconfig loading
- `virtualmcp_setup_test.go` - Tests that create and manage VirtualMCPServer resources
- `virtualmcp_deployment_test.go` - Tests for verifying VirtualMCPServer deployment
- `helpers.go` - Common helper functions for interacting with Kubernetes resources
- `README.md` - This file

### Test Coverage

The current test suite includes two types of tests:

#### Setup and Lifecycle Tests (`virtualmcp_setup_test.go`)
1. **Resource Creation**
   - Creates MCPServer backend
   - Creates MCPGroup
   - Creates VirtualMCPServer
   - Verifies resource creation

2. **Validation**
   - VirtualMCPServer references correct MCPGroup
   - Authentication configuration is applied
   - Controller creates Deployment and Service

#### Deployment Verification Tests (`virtualmcp_deployment_test.go`)
1. **Resource Existence**
   - VirtualMCPServer CRD exists
   - Referenced MCPGroup exists
   - Deployment is created
   - Service is created
   - Pods are created

2. **Resource Health**
   - Deployment has ready replicas
   - Pods are running and ready
   - VirtualMCPServer has Ready condition

3. **Resource Configuration**
   - Correct labels are applied
   - Service selectors match pods
   - vmcp container is properly configured
   - Ports are exposed correctly

4. **Configuration Inspection**
   - Aggregation configuration
   - Incoming authentication settings
   - Outgoing authentication settings

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `KUBECONFIG` | Path to kubeconfig file | `~/.kube/config` |
| `TEST_NAMESPACE` | Kubernetes namespace for tests | `default` |
| `VMCP_SERVER_NAME` | Name of the VirtualMCPServer to test | `test-vmcp-server` |
| `SKIP_IF_NOT_FOUND` | Skip deployment verification tests if VirtualMCPServer doesn't exist | `false` |

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
3. A VirtualMCPServer resource exists with the name specified in `VMCP_SERVER_NAME`
4. The resource is in the namespace specified in `TEST_NAMESPACE`

### Tests timeout waiting for resources

Check:
1. The operator is running: `kubectl get pods -n toolhive-system`
2. The operator logs for errors: `kubectl logs -n toolhive-system -l app.kubernetes.io/name=thv-operator`
3. The VirtualMCPServer status: `kubectl get virtualmcpserver -n <namespace> <name> -o yaml`

## Future Enhancements

Potential areas for expansion:

- [ ] Add tests for composite tool functionality
- [ ] Test authentication and authorization flows
- [ ] Test token caching behavior
- [ ] Test aggregation and conflict resolution
- [ ] Test integration with backend MCPServers
- [ ] Add performance/load tests
- [ ] Test failure scenarios and recovery
- [ ] Test updating VirtualMCPServer configurations
