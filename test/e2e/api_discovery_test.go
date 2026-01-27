// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Discovery API", Label("api", "discovery", "e2e"), func() {
	var apiServer *e2e.Server

	BeforeEach(func() {
		config := e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("GET /api/v1beta/discovery/clients", func() {
		Context("when listing clients", func() {
			It("should return 200 OK", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			})

			It("should return JSON content type", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				Expect(resp.Header.Get("Content-Type")).To(Equal("application/json"))
			})

			It("should return a list of client statuses", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result clientStatusResponse
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				// Should return at least one client (there are many supported clients)
				Expect(result.Clients).NotTo(BeEmpty())
			})

			It("should include expected client types", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result clientStatusResponse
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				// Create a map of returned client types
				clientTypes := make(map[client.MCPClient]bool)
				for _, status := range result.Clients {
					clientTypes[status.ClientType] = true
				}

				// Verify some well-known client types are included
				expectedClients := []client.MCPClient{
					client.RooCode,
					client.Cline,
					client.Cursor,
					client.VSCode,
					client.ClaudeCode,
					client.Windsurf,
				}

				for _, expectedClient := range expectedClients {
					Expect(clientTypes).To(HaveKey(expectedClient),
						"Expected client type %s to be in discovery results", expectedClient)
				}
			})

			It("should return valid client status structure for each client", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result clientStatusResponse
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				// Verify each client has required fields
				for _, status := range result.Clients {
					// ClientType should not be empty
					Expect(string(status.ClientType)).NotTo(BeEmpty(),
						"Client type should not be empty")

					// Installed should be a boolean (will be true or false)
					// Registered should be a boolean (will be true or false)
					// Both are validated by the type system, but we can check they're set
					Expect(status.Installed).To(BeAssignableToTypeOf(false))
					Expect(status.Registered).To(BeAssignableToTypeOf(false))
				}
			})

			It("should return consistent results across multiple requests", func() {
				// Make first request
				resp1 := discoverClients(apiServer)
				body1, err := io.ReadAll(resp1.Body)
				resp1.Body.Close()
				Expect(err).NotTo(HaveOccurred())

				var result1 clientStatusResponse
				err = json.Unmarshal(body1, &result1)
				Expect(err).NotTo(HaveOccurred())

				// Make second request
				resp2 := discoverClients(apiServer)
				body2, err := io.ReadAll(resp2.Body)
				resp2.Body.Close()
				Expect(err).NotTo(HaveOccurred())

				var result2 clientStatusResponse
				err = json.Unmarshal(body2, &result2)
				Expect(err).NotTo(HaveOccurred())

				// Should return same number of clients
				Expect(result1.Clients).To(HaveLen(len(result2.Clients)))

				// Create maps for comparison
				clients1 := make(map[client.MCPClient]client.MCPClientStatus)
				for _, status := range result1.Clients {
					clients1[status.ClientType] = status
				}

				clients2 := make(map[client.MCPClient]client.MCPClientStatus)
				for _, status := range result2.Clients {
					clients2[status.ClientType] = status
				}

				// Verify same client types in both responses
				Expect(clients1).To(HaveLen(len(clients2)))
				for clientType := range clients1 {
					Expect(clients2).To(HaveKey(clientType))
				}
			})

			It("should have registered=false for unregistered clients", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result clientStatusResponse
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				// Since no clients are registered in a fresh server, all should have registered=false
				for _, status := range result.Clients {
					Expect(status.Registered).To(BeFalse(),
						"Client %s should not be registered in fresh server", status.ClientType)
				}
			})
		})

		Context("when handling errors", func() {
			It("should handle concurrent requests gracefully", func() {
				// Make multiple concurrent requests
				numRequests := 5
				done := make(chan bool, numRequests)
				errors := make(chan error, numRequests)

				for i := 0; i < numRequests; i++ {
					go func() {
						defer GinkgoRecover()
						resp := discoverClients(apiServer)
						defer resp.Body.Close()

						if resp.StatusCode != http.StatusOK {
							errors <- http.ErrAbortHandler
							done <- false
							return
						}

						body, err := io.ReadAll(resp.Body)
						if err != nil {
							errors <- err
							done <- false
							return
						}

						var result clientStatusResponse
						err = json.Unmarshal(body, &result)
						if err != nil {
							errors <- err
							done <- false
							return
						}

						// Should return valid response
						if len(result.Clients) == 0 {
							errors <- http.ErrAbortHandler
							done <- false
							return
						}

						done <- true
					}()
				}

				// Wait for all requests to complete
				successCount := 0
				for i := 0; i < numRequests; i++ {
					if <-done {
						successCount++
					}
				}

				// All requests should succeed
				Expect(successCount).To(Equal(numRequests))
				Expect(errors).To(BeEmpty())
			})
		})

		Context("response format validation", func() {
			It("should return well-formed JSON", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				// Should be valid JSON
				var result interface{}
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should have 'clients' array at root level", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result map[string]interface{}
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				// Should have 'clients' key
				Expect(result).To(HaveKey("clients"))

				// 'clients' should be an array
				clients, ok := result["clients"].([]interface{})
				Expect(ok).To(BeTrue(), "'clients' should be an array")
				Expect(clients).NotTo(BeEmpty())
			})

			It("should include required fields in each client status", func() {
				resp := discoverClients(apiServer)
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())

				var result map[string]interface{}
				err = json.Unmarshal(body, &result)
				Expect(err).NotTo(HaveOccurred())

				clients, ok := result["clients"].([]interface{})
				Expect(ok).To(BeTrue())

				// Check each client has required fields
				for i, clientInterface := range clients {
					clientObj, ok := clientInterface.(map[string]interface{})
					Expect(ok).To(BeTrue(), "Client at index %d should be an object", i)

					// Required fields
					Expect(clientObj).To(HaveKey("client_type"),
						"Client at index %d missing 'client_type'", i)
					Expect(clientObj).To(HaveKey("installed"),
						"Client at index %d missing 'installed'", i)
					Expect(clientObj).To(HaveKey("registered"),
						"Client at index %d missing 'registered'", i)

					// Verify types
					Expect(clientObj["client_type"]).To(BeAssignableToTypeOf(""),
						"client_type should be string")
					Expect(clientObj["installed"]).To(BeAssignableToTypeOf(false),
						"installed should be boolean")
					Expect(clientObj["registered"]).To(BeAssignableToTypeOf(false),
						"registered should be boolean")
				}
			})
		})
	})
})

// -----------------------------------------------------------------------------
// Response types
// -----------------------------------------------------------------------------

type clientStatusResponse struct {
	Clients []client.MCPClientStatus `json:"clients"`
}

// -----------------------------------------------------------------------------
// Helper functions
// -----------------------------------------------------------------------------

func discoverClients(server *e2e.Server) *http.Response {
	resp, err := http.Get(server.BaseURL() + "/api/v1beta/discovery/clients")
	Expect(err).NotTo(HaveOccurred())
	return resp
}
