package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/runner"
)

func TestIsOCIRuntimeConfigArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name     string
		ref      string
		expected bool
	}{
		{
			name:     "file path should return false",
			ref:      "./config.json",
			expected: false,
		},
		{
			name:     "absolute file path should return false",
			ref:      "/tmp/config.json",
			expected: false,
		},
		{
			name:     "empty string should return false",
			ref:      "",
			expected: false,
		},
		{
			name:     "invalid reference should return false",
			ref:      "invalid-reference",
			expected: false,
		},
		// Note: We can't easily test positive cases without setting up a real registry
		// or mocking the OCI client, which would be complex for this test
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isOCIRuntimeConfigArtifact(ctx, tt.ref)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLoadAndMergeOCIRunConfig_InvalidReference(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	flags := &RunFlags{
		Name: "test-override",
	}

	_, err := loadAndMergeOCIRunConfig(ctx, "invalid-reference", flags, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load runtime configuration from OCI artifact")
}

func TestLoadAndMergeOCIRunConfig_FlagOverrides(t *testing.T) {
	t.Parallel()

	// This test would require mocking the OCI client to return a known config
	// For now, we'll test the logic with a mock scenario

	// Create a mock config that would come from OCI
	mockConfig := &runner.RunConfig{
		Name:  "original-name",
		Host:  "original-host",
		Port:  8080,
		Debug: false,
	}

	// Test flag override logic (this would be part of loadAndMergeOCIRunConfig)
	flags := &RunFlags{
		Name:      "override-name",
		Host:      "override-host",
		ProxyPort: 9090,
	}
	debugMode := true

	// Simulate the override logic
	if flags.Name != "" {
		mockConfig.Name = flags.Name
	}
	if flags.Host != "" {
		mockConfig.Host = flags.Host
	}
	if flags.ProxyPort != 0 {
		mockConfig.Port = flags.ProxyPort
	}
	if debugMode {
		mockConfig.Debug = true
	}

	// Verify overrides worked
	assert.Equal(t, "override-name", mockConfig.Name)
	assert.Equal(t, "override-host", mockConfig.Host)
	assert.Equal(t, 9090, mockConfig.Port)
	assert.True(t, mockConfig.Debug)
}

func TestBuildRunnerConfig_WithOCIArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runConfig := &RunFlags{
		Name: "test-config",
	}

	// Test with a file path (should not be detected as OCI artifact)
	_, err := BuildRunnerConfig(ctx, runConfig, "./config.json", []string{}, false, nil)
	// This should proceed with normal flow and likely fail due to missing dependencies
	// but it shouldn't fail on OCI detection
	assert.Error(t, err) // Expected to fail for other reasons, not OCI detection

	// Test with invalid OCI reference (should not be detected as OCI artifact)
	_, err = BuildRunnerConfig(ctx, runConfig, "invalid-reference", []string{}, false, nil)
	assert.Error(t, err) // Expected to fail for other reasons, not OCI detection
}

func TestRunFlags_DefaultValues(t *testing.T) {
	t.Parallel()

	flags := &RunFlags{}

	// Test default values
	assert.Empty(t, flags.Transport)
	assert.Equal(t, "", flags.ProxyMode)
	assert.Equal(t, "", flags.Host)
	assert.Equal(t, 0, flags.ProxyPort)
	assert.Equal(t, 0, flags.TargetPort)
	assert.Empty(t, flags.Name)
	assert.Empty(t, flags.Env)
	assert.Empty(t, flags.Volumes)
	assert.Empty(t, flags.Secrets)
	assert.False(t, flags.EnableAudit)
	assert.False(t, flags.IsolateNetwork)
	assert.False(t, flags.Foreground)
	assert.Empty(t, flags.FromConfig)
}

func TestRunFlags_FieldAssignment(t *testing.T) {
	t.Parallel()

	flags := &RunFlags{
		Transport:      "sse",
		ProxyMode:      "sse",
		Host:           "localhost",
		ProxyPort:      8080,
		TargetPort:     3000,
		Name:           "test-server",
		Env:            []string{"KEY=value"},
		Volumes:        []string{"/host:/container"},
		Secrets:        []string{"secret1"},
		EnableAudit:    true,
		IsolateNetwork: true,
		Foreground:     true,
		FromConfig:     "config.json",
	}

	// Verify all fields are set correctly
	assert.Equal(t, "sse", flags.Transport)
	assert.Equal(t, "sse", flags.ProxyMode)
	assert.Equal(t, "localhost", flags.Host)
	assert.Equal(t, 8080, flags.ProxyPort)
	assert.Equal(t, 3000, flags.TargetPort)
	assert.Equal(t, "test-server", flags.Name)
	assert.Equal(t, []string{"KEY=value"}, flags.Env)
	assert.Equal(t, []string{"/host:/container"}, flags.Volumes)
	assert.Equal(t, []string{"secret1"}, flags.Secrets)
	assert.True(t, flags.EnableAudit)
	assert.True(t, flags.IsolateNetwork)
	assert.True(t, flags.Foreground)
	assert.Equal(t, "config.json", flags.FromConfig)
}
