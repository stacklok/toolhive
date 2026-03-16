// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"testing"

	"github.com/stacklok/go-microvm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestBuildEgressPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		netPerm  *permissions.NetworkPermissions
		expected *microvm.EgressPolicy
	}{
		{
			name:     "nil network permissions",
			netPerm:  nil,
			expected: nil,
		},
		{
			name:     "nil outbound",
			netPerm:  &permissions.NetworkPermissions{},
			expected: nil,
		},
		{
			name: "insecure allow all",
			netPerm: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					InsecureAllowAll: true,
					AllowHost:        []string{"example.com"},
				},
			},
			expected: nil,
		},
		{
			name: "empty allow hosts",
			netPerm: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{},
				},
			},
			expected: nil,
		},
		{
			name: "hosts without ports",
			netPerm: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"api.github.com", "*.docker.io"},
				},
			},
			expected: &microvm.EgressPolicy{
				AllowedHosts: []microvm.EgressHost{
					{Name: "api.github.com"},
					{Name: "*.docker.io"},
				},
			},
		},
		{
			name: "hosts with ports",
			netPerm: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{443, 8080},
				},
			},
			expected: &microvm.EgressPolicy{
				AllowedHosts: []microvm.EgressHost{
					{Name: "example.com", Ports: []uint16{443, 8080}},
				},
			},
		},
		{
			name: "ports outside valid range are skipped",
			netPerm: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{0, -1, 443, 70000},
				},
			},
			expected: &microvm.EgressPolicy{
				AllowedHosts: []microvm.EgressHost{
					{Name: "example.com", Ports: []uint16{443}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildEgressPolicy(tc.netPerm)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestBuildVirtioFSMounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		permConfig *runtime.PermissionConfig
		expected   []microvm.VirtioFSMount
	}{
		{
			name:       "nil permission config",
			permConfig: nil,
			expected:   nil,
		},
		{
			name: "no bind mounts",
			permConfig: &runtime.PermissionConfig{
				Mounts: []runtime.Mount{
					{Source: "/tmp/foo", Target: "/tmp/foo", Type: runtime.MountTypeTmpfs},
				},
			},
			expected: nil,
		},
		{
			name: "bind mounts only",
			permConfig: &runtime.PermissionConfig{
				Mounts: []runtime.Mount{
					{Source: "/data/models", Target: "/models", Type: runtime.MountTypeBind},
					{Source: "/home/user/config", Target: "/config", Type: runtime.MountTypeBind},
				},
			},
			expected: []microvm.VirtioFSMount{
				{Tag: "thv0", HostPath: "/data/models"},
				{Tag: "thv1", HostPath: "/home/user/config"},
			},
		},
		{
			name: "mixed mount types filters to bind only",
			permConfig: &runtime.PermissionConfig{
				Mounts: []runtime.Mount{
					{Source: "/data", Target: "/data", Type: runtime.MountTypeBind},
					{Source: "", Target: "/tmp", Type: runtime.MountTypeTmpfs},
					{Source: "/logs", Target: "/logs", Type: runtime.MountTypeBind},
				},
			},
			expected: []microvm.VirtioFSMount{
				{Tag: "thv0", HostPath: "/data"},
				{Tag: "thv2", HostPath: "/logs"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildVirtioFSMounts(tc.permConfig)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestMountTag(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "thv0", mountTag(0, "/data"))
	assert.Equal(t, "thv5", mountTag(5, "/whatever"))
}

func TestMapPermissionProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		profile  *permissions.Profile
		expected *runtime.PermissionConfig
	}{
		{
			name:     "nil profile",
			profile:  nil,
			expected: nil,
		},
		{
			name: "read mounts",
			profile: &permissions.Profile{
				Read: []permissions.MountDeclaration{"/data"},
			},
			expected: &runtime.PermissionConfig{
				Mounts: []runtime.Mount{
					{Source: "/data", Target: "/data", ReadOnly: true, Type: runtime.MountTypeBind},
				},
			},
		},
		{
			name: "write mounts with host:container path",
			profile: &permissions.Profile{
				Write: []permissions.MountDeclaration{"/host/path:/container/path"},
			},
			expected: &runtime.PermissionConfig{
				Mounts: []runtime.Mount{
					{Source: "/host/path", Target: "/container/path", ReadOnly: false, Type: runtime.MountTypeBind},
				},
			},
		},
		{
			name: "network mode and privileged",
			profile: &permissions.Profile{
				Network:    &permissions.NetworkPermissions{Mode: "host"},
				Privileged: true,
			},
			expected: &runtime.PermissionConfig{
				NetworkMode: "host",
				Privileged:  true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapPermissionProfile(tc.profile)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestParseMountDecl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		decl          permissions.MountDeclaration
		expectedHost  string
		expectedGuest string
	}{
		{
			name:          "single path",
			decl:          "/data",
			expectedHost:  "/data",
			expectedGuest: "/data",
		},
		{
			name:          "host:container path",
			decl:          "/host:/container",
			expectedHost:  "/host",
			expectedGuest: "/container",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, guest := parseMountDecl(tc.decl)
			assert.Equal(t, tc.expectedHost, host)
			assert.Equal(t, tc.expectedGuest, guest)
		})
	}
}
