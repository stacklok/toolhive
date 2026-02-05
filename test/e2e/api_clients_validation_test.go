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
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Clients API Validation", Label("api", "clients", "validation", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("Invalid client type validation", func() {
		It("should return 400 Bad Request for unsupported client type", func() {
			workloadName := e2e.GenerateUniqueServerName("validation-workload")
			// Note: Workload cleanup handled by suite-level CLI cleanup

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
			}, 60*time.Second, 2*time.Second).Should(BeTrue())

			By("Attempting to register with an invalid client type")
			invalidClientName := fmt.Sprintf("invalid-client-%d", time.Now().UnixNano())
			registerReq := map[string]interface{}{
				"name":   invalidClientName,
				"groups": []string{groups.DefaultGroup},
			}
			reqBody, err := json.Marshal(registerReq)
			Expect(err).ToNot(HaveOccurred())

			req, err := http.NewRequest(http.MethodPost, apiServer.BaseURL()+"/api/v1beta/clients", bytes.NewReader(reqBody))
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"Should return 400 Bad Request for unsupported client type, not 500")

			var responseBody bytes.Buffer
			_, err = responseBody.ReadFrom(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(responseBody.String()).To(ContainSubstring("unsupported client type"),
				"Error message should mention unsupported client type")
		})

		It("should return 400 Bad Request for bulk registration with invalid client type", func() {
			workloadName := e2e.GenerateUniqueServerName("bulk-validation-workload")
			groupName := fmt.Sprintf("bulk-validation-group-%d", time.Now().UnixNano())
			// Note: Workload cleanup handled by suite-level CLI cleanup
			defer deleteGroup(apiServer, groupName)

			By("Creating a test group")
			createGroupReq := map[string]interface{}{
				"name": groupName,
			}
			groupResp := createGroup(apiServer, createGroupReq)
			groupResp.Body.Close()
			Expect(groupResp.StatusCode).To(Equal(http.StatusCreated))

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

			By("Attempting bulk register with invalid client types")
			invalidClientName1 := fmt.Sprintf("invalid-bulk-1-%d", time.Now().UnixNano())
			invalidClientName2 := fmt.Sprintf("invalid-bulk-2-%d", time.Now().UnixNano())
			bulkReq := map[string]interface{}{
				"names":  []string{invalidClientName1, invalidClientName2},
				"groups": []string{groupName},
			}
			reqBody, err := json.Marshal(bulkReq)
			Expect(err).ToNot(HaveOccurred())

			req, err := http.NewRequest(http.MethodPost, apiServer.BaseURL()+"/api/v1beta/clients/register", bytes.NewReader(reqBody))
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"Should return 400 Bad Request for unsupported client types in bulk operation")
		})
	})
})
