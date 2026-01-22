// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Groups API", Label("api", "groups", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("POST /api/v1beta/groups - Create group", func() {
		var groupName string

		BeforeEach(func() {
			groupName = fmt.Sprintf("api-test-group-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			deleteGroup(apiServer, groupName)
		})

		Context("when creating a group", func() {
			It("should successfully create a group with valid name", func() {
				By("Creating a new group")
				createReq := map[string]interface{}{
					"name": groupName,
				}
				resp := createGroup(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"Should return 201 Created for successful group creation")

				By("Verifying response contains group name")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["name"]).To(Equal(groupName), "Response should contain group name")

				By("Verifying group appears in list")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue(),
					"Group should appear in list")
			})

			It("should reject duplicate group name with 409 Conflict", func() {
				By("Creating the first group")
				createReq := map[string]interface{}{
					"name": groupName,
				}
				resp := createGroup(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"First group should be created successfully")

				By("Verifying group exists")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Attempting to create duplicate group")
				resp2 := createGroup(apiServer, createReq)
				defer resp2.Body.Close()

				By("Verifying response status is 409 Conflict")
				Expect(resp2.StatusCode).To(Equal(http.StatusConflict),
					"Should return 409 Conflict for duplicate group name")
			})

			It("should reject invalid group name with 400 Bad Request", func() {
				By("Attempting to create group with invalid name")
				createReq := map[string]interface{}{
					"name": "invalid@group!name",
				}
				resp := createGroup(apiServer, createReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid group name")
			})

			It("should handle concurrent creation of same group gracefully", func() {
				By("Attempting to create the same group concurrently")
				var wg sync.WaitGroup
				responses := make([]*http.Response, 3)

				for i := 0; i < 3; i++ {
					wg.Add(1)
					go func(index int) {
						defer wg.Done()
						createReq := map[string]interface{}{
							"name": groupName,
						}
						responses[index] = createGroup(apiServer, createReq)
					}(i)
				}

				wg.Wait()

				By("Verifying only one creation succeeded")
				successCount := 0
				conflictCount := 0

				for _, resp := range responses {
					defer resp.Body.Close()
					switch resp.StatusCode {
					case http.StatusCreated:
						successCount++
					case http.StatusConflict:
						conflictCount++
					}
				}

				Expect(successCount).To(Equal(1),
					"Exactly one concurrent creation should succeed")
				Expect(conflictCount).To(Equal(2),
					"Other concurrent attempts should receive conflict status")

				By("Verifying group exists exactly once")
				Eventually(func() int {
					groupList := listGroups(apiServer)
					count := 0
					for _, g := range groupList {
						if g.Name == groupName {
							count++
						}
					}
					return count
				}, 10*time.Second, 1*time.Second).Should(Equal(1),
					"Group should exist exactly once")
			})
		})
	})

	Describe("GET /api/v1beta/groups - List groups", func() {
		Context("when listing groups", func() {
			It("should return list including default group", func() {
				By("Listing all groups")
				groupList := listGroups(apiServer)

				By("Verifying default group exists")
				found := false
				for _, g := range groupList {
					if g.Name == groups.DefaultGroup {
						found = true
						break
					}
				}
				Expect(found).To(BeTrue(), "Default group should always exist")
			})

			It("should list all created groups", func() {
				groupName1 := fmt.Sprintf("api-list-test-1-%d", time.Now().UnixNano())
				groupName2 := fmt.Sprintf("api-list-test-2-%d", time.Now().UnixNano())
				defer deleteGroup(apiServer, groupName1)
				defer deleteGroup(apiServer, groupName2)

				By("Creating two groups")
				createReq1 := map[string]interface{}{"name": groupName1}
				resp1 := createGroup(apiServer, createReq1)
				resp1.Body.Close()
				Expect(resp1.StatusCode).To(Equal(http.StatusCreated))

				createReq2 := map[string]interface{}{"name": groupName2}
				resp2 := createGroup(apiServer, createReq2)
				resp2.Body.Close()
				Expect(resp2.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying both groups appear in list")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					found1, found2 := false, false
					for _, g := range groupList {
						if g.Name == groupName1 {
							found1 = true
						}
						if g.Name == groupName2 {
							found2 = true
						}
					}
					return found1 && found2
				}, 10*time.Second, 1*time.Second).Should(BeTrue(),
					"Both created groups should appear in list")
			})
		})
	})

	Describe("GET /api/v1beta/groups/{name} - Get group details", func() {
		var groupName string

		BeforeEach(func() {
			groupName = fmt.Sprintf("api-get-test-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			deleteGroup(apiServer, groupName)
		})

		Context("when getting group details", func() {
			It("should return group details for existing group", func() {
				By("Creating a group")
				createReq := map[string]interface{}{"name": groupName}
				resp := createGroup(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Waiting for group to be created")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Getting group details")
				getResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/groups/%s", groupName))
				Expect(err).ToNot(HaveOccurred())
				defer getResp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(getResp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for existing group")

				By("Verifying response contains group information")
				var group groups.Group
				err = json.NewDecoder(getResp.Body).Decode(&group)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(group.Name).To(Equal(groupName), "Response should contain group name")
				Expect(group.RegisteredClients).ToNot(BeNil(), "Response should contain registered clients list")
			})

			It("should return 404 for non-existent group", func() {
				By("Attempting to get non-existent group")
				getResp, err := apiServer.Get("/api/v1beta/groups/non-existent-group-12345")
				Expect(err).ToNot(HaveOccurred())
				defer getResp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(getResp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent group")
			})
		})
	})

	Describe("DELETE /api/v1beta/groups/{name} - Delete group", func() {
		var groupName string

		BeforeEach(func() {
			groupName = fmt.Sprintf("api-delete-test-%d", time.Now().UnixNano())
		})

		Context("when deleting a group", func() {
			It("should successfully delete an empty group", func() {
				By("Creating a group")
				createReq := map[string]interface{}{"name": groupName}
				resp := createGroup(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying group exists")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Deleting the group")
				delResp := deleteGroup(apiServer, groupName)
				defer delResp.Body.Close()

				By("Verifying response status is 204 No Content")
				Expect(delResp.StatusCode).To(Equal(http.StatusNoContent),
					"Should return 204 for successful deletion")

				By("Verifying group is removed from list")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeFalse(),
					"Group should not appear in list after deletion")
			})

			It("should delete group with workloads when with-workloads=true", func() {
				workloadName := e2e.GenerateUniqueServerName("api-group-workload")

				By("Creating a group")
				createReq := map[string]interface{}{"name": groupName}
				resp := createGroup(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

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
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Deleting the group with workloads")
				delResp := deleteGroupWithWorkloads(apiServer, groupName, true)
				defer delResp.Body.Close()

				By("Verifying response status is 204 No Content")
				Expect(delResp.StatusCode).To(Equal(http.StatusNoContent))

				By("Verifying group is removed")
				Eventually(func() bool {
					groupList := listGroups(apiServer)
					for _, g := range groupList {
						if g.Name == groupName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeFalse())

				By("Verifying workload is also deleted")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeFalse(),
					"Workload should be deleted with group")
			})

			It("should move workloads to default group when deleting group without with-workloads flag", func() {
				workloadName := e2e.GenerateUniqueServerName("api-group-workload-move")

				By("Creating a group")
				createReq := map[string]interface{}{"name": groupName}
				resp := createGroup(apiServer, createReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

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
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 60*time.Second, 2*time.Second).Should(BeTrue())

				By("Deleting the group without with-workloads flag")
				delResp := deleteGroupWithWorkloads(apiServer, groupName, false)
				defer delResp.Body.Close()

				By("Verifying response status is 204 No Content")
				Expect(delResp.StatusCode).To(Equal(http.StatusNoContent))

				By("Verifying workload still exists")
				Eventually(func() bool {
					workloads := listWorkloads(apiServer, true)
					for _, w := range workloads {
						if w.Name == workloadName {
							return true
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue(),
					"Workload should still exist after group deletion")

				By("Cleaning up workload")
				deleteWorkload(apiServer, workloadName)
			})

			It("should return 404 when deleting non-existent group", func() {
				By("Attempting to delete non-existent group")
				delResp := deleteGroup(apiServer, "non-existent-group-12345")
				defer delResp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(delResp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 for non-existent group")
			})
		})
	})
})

// Helper functions for group operations

func createGroup(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal create group request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/groups", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func listGroups(server *e2e.Server) []*groups.Group {
	resp, err := server.Get("/api/v1beta/groups")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list groups")
	defer resp.Body.Close()

	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK), "List groups should return 200")

	var result struct {
		Groups []*groups.Group `json:"groups"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to decode group list")

	return result.Groups
}

func deleteGroup(server *e2e.Server, name string) *http.Response {
	req, err := http.NewRequest(http.MethodDelete, server.BaseURL()+"/api/v1beta/groups/"+name, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create delete request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send delete request")

	return resp
}

func deleteGroupWithWorkloads(server *e2e.Server, name string, withWorkloads bool) *http.Response {
	url := fmt.Sprintf("%s/api/v1beta/groups/%s", server.BaseURL(), name)
	if withWorkloads {
		url += "?with-workloads=true"
	}

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create delete request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send delete request")

	return resp
}
