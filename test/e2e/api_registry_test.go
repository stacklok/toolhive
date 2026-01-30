// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/api/v1"
	"github.com/stacklok/toolhive/pkg/registry/registry"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Registry API", Label("api", "registry", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("GET /api/v1beta/registry - List registries", func() {
		Context("when listing registries", func() {
			It("should return list with default registry", func() {
				By("Listing all registries")
				registries := listRegistries(apiServer)

				By("Verifying default registry exists")
				Expect(registries).To(HaveLen(1), "Should have exactly one registry")
				Expect(registries[0].Name).To(Equal("default"), "Registry name should be 'default'")
			})

			It("should include correct metadata", func() {
				By("Listing registries")
				registries := listRegistries(apiServer)

				By("Verifying metadata fields")
				Expect(registries).To(HaveLen(1))
				reg := registries[0]
				Expect(reg.Version).ToNot(BeEmpty(), "Version should not be empty")
				Expect(reg.ServerCount).To(BeNumerically(">", 0), "Server count should be greater than 0")
				Expect(reg.Type).To(Equal(v1.RegistryTypeDefault), "Type should be 'default'")
			})
		})
	})

	Describe("POST /api/v1beta/registry - Add registry", func() {
		It("should return 501 Not Implemented", func() {
			By("Attempting to add a new registry")
			request := map[string]interface{}{
				"name": "custom-registry",
				"url":  "https://example.com/registry.json",
			}
			resp := addRegistry(apiServer, request)
			defer resp.Body.Close()

			By("Verifying response status is 501 Not Implemented")
			Expect(resp.StatusCode).To(Equal(http.StatusNotImplemented),
				"Adding custom registries should return 501 Not Implemented")
		})
	})

	Describe("GET /api/v1beta/registry/{name} - Get registry", func() {
		Context("when getting registry details", func() {
			It("should return registry details for default", func() {
				By("Getting default registry")
				resp := getRegistry(apiServer, "default")
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for default registry")

				By("Verifying response contains registry information")
				var result getRegistryResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result.Name).To(Equal("default"), "Response should contain registry name")
				Expect(result.Registry).ToNot(BeNil(), "Response should contain Registry object")
			})

			It("should return 404 for non-existent registry", func() {
				By("Attempting to get non-existent registry")
				resp := getRegistry(apiServer, "non-existent-registry-12345")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent registry")
			})

			It("should include Registry object with servers", func() {
				By("Getting default registry")
				resp := getRegistry(apiServer, "default")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying Registry contains servers")
				var result getRegistryResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Registry.Servers).ToNot(BeEmpty(), "Registry should contain servers")
			})
		})
	})

	Describe("PUT /api/v1beta/registry/{name} - Update registry", func() {
		// Reset registry to default after each test that modifies it
		AfterEach(func() {
			resetReq := map[string]interface{}{}
			resp := updateRegistry(apiServer, "default", resetReq)
			resp.Body.Close()
		})

		Context("valid updates", func() {
			It("should update with valid local file path", func() {
				By("Creating a valid test registry file")
				testFile := createTestRegistryFile(map[string]interface{}{
					"test-server": map[string]interface{}{
						"image":       "test/image:latest",
						"description": "Test server",
						"tier":        "Community",
						"status":      "Active",
						"transport":   "stdio",
					},
				})

				By("Updating registry with local file path")
				updateReq := map[string]interface{}{
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for successful update")

				By("Verifying response contains success message")
				var result updateRegistryResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Type).To(Equal("file"), "Type should be 'file'")
			})

			It("should reset to default with empty request", func() {
				By("First setting a custom registry")
				testFile := createTestRegistryFile(map[string]interface{}{
					"test-server": map[string]interface{}{
						"image":       "test/image:latest",
						"description": "Test server",
						"tier":        "Community",
						"status":      "Active",
						"transport":   "stdio",
					},
				})
				setResp := updateRegistry(apiServer, "default", map[string]interface{}{
					"local_path": testFile,
				})
				setResp.Body.Close()
				Expect(setResp.StatusCode).To(Equal(http.StatusOK))

				By("Resetting to default with empty request")
				resetReq := map[string]interface{}{}
				resp := updateRegistry(apiServer, "default", resetReq)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying response indicates reset to default")
				var result updateRegistryResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Type).To(Equal("default"), "Type should be 'default'")
			})
		})

		Context("validation errors", func() {
			It("should return 400 for invalid JSON", func() {
				By("Sending malformed JSON")
				resp := updateRegistryRaw(apiServer, "default", []byte(`{"invalid`))
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid JSON")
			})

			It("should return 400 when specifying multiple sources", func() {
				By("Sending request with multiple sources")
				testFile := createTestRegistryFile(map[string]interface{}{
					"test-server": map[string]interface{}{
						"image":       "test/image:latest",
						"description": "Test server",
						"tier":        "Community",
						"status":      "Active",
						"transport":   "stdio",
					},
				})
				updateReq := map[string]interface{}{
					"url":        "https://example.com/registry.json",
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 when specifying multiple sources")
			})

			It("should return 400 for non-existent file", func() {
				By("Sending request with non-existent file path")
				updateReq := map[string]interface{}{
					"local_path": "/non/existent/path/registry.json",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for non-existent file")
			})

			It("should return 502 for invalid JSON file", func() {
				By("Creating a file with invalid JSON")
				testFile := createTestRegistryFileWithContent([]byte(`{"invalid`))

				updateReq := map[string]interface{}{
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 502 Bad Gateway")
				Expect(resp.StatusCode).To(Equal(http.StatusBadGateway),
					"Should return 502 for invalid JSON file")
			})

			It("should return 502 for file without servers", func() {
				By("Creating a file without servers")
				testFile := createTestRegistryFileWithContent([]byte(`{"version": "1.0.0"}`))

				updateReq := map[string]interface{}{
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 502 Bad Gateway")
				Expect(resp.StatusCode).To(Equal(http.StatusBadGateway),
					"Should return 502 for file without servers")
			})
		})

		Context("non-default registry", func() {
			It("should return 404 for non-default name", func() {
				By("Attempting to update non-default registry")
				updateReq := map[string]interface{}{
					"url": "https://example.com/registry.json",
				}
				resp := updateRegistry(apiServer, "custom-registry", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-default registry name")
			})
		})

		Context("URL-based updates", func() {
			It("should return 504 for URL pointing to unreachable host", func() {
				By("Sending request with URL to unreachable host")
				updateReq := map[string]interface{}{
					"url": "https://nonexistent-host-12345.invalid/registry.json",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 504 Gateway Timeout")
				Expect(resp.StatusCode).To(Equal(http.StatusGatewayTimeout),
					"Should return 504 for unreachable URL")
			})

			It("should return 400 for HTTP URL without allow_private_ip", func() {
				By("Sending request with HTTP URL (not HTTPS) without allow_private_ip")
				updateReq := map[string]interface{}{
					"url": "http://example.com/registry.json",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for HTTP URL without allow_private_ip")
			})

			It("should return 400 for invalid URL format", func() {
				By("Sending request with invalid URL format")
				updateReq := map[string]interface{}{
					"url": "not-a-valid-url",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid URL format")
			})
		})

		Context("API URL updates", func() {
			It("should return 400 for api_url with HTTP when allow_private_ip is false", func() {
				By("Sending request with HTTP api_url without allow_private_ip")
				updateReq := map[string]interface{}{
					"api_url": "http://example.com/api",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for HTTP api_url without allow_private_ip")
			})

			It("should return 400 for api_url with invalid URL format", func() {
				By("Sending request with invalid api_url format")
				updateReq := map[string]interface{}{
					"api_url": "not-a-valid-url",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid api_url format")
			})

			It("should return 504 for api_url pointing to unreachable host", func() {
				// Note: api_url now validates reachability when allow_private_ip is false
				By("Sending request with api_url to unreachable host")
				updateReq := map[string]interface{}{
					"api_url": "https://nonexistent-host-12345.invalid/api",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 504 Gateway Timeout")
				Expect(resp.StatusCode).To(Equal(http.StatusGatewayTimeout),
					"Should return 504 for unreachable api_url")
			})

			It("should return 400 when specifying both url and api_url", func() {
				By("Sending request with both url and api_url")
				updateReq := map[string]interface{}{
					"url":     "https://example.com/registry.json",
					"api_url": "https://example.com/api",
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 when specifying multiple sources")
			})
		})
	})

	Describe("Cross-endpoint state consistency", func() {
		// Reset registry to default after each test
		AfterEach(func() {
			resetReq := map[string]interface{}{}
			resp := updateRegistry(apiServer, "default", resetReq)
			resp.Body.Close()
		})

		Context("after updating registry with local file", func() {
			var testFile string
			const testServerName = "e2e-test-server"

			BeforeEach(func() {
				By("Creating a test registry file with a unique server")
				testFile = createTestRegistryFile(map[string]interface{}{
					testServerName: map[string]interface{}{
						"image":       "test/e2e-image:latest",
						"description": "E2E Test server for state consistency",
						"tier":        "Community",
						"status":      "Active",
						"transport":   "stdio",
					},
				})

				By("Updating registry with the test file")
				updateReq := map[string]interface{}{
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			})

			It("should show type='file' in list registries", func() {
				By("Listing registries")
				registries := listRegistries(apiServer)

				By("Verifying registry type is 'file'")
				Expect(registries).To(HaveLen(1))
				Expect(registries[0].Type).To(Equal(v1.RegistryTypeFile),
					"Registry type should be 'file' after setting local file")
				Expect(registries[0].Source).To(Equal(testFile),
					"Registry source should match the test file path")
			})

			It("should show updated source in get registry", func() {
				By("Getting default registry")
				resp := getRegistry(apiServer, "default")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying registry details")
				var result getRegistryResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Type).To(Equal(v1.RegistryTypeFile),
					"Registry type should be 'file'")
				Expect(result.Source).To(Equal(testFile),
					"Registry source should match the test file path")
			})

			It("should return servers from the new file in list servers", func() {
				By("Listing servers from default registry")
				resp := listRegistryServers(apiServer, "default")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying test server appears in list")
				var result listServersResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())

				// Find our test server
				found := false
				for _, server := range result.Servers {
					if server.Name == testServerName {
						found = true
						Expect(server.Description).To(Equal("E2E Test server for state consistency"))
						break
					}
				}
				Expect(found).To(BeTrue(), "Test server should appear in list servers")
			})

			It("should find the new server via get server endpoint", func() {
				By("Getting the test server")
				resp := getRegistryServer(apiServer, "default", testServerName)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should find the test server")

				By("Verifying server details")
				var result getServerResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Server).ToNot(BeNil())
				Expect(result.Server.Name).To(Equal(testServerName))
				Expect(result.IsRemote).To(BeFalse())
			})

			It("should not find servers from original registry", func() {
				By("Attempting to get 'osv' server from default registry")
				resp := getRegistryServer(apiServer, "default", "osv")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"osv server should not exist in custom registry")
			})

			It("should show correct server count", func() {
				By("Listing registries")
				registries := listRegistries(apiServer)

				By("Verifying server count is 1")
				Expect(registries[0].ServerCount).To(Equal(1),
					"Server count should be 1 for test registry")
			})
		})

		Context("after resetting to default", func() {
			BeforeEach(func() {
				By("First setting a custom registry")
				testFile := createTestRegistryFile(map[string]interface{}{
					"custom-server": map[string]interface{}{
						"image":       "test/custom:latest",
						"description": "Custom server",
						"tier":        "Community",
						"status":      "Active",
						"transport":   "stdio",
					},
				})
				setResp := updateRegistry(apiServer, "default", map[string]interface{}{
					"local_path": testFile,
				})
				setResp.Body.Close()
				Expect(setResp.StatusCode).To(Equal(http.StatusOK))

				By("Resetting to default")
				resetReq := map[string]interface{}{}
				resetResp := updateRegistry(apiServer, "default", resetReq)
				resetResp.Body.Close()
				Expect(resetResp.StatusCode).To(Equal(http.StatusOK))
			})

			It("should show type='default' in list registries", func() {
				By("Listing registries")
				registries := listRegistries(apiServer)

				By("Verifying registry type is 'default'")
				Expect(registries).To(HaveLen(1))
				Expect(registries[0].Type).To(Equal(v1.RegistryTypeDefault),
					"Registry type should be 'default' after reset")
				Expect(registries[0].Source).To(BeEmpty(),
					"Registry source should be empty for default")
			})

			It("should find osv server again", func() {
				By("Getting 'osv' server")
				resp := getRegistryServer(apiServer, "default", "osv")
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"osv server should exist in default registry")
			})

			It("should not find custom server", func() {
				By("Attempting to get 'custom-server'")
				resp := getRegistryServer(apiServer, "default", "custom-server")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"custom-server should not exist in default registry")
			})
		})

		Context("with registry containing remote servers", func() {
			var testFile string
			const remoteServerName = "e2e-remote-server"

			BeforeEach(func() {
				By("Creating a test registry file with remote servers")
				registryData := map[string]interface{}{
					"version":      "1.0.0",
					"last_updated": "2025-01-01T00:00:00Z",
					"servers":      map[string]interface{}{},
					"remote_servers": map[string]interface{}{
						remoteServerName: map[string]interface{}{
							"url":         "https://example.com/mcp",
							"description": "E2E Test remote server",
							"tier":        "Community",
							"status":      "Active",
							"transport":   "sse",
						},
					},
				}
				data, err := json.Marshal(registryData)
				Expect(err).ToNot(HaveOccurred())
				testFile = createTestRegistryFileWithContent(data)

				By("Updating registry with the test file")
				updateReq := map[string]interface{}{
					"local_path": testFile,
				}
				resp := updateRegistry(apiServer, "default", updateReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			})

			It("should list remote servers in servers endpoint", func() {
				By("Listing servers from default registry")
				resp := listRegistryServers(apiServer, "default")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying remote server appears in list")
				var result listServersResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())

				// Remote server should be in the remote_servers array
				found := false
				for _, server := range result.RemoteServers {
					if server.Name == remoteServerName {
						found = true
						Expect(server.Description).To(Equal("E2E Test remote server"))
						break
					}
				}
				Expect(found).To(BeTrue(), "Remote server should appear in remote_servers list")
			})

			It("should return remote server via get server endpoint with is_remote=true", func() {
				By("Getting the remote test server")
				resp := getRegistryServer(apiServer, "default", remoteServerName)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should find the remote test server")

				By("Verifying server details indicate remote")
				var result getServerResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.IsRemote).To(BeTrue(), "is_remote should be true")
				Expect(result.RemoteServer).ToNot(BeNil(), "remote_server should be populated")
				Expect(result.Server).To(BeNil(), "server should be nil for remote servers")
				Expect(result.RemoteServer.Name).To(Equal(remoteServerName))
			})
		})
	})

	Describe("DELETE /api/v1beta/registry/{name} - Remove registry", func() {
		It("should return 400 for default registry", func() {
			By("Attempting to delete default registry")
			resp := deleteRegistry(apiServer, "default")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"Should return 400 when trying to delete default registry")
		})

		It("should return 404 for non-existent registry", func() {
			By("Attempting to delete non-existent registry")
			resp := deleteRegistry(apiServer, "non-existent-registry-12345")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
				"Should return 404 for non-existent registry")
		})
	})

	Describe("GET /api/v1beta/registry/{name}/servers - List servers", func() {
		Context("when listing servers", func() {
			It("should return servers from default registry", func() {
				By("Listing servers from default registry")
				resp := listRegistryServers(apiServer, "default")
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for default registry")

				By("Verifying response contains servers")
				var result listServersResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result.Servers).ToNot(BeEmpty(), "Should have at least one server")
			})

			It("should return 404 for non-existent registry", func() {
				By("Attempting to list servers from non-existent registry")
				resp := listRegistryServers(apiServer, "non-existent-registry-12345")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent registry")
			})

			It("should include both servers and remote_servers in response", func() {
				By("Listing servers from default registry")
				resp := listRegistryServers(apiServer, "default")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying response structure has both fields")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(HaveKey("servers"), "Response should have 'servers' field")
				// remote_servers may be empty but should be parseable
			})
		})
	})

	Describe("GET /api/v1beta/registry/{name}/servers/{serverName} - Get server", func() {
		Context("when getting server details", func() {
			It("should return server details for existing server", func() {
				By("Getting server details for 'osv' server")
				resp := getRegistryServer(apiServer, "default", "osv")
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for existing server")

				By("Verifying response contains server information")
				var result getServerResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result.Server).ToNot(BeNil(), "Response should contain server details")
				Expect(result.IsRemote).To(BeFalse(), "osv should not be a remote server")
			})

			It("should return 404 for non-existent server", func() {
				By("Attempting to get non-existent server")
				resp := getRegistryServer(apiServer, "default", "non-existent-server-12345")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent server")
			})

			It("should return 404 for non-existent registry", func() {
				By("Attempting to get server from non-existent registry")
				resp := getRegistryServer(apiServer, "non-existent-registry", "osv")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent registry")
			})

			It("should handle URL-encoded server names", func() {
				By("Getting server with URL-encoded name")
				// Use a server name that exists and test URL encoding
				encodedName := url.PathEscape("osv")
				resp := getRegistryServer(apiServer, "default", encodedName)
				defer resp.Body.Close()

				By("Verifying response is successful")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should handle URL-encoded server names")
			})
		})
	})
})

