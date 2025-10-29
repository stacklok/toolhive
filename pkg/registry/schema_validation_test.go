package registry

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedRegistrySchemaValidation validates that the embedded registry.json
// conforms to the registry schema. This is the main test that ensures our
// registry data is always valid.
func TestEmbeddedRegistrySchemaValidation(t *testing.T) {
	t.Parallel()

	err := ValidateEmbeddedRegistry()
	require.NoError(t, err, "Embedded registry.json must conform to the registry schema")
}

// TestRegistrySchemaValidation tests the schema validation function with various inputs
func TestRegistrySchemaValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registryJSON  string
		expectError   bool
		errorContains string
	}{
		{
			name: "valid minimal registry",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {}
			}`,
			expectError: false,
		},
		{
			name: "valid registry with server",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError: false,
		},
		{
			name: "missing required version field",
			registryJSON: `{
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {}
			}`,
			expectError:   true,
			errorContains: "version",
		},
		{
			name: "missing required last_updated field",
			registryJSON: `{
				"version": "1.0.0",
				"servers": {}
			}`,
			expectError:   true,
			errorContains: "last_updated",
		},
		{
			name: "missing required servers field",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z"
			}`,
			expectError:   true,
			errorContains: "servers",
		},
		{
			name: "invalid version format",
			registryJSON: `{
				"version": "invalid-version",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {}
			}`,
			expectError:   true,
			errorContains: "version",
		},
		{
			name: "invalid date format",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "invalid-date",
				"servers": {}
			}`,
			expectError:   true,
			errorContains: "last_updated",
		},
		{
			name: "server missing required description",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "description",
		},
		{
			name: "server missing required image",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "image",
		},
		{
			name: "server with invalid status",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "InvalidStatus",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "status",
		},
		{
			name: "server with invalid tier",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "InvalidTier",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "tier",
		},
		{
			name: "server with invalid transport",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "invalid-transport"
					}
				}
			}`,
			expectError:   true,
			errorContains: "transport",
		},
		{
			name: "server with empty tools array",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": [],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "tools",
		},
		{
			name: "server with description too short",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"description": "Short",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "description",
		},
		{
			name: "invalid server name pattern",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {
					"Invalid_Server_Name": {
						"description": "A test server for validation",
						"image": "test/server:latest",
						"status": "Active",
						"tier": "Community",
						"tools": ["test_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "additional properties",
		},
		{
			name: "valid remote server",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {},
				"remote_servers": {
					"test-remote": {
						"url": "https://api.example.com/mcp",
						"description": "A test remote server for validation",
						"status": "Active",
						"tier": "Community",
						"tools": ["remote_tool"],
						"transport": "sse"
					}
				}
			}`,
			expectError: false,
		},
		{
			name: "remote server with invalid transport (stdio not allowed)",
			registryJSON: `{
				"version": "1.0.0",
				"last_updated": "2025-01-01T00:00:00Z",
				"servers": {},
				"remote_servers": {
					"test-remote": {
						"url": "https://api.example.com/mcp",
						"description": "A test remote server for validation",
						"status": "Active",
						"tier": "Community",
						"tools": ["remote_tool"],
						"transport": "stdio"
					}
				}
			}`,
			expectError:   true,
			errorContains: "transport",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRegistrySchema([]byte(tt.registryJSON))

			if tt.expectError {
				require.Error(t, err, "Expected validation to fail for test case: %s", tt.name)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "Error should contain expected text")
				}
			} else {
				require.NoError(t, err, "Expected validation to pass for test case: %s", tt.name)
			}
		})
	}
}

// TestValidateRegistrySchemaWithInvalidJSON tests that the function handles invalid JSON gracefully
func TestValidateRegistrySchemaWithInvalidJSON(t *testing.T) {
	t.Parallel()

	invalidJSON := `{
		"version": "1.0.0",
		"last_updated": "2025-01-01T00:00:00Z",
		"servers": {
			"test-server": {
				"description": "A test server"
				// Missing comma - invalid JSON
				"image": "test/server:latest"
			}
		}
	}`

	err := ValidateRegistrySchema([]byte(invalidJSON))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse registry data")
}

// TestValidateEmbeddedRegistryCanLoadData tests that we can actually load the embedded registry
func TestValidateEmbeddedRegistryCanLoadData(t *testing.T) {
	t.Parallel()

	// Load the embedded registry data
	registryData, err := embeddedRegistryFS.ReadFile("data/registry.json")
	require.NoError(t, err, "Should be able to load embedded registry data")

	// Verify it's valid JSON
	var registry Registry
	err = json.Unmarshal(registryData, &registry)
	require.NoError(t, err, "Embedded registry should be valid JSON")

	// Verify basic structure
	assert.NotEmpty(t, registry.Version, "Registry should have a version")
	assert.NotEmpty(t, registry.LastUpdated, "Registry should have a last_updated timestamp")
	assert.NotNil(t, registry.Servers, "Registry should have a servers map")
}

// TestMultipleValidationErrors tests that multiple validation errors are reported together
func TestMultipleValidationErrors(t *testing.T) {
	t.Parallel()

	// Registry with multiple validation errors
	invalidRegistryJSON := `{
		"servers": {
			"test-server": {
				"description": "Short",
				"status": "InvalidStatus",
				"tier": "InvalidTier",
				"tools": [],
				"transport": "invalid-transport"
			}
		}
	}`

	err := ValidateRegistrySchema([]byte(invalidRegistryJSON))
	require.Error(t, err, "Expected validation to fail with multiple errors")

	errorMsg := err.Error()

	// Should contain multiple errors
	assert.Contains(t, errorMsg, "validation failed with", "Should indicate multiple errors")

	// Should contain specific error details
	assert.Contains(t, errorMsg, "version", "Should mention missing version")
	assert.Contains(t, errorMsg, "last_updated", "Should mention missing last_updated")
	assert.Contains(t, errorMsg, "description", "Should mention description length issue")
	assert.Contains(t, errorMsg, "status", "Should mention invalid status")
	assert.Contains(t, errorMsg, "tools", "Should mention empty tools array")

	// Verify it's formatted as a numbered list
	assert.Contains(t, errorMsg, "1.", "Should have numbered error list")
	assert.Contains(t, errorMsg, "2.", "Should have multiple numbered errors")

	t.Logf("Multi-error output:\n%s", errorMsg)
}
