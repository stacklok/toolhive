// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
		logger.Debugf("Kubernetes mode detected (VMCP_NAME=%s, VMCP_NAMESPACE=%s), creating K8sReporter", vmcpName, vmcpNamespace)

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

		logger.Debugf("K8sReporter created for %s/%s", vmcpNamespace, vmcpName)
		return k8sReporter, nil
	}

	// CLI mode - use LoggingReporter
	logger.Debug("CLI mode detected, creating LoggingReporter")
	return NewLoggingReporter(), nil
}
