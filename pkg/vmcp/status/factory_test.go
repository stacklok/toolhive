// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReporter_CLIMode(t *testing.T) {
	t.Parallel()

	// Ensure env vars are not set
	originalName := os.Getenv(EnvVMCPName)
	originalNamespace := os.Getenv(EnvVMCPNamespace)
	defer func() {
		if originalName != "" {
			os.Setenv(EnvVMCPName, originalName)
		} else {
			os.Unsetenv(EnvVMCPName)
		}
		if originalNamespace != "" {
			os.Setenv(EnvVMCPNamespace, originalNamespace)
		} else {
			os.Unsetenv(EnvVMCPNamespace)
		}
	}()

	os.Unsetenv(EnvVMCPName)
	os.Unsetenv(EnvVMCPNamespace)

	reporter, err := NewReporter()
	require.NoError(t, err)
	assert.IsType(t, &LoggingReporter{}, reporter)
}

func TestNewReporter_K8sMode_MissingNamespace(t *testing.T) {
	t.Parallel()

	// Set only name, missing namespace
	originalName := os.Getenv(EnvVMCPName)
	originalNamespace := os.Getenv(EnvVMCPNamespace)
	defer func() {
		if originalName != "" {
			os.Setenv(EnvVMCPName, originalName)
		} else {
			os.Unsetenv(EnvVMCPName)
		}
		if originalNamespace != "" {
			os.Setenv(EnvVMCPNamespace, originalNamespace)
		} else {
			os.Unsetenv(EnvVMCPNamespace)
		}
	}()

	os.Setenv(EnvVMCPName, "test-vmcp")
	os.Unsetenv(EnvVMCPNamespace)

	reporter, err := NewReporter()
	require.NoError(t, err)
	// Should fall back to LoggingReporter when namespace is missing
	assert.IsType(t, &LoggingReporter{}, reporter)
}

func TestNewReporter_K8sMode_MissingName(t *testing.T) {
	t.Parallel()

	// Set only namespace, missing name
	originalName := os.Getenv(EnvVMCPName)
	originalNamespace := os.Getenv(EnvVMCPNamespace)
	defer func() {
		if originalName != "" {
			os.Setenv(EnvVMCPName, originalName)
		} else {
			os.Unsetenv(EnvVMCPName)
		}
		if originalNamespace != "" {
			os.Setenv(EnvVMCPNamespace, originalNamespace)
		} else {
			os.Unsetenv(EnvVMCPNamespace)
		}
	}()

	os.Unsetenv(EnvVMCPName)
	os.Setenv(EnvVMCPNamespace, "default")

	reporter, err := NewReporter()
	require.NoError(t, err)
	// Should fall back to LoggingReporter when name is missing
	assert.IsType(t, &LoggingReporter{}, reporter)
}

//nolint:paralleltest // Cannot run in parallel due to environment variable manipulation
func TestNewReporter_K8sMode_OutsideCluster(t *testing.T) {
	// Note: This test cannot be run in parallel because it relies on environment state

	// Set both env vars to trigger K8s mode
	originalName := os.Getenv(EnvVMCPName)
	originalNamespace := os.Getenv(EnvVMCPNamespace)
	defer func() {
		if originalName != "" {
			os.Setenv(EnvVMCPName, originalName)
		} else {
			os.Unsetenv(EnvVMCPName)
		}
		if originalNamespace != "" {
			os.Setenv(EnvVMCPNamespace, originalNamespace)
		} else {
			os.Unsetenv(EnvVMCPNamespace)
		}
	}()

	os.Setenv(EnvVMCPName, "test-vmcp")
	os.Setenv(EnvVMCPNamespace, "default")

	reporter, err := NewReporter()
	// Outside cluster, InClusterConfig() will fail
	// This is expected when running tests locally
	if err != nil {
		assert.Contains(t, err.Error(), "failed to get in-cluster config")
		assert.Nil(t, reporter)
	} else {
		// If somehow we're in a cluster environment, verify K8sReporter was created
		assert.IsType(t, &K8sReporter{}, reporter)
	}
}

func TestEnvVarConstants(t *testing.T) {
	t.Parallel()

	// Verify constants match what operator sets
	assert.Equal(t, "VMCP_NAME", EnvVMCPName)
	assert.Equal(t, "VMCP_NAMESPACE", EnvVMCPNamespace)
}