// Response types for registry API

type registryInfo struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	LastUpdated string          `json:"last_updated"`
	ServerCount int             `json:"server_count"`
	Type        v1.RegistryType `json:"type"`
	Source      string          `json:"source"`
}

type registryListResponse struct {
	Registries []registryInfo `json:"registries"`
}

type getRegistryResponse struct {
	Name        string             `json:"name"`
	Version     string             `json:"version"`
	LastUpdated string             `json:"last_updated"`
	ServerCount int                `json:"server_count"`
	Type        v1.RegistryType    `json:"type"`
	Source      string             `json:"source"`
	Registry    *registry.Registry `json:"registry"`
}

type listServersResponse struct {
	Servers       []*registry.ImageMetadata        `json:"servers"`
	RemoteServers []*registry.RemoteServerMetadata `json:"remote_servers,omitempty"`
}

type getServerResponse struct {
	Server       *registry.ImageMetadata        `json:"server,omitempty"`
	RemoteServer *registry.RemoteServerMetadata `json:"remote_server,omitempty"`
	IsRemote     bool                           `json:"is_remote"`
}

type updateRegistryResponse struct {
	Type string `json:"type"`
}

// Helper functions for registry operations

func listRegistries(server *e2e.Server) []registryInfo {
	resp, err := server.Get("/api/v1beta/registry")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list registries")
	defer resp.Body.Close()

	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK), "List registries should return 200")

	var result registryListResponse
	err = json.NewDecoder(resp.Body).Decode(&result)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to decode registry list")

	return result.Registries
}

