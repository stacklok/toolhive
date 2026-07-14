// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	runtime "k8s.io/apimachinery/pkg/runtime"

	// Blank-imported so authorizers.IsRegistered("cedarv1") returns true
	// inside Validate(). Without this, every test case using a real backend
	// type would hit the unregistered-type branch.
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
)

func TestMCPAuthzConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        MCPAuthzConfigSpec
		expectError bool
		errContains string
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
			errContains: "type must not be empty",
		},
		{
			name: "unregistered type fails",
			spec: MCPAuthzConfigSpec{
				Type:   "nope",
				Config: runtime.RawExtension{Raw: []byte(`{"policies":[]}`)},
			},
			expectError: true,
			errContains: `"nope" is not a registered authorizer backend`,
		},
		{
			name: "empty config raw fails",
			spec: MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: []byte{}},
			},
			expectError: true,
			errContains: "config must not be empty",
		},
		{
			name: "nil config raw fails",
			spec: MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: nil},
			},
			expectError: true,
			errContains: "config must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &MCPAuthzConfig{Spec: tt.spec}
			err := cfg.Validate()
			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.ErrorContains(t, err, tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
