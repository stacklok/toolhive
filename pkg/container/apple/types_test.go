// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestParseAppleTimestamp(t *testing.T) {
	t.Parallel()

	zero := 0.0
	neg := -10.5
	nan := math.NaN()
	inf := math.Inf(1)
	valid := 788918400.5 // 2026-01-01T00:00:00.5Z (25 years from 2001-01-01)

	assert.True(t, parseAppleTimestamp(nil).IsZero())
	assert.True(t, parseAppleTimestamp(&zero).IsZero())
	assert.True(t, parseAppleTimestamp(&neg).IsZero())
	assert.True(t, parseAppleTimestamp(&nan).IsZero())
	assert.True(t, parseAppleTimestamp(&inf).IsZero())

	got := parseAppleTimestamp(&valid)
	expected := appleReferenceEpoch.Add(788918400*time.Second + 500*time.Millisecond)
	assert.Equal(t, expected, got)
}

func TestStripCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "192.168.64.2/24", want: "192.168.64.2"},
		{input: "192.168.64.2", want: "192.168.64.2"},
		{input: "fd00::1/64", want: "fd00::1"},
		{input: "  ", want: ""},
		{input: "not-an-ip/24", want: "not-an-ip"},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, stripCIDR(tc.input))
	}
}

func TestAppleToDomainStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want runtime.WorkloadStatus
	}{
		{raw: "running", want: runtime.WorkloadStatusRunning},
		{raw: "RUNNING", want: runtime.WorkloadStatusRunning},
		{raw: "stopped", want: runtime.WorkloadStatusStopped},
		{raw: "exited", want: runtime.WorkloadStatusStopped},
		{raw: "created", want: runtime.WorkloadStatusStarting},
		{raw: "booting", want: runtime.WorkloadStatusStarting},
		{raw: "starting", want: runtime.WorkloadStatusStarting},
		{raw: "stopping", want: runtime.WorkloadStatusStopping},
		{raw: "error", want: runtime.WorkloadStatusError},
		{raw: "failed", want: runtime.WorkloadStatusError},
		{raw: "mystery", want: runtime.WorkloadStatusUnknown},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, appleToDomainStatus(tc.raw))
	}
}

