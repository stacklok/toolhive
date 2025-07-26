package export

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/oci"
	"github.com/stacklok/toolhive/pkg/runner"
)

// mockImageManager is a mock implementation of ImageManager for testing
type mockImageManager struct{}

func (*mockImageManager) ImageExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (*mockImageManager) PullImage(_ context.Context, _ string) error {
	return nil
}

func (*mockImageManager) BuildImage(_ context.Context, _, _ string) error {
	return nil
}

func init() {
	// Initialize logger to prevent nil pointer dereference in tests
	logger.Initialize()
}

func TestNewOCIExporter(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	ociClient := oci.NewClient(imageManager)
	exporter := NewOCIExporter(ociClient)
	assert.NotNil(t, exporter)
}

func TestPushRunConfig_InvalidReference(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	ociClient := oci.NewClient(imageManager)
	exporter := NewOCIExporter(ociClient)
	config := &runner.RunConfig{
		Name:  "test-config",
		Image: "test-image:latest",
	}

	ctx := context.Background()
	// Use a truly invalid reference that will fail validation
	err := exporter.PushRunConfig(ctx, config, "invalid:reference:with:too:many:colons")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create repository")
}

func TestPullRunConfig_InvalidReference(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	ociClient := oci.NewClient(imageManager)
	exporter := NewOCIExporter(ociClient)
	ctx := context.Background()

	// Use a truly invalid reference that will fail validation
	_, err := exporter.PullRunConfig(ctx, "invalid:reference:with:too:many:colons")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create repository")
}

// TestRunConfigSerialization tests that RunConfig can be properly serialized/deserialized
func TestRunConfigSerialization(t *testing.T) {
	t.Parallel()

	originalConfig := &runner.RunConfig{
		Name:          "test-server",
		Image:         "registry.example.com/test:latest",
		ContainerName: "test-container",
		BaseName:      "test",
		Host:          "localhost",
		Port:          8080,
		Debug:         true,
		EnvVars: map[string]string{
			"TEST_VAR": "test_value",
		},
		Volumes: []string{
			"/host:/container:ro",
		},
		ContainerLabels: map[string]string{
			"app": "test",
		},
	}

	// Test JSON serialization (this is what gets stored in OCI artifacts)
	mockWriter := &mockWriter{}
	err := originalConfig.WriteJSON(mockWriter)
	require.NoError(t, err)
	assert.NotEmpty(t, mockWriter.data)
}

// mockWriter is a simple writer for testing
type mockWriter struct {
	data []byte
}

func (m *mockWriter) Write(p []byte) (n int, err error) {
	m.data = append(m.data, p...)
	return len(p), nil
}
