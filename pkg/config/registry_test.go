// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIEndpoint = "/v0.1/servers"

func TestDetectRegistryType(t *testing.T) { //nolint:tparallel,paralleltest // Cannot use t.Parallel() on subtests using t.Setenv()
	tests := []struct {
		name              string
		input             string
		allowPrivateIPs   bool
		expectedType      string
		expectedCleanPath string
		setupMockServer   func() *httptest.Server
	}{
		{
			name:              "file protocol",
			input:             "file:///path/to/registry.json",
			allowPrivateIPs:   false,
			expectedType:      RegistryTypeFile,
			expectedCleanPath: "/path/to/registry.json",
		},
		{
			name:              "URL with .json extension",
			input:             "https://example.com/registry.json",
			allowPrivateIPs:   false,
			expectedType:      RegistryTypeURL,
			expectedCleanPath: "https://example.com/registry.json",
		},
		{
			name:              "local file path",
			input:             "/path/to/registry.json",
			allowPrivateIPs:   false,
			expectedType:      RegistryTypeFile,
			expectedCleanPath: "/path/to/registry.json",
		},
		{
			name:            "URL without .json returning valid registry JSON",
			allowPrivateIPs: true,
			expectedType:    RegistryTypeURL,
			setupMockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"$schema": "https://example.com/schema.json",
							"version": "1.0.0",
							"meta":    map[string]interface{}{"last_updated": "2025-01-01T00:00:00Z"},
							"data": map[string]interface{}{
								"servers": []interface{}{
									map[string]interface{}{"name": "io.example.test-server"},
								},
							},
						})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			},
		},
		{
			name:            "URL without .json but has /v0.1/servers (API endpoint)",
			allowPrivateIPs: true,
			expectedType:    RegistryTypeAPI,
			setupMockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case testAPIEndpoint:
						// Return success for MCP Registry API endpoint
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						if r.Method == http.MethodGet {
							// Return proper MCP Registry API response structure
							json.NewEncoder(w).Encode(map[string]interface{}{
								"servers": []interface{}{},
								"metadata": map[string]interface{}{
									"nextCursor": "",
								},
							})
						}
					case "/":
						// Return non-JSON response
						w.Header().Set("Content-Type", "text/html")
						w.WriteHeader(http.StatusOK)
						if r.Method == http.MethodGet {
							w.Write([]byte("<html>API Root</html>"))
						}
					default:
						http.NotFound(w, r)
					}
				}))
			},
		},
		{
			name:            "URL without .json, no valid JSON, no openapi.yaml (defaults to URL)",
			allowPrivateIPs: true,
			expectedType:    RegistryTypeURL,
			setupMockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Return 404 for everything
					http.NotFound(w, r)
				}))
			},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			// Enable HTTP for test servers that use httptest.NewServer (not TLS)
			// This is needed because the networking client requires HTTPS by default
			if tt.setupMockServer != nil {
				t.Setenv("INSECURE_DISABLE_URL_VALIDATION", "true")
			} else {
				t.Parallel()
			}

			input := tt.input
			expectedCleanPath := tt.expectedCleanPath

			// Setup mock server if needed
			if tt.setupMockServer != nil {
				server := tt.setupMockServer()
				defer server.Close()
				input = server.URL
				expectedCleanPath = server.URL
			}

			registryType, cleanPath := DetectRegistryType(input, tt.allowPrivateIPs)

			assert.Equal(t, tt.expectedType, registryType, "registry type should match")
			if expectedCleanPath != "" {
				assert.Equal(t, expectedCleanPath, cleanPath, "clean path should match")
			}
		})
	}
}

func TestIsValidRegistryJSON(t *testing.T) {
	t.Parallel()

	upstreamWithServer := map[string]interface{}{
		"$schema": "https://example.com/schema.json",
		"version": "1.0.0",
		"meta":    map[string]interface{}{"last_updated": "2025-01-01T00:00:00Z"},
		"data": map[string]interface{}{
			"servers": []interface{}{
				map[string]interface{}{"name": "io.example.test"},
			},
		},
	}

	tests := []struct {
		name          string
		setupServer   func() *httptest.Server
		expectedError bool
	}{
		{
			name: "valid upstream registry with servers",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(upstreamWithServer)
				}))
			},
			expectedError: false,
		},
		{
			name: "invalid JSON - not JSON at all",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte("<html>Not JSON</html>"))
				}))
			},
			expectedError: true,
		},
		{
			name: "server error",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
			},
			expectedError: true,
		},
		{
			name: "upstream registry with empty servers list",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"$schema": "https://example.com/schema.json",
						"version": "1.0.0",
						"meta":    map[string]interface{}{},
						"data":    map[string]interface{}{"servers": []interface{}{}},
					})
				}))
			},
			expectedError: true,
		},
		{
			name: "valid upstream registry with groups",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"$schema": "https://example.com/schema.json",
						"version": "1.0.0",
						"meta":    map[string]interface{}{},
						"data": map[string]interface{}{
							"servers": []interface{}{},
							"groups": []map[string]interface{}{
								{
									"name":        "test-group",
									"description": "Test group",
									"servers": []interface{}{
										map[string]interface{}{"name": "io.example.grouped"},
									},
								},
							},
						},
					})
				}))
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			client := &http.Client{}
			err := isValidRegistryJSON(client, server.URL)

			if tt.expectedError {
				assert.Error(t, err, "isValidRegistryJSON should return an error")
			} else {
				assert.NoError(t, err, "isValidRegistryJSON should not return an error")
			}
		})
	}
}

