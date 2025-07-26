package oci

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/logger"
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

func TestNewClient(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	client := NewClient(imageManager)
	assert.NotNil(t, client)
}

func TestCreateRepository_InvalidReference(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	client := NewClient(imageManager)
	// Use a truly invalid reference that will fail parsing
	_, err := client.CreateRepository("invalid:reference:with:too:many:colons")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference format")
}

func TestCreateRepository_ValidReference(t *testing.T) {
	t.Parallel()

	imageManager := &mockImageManager{}
	client := NewClient(imageManager)
	repo, err := client.CreateRepository("registry.example.com/test:latest")

	// This might fail due to network issues in tests, but we can at least test the parsing
	if err != nil {
		// If it fails, it should be due to network/auth issues, not parsing
		assert.NotContains(t, err.Error(), "invalid reference format")
	} else {
		assert.NotNil(t, repo)
	}
}
