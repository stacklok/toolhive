// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Clients API", Label("api", "clients", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("GET /api/v1beta/clients - List clients", func() {
		Context("when listing clients", func() {
			It("should return list of registered clients", func() {
				By("Listing clients")
				clients := listClients(apiServer)

				By("Verifying response is valid")
				Expect(clients).ToNot(BeNil(), "Client list should not be nil")
			})
		})
	})

	Describe("POST /api/v1beta/clients - Register client with workloads", func() {
		var testClientName client.MCPClient
		var groupName string
		var workloadName string

		BeforeEach(func() {
			testClientName = client.ClaudeCode // Use a valid client type
			groupName = fmt.Sprintf("test-group-%d", time.Now().UnixNano())
			workloadName = e2e.GenerateUniqueServerName("api-client-workload")
		})

		AfterEach(func() {
			// Clean up in reverse order
			deleteWorkload(apiServer, workloadName)
			unregisterClientFromGroup(apiServer, string(testClientName), groupName)
			deleteGroup(apiServer, groupName)
		})

		Context("when registering client with workloads in group", func() {
			It("should successfully register client with default group", func() {
				// Use the pre-existing default group
				By("Creating a workload in the default group")
				workloadReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"group": groups.DefaultGroup,
				}
				workloadResp := createWorkload(apiServer, workloadReq)
				workloadResp.Body.Close()
				Expect(workloadResp.StatusCode).To(Equal(http.StatusCreated))

				// Wait for workload to be running
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be running before client registration")

				By("Registering client with default group")
				registerReq := map[string]interface{}{
					"name":   testClientName,
					"groups": []string{groups.DefaultGroup},
				}
				resp := registerClient(apiServer, registerReq)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				if resp.StatusCode != http.StatusOK {
					bodyBytes, _ := io.ReadAll(resp.Body)
					GinkgoWriter.Printf("Unexpected status %d, body: %s\n", resp.StatusCode, string(bodyBytes))
				}
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 OK for successful client registration")

				By("Verifying response contains client details")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["name"]).To(Equal(string(testClientName)))

				By("Verifying client appears in list")
				Eventually(func() bool {
					clients := listClients(apiServer)
					for _, c := range clients {
						if c.Name == testClientName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue(),
					"Client should appear in list")
			})

			It("should successfully register client with custom group and workload", func() {
				By("Creating a test group")
				createReq := map[string]interface{}{"name": groupName}
				groupResp := createGroup(apiServer, createReq)
				groupResp.Body.Close()
				Expect(groupResp.StatusCode).To(Equal(http.StatusCreated))

				// Wait for group to be created
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Creating a workload in the custom group")
				workloadReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"group": groupName,
				}
				workloadResp := createWorkload(apiServer, workloadReq)
				workloadResp.Body.Close()
				Expect(workloadResp.StatusCode).To(Equal(http.StatusCreated))

				// Wait for workload to be running
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Registering client with the custom group")
				registerReq := map[string]interface{}{
					"name":   testClientName,
					"groups": []string{groupName},
				}
				resp := registerClient(apiServer, registerReq)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying client appears in list")
				Eventually(func() bool {
					clients := listClients(apiServer)
					for _, c := range clients {
						if c.Name == testClientName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())
			})

			It("should reject registration with non-existent group", func() {
				By("Attempting to register client with non-existent group")
				registerReq := map[string]interface{}{
					"name":   testClientName,
					"groups": []string{"non-existent-group-12345"},
				}
				resp := registerClient(apiServer, registerReq)
				defer resp.Body.Close()

				By("Verifying response status indicates error")
				Expect(resp.StatusCode).To(SatisfyAny(
					Equal(http.StatusBadRequest),
					Equal(http.StatusNotFound),
					Equal(http.StatusInternalServerError), // May occur if group doesn't exist
				), "Should return error for non-existent group")
			})

			It("should reject malformed JSON request", func() {
				By("Attempting to register with malformed JSON")
				reqBody := []byte(`{"name": "test-client"`)
				req, err := http.NewRequest(http.MethodPost, apiServer.BaseURL()+"/api/v1beta/clients", bytes.NewReader(reqBody))
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := http.DefaultClient.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for malformed JSON")
			})
		})
	})

	Describe("DELETE /api/v1beta/clients/{name}/groups/{group} - Unregister client from group", func() {
		var testClientName client.MCPClient
		var groupName string
		var workloadName string

		BeforeEach(func() {
			testClientName = client.Cursor // Use a different valid client type for this test
			groupName = fmt.Sprintf("test-group-%d", time.Now().UnixNano())
			workloadName = e2e.GenerateUniqueServerName("api-unreg-workload")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
			deleteGroup(apiServer, groupName)
		})

		Context("when unregistering client from group", func() {
			It("should successfully unregister client from specific group", func() {
				By("Creating a test group")
				createReq := map[string]interface{}{"name": groupName}
				groupResp := createGroup(apiServer, createReq)
				groupResp.Body.Close()
				Expect(groupResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Creating a workload in the group")
				workloadReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"group": groupName,
				}
				workloadResp := createWorkload(apiServer, workloadReq)
				workloadResp.Body.Close()
				Expect(workloadResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Registering client with the group")
				registerReq := map[string]interface{}{
					"name":   testClientName,
					"groups": []string{groupName},
				}
				resp := registerClient(apiServer, registerReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				Eventually(func() bool {
					clients := listClients(apiServer)
					for _, c := range clients {
						if c.Name == testClientName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Unregistering client from the group")
				unregResp := unregisterClientFromGroup(apiServer, string(testClientName), groupName)
				defer unregResp.Body.Close()

				By("Verifying response status is 204 No Content")
				if unregResp.StatusCode != http.StatusNoContent {
					bodyBytes, _ := io.ReadAll(unregResp.Body)
					GinkgoWriter.Printf("Unexpected status %d, body: %s\n", unregResp.StatusCode, string(bodyBytes))
				}
				Expect(unregResp.StatusCode).To(Equal(http.StatusNoContent),
					"Should return 204 for successful group unregistration")
			})
		})
	})

	Describe("POST /api/v1beta/clients/register - Bulk register clients", func() {
		var testClientNames []client.MCPClient
		var groupName string
		var workloadName string

		BeforeEach(func() {
			testClientNames = []client.MCPClient{
				client.VSCode, // Use valid client types for bulk tests
				client.Cline,
			}
			groupName = fmt.Sprintf("bulk-group-%d", time.Now().UnixNano())
			workloadName = e2e.GenerateUniqueServerName("bulk-workload")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
			// Unregister clients from group
			for _, name := range testClientNames {
				unregisterClientFromGroup(apiServer, string(name), groupName)
			}
			deleteGroup(apiServer, groupName)
		})

		Context("when bulk registering clients", func() {
			It("should successfully register multiple clients with workload group", func() {
				By("Creating a test group")
				createReq := map[string]interface{}{"name": groupName}
				groupResp := createGroup(apiServer, createReq)
				groupResp.Body.Close()
				Expect(groupResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Creating a workload in the group")
				workloadReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"group": groupName,
				}
				workloadResp := createWorkload(apiServer, workloadReq)
				workloadResp.Body.Close()
				Expect(workloadResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Bulk registering clients with group")
				bulkReq := map[string]interface{}{
					"names":  testClientNames,
					"groups": []string{groupName},
				}
				resp := bulkRegisterClients(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				if resp.StatusCode != http.StatusOK {
					bodyBytes, _ := io.ReadAll(resp.Body)
					GinkgoWriter.Printf("Unexpected status %d, body: %s\n", resp.StatusCode, string(bodyBytes))
				}
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 OK for successful bulk registration")

				By("Verifying all clients appear in list")
				Eventually(func() int {
					clients := listClients(apiServer)
					foundCount := 0
					for _, testName := range testClientNames {
						for _, c := range clients {
							if c.Name == testName {
								foundCount++
								break
							}
						}
					}
					return foundCount
				}, 10*time.Second, 1*time.Second).Should(Equal(len(testClientNames)),
					"All bulk registered clients should appear in list")
			})

			It("should reject bulk registration with empty names array", func() {
				By("Attempting bulk registration with no names")
				bulkReq := map[string]interface{}{
					"names": []string{},
				}
				resp := bulkRegisterClients(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for empty names array")
			})
		})
	})

	Describe("POST /api/v1beta/clients/unregister - Bulk unregister clients", func() {
		var testClientNames []client.MCPClient
		var groupName string
		var workloadName string

		BeforeEach(func() {
			testClientNames = []client.MCPClient{
				client.Windsurf, // Use different valid client types for bulk unregister tests
				client.LMStudio,
			}
			groupName = fmt.Sprintf("bulk-unreg-group-%d", time.Now().UnixNano())
			workloadName = e2e.GenerateUniqueServerName("bulk-unreg-workload")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
			deleteGroup(apiServer, groupName)
		})

		Context("when bulk unregistering clients", func() {
			It("should successfully unregister multiple clients from group", func() {
				By("Setting up group and workload")
				createReq := map[string]interface{}{"name": groupName}
				groupResp := createGroup(apiServer, createReq)
				groupResp.Body.Close()
				Expect(groupResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				workloadReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"group": groupName,
				}
				workloadResp := createWorkload(apiServer, workloadReq)
				workloadResp.Body.Close()
				Expect(workloadResp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Bulk registering clients")
				bulkRegReq := map[string]interface{}{
					"names":  testClientNames,
					"groups": []string{groupName},
				}
				regResp := bulkRegisterClients(apiServer, bulkRegReq)
				regResp.Body.Close()
				Expect(regResp.StatusCode).To(Equal(http.StatusOK))

				Eventually(func() int {
					clients := listClients(apiServer)
					foundCount := 0
					for _, testName := range testClientNames {
						for _, c := range clients {
							if c.Name == testName {
								foundCount++
								break
							}
						}
					}
					return foundCount
				}, 10*time.Second, 1*time.Second).Should(Equal(len(testClientNames)))

				By("Bulk unregistering clients from group")
				bulkUnregReq := map[string]interface{}{
					"names":  testClientNames,
					"groups": []string{groupName},
				}
				unregResp := bulkUnregisterClients(apiServer, bulkUnregReq)
				defer unregResp.Body.Close()

				By("Verifying response status is 204 No Content")
				if unregResp.StatusCode != http.StatusNoContent {
					bodyBytes, _ := io.ReadAll(unregResp.Body)
					GinkgoWriter.Printf("Unexpected status %d, body: %s\n", unregResp.StatusCode, string(bodyBytes))
				}
				Expect(unregResp.StatusCode).To(Equal(http.StatusNoContent),
					"Should return 204 for successful bulk unregistration")
			})

			It("should reject bulk unregistration with empty names array", func() {
				By("Attempting bulk unregistration with no names")
				bulkReq := map[string]interface{}{
					"names": []string{},
				}
				resp := bulkUnregisterClients(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for empty names array")
			})
		})
	})
})

// Helper functions for client operations

func registerClient(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal register request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/clients", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func listClients(server *e2e.Server) []client.RegisteredClient {
	resp, err := server.Get("/api/v1beta/clients")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list clients")
	defer resp.Body.Close()

	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK), "List clients should return 200")

	var clients []client.RegisteredClient
	err = json.NewDecoder(resp.Body).Decode(&clients)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to decode client list")

	return clients
}

func unregisterClientFromGroup(server *e2e.Server, clientName, groupName string) *http.Response {
	url := fmt.Sprintf("%s/api/v1beta/clients/%s/groups/%s", server.BaseURL(), clientName, groupName)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create unregister from group request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send unregister from group request")

	return resp
}

func bulkRegisterClients(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal bulk register request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/clients/register", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func bulkUnregisterClients(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal bulk unregister request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/clients/unregister", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}
