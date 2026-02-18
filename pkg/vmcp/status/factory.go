// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"fmt"
	"log/slog"
	"os"

	"k8s.io/client-go/rest"
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
//  2. Otherwise → CLI mode → LoggingReporter
//
// In Kubernetes mode, the function uses in-cluster configuration to create
// a Kubernetes client for updating VirtualMCPServer status.
//
// Returns:
//   - Reporter instance (K8sReporter or LoggingReporter)
//   - Error if Kubernetes mode is detected but client creation fails
func NewReporter() (Reporter, error) {
	vmcpName := os.Getenv(EnvVMCPName)
	vmcpNamespace := os.Getenv(EnvVMCPNamespace)
	return newReporterFromEnv(vmcpName, vmcpNamespace)
}

// newReporterFromEnv creates a Reporter based on the provided environment variable values.
// This function is extracted for testability - tests can call this directly with different
// values without manipulating global environment state, enabling parallel test execution.
func newReporterFromEnv(vmcpName, vmcpNamespace string) (Reporter, error) {
	// Check if we're in Kubernetes mode
	if vmcpName != "" && vmcpNamespace != "" {
		//nolint:gosec // G706: vmcpName and vmcpNamespace are from trusted env vars
		slog.Debug("Kubernetes mode detected, creating K8sReporter", "vmcp_name", vmcpName, "vmcp_namespace", vmcpNamespace)

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

		//nolint:gosec // G706: vmcpName and vmcpNamespace are from trusted env vars
		slog.Debug("K8sReporter created", "namespace", vmcpNamespace, "name", vmcpName)
		return k8sReporter, nil
	}

	// CLI mode - use LoggingReporter
	slog.Debug("CLI mode detected, creating LoggingReporter")
	return NewLoggingReporter(), nil
}
