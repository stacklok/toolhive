// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

func TestMCPAuthzConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        MCPAuthzConfigSpec
		expectError bool
	}{
		{
			name: "valid spec passes",
			spec: MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: []byte(`{"policies":[]}`)},
			},
			expectError: false,
		},
		{
			name: "empty type fails",
			spec: MCPAuthzConfigSpec{
				Type:   "",
				Config: runtime.RawExtension{Raw: []byte(`{}`)},
			},
			expectError: true,
		},
		{
			name: "empty config raw fails",
			spec: MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: []byte{}},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &MCPAuthzConfig{Spec: tt.spec}
			err := cfg.Validate()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
