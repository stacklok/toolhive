package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/runner"
)

func TestExportToFile_InvalidPath(t *testing.T) {
	t.Parallel()

	// Create a valid config for testing
	config := &runner.RunConfig{
		Name:  "test-config",
		Image: "test-image:latest",
	}

	// Test with invalid directory path
	err := exportToFile(config, "test", "/invalid/path/that/does/not/exist/config.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create output directory")
}

func TestExportToFile_NilConfig(t *testing.T) {
	t.Parallel()

	// Test with nil config
	err := exportToFile(nil, "test", "/tmp/test-config.json")
	assert.Error(t, err)
}
