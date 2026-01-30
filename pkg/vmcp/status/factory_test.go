// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReporter_CLIMode(t *testing.T) {
	t.Parallel()

	// Test with empty env vars (CLI mode)
	reporter, err := newReporterFromEnv("", "")
	require.NoError(t, err)
	assert.IsType(t, &LoggingReporter{}, reporter)
}

func TestNewReporter_K8sMode_MissingNamespace(t *testing.T) {
	t.Parallel()

	// Set only name, missing namespace
	reporter, err := newReporterFromEnv("test-vmcp", "")
	require.NoError(t, err)
	// Should fall back to LoggingReporter when namespace is missing
	assert.IsType(t, &LoggingReporter{}, reporter)
}

func TestNewReporter_K8sMode_MissingName(t *testing.T) {
	t.Parallel()

	// Set only namespace, missing name
	reporter, err := newReporterFromEnv("", "default")
	require.NoError(t, err)
	// Should fall back to LoggingReporter when name is missing
	assert.IsType(t, &LoggingReporter{}, reporter)
}

func TestNewReporter_K8sMode_OutsideCluster(t *testing.T) {
	t.Parallel()

	// Set both env vars to trigger K8s mode
	reporter, err := newReporterFromEnv("test-vmcp", "default")
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
