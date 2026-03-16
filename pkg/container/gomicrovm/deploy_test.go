// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestExtractContainerPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		options     *runtime.DeployWorkloadOptions
		expected    int
		expectError bool
	}{
		{
			name:     "nil options",
			options:  nil,
			expected: 0,
		},
		{
			name:     "empty exposed ports",
			options:  &runtime.DeployWorkloadOptions{ExposedPorts: map[string]struct{}{}},
			expected: 0,
		},
		{
			name: "valid port with protocol",
			options: &runtime.DeployWorkloadOptions{
				ExposedPorts: map[string]struct{}{"8080/tcp": {}},
			},
			expected: 8080,
		},
		{
			name: "port without protocol",
			options: &runtime.DeployWorkloadOptions{
				ExposedPorts: map[string]struct{}{"3000": {}},
			},
			expected: 3000,
		},
		{
			name: "invalid port string",
			options: &runtime.DeployWorkloadOptions{
				ExposedPorts: map[string]struct{}{"notaport/tcp": {}},
			},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractContainerPort(tc.options)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}
