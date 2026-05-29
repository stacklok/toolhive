// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

// TestClientName verifies that Name reports the concrete detected runtime type
// rather than the static factory-registration constant. Distinct identities are
// required so that workload-ownership tracking does not let Docker- and
// Podman-owned workloads (separate daemons) corrupt each other's status.
func TestClientName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runtimeType rt.Type
		want        string
	}{
		{name: "docker", runtimeType: rt.TypeDocker, want: "docker"},
		{name: "podman", runtimeType: rt.TypePodman, want: "podman"},
		{name: "colima", runtimeType: rt.TypeColima, want: "colima"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Client{runtimeType: tt.runtimeType}
			assert.Equal(t, tt.want, c.Name())
		})
	}
}

// TestClientNameOwnershipRoundTrip asserts the property the cross-runtime
// protection relies on: the identity stamped at create time and the identity
// compared at reconcile time come from the same Name call, so a workload created
// under Podman is recognized as Podman-owned (and not as Docker-owned) later.
func TestClientNameOwnershipRoundTrip(t *testing.T) {
	t.Parallel()

	podman := &Client{runtimeType: rt.TypePodman}
	docker := &Client{runtimeType: rt.TypeDocker}

	// Identity recorded on the RunConfig when the workload is created.
	stampedAtCreate := podman.Name()

	// Same Podman runtime later reconciling: must recognize ownership.
	assert.Equal(t, stampedAtCreate, podman.Name(),
		"podman must recognize its own workloads across calls")

	// A different runtime (Docker) reconciling the same workload: must NOT
	// claim ownership, otherwise it would corrupt the Podman workload's status.
	assert.NotEqual(t, stampedAtCreate, docker.Name(),
		"docker must not claim ownership of a podman-created workload")
}