func TestToContainerInfo(t *testing.T) {
	t.Parallel()

	inputLabels := map[string]string{"toolhive": "true"}
	cont := appleContainerJSON{
		Status:    "running",
		StartedAt: appleReferenceEpoch.Add(100 * time.Second),
		Configuration: appleContainerConfigJSON{
			ID:     "mcp-test",
			Labels: inputLabels,
			Image:  appleContainerImageJSON{Reference: "ghcr.io/example/mcp:latest"},
			PublishedPorts: []applePublishedPortJSON{
				{ContainerPort: 8080, HostPort: 18080, Count: 2, Proto: ""},
				{ContainerPort: 9000, HostPort: 19000, Count: 1, Proto: "UDP"},
			},
		},
	}

	info := toContainerInfo(cont)
	assert.Equal(t, "mcp-test", info.Name)
	assert.Equal(t, "ghcr.io/example/mcp:latest", info.Image)
	assert.Equal(t, runtime.WorkloadStatusRunning, info.State)
	assert.Equal(t, appleReferenceEpoch.Add(100*time.Second), info.StartedAt)
	assert.Equal(t, []runtime.PortMapping{
		{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"},
		{ContainerPort: 8081, HostPort: 18081, Protocol: "tcp"},
		{ContainerPort: 9000, HostPort: 19000, Protocol: "udp"},
	}, info.Ports)

	// Labels must be cloned so mutating info.Labels does not affect source map.
	info.Labels["extra"] = "mutated"
	assert.NotContains(t, inputLabels, "extra")
}

func TestIsNotFoundError(t *testing.T) {
	t.Parallel()

	assert.False(t, isNotFoundError(nil))
	assert.False(t, isNotFoundError(errors.New("permission denied")))
	assert.False(t, isNotFoundError(errors.New("service not found")))
	assert.False(t, isNotFoundError(errors.New("XPC endpoint does not exist")))
	assert.False(t, isNotFoundError(&CommandError{
		Command: containerBinary,
		Args:    []string{"inspect", "--", "missing"},
		Stderr:  "Error: daemon service not found",
		Err:     errors.New("exit status 1"),
	}))

	assert.True(t, isNotFoundError(errors.New("Error: container not found: missing")))
	assert.True(t, isNotFoundError(errors.New("Error: image not found: alpine:bad")))
	assert.True(t, isNotFoundError(errors.New("Error: network not found: toolhive-foo-internal")))
	assert.True(t, isNotFoundError(errors.New(`Error: container with ID missing not found`)))
	assert.True(t, isNotFoundError(&CommandError{
		Command: containerBinary,
		Args:    []string{"inspect", "--", "missing"},
		Stderr:  `Error: notFound: "container with ID missing not found"`,
		Err:     errors.New("exit status 1"),
	}))
	assert.True(t, isNotFoundError(&CommandError{
		Command: containerBinary,
		Args:    []string{"logs", "--", "missing"},
		Stderr:  `Error: failed to get logs for container missing (cause: "internalError: \"failed to open container logs: notFound: \"container with ID missing not found\"\"")`,
		Err:     errors.New("exit status 1"),
	}))
}

func TestParseInspectOutput(t *testing.T) {
	t.Parallel()

	t.Run("empty and null return ErrContainerNotFound", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"", "   ", "[]", "null"} {
			_, err := parseInspectOutput([]byte(raw))
			assert.ErrorIs(t, err, runtime.ErrContainerNotFound)
		}
	})

	t.Run("array payload", func(t *testing.T) {
		t.Parallel()
		cont, err := parseInspectOutput([]byte(`[{"status":"running","configuration":{"id":"c1","image":{"reference":"alpine:latest"}}}]`))
		require.NoError(t, err)
		assert.Equal(t, "c1", cont.Configuration.ID)
		assert.Equal(t, "running", cont.Status)
	})

	t.Run("object payload", func(t *testing.T) {
		t.Parallel()
		cont, err := parseInspectOutput([]byte(`{"status":"stopped","configuration":{"id":"c2","image":{"reference":"alpine:latest"}}}`))
		require.NoError(t, err)
		assert.Equal(t, "c2", cont.Configuration.ID)
		assert.Equal(t, "stopped", cont.Status)
	})

	t.Run("nested status object payload (apple/container 1.7+)", func(t *testing.T) {
		t.Parallel()
		raw := `[{
			"id": "c3",
			"configuration": {
				"id": "c3",
				"creationDate": "2026-11-24T13:24:21Z",
				"image": {"reference": "docker.io/library/alpine:3.20"},
				"labels": {"toolhive": "true"}
			},
			"status": {
				"state": "running",
				"startedDate": "2026-11-24T13:24:22Z",
				"networks": [{"network": "default", "ipv4Address": "192.168.64.2/24", "ipv4Gateway": "192.168.64.1"}]
			}
		}]`
		cont, err := parseInspectOutput([]byte(raw))
		require.NoError(t, err)
		assert.Equal(t, "c3", cont.Configuration.ID)
		assert.Equal(t, "running", cont.Status)
		require.Len(t, cont.Networks, 1)
		assert.Equal(t, "192.168.64.2/24", cont.Networks[0].IPv4Address)

		info := toContainerInfo(cont)
		assert.Equal(t, runtime.WorkloadStatusRunning, info.State)
		assert.Equal(t, time.Date(2026, 11, 24, 13, 24, 21, 0, time.UTC), info.Created)
		assert.Equal(t, time.Date(2026, 11, 24, 13, 24, 22, 0, time.UTC), info.StartedAt)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseInspectOutput([]byte(`{invalid`))
		require.Error(t, err)
		assert.NotErrorIs(t, err, runtime.ErrContainerNotFound)
	})
}
