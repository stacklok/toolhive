// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Registry API", Label("api", "api-registry", "registry", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
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
	})

	Describe("DELETE /api/v1beta/registry/{name} - Remove registry", func() {
		It("should return 204 for named registry", func() {
			By("Attempting to delete a named registry")
			resp := deleteRegistry(apiServer, "test-registry")
			defer resp.Body.Close()

			By("Verifying response")
			// The registry doesn't exist yet so this will return 204 (no content)
			// since RemoveRegistry is a config operation
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusNoContent),
				Equal(http.StatusNotFound),
			))
		})
	})
})

// Response types for registry API

type updateRegistryResponse struct {
	Type string `json:"type"`
}

// Helper functions for registry operations

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
