// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
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
						// Return valid ToolHive registry JSON
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"version": "1.0.0",
							"servers": map[string]interface{}{
								"test-server": map[string]interface{}{
									"image": "test:latest",
								},
							},
						})
					default:
						// Return 404 for API endpoint and any other path
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
		{
			name:            "URL with remote_servers field (valid registry JSON)",
			allowPrivateIPs: true,
			expectedType:    RegistryTypeURL,
			setupMockServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/":
						// Return valid ToolHive registry JSON with remote_servers
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"version": "1.0.0",
							"remote_servers": map[string]interface{}{
								"remote-server": map[string]interface{}{
									"url": "https://remote.example.com",
								},
							},
						})
					default:
						// Return 404 for API endpoint and any other path
						w.WriteHeader(http.StatusNotFound)
					}
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

	tests := []struct {
		name          string
		setupServer   func() *httptest.Server
		expectedError bool
	}{
		{
			name: "valid registry JSON with servers field",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						"servers": map[string]interface{}{
							"test": map[string]interface{}{"image": "test:latest"},
						},
					})
				}))
			},
			expectedError: false,
		},
		{
			name: "valid registry JSON with remote_servers field",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						"remote_servers": map[string]interface{}{
							"remote": map[string]interface{}{"url": "https://example.com"},
						},
					})
				}))
			},
			expectedError: false,
		},
		{
			name: "valid registry JSON with both servers and remote_servers",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						"servers": map[string]interface{}{
							"test": map[string]interface{}{"image": "test:latest"},
						},
						"remote_servers": map[string]interface{}{
							"remote": map[string]interface{}{"url": "https://example.com"},
						},
					})
				}))
			},
			expectedError: false,
		},
		{
			name: "invalid JSON - missing registry fields",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						// Missing servers and remote_servers
					})
				}))
			},
			expectedError: true,
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
			name: "invalid structure - servers as string instead of map",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					// This would pass weak validation but fails strong type validation
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						"servers": "not-a-map",
					})
				}))
			},
			expectedError: true,
		},
		{
			name: "invalid structure - remote_servers as array instead of map",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					// This would pass weak validation but fails strong type validation
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version":        "1.0.0",
						"remote_servers": []string{"wrong", "type"},
					})
				}))
			},
			expectedError: true,
		},
		{
			name: "empty registry - has fields but no servers",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version":        "1.0.0",
						"servers":        map[string]interface{}{},
						"remote_servers": map[string]interface{}{},
					})
				}))
			},
			expectedError: true,
		},
		{
			name: "valid registry with groups only",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"version": "1.0.0",
						"groups": []map[string]interface{}{
							{
								"name":        "test-group",
								"description": "Test group",
								"servers": map[string]interface{}{
									"grouped-server": map[string]interface{}{"image": "test:latest"},
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
							"version": "1.0.0",
							"servers": map[string]interface{}{
								"test-server": map[string]interface{}{"image": "test:latest"},
							},
						})
					default:
						// Return 404 for API endpoint and any other path
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
