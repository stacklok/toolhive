// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Workload Lifecycle API", Label("api", "workloads", "lifecycle", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("POST /api/v1beta/workloads/{name}/stop - Stop workload", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-stop-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when stopping a workload", func() {
			It("should successfully stop a running workload", func() {
				By("Creating a running workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be running before stopping")

				By("Stopping the workload")
				stopResp := stopWorkload(apiServer, workloadName)
				defer stopResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted),
					"Stop operation should return 202 Accepted")

				By("Verifying workload is stopped")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be stopped within 60 seconds")
			})

			It("should be idempotent when stopping an already stopped workload", func() {
				By("Creating and stopping a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				stopResp := stopWorkload(apiServer, workloadName)
				stopResp.Body.Close()
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Stopping the already stopped workload again")
				stopResp2 := stopWorkload(apiServer, workloadName)
				defer stopResp2.Body.Close()

				By("Verifying idempotent behavior with 202 Accepted")
				Expect(stopResp2.StatusCode).To(Equal(http.StatusAccepted),
					"Stopping an already stopped workload should be idempotent")
			})

			It("should return 404 when stopping a non-existent workload", func() {
				By("Attempting to stop non-existent workload")
				stopResp := stopWorkload(apiServer, "non-existent-workload-12345")
				defer stopResp.Body.Close()

				By("Verifying response status indicates error")
				Expect(stopResp.StatusCode).To(SatisfyAny(
					Equal(http.StatusNotFound),
					Equal(http.StatusBadRequest),
				), "Should return error for non-existent workload")
			})
		})
	})

	Describe("POST /api/v1beta/workloads/{name}/restart - Restart workload", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-restart-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when restarting a workload", func() {
			It("should successfully restart a running workload and keep same URL", func() {
				By("Creating a running workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running and getting original URL")
				var originalURL string
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							originalURL = w.URL
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				Expect(originalURL).ToNot(BeEmpty(), "Original URL should be set")

				By("Restarting the workload")
				restartResp := restartWorkload(apiServer, workloadName)
				defer restartResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(restartResp.StatusCode).To(Equal(http.StatusAccepted),
					"Restart operation should return 202 Accepted")

				By("Verifying workload is running again with same URL")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							GinkgoWriter.Printf("Workload URL after restart: %s (original: %s)\n", w.URL, originalURL)
							return w.URL == originalURL
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Workload should be running with same URL after restart")
			})

			It("should successfully restart a stopped workload", func() {
				By("Creating and stopping a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				stopResp := stopWorkload(apiServer, workloadName)
				stopResp.Body.Close()
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Restarting the stopped workload")
				restartResp := restartWorkload(apiServer, workloadName)
				defer restartResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(restartResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Verifying workload is running again")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue(),
					"Stopped workload should be running after restart")
			})

			It("should return error when restarting a non-existent workload", func() {
				By("Attempting to restart non-existent workload")
				restartResp := restartWorkload(apiServer, "non-existent-workload-12345")
				defer restartResp.Body.Close()

				By("Verifying response status indicates error")
				Expect(restartResp.StatusCode).To(SatisfyAny(
					Equal(http.StatusNotFound),
					Equal(http.StatusBadRequest),
				), "Should return error for non-existent workload")
			})
		})
	})

	Describe("GET /api/v1beta/workloads/{name}/status - Get workload status", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-status-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when getting workload status", func() {
			It("should return status of a running workload", func() {
				By("Creating a running workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Getting workload status")
				statusResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s/status", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer statusResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(statusResp.StatusCode).To(Equal(http.StatusOK),
					"Status endpoint should return 200 OK")

				By("Verifying response contains running status")
				var statusResponse struct {
					Status runtime.WorkloadStatus `json:"status"`
				}
				err = json.NewDecoder(statusResp.Body).Decode(&statusResponse)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(statusResponse.Status).To(Equal(runtime.WorkloadStatusRunning),
					"Status should indicate workload is running")
			})

			It("should return status of a stopped workload", func() {
				By("Creating and stopping a workload")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				stopResp := stopWorkload(apiServer, workloadName)
				stopResp.Body.Close()
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusStopped {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Getting workload status")
				statusResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s/status", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer statusResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(statusResp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying response contains stopped status")
				var statusResponse struct {
					Status runtime.WorkloadStatus `json:"status"`
				}
				err = json.NewDecoder(statusResp.Body).Decode(&statusResponse)
				Expect(err).ToNot(HaveOccurred())
				Expect(statusResponse.Status).To(Equal(runtime.WorkloadStatusStopped),
					"Status should indicate workload is stopped")
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to get status of non-existent workload")
				statusResp, err := apiServer.Get("/api/v1beta/workloads/non-existent-workload-12345/status")
				Expect(err).ToNot(HaveOccurred())
				defer statusResp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(statusResp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent workload")
			})
		})
	})
})

// Helper function for restarting workloads
func restartWorkload(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/workloads/"+name+"/restart", nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create restart request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send restart request")

	return resp
}
