// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
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

	Describe("POST /api/v1beta/workloads/{name}/edit - Update workload", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-update-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when updating a workload", func() {
			It("should successfully update workload environment variables", func() {
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
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Updating the workload with environment variables")
				updateReq := map[string]interface{}{
					"image": "osv",
					"env": map[string]string{
						"TEST_VAR": "test-value",
					},
				}
				updateResp := updateWorkload(apiServer, workloadName, updateReq)
				defer updateResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(updateResp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for successful update")

				By("Verifying response contains workload details")
				var result map[string]interface{}
				err := json.NewDecoder(updateResp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result["name"]).To(Equal(workloadName))
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to update non-existent workload")
				updateReq := map[string]interface{}{
					"image": "osv",
				}
				resp := updateWorkload(apiServer, "non-existent-workload-12345", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent workload")
			})

			It("should reject invalid JSON", func() {
				By("Creating a workload first")
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
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Attempting to update with malformed JSON")
				updateResp := updateWorkloadRaw(apiServer, workloadName, []byte(`{"image": "osv"`))
				defer updateResp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(updateResp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for malformed JSON")
			})
		})
	})

	Describe("GET /api/v1beta/workloads/{name}/logs - Get workload logs", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-logs-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when getting workload logs", func() {
			It("should return logs for running workload", func() {
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
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName && w.Status == runtime.WorkloadStatusRunning {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Getting workload logs")
				logsResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s/logs", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer logsResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(logsResp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying content type is text/plain")
				Expect(logsResp.Header.Get("Content-Type")).To(Equal("text/plain"))
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to get logs of non-existent workload")
				resp, err := apiServer.Get("/api/v1beta/workloads/non-existent-workload-12345/logs")
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("GET /api/v1beta/workloads/{name}/proxy-logs - Get proxy logs", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-proxy-logs-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when getting proxy logs", func() {
			It("should return 404 when workload has no proxy", func() {
				By("Creating a workload without proxy")
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

				By("Attempting to get proxy logs")
				logsResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s/proxy-logs", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer logsResp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(logsResp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when workload has no proxy logs")
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to get proxy logs of non-existent workload")
				resp, err := apiServer.Get("/api/v1beta/workloads/non-existent-workload-12345/proxy-logs")
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("GET /api/v1beta/workloads/{name}/export - Export workload", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = e2e.GenerateUniqueServerName("api-export-test")
		})

		AfterEach(func() {
			deleteWorkload(apiServer, workloadName)
		})

		Context("when exporting workload configuration", func() {
			It("should export workload as RunConfig JSON", func() {
				By("Creating a workload with environment variables")
				createReq := map[string]interface{}{
					"name":  workloadName,
					"image": "osv",
					"env": map[string]string{
						"TEST_VAR": "test-value",
					},
				}
				resp := createWorkload(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for workload to be running")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Exporting the workload")
				exportResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/workloads/%s/export", workloadName))
				Expect(err).ToNot(HaveOccurred())
				defer exportResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(exportResp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying response is valid JSON")
				var runConfig map[string]interface{}
				err = json.NewDecoder(exportResp.Body).Decode(&runConfig)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(runConfig).To(HaveKey("container_name"))
			})

			It("should return 404 for non-existent workload", func() {
				By("Attempting to export non-existent workload")
				resp, err := apiServer.Get("/api/v1beta/workloads/non-existent-workload-12345/export")
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("POST /api/v1beta/workloads/stop - Bulk stop workloads", func() {
		var workloadNames []string

		BeforeEach(func() {
			workloadNames = []string{
				e2e.GenerateUniqueServerName("bulk-stop-1"),
				e2e.GenerateUniqueServerName("bulk-stop-2"),
				e2e.GenerateUniqueServerName("bulk-stop-3"),
			}
		})

		AfterEach(func() {
			for _, name := range workloadNames {
				deleteWorkload(apiServer, name)
			}
		})

		Context("when stopping workloads in bulk by names", func() {
			It("should stop multiple workloads", func() {
				By("Creating multiple workloads")
				for _, name := range workloadNames {
					createReq := map[string]interface{}{
						"name":  name,
						"image": "osv",
					}
					resp := createWorkload(apiServer, createReq)
					resp.Body.Close()
					Expect(resp.StatusCode).To(Equal(http.StatusCreated))
				}

				By("Waiting for all workloads to be running")
				Eventually(func() int {
					runningCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name && w.Status == runtime.WorkloadStatusRunning {
								runningCount++
							}
						}
					}
					return runningCount
				}, 60*time.Second, 2*time.Second).Should(Equal(len(workloadNames)))

				By("Stopping all workloads in bulk")
				bulkReq := map[string]interface{}{
					"names": workloadNames,
				}
				stopResp := bulkStopWorkloads(apiServer, bulkReq)
				defer stopResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(stopResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Verifying all workloads are stopped")
				Eventually(func() int {
					stoppedCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name && w.Status == runtime.WorkloadStatusStopped {
								stoppedCount++
							}
						}
					}
					return stoppedCount
				}, 60*time.Second, 2*time.Second).Should(Equal(len(workloadNames)))
			})

			It("should reject empty names array", func() {
				By("Attempting bulk stop with empty names")
				bulkReq := map[string]interface{}{
					"names": []string{},
				}
				resp := bulkStopWorkloads(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject request with both names and group", func() {
				By("Attempting bulk stop with both names and group")
				bulkReq := map[string]interface{}{
					"names": []string{"workload1"},
					"group": "test-group",
				}
				resp := bulkStopWorkloads(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should reject requests specifying both names and group")
			})
		})
	})

	Describe("POST /api/v1beta/workloads/restart - Bulk restart workloads", func() {
		var workloadNames []string

		BeforeEach(func() {
			workloadNames = []string{
				e2e.GenerateUniqueServerName("bulk-restart-1"),
				e2e.GenerateUniqueServerName("bulk-restart-2"),
			}
		})

		AfterEach(func() {
			for _, name := range workloadNames {
				deleteWorkload(apiServer, name)
			}
		})

		Context("when restarting workloads in bulk", func() {
			It("should restart multiple workloads", func() {
				By("Creating multiple workloads")
				for _, name := range workloadNames {
					createReq := map[string]interface{}{
						"name":  name,
						"image": "osv",
					}
					resp := createWorkload(apiServer, createReq)
					resp.Body.Close()
					Expect(resp.StatusCode).To(Equal(http.StatusCreated))
				}

				By("Waiting for all workloads to be running")
				Eventually(func() int {
					runningCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name && w.Status == runtime.WorkloadStatusRunning {
								runningCount++
							}
						}
					}
					return runningCount
				}, 60*time.Second, 2*time.Second).Should(Equal(len(workloadNames)))

				By("Restarting all workloads in bulk")
				bulkReq := map[string]interface{}{
					"names": workloadNames,
				}
				restartResp := bulkRestartWorkloads(apiServer, bulkReq)
				defer restartResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(restartResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Verifying all workloads return to running state")
				Eventually(func() int {
					runningCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name && w.Status == runtime.WorkloadStatusRunning {
								runningCount++
							}
						}
					}
					return runningCount
				}, 60*time.Second, 2*time.Second).Should(Equal(len(workloadNames)))
			})

			It("should reject empty names array", func() {
				By("Attempting bulk restart with empty names")
				bulkReq := map[string]interface{}{
					"names": []string{},
				}
				resp := bulkRestartWorkloads(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})
	})

	Describe("POST /api/v1beta/workloads/delete - Bulk delete workloads", func() {
		var workloadNames []string

		BeforeEach(func() {
			workloadNames = []string{
				e2e.GenerateUniqueServerName("bulk-delete-1"),
				e2e.GenerateUniqueServerName("bulk-delete-2"),
			}
		})

		AfterEach(func() {
			for _, name := range workloadNames {
				deleteWorkload(apiServer, name)
			}
		})

		Context("when deleting workloads in bulk", func() {
			It("should delete multiple workloads", func() {
				By("Creating multiple workloads")
				for _, name := range workloadNames {
					createReq := map[string]interface{}{
						"name":  name,
						"image": "osv",
					}
					resp := createWorkload(apiServer, createReq)
					resp.Body.Close()
					Expect(resp.StatusCode).To(Equal(http.StatusCreated))
				}

				By("Waiting for all workloads to be running")
				Eventually(func() int {
					runningCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name && w.Status == runtime.WorkloadStatusRunning {
								runningCount++
							}
						}
					}
					return runningCount
				}, 60*time.Second, 2*time.Second).Should(Equal(len(workloadNames)))

				By("Deleting all workloads in bulk")
				bulkReq := map[string]interface{}{
					"names": workloadNames,
				}
				deleteResp := bulkDeleteWorkloads(apiServer, bulkReq)
				defer deleteResp.Body.Close()

				By("Verifying response status is 202 Accepted")
				Expect(deleteResp.StatusCode).To(Equal(http.StatusAccepted))

				By("Verifying all workloads are deleted")
				Eventually(func() int {
					foundCount := 0
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						for _, name := range workloadNames {
							if w.Name == name {
								foundCount++
							}
						}
					}
					return foundCount
				}, 60*time.Second, 2*time.Second).Should(Equal(0),
					"All workloads should be deleted")
			})

			It("should reject empty names array", func() {
				By("Attempting bulk delete with empty names")
				bulkReq := map[string]interface{}{
					"names": []string{},
				}
				resp := bulkDeleteWorkloads(apiServer, bulkReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject malformed JSON", func() {
				By("Attempting bulk delete with malformed JSON")
				resp := bulkDeleteWorkloadsRaw(apiServer, []byte(`{"names": ["test"`))
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})
	})
})

// Helper functions for workload lifecycle operations

func restartWorkload(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/workloads/"+name+"/restart", nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create restart request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send restart request")

	return resp
}

func updateWorkload(server *e2e.Server, name string, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal update request")

	return updateWorkloadRaw(server, name, reqBody)
}

func updateWorkloadRaw(server *e2e.Server, name string, body []byte) *http.Response {
	req, err := http.NewRequest(http.MethodPost,
		server.BaseURL()+"/api/v1beta/workloads/"+name+"/edit",
		bytes.NewReader(body))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func bulkStopWorkloads(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal bulk stop request")

	req, err := http.NewRequest(http.MethodPost,
		server.BaseURL()+"/api/v1beta/workloads/stop",
		bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func bulkRestartWorkloads(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal bulk restart request")

	req, err := http.NewRequest(http.MethodPost,
		server.BaseURL()+"/api/v1beta/workloads/restart",
		bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func bulkDeleteWorkloads(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal bulk delete request")

	return bulkDeleteWorkloadsRaw(server, reqBody)
}

func bulkDeleteWorkloadsRaw(server *e2e.Server, body []byte) *http.Response {
	req, err := http.NewRequest(http.MethodPost,
		server.BaseURL()+"/api/v1beta/workloads/delete",
		bytes.NewReader(body))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}
