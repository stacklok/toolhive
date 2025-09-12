package docker

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestDockerToDomainStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect runtime.WorkloadStatus
	}{
		{"running", "running", runtime.WorkloadStatusRunning},
		{"created", "created", runtime.WorkloadStatusStarting},
		{"restarting", "restarting", runtime.WorkloadStatusStarting},
		{"paused", "paused", runtime.WorkloadStatusStopped},
		{"exited", "exited", runtime.WorkloadStatusStopped},
		{"dead", "dead", runtime.WorkloadStatusStopped},
		{"removing", "removing", runtime.WorkloadStatusRemoving},
		{"unknown", "something-else", runtime.WorkloadStatusUnknown},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dockerToDomainStatus(tt.input)
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestExtractFirstPort(t *testing.T) {
	t.Parallel()

	t.Run("returns one of the exposed ports", func(t *testing.T) {
		t.Parallel()
		opts := &runtime.DeployWorkloadOptions{
			ExposedPorts: map[string]struct{}{
				"8080/tcp": {},
				"9090/tcp": {},
			},
		}
		got, err := extractFirstPort(opts)
		require.NoError(t, err)
		// Map iteration order is randomized; assert membership
		assert.True(t, got == 8080 || got == 9090, "got %d, expected 8080 or 9090", got)
	})

	t.Run("error on empty", func(t *testing.T) {
		t.Parallel()
		opts := &runtime.DeployWorkloadOptions{
			ExposedPorts: map[string]struct{}{},
		}
		_, err := extractFirstPort(opts)
		require.Error(t, err)
	})
}

func TestGeneratePortBindings_AuxiliaryKeepsHostPort(t *testing.T) {
	t.Parallel()

	labels := map[string]string{
		"toolhive-auxiliary": "true",
	}
	in := map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "", HostPort: "12345"},
		},
	}
	out, hostPort, err := generatePortBindings(labels, in)
	require.NoError(t, err)

	require.Contains(t, out, "8080/tcp")
	require.Len(t, out["8080/tcp"], 1)
	assert.Equal(t, "12345", out["8080/tcp"][0].HostPort)
	assert.Equal(t, 12345, hostPort)
}

func TestGeneratePortBindings_NonAuxiliaryAssignsRandomPortAndMutatesFirstBinding(t *testing.T) {
	t.Parallel()

	labels := map[string]string{} // not auxiliary
	in := map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "", HostPort: ""}, // to be filled by function
		},
		"9090/tcp": {
			{HostIP: "", HostPort: ""}, // additional entry to ensure only first binding gets updated
		},
	}
	out, hostPort, err := generatePortBindings(labels, in)
	require.NoError(t, err)
	require.NotZero(t, hostPort)

	// The function updates the first binding it encounters with the random host port.
	// We don't know which key is first (map iteration), but exactly one binding's HostPort
	// should be set to hostPort (as string). Validate this invariant.
	expected := fmt.Sprintf("%d", hostPort)

	countMatches := 0
	for _, bindings := range out {
		if len(bindings) > 0 && bindings[0].HostPort == expected {
			countMatches++
		}
	}

	assert.Equal(t, 1, countMatches, "expected exactly one first binding to be updated to hostPort=%s", expected)
}

func TestAddEgressEnvVars_SetsAll(t *testing.T) {
	t.Parallel()

	vars := addEgressEnvVars(nil, "egress-proxy")
	require.NotNil(t, vars)

	host := "http://egress-proxy:3128"
	assert.Equal(t, host, vars["HTTP_PROXY"])
	assert.Equal(t, host, vars["HTTPS_PROXY"])
	assert.Equal(t, host, vars["http_proxy"])
	assert.Equal(t, host, vars["https_proxy"])
	assert.Equal(t, "localhost,127.0.0.1,::1", vars["NO_PROXY"])
	assert.Equal(t, "localhost,127.0.0.1,::1", vars["no_proxy"])
}

func TestAddEgressEnvVars_PreservesExistingAndOverrides(t *testing.T) {
	t.Parallel()

	input := map[string]string{"EXISTING": "1", "HTTP_PROXY": "old"}
	out := addEgressEnvVars(input, "egress-proxy")
	require.NotNil(t, out)
	assert.Equal(t, "1", out["EXISTING"])

	// Should override HTTP_PROXY
	assert.Equal(t, "http://egress-proxy:3128", out["HTTP_PROXY"])
}
