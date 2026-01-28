// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
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
	"gopkg.in/yaml.v3"

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

type createSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type updateSecretRequest struct {
	Value string `json:"value"`
}

// Helper functions

func setupSecretsProvider(server *e2e.Server, providerType string) *http.Response {
	reqBody := setupSecretsRequest{
		ProviderType: providerType,
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

func listSecrets(server *e2e.Server) *http.Response {
	resp, err := server.Get("/api/v1beta/secrets/default/keys")
	Expect(err).ToNot(HaveOccurred())
	return resp
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
		server.BaseURL()+"/api/v1beta/secrets/default/keys/"+key,
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
		server.BaseURL()+"/api/v1beta/secrets/default/keys/"+key,
		nil,
	)
	Expect(err).ToNot(HaveOccurred())

	resp, err := client.Do(req)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

func cleanupSecretsConfig() {
	// Reset secrets configuration by updating the config file directly
	// This ensures subsequent tests start with a clean slate
	configDir := os.Getenv("TOOLHIVE_E2E_SHARED_CONFIG")
	if configDir == "" {
		// If not using shared config, use standard config location
		return
	}

	// Path to the config file
	configPath := filepath.Join(configDir, "toolhive", "config.yaml")

	// Read the current config
	data, err := os.ReadFile(configPath)
	if err != nil {
		// If config doesn't exist, nothing to clean up
		if os.IsNotExist(err) {
			return
		}
		Expect(err).ToNot(HaveOccurred())
	}

	// Parse and update the config
	var configData map[string]interface{}
	err = yaml.Unmarshal(data, &configData)
	if err != nil {
		// If config is malformed, just remove it
		_ = os.Remove(configPath)
		return
	}

	// Reset secrets configuration
	if secrets, ok := configData["secrets"].(map[string]interface{}); ok {
		secrets["setup_completed"] = false
		secrets["provider_type"] = ""
	} else {
		// If secrets section doesn't exist, create it
		configData["secrets"] = map[string]interface{}{
			"setup_completed": false,
			"provider_type":   "",
		}
	}

	// Write the updated config back
	updatedData, err := yaml.Marshal(configData)
	Expect(err).ToNot(HaveOccurred())

	err = os.WriteFile(configPath, updatedData, 0600)
	Expect(err).ToNot(HaveOccurred())
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

		// Register cleanup to run after the server stops
		// DeferCleanup runs in reverse order, so this runs after server.Stop()
		DeferCleanup(func() {
			// Clean up secrets configuration to ensure test isolation
			// This is necessary because tests share a config directory
			By("Cleaning up secrets configuration")
			cleanupSecretsConfig()
		})
	})

	Describe("POST /api/v1beta/secrets - Setup secrets provider", func() {
		Context("when setting up environment provider", func() {
			It("should setup successfully", func() {
				By("Setting up environment provider")
				resp := setupSecretsProvider(apiServer, "environment")
				defer resp.Body.Close()

				By("Verifying response status is 201 Created")
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Verifying response body")
				var result setupSecretsResponse
				err := json.NewDecoder(resp.Body).Decode(&result)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ProviderType).To(Equal("environment"))
				Expect(result.Message).To(ContainSubstring("setup successfully"))
			})
		})

		Context("when providing invalid input", func() {
			It("should reject empty provider type", func() {
				By("Attempting to setup with empty provider type")
				resp := setupSecretsProvider(apiServer, "")
				defer resp.Body.Close()

				By("Verifying response status is 400 Bad Request")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject invalid provider type", func() {
				By("Attempting to setup with invalid provider type")
				resp := setupSecretsProvider(apiServer, "invalid-provider-type")
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
	})

	Describe("GET /api/v1beta/secrets/default - Get secrets provider", func() {
		Context("when provider is configured", func() {
			It("should return correct capabilities for environment provider", func() {
				By("Setting up environment provider")
				resp := setupSecretsProvider(apiServer, "environment")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				By("Getting provider details")
				provider, resp := getSecretsProvider(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 200 OK")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying provider information")
				Expect(provider.Name).To(Equal("default"))
				Expect(provider.ProviderType).To(Equal("environment"))

				By("Verifying environment provider capabilities")
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

	Describe("Environment Provider Read-Only Operations", func() {
		BeforeEach(func() {
			By("Setting up environment provider")
			resp := setupSecretsProvider(apiServer, "environment")
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		})

		Describe("GET /api/v1beta/secrets/default/keys - List secrets", func() {
			It("should reject listing (not supported by environment provider)", func() {
				By("Attempting to list secrets")
				resp := listSecrets(apiServer)
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})
		})

		Describe("POST /api/v1beta/secrets/default/keys - Create secret", func() {
			It("should reject creating secrets", func() {
				By("Attempting to create secret")
				resp := createSecret(apiServer, "test-key", "test-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject creating secret with empty key", func() {
				By("Attempting to create secret with empty key")
				resp := createSecret(apiServer, "", "some-value")
				defer resp.Body.Close()

				By("Verifying response status is 400")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should reject creating secret with empty value", func() {
				By("Attempting to create secret with empty value")
				resp := createSecret(apiServer, "some-key", "")
				defer resp.Body.Close()

				By("Verifying response status is 400")
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
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

		Describe("PUT /api/v1beta/secrets/default/keys/{key} - Update secret", func() {
			It("should reject updating secrets", func() {
				By("Attempting to update secret")
				resp := updateSecret(apiServer, "test-key", "new-value")
				defer resp.Body.Close()

				By("Verifying response status is 405 Method Not Allowed")
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			})

			It("should reject updating with empty value", func() {
				By("Attempting to update with empty value")
				resp := updateSecret(apiServer, "test-key", "")
				defer resp.Body.Close()

				By("Verifying response status is 400 or 405")
				Expect(resp.StatusCode).To(SatisfyAny(
					Equal(http.StatusBadRequest),
					Equal(http.StatusMethodNotAllowed),
				))
			})

			It("should reject malformed JSON", func() {
				By("Sending malformed JSON")
				client := &http.Client{}
				req, err := http.NewRequest(
					"PUT",
					apiServer.BaseURL()+"/api/v1beta/secrets/default/keys/test-key",
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

		Describe("DELETE /api/v1beta/secrets/default/keys/{key} - Delete secret", func() {
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
		It("should return 404 for get provider operation", func() {
			By("Attempting to get provider without setup")
			_, resp := getSecretsProvider(apiServer)
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 404 for list operation", func() {
			By("Attempting to list secrets without setup")
			resp := listSecrets(apiServer)
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
})
