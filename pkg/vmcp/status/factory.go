package status

import (
	"fmt"
	"os"

	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// EnvVMCPName is the environment variable for the VirtualMCPServer name
	EnvVMCPName = "VMCP_NAME"

	// EnvVMCPNamespace is the environment variable for the VirtualMCPServer namespace
	EnvVMCPNamespace = "VMCP_NAMESPACE"
)

// NewReporter creates an appropriate Reporter based on the runtime environment.
//
// Detection logic:
//  1. If VMCP_NAME and VMCP_NAMESPACE env vars are set → Kubernetes mode → K8sReporter
//  2. Otherwise → CLI mode → NoOpReporter
//
// In Kubernetes mode, the function uses in-cluster configuration to create
// a Kubernetes client for updating VirtualMCPServer status.
//
// Returns:
//   - Reporter instance (K8sReporter or NoOpReporter)
//   - Error if Kubernetes mode is detected but client creation fails
func NewReporter() (Reporter, error) {
	vmcpName := os.Getenv(EnvVMCPName)
	vmcpNamespace := os.Getenv(EnvVMCPNamespace)

	// Check if we're in Kubernetes mode
	if vmcpName != "" && vmcpNamespace != "" {
		logger.Infof("Kubernetes mode detected (VMCP_NAME=%s, VMCP_NAMESPACE=%s), creating K8sReporter", vmcpName, vmcpNamespace)

		// Get in-cluster REST config
		restConfig, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		// Create K8sReporter
		k8sReporter, err := NewK8sReporter(restConfig, vmcpName, vmcpNamespace)
		if err != nil {
			return nil, fmt.Errorf("failed to create K8sReporter: %w", err)
		}

		logger.Infof("K8sReporter created for %s/%s", vmcpNamespace, vmcpName)
		return k8sReporter, nil
	}

	// CLI mode - use NoOpReporter
	logger.Debug("CLI mode detected, creating NoOpReporter")
	return NewNoOpReporter(), nil
}
