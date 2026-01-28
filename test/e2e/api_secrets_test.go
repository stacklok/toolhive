// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// Response structures matching pkg/api/v1/secrets.go

type setupSecretsRequest struct {
	ProviderType string `json:"provider_type"`
	Password     string `json:"password,omitempty"`
}

type setupSecretsResponse struct {
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
}

type getSecretsProviderResponse struct {
	Name         string                       `json:"name"`
	ProviderType string                       `json:"provider_type"`
	Capabilities providerCapabilitiesResponse `json:"capabilities"`
}

type providerCapabilitiesResponse struct {
	CanRead    bool `json:"can_read"`
	CanWrite   bool `json:"can_write"`
	CanDelete  bool `json:"can_delete"`
	CanList    bool `json:"can_list"`
	CanCleanup bool `json:"can_cleanup"`
}

type listSecretsResponse struct {
	Keys []secretKeyResponse `json:"keys"`
}

type secretKeyResponse struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
}

type createSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type createSecretResponse struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

type updateSecretRequest struct {
	Value string `json:"value"`
}

type updateSecretResponse struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Helper functions

func setupSecretsProvider(server *e2e.Server, providerType, password string) *http.Response {
	reqBody := setupSecretsRequest{
		ProviderType: providerType,
	}
	if password != "" {
		reqBody.Password = password
	}

	jsonData, err := json.Marshal(reqBody)
	Expect(err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/secrets",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

func getSecretsProvider(server *e2e.Server) (*getSecretsProviderResponse, *http.Response) {
	resp, err := server.Get("/api/v1beta/secrets/default")
	Expect(err).ToNot(HaveOccurred())

	if resp.StatusCode == http.StatusOK {
		var result getSecretsProviderResponse
		err := json.NewDecoder(resp.Body).Decode(&result)
		Expect(err).ToNot(HaveOccurred())
		return &result, resp
	}

	return nil, resp
}

func listSecrets(server *e2e.Server) ([]secretKeyResponse, *http.Response) {
	resp, err := server.Get("/api/v1beta/secrets/default/keys")
	Expect(err).ToNot(HaveOccurred())

	if resp.StatusCode == http.StatusOK {
		var result listSecretsResponse
		err := json.NewDecoder(resp.Body).Decode(&result)
		Expect(err).ToNot(HaveOccurred())
		return result.Keys, resp
	}

	return nil, resp
}

func createSecret(server *e2e.Server, key, value string) *http.Response {
	reqBody := createSecretRequest{
		Key:   key,
		Value: value,
	}

	jsonData, err := json.Marshal(reqBody)
	Expect(err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/secrets/default/keys",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

func updateSecret(server *e2e.Server, key, value string) *http.Response {
	reqBody := updateSecretRequest{
		Value: value,
	}

	jsonData, err := json.Marshal(reqBody)
	Expect(err).ToNot(HaveOccurred())

	client := &http.Client{}
	req, err := http.NewRequest(
		"PUT",
		server.BaseURL()+"/api/v1beta/secrets/default/keys/"+url.PathEscape(key),
		bytes.NewBuffer(jsonData),
	)
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

func deleteSecret(server *e2e.Server, key string) *http.Response {
	client := &http.Client{}
	req, err := http.NewRequest(
		"DELETE",
		server.BaseURL()+"/api/v1beta/secrets/default/keys/"+url.PathEscape(key),
		nil,
	)
	Expect(err).ToNot(HaveOccurred())

	resp, err := client.Do(req)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

// Test suite

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
		Context("when setting up encrypted provider", func() {
			It("should setup successfully without password", func() {
				By("Setting up encrypted provider without password")
				resp := setupSecretsProvider(apiServer, "encrypted", "")
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result setupSecretsResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ProviderType).To(Equal("encrypted"))
				Expect(result.Message).To(ContainSubstring("setup successfully"))
			})

			It("should setup successfully with custom password", func() {
				By("Setting up encrypted provider with custom password")
				resp := setupSecretsProvider(apiServer, "encrypted", "custom-test-password-123")
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result setupSecretsResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ProviderType).To(Equal("encrypted"))
			})
		})

		Context("when setting up 1password provider", func() {
			It("should setup successfully", func() {
				By("Setting up 1password provider")
				resp := setupSecretsProvider(apiServer, "1password", "")
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result setupSecretsResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ProviderType).To(Equal("1password"))
			})
		})

		Context("when setting up environment provider", func() {
			It("should setup successfully", func() {
				By("Setting up environment provider")
				resp := setupSecretsProvider(apiServer, "environment", "")
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result setupSecretsResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ProviderType).To(Equal("environment"))
			})
		})

		Context("when providing invalid input", func() {
			It("should reject empty provider type", func() {
				By("Attempting to setup with empty provider type")
				resp := setupSecretsProvider(apiServer, "", "")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject invalid provider type", func() {
				By("Attempting to setup with invalid provider type")
				resp := setupSecretsProvider(apiServer, "invalid-provider-type", "")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject malformed JSON", func() {
				By("Sending malformed JSON")
				resp, err := http.Post(
					apiServer.BaseURL()+"/api/v1beta/secrets",
					"application/json",
					bytes.NewBufferString(`{"invalid json`),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when reconfiguring provider", func() {
			It("should allow reconfiguring to different provider type", func() {
				By("Setting up initial encrypted provider")
				resp := setupSecretsProvider(apiServer, "encrypted", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Reconfiguring to environment provider")
				resp = setupSecretsProvider(apiServer, "environment", "")
				defer resp.Body.Close()

				By("Verifying reconfiguration was successful")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying provider was changed")
				provider, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				Expect(provider.ProviderType).To(Equal("environment"))
			})
		})
	})

	Describe("GET /api/v1beta/secrets/default - Get secrets provider", func() {
		Context("when provider is configured", func() {
			It("should return provider details for encrypted provider", func() {
				By("Setting up encrypted provider")
				resp := setupSecretsProvider(apiServer, "encrypted", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Getting provider details")
				provider, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying provider information")
				Expect(provider.Name).To(Equal("default"))
				Expect(provider.ProviderType).To(Equal("encrypted"))

				By("Verifying encrypted provider capabilities")
				Expect(provider.Capabilities.CanRead).To(BeTrue())
				Expect(provider.Capabilities.CanWrite).To(BeTrue())
				Expect(provider.Capabilities.CanDelete).To(BeTrue())
				Expect(provider.Capabilities.CanList).To(BeTrue())
				Expect(provider.Capabilities.CanCleanup).To(BeTrue())
			})

			It("should return correct capabilities for 1password provider", func() {
				By("Setting up 1password provider")
				resp := setupSecretsProvider(apiServer, "1password", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Getting provider details")
				provider, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying 1password provider capabilities")
				Expect(provider.ProviderType).To(Equal("1password"))
				Expect(provider.Capabilities.CanRead).To(BeTrue())
				Expect(provider.Capabilities.CanWrite).To(BeFalse())
				Expect(provider.Capabilities.CanDelete).To(BeFalse())
				Expect(provider.Capabilities.CanList).To(BeTrue())
				Expect(provider.Capabilities.CanCleanup).To(BeFalse())
			})

			It("should return correct capabilities for environment provider", func() {
				By("Setting up environment provider")
				resp := setupSecretsProvider(apiServer, "environment", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Getting provider details")
				provider, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying environment provider capabilities")
				Expect(provider.ProviderType).To(Equal("environment"))
				Expect(provider.Capabilities.CanRead).To(BeTrue())
				Expect(provider.Capabilities.CanWrite).To(BeFalse())
				Expect(provider.Capabilities.CanDelete).To(BeFalse())
				Expect(provider.Capabilities.CanList).To(BeFalse())
				Expect(provider.Capabilities.CanCleanup).To(BeFalse())
			})
		})

		Context("when provider is not configured", func() {
			It("should return 404 Not Found", func() {
				By("Attempting to get provider without setup")
				_, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("Complete Secrets Lifecycle with Encrypted Provider", Ordered, func() {
		const testSecretKey = "test-api-key"
		const testSecretValue = "test-value-abc123"
		const updatedSecretValue = "updated-value-xyz789"

		BeforeAll(func() {
			By("Setting up encrypted secrets provider")
			resp := setupSecretsProvider(apiServer, "encrypted", "test-password")
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		})

		Describe("GET /api/v1beta/secrets/default/keys - List secrets", func() {
			It("should initially return empty list", func() {
				By("Listing secrets")
				secrets, resp := listSecrets(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying empty list")
				Expect(secrets).To(BeEmpty())
			})
		})

		Describe("POST /api/v1beta/secrets/default/keys - Create secret", func() {
			It("should create a secret successfully", func() {
				By("Creating a new secret")
				resp := createSecret(apiServer, testSecretKey, testSecretValue)
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result createSecretResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Key).To(Equal(testSecretKey))
				Expect(result.Message).To(ContainSubstring("created successfully"))
			})

			It("should reject creating secret with empty key", func() {
				By("Attempting to create secret with empty key")
				resp := createSecret(apiServer, "", "some-value")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject creating secret with empty value", func() {
				By("Attempting to create secret with empty value")
				resp := createSecret(apiServer, "some-key", "")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject duplicate secret creation", func() {
				By("Attempting to create duplicate secret")
				resp := createSecret(apiServer, testSecretKey, "another-value")
				defer resp.Body.Close()

				By("Verifying response status is 409 Conflict")
				Expect(resp.StatusCode).To(Equal(http.StatusConflict))
			})

			It("should reject malformed JSON", func() {
				By("Sending malformed JSON")
				resp, err := http.Post(
					apiServer.BaseURL()+"/api/v1beta/secrets/default/keys",
					"application/json",
					bytes.NewBufferString(`{"invalid`),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Describe("GET /api/v1beta/secrets/default/keys - List secrets after creation", func() {
			It("should list the created secret", func() {
				By("Listing secrets")
				secrets, resp := listSecrets(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying secret appears in list")
				Expect(secrets).ToNot(BeEmpty())
				found := false
				for _, s := range secrets {
					if s.Key == testSecretKey {
						found = true
						break
					}
				}
				Expect(found).To(BeTrue(), "Created secret should appear in list")
			})
		})

		Describe("PUT /api/v1beta/secrets/default/keys/{key} - Update secret", func() {
			It("should update existing secret", func() {
				By("Updating the secret")
				resp := updateSecret(apiServer, testSecretKey, updatedSecretValue)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying response body")
				var result updateSecretResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Key).To(Equal(testSecretKey))
				Expect(result.Message).To(ContainSubstring("updated successfully"))
			})

			It("should reject updating with empty value", func() {
				By("Attempting to update with empty value")
				resp := updateSecret(apiServer, testSecretKey, "")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should return 404 for non-existent secret", func() {
				By("Attempting to update non-existent secret")
				resp := updateSecret(apiServer, "non-existent-key-12345", "some-value")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})

			It("should reject malformed JSON", func() {
				By("Sending malformed JSON")
				client := &http.Client{}
				req, err := http.NewRequest(
					"PUT",
					apiServer.BaseURL()+"/api/v1beta/secrets/default/keys/"+testSecretKey,
					bytes.NewBufferString(`{"invalid`),
				)
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Describe("Special characters in secret keys", func() {
			It("should handle keys with slashes", func() {
				By("Creating secret with slashes in key")
				specialKey := "api/tokens/github"
				resp := createSecret(apiServer, specialKey, "special-value")
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Updating secret with special key")
				resp = updateSecret(apiServer, specialKey, "updated-special-value")
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Deleting secret with special key")
				resp = deleteSecret(apiServer, specialKey)
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			})

			It("should handle keys with spaces", func() {
				By("Creating secret with spaces in key")
				specialKey := "my secret key"
				resp := createSecret(apiServer, specialKey, "value-with-spaces")
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Deleting secret with spaces")
				resp = deleteSecret(apiServer, specialKey)
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			})

			It("should handle keys with special URL characters", func() {
				By("Creating secret with special characters")
				specialKey := "key?with&special=chars"
				resp := createSecret(apiServer, specialKey, "special-char-value")
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Deleting secret with special characters")
				resp = deleteSecret(apiServer, specialKey)
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			})
		})

		Describe("DELETE /api/v1beta/secrets/default/keys/{key} - Delete secret", func() {
			It("should delete existing secret", func() {
				By("Deleting the secret")
				resp := deleteSecret(apiServer, testSecretKey)
				defer resp.Body.Close()

				By("Verifying response status is 204 No Content")
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			})

			It("should return 404 for non-existent secret", func() {
				By("Attempting to delete non-existent secret")
				resp := deleteSecret(apiServer, "non-existent-secret-99999")
				defer resp.Body.Close()

				By("Verifying response status is 404 Not Found")
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})

			It("should not find deleted secret in list", func() {
				By("Listing secrets after deletion")
				secrets, resp := listSecrets(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying deleted secret is not in list")
				found := false
				for _, s := range secrets {
					if s.Key == testSecretKey {
						found = true
						break
					}
				}
				Expect(found).To(BeFalse(), "Deleted secret should not appear in list")
			})
		})
	})

	Describe("Read-only Provider Operations", func() {
		Context("with environment provider", func() {
			BeforeEach(func() {
				By("Setting up environment provider")
				resp := setupSecretsProvider(apiServer, "environment", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			})

			It("should reject listing secrets (not supported)", func() {
				By("Attempting to list secrets")
				_, resp := listSecrets(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject creating secrets", func() {
				By("Attempting to create secret")
				resp := createSecret(apiServer, "test-key", "test-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject updating secrets", func() {
				By("Attempting to update secret")
				resp := updateSecret(apiServer, "test-key", "new-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject deleting secrets", func() {
				By("Attempting to delete secret")
				resp := deleteSecret(apiServer, "test-key")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})
		})

		Context("with 1password provider", func() {
			BeforeEach(func() {
				By("Setting up 1password provider")
				resp := setupSecretsProvider(apiServer, "1password", "")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			})

			It("should reject creating secrets", func() {
				By("Attempting to create secret")
				resp := createSecret(apiServer, "test-key", "test-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject updating secrets", func() {
				By("Attempting to update secret")
				resp := updateSecret(apiServer, "test-key", "new-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject deleting secrets", func() {
				By("Attempting to delete secret")
				resp := deleteSecret(apiServer, "test-key")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})
		})
	})

	Describe("Operations without provider setup", func() {
		It("should return 404 for list operation", func() {
			By("Attempting to list secrets without setup")
			_, resp := listSecrets(apiServer)
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 404 for create operation", func() {
			By("Attempting to create secret without setup")
			resp := createSecret(apiServer, "test-key", "test-value")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 404 for update operation", func() {
			By("Attempting to update secret without setup")
			resp := updateSecret(apiServer, "test-key", "test-value")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 404 for delete operation", func() {
			By("Attempting to delete secret without setup")
			resp := deleteSecret(apiServer, "test-key")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	Describe("Multiple secrets management", func() {
		const numSecrets = 5

		BeforeEach(func() {
			By("Setting up encrypted provider")
			resp := setupSecretsProvider(apiServer, "encrypted", "")
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		})

		AfterEach(func() {
			By("Cleaning up all test secrets")
			secrets, resp := listSecrets(apiServer)
			if resp != nil {
				resp.Body.Close()
			}
			for _, secret := range secrets {
				resp := deleteSecret(apiServer, secret.Key)
				resp.Body.Close()
			}
		})

		It("should create and manage multiple secrets", func() {
			By(fmt.Sprintf("Creating %d secrets", numSecrets))
			for i := 0; i < numSecrets; i++ {
				key := fmt.Sprintf("test-secret-%d", i)
				value := fmt.Sprintf("test-value-%d", i)
				resp := createSecret(apiServer, key, value)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			}

			By("Listing all secrets")
			secrets, resp := listSecrets(apiServer)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(secrets).To(HaveLen(numSecrets))

			By("Updating all secrets")
			for i := 0; i < numSecrets; i++ {
				key := fmt.Sprintf("test-secret-%d", i)
				value := fmt.Sprintf("updated-value-%d", i)
				resp := updateSecret(apiServer, key, value)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			}

			By("Deleting all secrets")
			for i := 0; i < numSecrets; i++ {
				key := fmt.Sprintf("test-secret-%d", i)
				resp := deleteSecret(apiServer, key)
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			}

			By("Verifying all secrets are deleted")
			secrets, resp = listSecrets(apiServer)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(secrets).To(BeEmpty())
		})
	})
})
