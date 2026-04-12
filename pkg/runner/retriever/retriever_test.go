// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package retriever

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestResolveMCPServer_WithGroup_ReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Registry-based groups are no longer supported; any group lookup should
	// return an error.
	_, _, err := ResolveMCPServer(
		ctx,
		"some-server",
		"",
		VerifyImageDisabled,
		"any-group",
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer supported")
}

func TestResolveMCPServer_WithoutGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test that passing empty group name still works (normal behavior)
	imageURL, serverMetadata, err := ResolveMCPServer(
		ctx,
		"osv",               // Use a known server from the registry
		"",                  // rawCACertPath
		VerifyImageDisabled, // verificationType
		"",                  // empty groupName should use normal registry lookup
		nil,                 // no runtime override
	)

	// This should work as it's the normal flow
	assert.NoError(t, err)
	assert.NotEmpty(t, imageURL)
	assert.NotNil(t, serverMetadata)
}

func TestResolveCACertPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flagValue string
		expected  string
	}{
		{
			name:      "flag value provided",
			flagValue: "/path/to/ca.crt",
			expected:  "/path/to/ca.crt",
		},
		{
			name:      "empty flag value",
			flagValue: "",
			expected:  "", // Will use config or empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := resolveCACertPath(tt.flagValue)

			if tt.flagValue != "" {
				assert.Equal(t, tt.expected, result)
			} else {
				// When flag is empty, it uses config - we just verify it returns a string
				assert.IsType(t, "", result)
			}
		})
	}
}

func TestHasLatestTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		imageRef string
		expected bool
	}{
		{
			name:     "explicit latest tag",
			imageRef: "ubuntu:latest",
			expected: true,
		},
		{
			name:     "no tag defaults to latest",
			imageRef: "ubuntu",
			expected: true,
		},
		{
			name:     "specific tag",
			imageRef: "ubuntu:20.04",
			expected: false,
		},
		{
			name:     "digest reference",
			imageRef: "ubuntu@sha256:abcdef123456",
			expected: false,
		},
		{
			name:     "invalid reference",
			imageRef: "invalid::reference",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hasLatestTag(tt.imageRef)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// errorPolicyGate is a test PolicyGate that rejects server creation with a
// configurable error. It embeds runner.NoopPolicyGate for forward compatibility.
type errorPolicyGate struct {
	runner.NoopPolicyGate
	err error
}

func (g *errorPolicyGate) CheckCreateServer(_ context.Context, _ *runner.RunConfig) error {
	return g.err
}

//nolint:paralleltest // Subtests mutate the global policy gate and env vars.
func TestEnforcePolicyAndPullImage(t *testing.T) {
	const testImageURL = "ghcr.io/example/server:v1.0.0"
	errPullFailed := errors.New("pull failed: connection reset")

	tests := []struct {
		name string
		// setup runs before the subtest call. It may register a custom policy
		// gate or set env vars using t.Setenv.
		setup          func(t *testing.T)
		nilRunConfig   bool // when true, pass nil *RunConfig to exercise nil-safety
		locallyBuilt   bool // when true, image was built from a protocol scheme
		serverMetadata regtypes.ServerMetadata
		pullerErr      error
		expectPulled   bool
		expectImageURL string
		expectErr      string
	}{
		{
			name:           "remote server metadata skips policy and pull",
			serverMetadata: &regtypes.RemoteServerMetadata{},
			expectPulled:   false,
		},
		{
			name: "policy gate rejects server creation",
			setup: func(t *testing.T) {
				t.Helper()
				original := runner.ActivePolicyGate()
				runner.RegisterPolicyGate(&errorPolicyGate{
					err: errors.New("policy violation: image not allowed"),
				})
				t.Cleanup(func() { runner.RegisterPolicyGate(original) })
			},
			serverMetadata: &regtypes.ImageMetadata{},
			expectPulled:   false,
			expectErr:      "server creation blocked by policy: policy violation: image not allowed",
		},
		{
			name: "kubernetes runtime skips pull",
			setup: func(t *testing.T) {
				t.Helper()
				t.Setenv("TOOLHIVE_RUNTIME", "kubernetes")
			},
			serverMetadata: &regtypes.ImageMetadata{},
			expectPulled:   false,
		},
		{
			name:           "happy path pulls image",
			serverMetadata: &regtypes.ImageMetadata{},
			expectPulled:   true,
			expectImageURL: testImageURL,
		},
		{
			name:           "puller error is propagated",
			serverMetadata: &regtypes.ImageMetadata{},
			pullerErr:      errPullFailed,
			expectPulled:   true,
			expectImageURL: testImageURL,
			expectErr:      "pull failed: connection reset",
		},
		{
			name:           "nil server metadata proceeds to policy check and pull",
			serverMetadata: nil,
			expectPulled:   true,
			expectImageURL: testImageURL,
		},
		{
			name:           "locally built image skips pull",
			locallyBuilt:   true,
			serverMetadata: nil,
			expectPulled:   false,
		},
		{
			name:           "nil runConfig with default policy gate",
			nilRunConfig:   true,
			serverMetadata: &regtypes.ImageMetadata{},
			expectPulled:   true,
			expectImageURL: testImageURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}

			var pulled bool
			var pulledURL string
			puller := func(_ context.Context, imageURL string) error {
				pulled = true
				pulledURL = imageURL
				return tt.pullerErr
			}

			var rc *runner.RunConfig
			if !tt.nilRunConfig {
				rc = runner.NewRunConfig()
			}

			err := EnforcePolicyAndPullImage(
				context.Background(),
				rc,
				tt.serverMetadata,
				testImageURL,
				puller,
				0,
				tt.locallyBuilt,
			)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.expectPulled, pulled, "puller called mismatch")
			if tt.expectPulled {
				assert.Equal(t, tt.expectImageURL, pulledURL, "puller received wrong imageURL")
			}
		})
	}
}
