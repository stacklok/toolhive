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

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Workloads API", Label("api", "workloads", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("POST /api/v1beta/workloads - Create workload", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-workload")
		})

		AfterEach(func() {
			// Clean up created workload
			deleteWorkload(apiServer, workloadName)
		})

		Context("when creating workload from registry", func() {
			It("should successfully create OSV server workload", func() {
				By("Creating an OSV workload via API")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"Should return 201 Created for successful workload creation")

				By("Verifying the response contains workload details")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["name"]).To(Equal(workloadName), "Response should contain workload name")
				Expect(result["port"]).ToNot(BeZero(), "Response should contain assigned port")

				By("Verifying the workload appears in the list")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to include all states
					for _, w := range workloads {
						if w.Name == workloadName {
							GinkgoWriter.Printf("Found workload %s with status %s\n", w.Name, w.Status)
							return true
						}
					}
					GinkgoWriter.Printf("Workload %s not found in list of %d workloads\n", workloadName, len(workloads))
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should appear in the list within 60 seconds")
			})

			It("should successfully create Fetch server workload", func() {
				By("Creating a Fetch workload via API")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "fetch",
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying the response contains workload details")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result["name"]).To(Equal(workloadName))
				Expect(result["port"]).ToNot(BeZero())
			})
		})

		Context("when creating workload with validation errors", func() {
			It("should reject workload with empty name", func() {
				By("Attempting to create workload without name")
				createReq := map[string]interface{}{
					"name":  "",
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for empty workload name")
			})

			It("should reject workload with invalid name characters", func() {
				By("Attempting to create workload with invalid name")
				createReq := map[string]interface{}{
					"name":  "invalid@name!",
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid workload name")
			})

			It("should reject workload with missing image field", func() {
				By("Attempting to create workload without image")
				createReq := map[string]interface{}{
					"name": workloadName,
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for missing image field")
			})

			It("should reject workload with non-existent image", func() {
				By("Attempting to create workload with non-existent image")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "non-existent-server-12345",
				}
				resp := createWorkload(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying the response status is 400 or 404")
				Expect(resp.StatusCode).To(SatisfyAny(
					Equal(http.StatusBadRequest),
					Equal(http.StatusNotFound),
				), "Should return error for non-existent image")
			})

			It("should reject malformed JSON request", func() {
				By("Attempting to create workload with malformed JSON")
				reqBody := []byte(`{"name": "test", "image": "osv"`)
				req, err := http.NewRequest(http.MethodPost, apiServer.BaseURL()+"/api/v1beta/workloads", bytes.NewReader(reqBody))
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := http.DefaultClient.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying the response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for malformed JSON")
			})
		})

		Context("when creating duplicate workload", func() {
			It("should reject creating workload with existing name", func() {
				By("Creating the first workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"First workload should be created successfully")

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Attempting to create duplicate workload with same name")
				resp2 := createWorkload(apiServer, createReq)
				defer resp2.Body.Close()

				By("Verifying the response indicates conflict")
				Expect(resp2.StatusCode).To(SatisfyAny(
					Equal(http.StatusConflict),
					Equal(http.StatusBadRequest),
				), "Should return 409 Conflict or 400 for duplicate workload name")

				By("Reading error message")
				bodyBytes, _ := io.ReadAll(resp2.Body)
				bodyStr := string(bodyBytes)
				Expect(bodyStr).To(ContainSubstring("already exists"),
					"Error message should indicate workload already exists")
			})
		})
	})

	Describe("GET /api/v1beta/workloads - List workloads", func() {
		Context("when listing workloads", func() {
			It("should return empty list when no workloads exist", func() {
				By("Listing workloads")
				workloads := listWorkloads(apiServer, false)

				By("Verifying the list can be empty or contain only system workloads")
				// The list might contain pre-existing workloads, so we just verify
				// the response is valid
				Expect(workloads).ToNot(BeNil(), "Workload list should not be nil")
			})

			It("should list running workloads by default", func() {
				workloadName := e2e.GenerateUniqueServerName("api-list-test")
				defer deleteWorkload(apiServer, workloadName)

				By("Creating a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be running within 30 seconds")

				By("Verifying workload appears in default list")
				workloads := listWorkloads(apiServer, false)
				found := false
				for _, w := range workloads {
					if w.Name == workloadName {
						found = true
						Expect(w.Status).To(Equal(runtime.WorkloadStatusRunning))
						break
					}
				}
				Expect(found).To(BeTrue(), "Created workload should appear in list")
			})

			It("should list all workloads including stopped when all=true", func() {
				workloadName := e2e.GenerateUniqueServerName("api-list-all-test")
				defer deleteWorkload(apiServer, workloadName)

				By("Creating and then stopping a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				// Wait for it to be running
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				// Stop the workload
				stopResp := stopWorkload(apiServer, workloadName)
				stopResp.Body.Close()
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				// Wait for it to be stopped
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be stopped within 30 seconds")

				By("Verifying stopped workload appears with all=true")
				workloadsAll := listWorkloads(apiServer, true)
				found := false
				for _, w := range workloadsAll {
					if w.Name == workloadName {
						found = true
						Expect(w.Status).To(Equal(runtime.WorkloadStatusStopped))
						break
					}
				}
				Expect(found).To(BeTrue(), "Stopped workload should appear with all=true")

				By("Verifying stopped workload does not appear in default list")
				workloadsRunning := listWorkloads(apiServer, false)
				foundRunning := false
				for _, w := range workloadsRunning {
					if w.Name == workloadName {
						foundRunning = true
						break
					}
				}
				Expect(foundRunning).To(BeFalse(), "Stopped workload should not appear in default list")
			})
		})
	})

	Describe("GET /api/v1beta/workloads/{name} - Get workload details", func() {
		Context("when getting workload details", func() {
			It("should return workload configuration for existing workload", func() {
				workloadName := e2e.GenerateUniqueServerName("api-get-test")
				defer deleteWorkload(apiServer, workloadName)

				By("Creating a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Getting workload details")
				getResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer getResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(getResp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for existing workload")

				By("Verifying response contains RunConfig")
				var config map[string]interface{}
				err = json.NewDecoder(getResp.Body).Decode(&config)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(config["name"]).To(Equal(workloadName), "Config should contain workload name")
				Expect(config["image"]).ToNot(BeEmpty(), "Config should contain image")
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to get non-existent workload")
				resp, err := apiServer.Get("/api/v1beta/workloads/non-existent-workload-12345")
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent workload")
			})
		})
	})

	Describe("DELETE /api/v1beta/workloads/{name} - Delete workload", func() {
		Context("when deleting workload", func() {
			It("should successfully delete running workload", func() {
				workloadName := e2e.GenerateUniqueServerName("api-delete-running")

				By("Creating a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Deleting the workload")
				delResp := deleteWorkload(apiServer, workloadName)
				defer delResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(delResp.StatusCode).To(Equal(http.StatusAccepted),
					"Should return 202 for async delete operation")

				By("Verifying workload is removed from list")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeFalse(),
					"Workload should be removed from list within 30 seconds")
			})

			It("should successfully delete stopped workload", func() {
				workloadName := e2e.GenerateUniqueServerName("api-delete-stopped")

				By("Creating a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true) // Use all=true to see all states
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Stopping the workload")
				stopResp := stopWorkload(apiServer, workloadName)
				stopResp.Body.Close()
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Waiting for workload to be stopped")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Deleting the stopped workload")
				delResp := deleteWorkload(apiServer, workloadName)
				defer delResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(delResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Verifying workload is removed from list")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeFalse())
			})

			It("should handle deleting non-existent workload gracefully", func() {
				By("Attempting to delete non-existent workload")
				req, err := http.NewRequest(http.MethodDelete, apiServer.BaseURL()+"/api/v1beta/workloads/non-existent-workload-12345", nil)
				Expect(err).ToNot(HaveOccurred())

				resp, err := http.DefaultClient.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 202 Accepted or 404 Not Found")
				// API currently returns 202 even for non-existent workloads (idempotent behavior)
				Expect(resp.StatusCode).To(SatisfyAny(
					Equal(http.StatusAccepted),
					Equal(http.StatusNotFound),
				), "Should handle delete of non-existent workload gracefully")
			})
		})
	})

	Describe("Workload lifecycle verification", func() {
		It("should track workload through create-list-delete lifecycle", func() {
			workloadName := e2e.GenerateUniqueServerName("api-lifecycle")

			By("Step 1: Verifying workload does not exist initially")
			initialWorkloads := listWorkloads(apiServer, true)
			for _, w := range initialWorkloads {
				Expect(w.Name).ToNot(Equal(workloadName),
					"Workload should not exist initially")
			}

			By("Step 2: Creating workload")
			createReq := map[string]interface{}{
				"name":  workloadName,
				"image": "osv",
			}
			resp := createWorkload(apiServer, createReq)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			By("Step 3: Verifying workload appears in list")
			Eventually(func() bool {
				workloads := listWorkloads(apiServer, true) // Use all=true to see all states
				for _, w := range workloads {
					if w.Name == workloadName {
						return true
					}
				}
				return false
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"Created workload should appear in list")

			By("Step 4: Deleting workload")
			delResp := deleteWorkload(apiServer, workloadName)
			delResp.Body.Close()
			Expect(delResp.StatusCode).To(Equal(http.StatusAccepted))

			By("Step 5: Verifying workload is removed from list")
			Eventually(func() bool {
				workloads := listWorkloads(apiServer, true)
				for _, w := range workloads {
					if w.Name == workloadName {
						return true
					}
				}
				return false
			}, 60*time.Second, 2*time.Second).Should(BeFalse(),
				"Deleted workload should not appear in list")
		})
	})
})

// Helper functions

func createWorkload(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal create request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/workloads", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func listWorkloads(server *e2e.Server, all bool) []core.Workload {
	url := "/api/v1beta/workloads"
	if all {
		url += "?all=true"
	}

	resp, err := server.Get(url)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list workloads")
	defer resp.Body.Close()

	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK), "List workloads should return 200")

	var result struct {
		Workloads []core.Workload `json:"workloads"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to decode workload list")

	return result.Workloads
}

func deleteWorkload(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodDelete, server.BaseURL()+"/api/v1beta/workloads/"+name, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create delete request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send delete request")

	return resp
}

func stopWorkload(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/workloads/"+name+"/stop", nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create stop request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send stop request")

	return resp
}