func getRegistry(server *e2e.Server, name string) *http.Response {
	resp, err := server.Get("/api/v1beta/registry/" + name)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to get registry")

	return resp
}

func addRegistry(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal add registry request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/registry", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func updateRegistry(server *e2e.Server, name string, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal update registry request")

	return updateRegistryRaw(server, name, reqBody)
}

func updateRegistryRaw(server *e2e.Server, name string, body []byte) *http.Response {
	req, err := http.NewRequest(http.MethodPut, server.BaseURL()+"/api/v1beta/registry/"+name, bytes.NewReader(body))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func deleteRegistry(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodDelete, server.BaseURL()+"/api/v1beta/registry/"+name, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create delete request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send delete request")

	return resp
}

func listRegistryServers(server *e2e.Server, registryName string) *http.Response {
	resp, err := server.Get("/api/v1beta/registry/" + registryName + "/servers")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list registry servers")

	return resp
}

func getRegistryServer(server *e2e.Server, registryName, serverName string) *http.Response {
	resp, err := server.Get("/api/v1beta/registry/" + registryName + "/servers/" + serverName)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to get registry server")

	return resp
}

func createTestRegistryFile(servers map[string]interface{}) string {
	registryData := map[string]interface{}{
		"version":      "1.0.0",
		"last_updated": "2025-01-01T00:00:00Z",
		"servers":      servers,
	}

	data, err := json.Marshal(registryData)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal test registry")

	return createTestRegistryFileWithContent(data)
}

func createTestRegistryFileWithContent(content []byte) string {
	tempDir := GinkgoT().TempDir()
	testFile := filepath.Join(tempDir, "test-registry.json")

	err := os.WriteFile(testFile, content, 0600)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to write test registry file")

	return testFile
}
