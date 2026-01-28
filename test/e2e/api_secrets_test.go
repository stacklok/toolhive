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

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Secrets API", Label("api", "secrets", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("POST /api/v1beta/secrets - Setup secrets provider", func() {
		Context("when configuring a provider", func() {
			It("should successfully setup encrypted provider", func() {
				By("Setting up encrypted provider")
				setupReq := map[string]interface{}{
					"provider_type": "encrypted",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"Should return 201 Created for successful setup")

				By("Verifying response contains provider details")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["provider_type"]).To(Equal("encrypted"))
				Expect(result["message"]).ToNot(BeEmpty())
			})

			It("should successfully setup environment provider", func() {
				By("Setting up environment provider")
				setupReq := map[string]interface{}{
					"provider_type": "environment",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response contains provider details")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result["provider_type"]).To(Equal("environment"))
			})

			It("should reject empty provider type", func() {
				By("Attempting to setup with empty provider type")
				setupReq := map[string]interface{}{
					"provider_type": "",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for empty provider type")
			})

			It("should reject invalid provider type", func() {
				By("Attempting to setup with invalid provider type")
				setupReq := map[string]interface{}{
					"provider_type": "invalid-provider-12345",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for invalid provider type")
			})

			It("should reject malformed JSON", func() {
				By("Attempting to setup with malformed JSON")
				reqBody := []byte(`{"provider_type": "encrypted"`)
				req, err := http.NewRequest(http.MethodPost, apiServer.BaseURL()+"/api/v1beta/secrets", bytes.NewReader(reqBody))
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

		Context("when reconfiguring a provider", func() {
			It("should allow reconfiguration with same provider type", func() {
				By("Setting up initial encrypted provider")
				setupReq := map[string]interface{}{
					"provider_type": "encrypted",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				// Wait a moment to ensure setup completes
				time.Sleep(100 * time.Millisecond)

				By("Reconfiguring with same provider type")
				resp2 := setupSecretsProvider(apiServer, setupReq)
				defer resp2.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp2.StatusCode).To(Equal(http.StatusCreated),
					"Should allow reconfiguration")

				By("Verifying message indicates reconfiguration")
				var result map[string]interface{}
				err := json.NewDecoder(resp2.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result["message"]).To(ContainSubstring("reconfigured"),
					"Message should indicate reconfiguration")
			})
		})
	})

	Describe("GET /api/v1beta/secrets/default - Get secrets provider", func() {
		Context("when provider is configured", func() {
			BeforeEach(func() {
				By("Setting up environment provider")
				setupReq := map[string]interface{}{
					"provider_type": "environment",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				// Wait to ensure setup completes
				time.Sleep(100 * time.Millisecond)
			})

			It("should return provider details", func() {
				By("Getting provider details")
				resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for configured provider")

				By("Verifying response contains provider information")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["name"]).To(Equal("default"))
				Expect(result["provider_type"]).To(Equal("environment"))
				Expect(result["capabilities"]).ToNot(BeNil())
			})

			It("should include capabilities", func() {
				By("Getting provider details")
				resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying capabilities structure")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())

				capabilities := result["capabilities"].(map[string]interface{})
				Expect(capabilities).To(HaveKey("can_read"))
				Expect(capabilities).To(HaveKey("can_write"))
				Expect(capabilities).To(HaveKey("can_delete"))
				Expect(capabilities).To(HaveKey("can_list"))
				Expect(capabilities).To(HaveKey("can_cleanup"))
			})
		})

		Context("when provider is not configured", func() {
			It("should return 404 Not Found", func() {
				By("Attempting to get provider without configuration")
				resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})
		})
	})

	Describe("GET /api/v1beta/secrets/default/keys - List secrets", func() {
		Context("when provider is configured and supports listing", func() {
			BeforeEach(func() {
				By("Setting up environment provider")
				setupReq := map[string]interface{}{
					"provider_type": "environment",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				time.Sleep(100 * time.Millisecond)
			})

			It("should return list of secrets", func() {
				By("Listing secrets")
				resp := listSecrets(apiServer, "default")
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"Should return 200 for list operation")

				By("Verifying response structure")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result).To(HaveKey("keys"))
			})
		})
	})

	Describe("POST /api/v1beta/secrets/default/keys - Create secret", func() {
		Context("when provider is configured and supports writing", func() {
			BeforeEach(func() {
				By("Setting up encrypted provider")
				setupReq := map[string]interface{}{
					"provider_type": "encrypted",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				time.Sleep(100 * time.Millisecond)
			})

			It("should successfully create a secret", func() {
				secretKey := fmt.Sprintf("test-key-%d", time.Now().UnixNano())

				By("Creating a new secret")
				createReq := map[string]interface{}{
					"key":   secretKey,
					"value": "test-value",
				}
				resp := createSecret(apiServer, "default", createReq)
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated),
					"Should return 201 for successful creation")

				By("Verifying response contains secret key")
				var result map[string]interface{}
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON")
				Expect(result["key"]).To(Equal(secretKey))
				Expect(result["message"]).ToNot(BeEmpty())
			})

			It("should reject request with missing key", func() {
				By("Attempting to create secret without key")
				createReq := map[string]interface{}{
					"value": "test-value",
				}
				resp := createSecret(apiServer, "default", createReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for missing key")
			})

			It("should reject request with missing value", func() {
				By("Attempting to create secret without value")
				createReq := map[string]interface{}{
					"key": "test-key",
				}
				resp := createSecret(apiServer, "default", createReq)
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
					"Should return 400 for missing value")
			})
		})
	})

	Describe("Operations without provider setup", func() {
		Context("when no provider is configured", func() {
			It("should return 404 for get provider operation", func() {
				By("Attempting to get provider without setup")
				resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})

			It("should return 404 for list operation", func() {
				By("Attempting to list secrets without setup")
				resp := listSecrets(apiServer, "default")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})

			It("should return 404 for create operation", func() {
				By("Attempting to create secret without setup")
				createReq := map[string]interface{}{
					"key":   "test-key",
					"value": "test-value",
				}
				resp := createSecret(apiServer, "default", createReq)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})

			It("should return 404 for update operation", func() {
				By("Attempting to update secret without setup")
				updateReq := map[string]interface{}{
					"value": "new-value",
				}
				resp := updateSecret(apiServer, "default", "test-key", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})

			It("should return 404 for delete operation", func() {
				By("Attempting to delete secret without setup")
				resp := deleteSecret(apiServer, "default", "test-key")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
					"Should return 404 when provider not configured")
			})
		})
	})

	Describe("Read-only provider operations", func() {
		Context("when provider does not support write operations", func() {
			BeforeEach(func() {
				By("Setting up environment provider (read-only)")
				setupReq := map[string]interface{}{
					"provider_type": "environment",
				}
				resp := setupSecretsProvider(apiServer, setupReq)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				time.Sleep(100 * time.Millisecond)
			})

			It("should return 405 for create operation", func() {
				By("Attempting to create secret on read-only provider")
				createReq := map[string]interface{}{
					"key":   "test-key",
					"value": "test-value",
				}
				resp := createSecret(apiServer, "default", createReq)
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"Should return 405 for unsupported create operation")
			})

			It("should return 405 for update operation", func() {
				By("Attempting to update secret on read-only provider")
				updateReq := map[string]interface{}{
					"value": "new-value",
				}
				resp := updateSecret(apiServer, "default", "test-key", updateReq)
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"Should return 405 for unsupported update operation")
			})

			It("should return 405 for delete operation", func() {
				By("Attempting to delete secret on read-only provider")
				resp := deleteSecret(apiServer, "default", "test-key")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"Should return 405 for unsupported delete operation")
			})
		})
	})
})

