// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
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
			errorContains: "Additional property",
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
	// gojsonschema returns validation error for invalid JSON
	assert.Contains(t, err.Error(), "invalid character")
}

// TestValidateEmbeddedRegistryCanLoadData tests that we can actually load the embedded registry
func TestValidateEmbeddedRegistryCanLoadData(t *testing.T) {
	t.Parallel()

	// Load the embedded registry data
	registryData, err := embeddedRegistryFS.ReadFile("data/registry.json")
	require.NoError(t, err, "Should be able to load embedded registry data")

	// Verify it's valid JSON
	var registry types.Registry
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

// TestValidateUpstreamRegistry tests the ValidateUpstreamRegistry function
func TestValidateUpstreamRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          string
		wantErr       bool
		errorContains string
	}{
		{
			name: "valid registry with all fields",
			data: `{
				"$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {
					"servers": [],
					"groups": []
				}
			}`,
			wantErr: false,
		},
		{
			name: "valid registry without groups (optional)",
			data: `{
				"$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {
					"servers": []
				}
			}`,
			wantErr: false,
		},
		{
			name: "valid registry with group",
			data: `{
				"$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {
					"servers": [],
					"groups": [
						{
							"name": "test-group",
							"description": "Test group",
							"servers": []
						}
					]
				}
			}`,
			wantErr: false,
		},
		{
			name: "missing meta",
			data: `{
				"version": "1.0.0",
				"data": {
					"servers": []
				}
			}`,
			wantErr:       true,
			errorContains: "meta",
		},
		{
			name: "missing data",
			data: `{
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				}
			}`,
			wantErr:       true,
			errorContains: "data",
		},
		{
			name: "missing servers in data",
			data: `{
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {}
			}`,
			wantErr:       true,
			errorContains: "servers",
		},
		{
			name: "missing last_updated in meta",
			data: `{
				"version": "1.0.0",
				"meta": {},
				"data": {
					"servers": []
				}
			}`,
			wantErr:       true,
			errorContains: "last_updated",
		},
		{
			name: "invalid version format",
			data: `{
				"version": "invalid",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {
					"servers": []
				}
			}`,
			wantErr:       true,
			errorContains: "version",
		},
		{
			name: "invalid date format",
			data: `{
				"version": "1.0.0",
				"meta": {
					"last_updated": "not-a-date"
				},
				"data": {
					"servers": []
				}
			}`,
			wantErr:       true,
			errorContains: "date-time",
		},
		{
			name: "missing required group fields",
			data: `{
				"version": "1.0.0",
				"meta": {
					"last_updated": "2024-01-15T10:30:00Z"
				},
				"data": {
					"servers": [],
					"groups": [
						{
							"name": "incomplete-group"
						}
					]
				}
			}`,
			wantErr:       true,
			errorContains: "description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUpstreamRegistry([]byte(tt.data))
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidateUpstreamRegistry_RealWorld tests validation with realistic registry data
func TestValidateUpstreamRegistry_RealWorld(t *testing.T) {
	t.Parallel()

	// Simulate a realistic upstream registry
	realWorldRegistry := `{
		"$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		"version": "1.0.0",
		"meta": {
			"last_updated": "2024-11-25T10:30:00Z"
		},
		"data": {
			"servers": [
				{
					"$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
					"name": "io.github.stacklok/test-server",
					"description": "A test MCP server",
					"version": "1.0.0",
					"title": "Test Server"
				}
			],
			"groups": []
		}
	}`

	err := ValidateUpstreamRegistry([]byte(realWorldRegistry))
	assert.NoError(t, err, "Real-world registry example should validate successfully")
}

// walkJSONObjects walks through nested JSON objects following the provided path.
// Returns the final object and true if successful, or nil and false if any path segment fails.
func walkJSONObjects(root map[string]any, paths ...string) (map[string]any, bool) {
	current := root
	for _, path := range paths {
		next, ok := current[path].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// TestValidatePublisherProvidedExtensions tests the ValidatePublisherProvidedExtensions function
func TestValidatePublisherProvidedExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          string
		wantErr       bool
		errorContains string
	}{
		{
			name: "valid image extensions",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active",
						"tier": "Official",
						"tools": ["tool1", "tool2"],
						"tags": ["api", "test"],
						"metadata": {
							"stars": 100,
							"pulls": 5000,
							"last_updated": "2025-01-15T10:30:00Z"
						},
						"permissions": {
							"network": {
								"outbound": {
									"allow_host": [".example.com"],
									"allow_port": [443]
								}
							}
						},
						"args": ["--verbose"],
						"docker_tags": ["v1.0.0", "latest"],
						"proxy_port": 8080,
						"custom_metadata": {
							"maintainer": "Test User"
						}
					}
				}
			}`,
			wantErr: false,
		},
		{
			name: "valid remote extensions",
			data: `{
				"io.github.stacklok": {
					"https://api.example.com/mcp": {
						"status": "active",
						"tier": "Community",
						"tools": ["remote_tool"],
						"tags": ["remote", "api"],
						"metadata": {
							"stars": 50,
							"pulls": 1000,
							"last_updated": "2025-01-15T10:30:00Z"
						},
						"oauth_config": {
							"issuer": "https://auth.example.com",
							"client_id": "test-client",
							"scopes": ["openid", "profile"],
							"use_pkce": true,
							"oauth_params": {
								"prompt": "consent"
							},
							"callback_port": 8000,
							"resource": "https://api.example.com"
						},
						"env_vars": [
							{
								"name": "API_KEY",
								"description": "API key for authentication",
								"required": true,
								"secret": true
							}
						],
						"custom_metadata": {
							"provider": "Example Corp"
						}
					}
				}
			}`,
			wantErr: false,
		},
		{
			name: "valid minimal extensions (status only)",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/minimal/server:latest": {
						"status": "active"
					}
				}
			}`,
			wantErr: false,
		},
		{
			name: "missing required status field",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"tier": "Official"
					}
				}
			}`,
			wantErr:       true,
			errorContains: "status",
		},
		{
			name: "invalid status value",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "invalid-status"
					}
				}
			}`,
			wantErr:       true,
			errorContains: "status",
		},
		{
			name: "invalid tier value",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active",
						"tier": "InvalidTier"
					}
				}
			}`,
			wantErr:       true,
			errorContains: "tier",
		},
		{
			name: "invalid proxy_port (too high)",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active",
						"proxy_port": 70000
					}
				}
			}`,
			wantErr:       true,
			errorContains: "proxy_port",
		},
		{
			name: "invalid metadata stars (negative)",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active",
						"metadata": {
							"stars": -1
						}
					}
				}
			}`,
			wantErr:       true,
			errorContains: "stars",
		},
		{
			name: "invalid oauth_config callback_port",
			data: `{
				"io.github.stacklok": {
					"https://api.example.com/mcp": {
						"status": "active",
						"oauth_config": {
							"callback_port": 0
						}
					}
				}
			}`,
			wantErr:       true,
			errorContains: "callback_port",
		},
		{
			name: "valid provenance structure",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active",
						"provenance": {
							"sigstore_url": "tuf-repo-cdn.sigstore.dev",
							"repository_uri": "https://github.com/example/server",
							"signer_identity": "/.github/workflows/release.yml",
							"runner_environment": "github-hosted",
							"cert_issuer": "https://token.actions.githubusercontent.com"
						}
					}
				}
			}`,
			wantErr: false,
		},
		{
			name: "empty publisher namespace is valid",
			data: `{
				"io.github.stacklok": {}
			}`,
			wantErr: false,
		},
		{
			name: "allows other publisher namespaces",
			data: `{
				"io.github.stacklok": {
					"ghcr.io/example/server:v1.0.0": {
						"status": "active"
					}
				},
				"io.example.other": {
					"arbitrary": "data"
				}
			}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePublisherProvidedExtensions([]byte(tt.data))
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidatePublisherProvidedExtensions_ConverterOutput tests that actual converter output validates
func TestValidatePublisherProvidedExtensions_ConverterOutput(t *testing.T) {
	t.Parallel()

	// This is a realistic output from the createImageExtensions converter
	converterOutput := `{
		"io.github.stacklok": {
			"ghcr.io/github/github-mcp-server:v0.19.1": {
				"status": "Active",
				"tier": "Official",
				"tools": [
					"add_comment_to_pending_review",
					"create_issue",
					"get_file_contents"
				],
				"tags": ["api", "github", "repository"],
				"metadata": {
					"stars": 23700,
					"pulls": 5000,
					"last_updated": "2025-10-18T02:26:51Z"
				},
				"permissions": {
					"network": {
						"outbound": {
							"allow_host": [".github.com", ".githubusercontent.com"],
							"allow_port": [443]
						}
					}
				},
				"provenance": {
					"cert_issuer": "https://token.actions.githubusercontent.com",
					"repository_uri": "https://github.com/github/github-mcp-server",
					"runner_environment": "github-hosted",
					"signer_identity": "/.github/workflows/docker-publish.yml",
					"sigstore_url": "tuf-repo-cdn.sigstore.dev"
				},
				"docker_tags": ["v0.19.1", "v0.19.0", "latest"],
				"proxy_port": 8080,
				"custom_metadata": {
					"maintainer": "GitHub",
					"license": "MIT"
				}
			}
		}
	}`

	err := ValidatePublisherProvidedExtensions([]byte(converterOutput))
	assert.NoError(t, err, "Converter output should validate against the schema")
}

// TestValidatePublisherProvidedExtensions_RemoteConverterOutput tests remote server converter output
func TestValidatePublisherProvidedExtensions_RemoteConverterOutput(t *testing.T) {
	t.Parallel()

	// This is a realistic output from the createRemoteExtensions converter
	converterOutput := `{
		"io.github.stacklok": {
			"https://api.example.com/mcp": {
				"status": "active",
				"tier": "Community",
				"tools": ["get_data", "send_notification", "query_api"],
				"tags": ["remote", "sse", "api"],
				"metadata": {
					"stars": 150,
					"pulls": 500,
					"last_updated": "2025-10-20T10:00:00Z"
				},
				"oauth_config": {
					"issuer": "https://auth.example.com",
					"client_id": "example-client",
					"scopes": ["openid", "profile"]
				},
				"env_vars": [
					{
						"name": "API_ENDPOINT",
						"description": "Base URL for API calls",
						"required": false,
						"default": "https://api.example.com"
					},
					{
						"name": "CLIENT_SECRET",
						"description": "Client secret for OAuth",
						"required": true,
						"secret": true
					}
				],
				"custom_metadata": {
					"provider": "Example Corp",
					"api_version": "v2"
				}
			}
		}
	}`

	err := ValidatePublisherProvidedExtensions([]byte(converterOutput))
	assert.NoError(t, err, "Remote converter output should validate against the schema")
}

// TestConverterFixtures_PublisherProvidedSchemaValidation validates that converter output fixture files
// containing _meta extensions conform to the publisher-provided schema.
// This ensures that the converter-produced extensions remain valid as the schema evolves.
//
// Note: Only "expected_*" files are validated since they represent what ToolHive converters produce.
// The "input_*" files represent external registry data which may contain additional fields
// that ToolHive tolerates but doesn't produce.
func TestConverterFixtures_PublisherProvidedSchemaValidation(t *testing.T) {
	t.Parallel()

	// Expected output fixtures that contain _meta with publisher-provided extensions
	// These represent what ToolHive converters produce and should conform to the schema
	// Paths are relative to pkg/registry/ (the current package directory)
	fixturesWithMeta := []string{
		"converters/testdata/image_to_server/expected_github.json",
		"converters/testdata/remote_to_server/expected_example.json",
	}

	for _, fixturePath := range fixturesWithMeta {
		t.Run(fixturePath, func(t *testing.T) {
			t.Parallel()

			// Read the fixture file from disk
			data, err := os.ReadFile(fixturePath)
			require.NoError(t, err, "Failed to read fixture: %s", fixturePath)

			// Parse the JSON to extract the _meta field
			var serverJSON map[string]any
			require.NoError(t, json.Unmarshal(data, &serverJSON), "Failed to parse fixture JSON")

			// Extract _meta["io.modelcontextprotocol.registry/publisher-provided"]
			meta, ok := serverJSON["_meta"].(map[string]any)
			require.True(t, ok, "Fixture should have _meta field")

			publisherProvided, ok := meta["io.modelcontextprotocol.registry/publisher-provided"].(map[string]any)
			require.True(t, ok, "Fixture should have publisher-provided extensions in _meta")

			// Serialize just the publisher-provided extensions for validation
			extensionsData, err := json.Marshal(publisherProvided)
			require.NoError(t, err, "Failed to marshal publisher-provided extensions")

			// Validate against the schema
			err = ValidatePublisherProvidedExtensions(extensionsData)
			assert.NoError(t, err, "Fixture %s publisher-provided extensions should validate against schema", fixturePath)
		})
	}
}

// TestUpstreamRegistrySchemaVersionSync ensures that the schema reference in
// upstream-registry.schema.json matches the schema version from the Go package
// (model.CurrentSchemaVersion). This prevents schema drift when upgrading the
// modelcontextprotocol/registry package.
func TestUpstreamRegistrySchemaVersionSync(t *testing.T) {
	t.Parallel()

	// Read the upstream registry schema file
	schemaPath := "data/upstream-registry.schema.json"
	schemaData, err := embeddedSchemaFS.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("Failed to read embedded schema file: %v", err)
	}

	// Parse the schema JSON
	var schema map[string]interface{}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		t.Fatalf("Failed to parse schema JSON: %v", err)
	}

	// Navigate to the $ref field in data.properties.servers.items
	items, ok := walkJSONObjects(schema, "properties", "data", "properties", "servers", "items")
	if !ok {
		t.Fatal("Failed to navigate to data.properties.servers.items in schema")
	}

	refURL, ok := items["$ref"].(string)
	if !ok {
		t.Fatal("Failed to get $ref URL from items")
	}

	// Extract the date from the URL
	// Expected format: https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json
	re := regexp.MustCompile(`/schemas/([0-9]{4}-[0-9]{2}-[0-9]{2})/`)
	matches := re.FindStringSubmatch(refURL)
	if len(matches) != 2 {
		t.Fatalf("Failed to extract date from schema URL: %s", refURL)
	}
	schemaDate := matches[1]

	// Compare with the Go package constant
	expectedDate := model.CurrentSchemaVersion
	if schemaDate != expectedDate {
		t.Errorf("Schema version mismatch!\n"+
			"  Schema file (%s): %s\n"+
			"  Go package (model.CurrentSchemaVersion): %s\n\n"+
			"To fix: Update pkg/registry/data/upstream-registry.schema.json to use date %s:\n"+
			"  In data.properties.servers.items.$ref:\n"+
			"  \"$ref\": \"https://static.modelcontextprotocol.io/schemas/%s/server.schema.json\"",
			schemaPath, schemaDate, expectedDate, expectedDate, expectedDate)
	}

	// Also check groups schema if present
	groupServerItems, ok := walkJSONObjects(schema, "properties", "data", "properties", "groups", "items", "properties", "servers", "items")
	if ok {
		groupRefURL, ok := groupServerItems["$ref"].(string)
		if ok {
			groupMatches := re.FindStringSubmatch(groupRefURL)
			if len(groupMatches) == 2 {
				groupSchemaDate := groupMatches[1]
				if groupSchemaDate != expectedDate {
					t.Errorf("Groups schema version mismatch!\n"+
						"  Groups $ref date: %s\n"+
						"  Expected: %s\n\n"+
						"To fix: Update data.properties.groups.items.properties.servers.items.$ref",
						groupSchemaDate, expectedDate)
				}
			}
		}
	}
}