func TestValidateRegistryFileStructure_UpstreamFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		expectError bool
	}{
		{
			name: "valid upstream format with servers",
			content: `{
				"$schema": "https://cdn.mcpregistry.io/schema/v0/registry.json",
				"version": "1.0.0",
				"meta": {"last_updated": "2025-01-01T00:00:00Z"},
				"data": {
					"servers": [
						{
							"name": "io.example.test",
							"description": "Test",
							"packages": [{"registryType": "oci", "identifier": "test:latest", "transport": {"type": "stdio"}}]
						}
					]
				}
			}`,
			expectError: false,
		},
		{
			name: "upstream format with empty servers",
			content: `{
				"$schema": "https://cdn.mcpregistry.io/schema/v0/registry.json",
				"version": "1.0.0",
				"meta": {"last_updated": "2025-01-01T00:00:00Z"},
				"data": {"servers": []}
			}`,
			expectError: true,
		},
		{
			name: "legacy format is no longer accepted",
			content: `{
				"version": "1.0.0",
				"servers": {"test": {"image": "test:latest"}}
			}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			path := tmpDir + "/registry.json"
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0644))

			err := validateRegistryFileStructure(path)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsValidRegistryJSON_UpstreamFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		body          string
		expectedError bool
	}{
		{
			name: "valid upstream format",
			body: `{
				"$schema": "https://cdn.mcpregistry.io/schema/v0/registry.json",
				"version": "1.0.0",
				"meta": {"last_updated": "2025-01-01T00:00:00Z"},
				"data": {
					"servers": [
						{
							"name": "io.example.test",
							"description": "Test",
							"packages": [{"registryType": "oci", "identifier": "test:latest", "transport": {"type": "stdio"}}]
						}
					]
				}
			}`,
			expectedError: false,
		},
		{
			name: "upstream format with no servers",
			body: `{
				"$schema": "https://cdn.mcpregistry.io/schema/v0/registry.json",
				"version": "1.0.0",
				"meta": {"last_updated": "2025-01-01T00:00:00Z"},
				"data": {"servers": []}
			}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := &http.Client{}
			err := isValidRegistryJSON(client, server.URL)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestProbeRegistryURL(t *testing.T) { //nolint:tparallel,paralleltest // Cannot use t.Parallel() on subtests using t.Setenv()
	tests := []struct {
		name            string
		allowPrivateIPs bool
		setupServer     func() *httptest.Server
		expectedType    string
	}{
		{
			name:            "valid registry JSON - should return RegistryTypeURL",
			allowPrivateIPs: true,
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"$schema": "https://example.com/schema.json",
							"version": "1.0.0",
							"meta":    map[string]interface{}{},
							"data": map[string]interface{}{
								"servers": []interface{}{
									map[string]interface{}{"name": "io.example.test-server"},
								},
							},
						})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			},
			expectedType: RegistryTypeURL,
		},
		{
			name:            "API with /v0.1/servers - should return RegistryTypeAPI",
			allowPrivateIPs: true,
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case testAPIEndpoint:
						// Support GET with proper API response structure
						if r.Method == http.MethodGet {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusOK)
							// Return proper MCP Registry API response structure
							json.NewEncoder(w).Encode(map[string]interface{}{
								"servers": []interface{}{},
								"metadata": map[string]interface{}{
									"nextCursor": "",
								},
							})
						} else {
							w.WriteHeader(http.StatusMethodNotAllowed)
						}
					case "/":
						// Return invalid JSON to trigger API endpoint check
						w.Header().Set("Content-Type", "text/html")
						w.WriteHeader(http.StatusOK)
						w.Write([]byte("<html>API</html>"))
					default:
						http.NotFound(w, r)
					}
				}))
			},
			expectedType: RegistryTypeAPI,
		},
		{
			name:            "neither valid JSON nor API - defaults to RegistryTypeURL",
			allowPrivateIPs: true,
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.NotFound(w, r)
				}))
			},
			expectedType: RegistryTypeURL,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			// Enable HTTP for test servers (all tests in this function use httptest.NewServer)
			// Note: Cannot use t.Parallel() with t.Setenv()
			t.Setenv("INSECURE_DISABLE_URL_VALIDATION", "true")

			server := tt.setupServer()
			defer server.Close()

			result := probeRegistryURL(server.URL, tt.allowPrivateIPs)

			assert.Equal(t, tt.expectedType, result, "probeRegistryURL result should match expected type")
		})
	}
}