// Helper functions for secrets API operations

func setupSecretsProvider(server *e2e.Server, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal setup request")

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v1beta/secrets", bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func getSecretsProvider(server *e2e.Server) *http.Response {
	resp, err := server.Get("/api/v1beta/secrets/default")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to get secrets provider")

	return resp
}

func listSecrets(server *e2e.Server, providerName string) *http.Response {
	url := fmt.Sprintf("/api/v1beta/secrets/%s/keys", providerName)
	resp, err := server.Get(url)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list secrets")

	return resp
}

func createSecret(server *e2e.Server, providerName string, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal create request")

	url := fmt.Sprintf("%s/api/v1beta/secrets/%s/keys", server.BaseURL(), providerName)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func updateSecret(server *e2e.Server, providerName, key string, request map[string]interface{}) *http.Response {
	reqBody, err := json.Marshal(request)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to marshal update request")

	url := fmt.Sprintf("%s/api/v1beta/secrets/%s/keys/%s", server.BaseURL(), providerName, key)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(reqBody))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create HTTP request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send HTTP request")

	return resp
}

func deleteSecret(server *e2e.Server, providerName, key string) *http.Response {
	url := fmt.Sprintf("%s/api/v1beta/secrets/%s/keys/%s", server.BaseURL(), providerName, key)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to create delete request")

	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to send delete request")

	return resp
}
