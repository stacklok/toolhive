package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

const (
	// defaultSecretsProviderName is the name of the default secrets provider
	defaultSecretsProviderName = "default"
)

// SecretsRoutes defines the routes for the secrets API.
type SecretsRoutes struct{}

// SecretsRouter creates a new router for the secrets API.
func SecretsRouter() http.Handler {
	routes := SecretsRoutes{}

	r := chi.NewRouter()

	// Setup secrets provider
	r.Post("/", routes.setupSecretsProvider)

	// Default provider routes
	r.Route("/default", func(r chi.Router) {
		r.Get("/", routes.getSecretsProvider)
		r.Route("/keys", func(r chi.Router) {
			r.Get("/", routes.listSecrets)
			r.Post("/", routes.createSecret)
			r.Put("/{key}", routes.updateSecret)
			r.Delete("/{key}", routes.deleteSecret)
		})
	})

	return r
}

// nolint:gocyclo //TODO refactor this method to use common Secrets management functions
// setupSecretsProvider
//
//	@Summary		Setup or reconfigure secrets provider
//	@Description	Setup the secrets provider with the specified type and configuration.
//	Can be used to initially configure or reconfigure an existing provider.
//	@Tags			secrets
//	@Accept			json
//	@Produce		json
//	@Param			request	body		setupSecretsRequest	true	"Setup secrets provider request"
//	@Success		201		{object}	setupSecretsResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets [post]
func (*SecretsRoutes) setupSecretsProvider(w http.ResponseWriter, r *http.Request) {
	var req setupSecretsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate provider type
	var providerType secrets.ProviderType
	switch req.ProviderType {
	case string(secrets.EncryptedType):
		providerType = secrets.EncryptedType
	case string(secrets.OnePasswordType):
		providerType = secrets.OnePasswordType
	case string(secrets.NoneType):
		providerType = secrets.NoneType
	case "":
		http.Error(w, "Provider type cannot be empty", http.StatusBadRequest)
		return
	default:
		http.Error(w, fmt.Sprintf("Invalid secrets provider type: %s (valid types: %s, %s, %s)",
			req.ProviderType, string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType)),
			http.StatusBadRequest)
		return
	}

	// Check current secrets provider configuration for appropriate messaging
	cfg := config.GetConfig()
	isReconfiguration := false
	isInitialSetup := !cfg.Secrets.SetupCompleted
	if cfg.Secrets.SetupCompleted {
		currentProviderType, err := cfg.Secrets.GetProviderType()
		if err != nil {
			logger.Errorf("Failed to get current provider type: %v", err)
			http.Error(w, "Failed to get current provider configuration", http.StatusInternalServerError)
			return
		}

		// TODO Handle provider reconfiguration in a better way
		if currentProviderType == providerType {
			isReconfiguration = true
			logger.Infof("Reconfiguring existing %s secrets provider", providerType)
		} else {
			isReconfiguration = true // Changing provider type is also considered reconfiguration
			logger.Warnf("Changing secrets provider from %s to %s", currentProviderType, providerType)
		}
	}

	// Determine password to use - only for encrypted provider during initial setup or reconfiguration
	// TODO Temporary hack to allow API users to not have to use a password
	var passwordToUse string
	if providerType == secrets.EncryptedType && (isInitialSetup || isReconfiguration) {
		if req.Password != "" {
			// Use provided password
			passwordToUse = req.Password
			logger.Infof("Using provided password for encrypted provider setup")
		} else {
			// Generate a secure random password
			generatedPassword, err := secrets.GenerateSecurePassword()
			if err != nil {
				logger.Errorf("Failed to generate secure password: %v", err)
				http.Error(w, "Failed to generate secure password", http.StatusInternalServerError)
				return
			}
			passwordToUse = generatedPassword
			logger.Infof("Generated secure random password for encrypted provider setup")
		}
	}

	// TODO Validation, creation, config updates etc should all happen in a common cli/api place, needs refactor
	// Validate that the provider can be created and works correctly
	// Use the password from the request for encrypted provider validation and setup
	ctx := context.Background()
	result := secrets.ValidateProviderWithPassword(ctx, providerType, passwordToUse)
	if !result.Success {
		logger.Errorf("Provider validation failed: %v", result.Error)
		if errors.Is(result.Error, secrets.ErrKeyringNotAvailable) {
			http.Error(w, result.Error.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf("Provider validation failed: %v", result.Error), http.StatusInternalServerError)
		return
	}

	// For encrypted provider during initial setup or reconfiguration, ensure we create the provider
	// at least once to save password in keyring
	if providerType == secrets.EncryptedType && (isInitialSetup || isReconfiguration) {
		_, err := secrets.CreateSecretProviderWithPassword(providerType, passwordToUse)
		if err != nil {
			logger.Errorf("Failed to initialize encrypted provider: %v", err)
			http.Error(w, fmt.Sprintf("Failed to initialize encrypted provider: %v", err), http.StatusInternalServerError)
			return
		}
		logger.Info("Encrypted provider initialized and password saved to keyring")
	}

	// Update the secrets provider type and mark setup as completed
	err := config.UpdateConfig(func(c *config.Config) {
		c.Secrets.ProviderType = string(providerType)
		c.Secrets.SetupCompleted = true
	})
	if err != nil {
		logger.Errorf("Failed to update configuration: %v", err)
		http.Error(w, fmt.Sprintf("Failed to update configuration: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	var message string
	if isReconfiguration {
		message = "Secrets provider reconfigured successfully"
	} else {
		message = "Secrets provider setup successfully"
	}

	resp := setupSecretsResponse{
		ProviderType: string(providerType),
		Message:      message,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// getSecretsProvider
//
//	@Summary		Get secrets provider details
//	@Description	Get details of the default secrets provider
//	@Tags			secrets
//	@Produce		json
//	@Success		200	{object}	getSecretsProviderResponse
//	@Failure		404	{string}	string	"Not Found - Provider not setup"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets/default [get]
func (s *SecretsRoutes) getSecretsProvider(w http.ResponseWriter, _ *http.Request) {
	cfg := config.GetConfig()

	// Check if secrets provider is setup
	if !cfg.Secrets.SetupCompleted {
		http.Error(w, "Secrets provider not setup", http.StatusNotFound)
		return
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		logger.Errorf("Failed to get provider type: %v", err)
		http.Error(w, "Failed to get provider type", http.StatusInternalServerError)
		return
	}

	// Get provider capabilities
	provider, err := s.getSecretsManager()
	if err != nil {
		logger.Errorf("Failed to create secrets provider: %v", err)
		http.Error(w, "Failed to access secrets provider", http.StatusInternalServerError)
		return
	}

	capabilities := provider.Capabilities()

	w.Header().Set("Content-Type", "application/json")
	resp := getSecretsProviderResponse{
		Name:         defaultSecretsProviderName,
		ProviderType: string(providerType),
		Capabilities: providerCapabilitiesResponse{
			CanRead:    capabilities.CanRead,
			CanWrite:   capabilities.CanWrite,
			CanDelete:  capabilities.CanDelete,
			CanList:    capabilities.CanList,
			CanCleanup: capabilities.CanCleanup,
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// listSecrets
//
//	@Summary		List secrets
//	@Description	Get a list of all secret keys from the default provider
//	@Tags			secrets
//	@Produce		json
//	@Success		200	{object}	listSecretsResponse
//	@Failure		404	{string}	string	"Not Found - Provider not setup"
//	@Failure		405	{string}	string	"Method Not Allowed - Provider doesn't support listing"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets/default/keys [get]
func (s *SecretsRoutes) listSecrets(w http.ResponseWriter, r *http.Request) {
	provider, err := s.getSecretsManager()
	if err != nil {
		if errors.Is(err, secrets.ErrSecretsNotSetup) {
			http.Error(w, "Secrets provider not setup", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get secrets manager: %v", err)
		http.Error(w, "Failed to access secrets provider", http.StatusInternalServerError)
		return
	}

	// Check if provider supports listing
	if !provider.Capabilities().CanList {
		http.Error(w, "Secrets provider does not support listing keys", http.StatusMethodNotAllowed)
		return
	}

	secretDescriptions, err := provider.ListSecrets(r.Context())
	if err != nil {
		logger.Errorf("Failed to list secrets: %v", err)
		http.Error(w, "Failed to list secrets", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := listSecretsResponse{
		Keys: make([]secretKeyResponse, len(secretDescriptions)),
	}
	for i, desc := range secretDescriptions {
		resp.Keys[i] = secretKeyResponse{
			Key:         desc.Key,
			Description: desc.Description,
		}
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// createSecret
//
//	@Summary		Create a new secret
//	@Description	Create a new secret in the default provider (encrypted provider only)
//	@Tags			secrets
//	@Accept			json
//	@Produce		json
//	@Param			request	body		createSecretRequest	true	"Create secret request"
//	@Success		201		{object}	createSecretResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found - Provider not setup"
//	@Failure		405		{string}	string	"Method Not Allowed - Provider doesn't support writing"
//	@Failure		409		{string}	string	"Conflict - Secret already exists"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets/default/keys [post]
func (s *SecretsRoutes) createSecret(w http.ResponseWriter, r *http.Request) {
	var req createSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Key == "" || req.Value == "" {
		http.Error(w, "Both 'key' and 'value' are required", http.StatusBadRequest)
		return
	}

	provider, err := s.getSecretsManager()
	if err != nil {
		if errors.Is(err, secrets.ErrSecretsNotSetup) {
			http.Error(w, "Secrets provider not setup", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get secrets manager: %v", err)
		http.Error(w, "Failed to access secrets provider", http.StatusInternalServerError)
		return
	}

	// Check if provider supports writing
	if !provider.Capabilities().CanWrite {
		http.Error(w, "Secrets provider does not support creating secrets", http.StatusMethodNotAllowed)
		return
	}

	// Check if secret already exists (if provider supports reading)
	if provider.Capabilities().CanRead {
		_, err := provider.GetSecret(r.Context(), req.Key)
		if err == nil {
			http.Error(w, "Secret already exists", http.StatusConflict)
			return
		}
	}

	// Create the secret
	if err := provider.SetSecret(r.Context(), req.Key, req.Value); err != nil {
		logger.Errorf("Failed to create secret: %v", err)
		http.Error(w, "Failed to create secret", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	resp := createSecretResponse{
		Key:     req.Key,
		Message: "Secret created successfully",
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// updateSecret
//
//	@Summary		Update a secret
//	@Description	Update an existing secret in the default provider (encrypted provider only)
//	@Tags			secrets
//	@Accept			json
//	@Produce		json
//	@Param			key		path		string				true	"Secret key"
//	@Param			request	body		updateSecretRequest	true	"Update secret request"
//	@Success		200		{object}	updateSecretResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found - Provider not setup or secret not found"
//	@Failure		405		{string}	string	"Method Not Allowed - Provider doesn't support writing"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets/default/keys/{key} [put]
func (s *SecretsRoutes) updateSecret(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		http.Error(w, "Secret key is required", http.StatusBadRequest)
		return
	}

	var req updateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Value == "" {
		http.Error(w, "Value is required", http.StatusBadRequest)
		return
	}

	provider, err := s.getSecretsManager()
	if err != nil {
		if errors.Is(err, secrets.ErrSecretsNotSetup) {
			http.Error(w, "Secrets provider not setup", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get secrets manager: %v", err)
		http.Error(w, "Failed to access secrets provider", http.StatusInternalServerError)
		return
	}

	// Check if provider supports writing
	if !provider.Capabilities().CanWrite {
		http.Error(w, "Secrets provider does not support updating secrets", http.StatusMethodNotAllowed)
		return
	}

	// Check if secret exists (if provider supports reading)
	if provider.Capabilities().CanRead {
		_, err := provider.GetSecret(r.Context(), key)
		if err != nil {
			http.Error(w, "Secret not found", http.StatusNotFound)
			return
		}
	}

	// Update the secret
	if err := provider.SetSecret(r.Context(), key, req.Value); err != nil {
		logger.Errorf("Failed to update secret: %v", err)
		http.Error(w, "Failed to update secret", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := updateSecretResponse{
		Key:     key,
		Message: "Secret updated successfully",
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// deleteSecret
//
//	@Summary		Delete a secret
//	@Description	Delete a secret from the default provider (encrypted provider only)
//	@Tags			secrets
//	@Param			key	path		string	true	"Secret key"
//	@Success		204	{string}	string	"No Content"
//	@Failure		404	{string}	string	"Not Found - Provider not setup or secret not found"
//	@Failure		405	{string}	string	"Method Not Allowed - Provider doesn't support deletion"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/secrets/default/keys/{key} [delete]
func (s *SecretsRoutes) deleteSecret(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		http.Error(w, "Secret key is required", http.StatusBadRequest)
		return
	}

	provider, err := s.getSecretsManager()
	if err != nil {
		if errors.Is(err, secrets.ErrSecretsNotSetup) {
			http.Error(w, "Secrets provider not setup", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get secrets manager: %v", err)
		http.Error(w, "Failed to access secrets provider", http.StatusInternalServerError)
		return
	}

	// Check if provider supports deletion
	if !provider.Capabilities().CanDelete {
		http.Error(w, "Secrets provider does not support deleting secrets", http.StatusMethodNotAllowed)
		return
	}

	// Delete the secret
	if err := provider.DeleteSecret(r.Context(), key); err != nil {
		logger.Errorf("Failed to delete secret: %v", err)
		// Check if it's a "not found" error
		if err.Error() == "cannot delete non-existent secret: "+key {
			http.Error(w, "Secret not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to delete secret", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// getSecretsManager is a helper function to get the secrets manager
func (*SecretsRoutes) getSecretsManager() (secrets.Provider, error) {
	cfg := config.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, err
	}

	return secrets.CreateSecretProvider(providerType)
}

// Request and response type definitions

// setupSecretsRequest represents the request for initializing a secrets provider
//
//	@Description	Request to setup a secrets provider
type setupSecretsRequest struct {
	// Type of the secrets provider (encrypted, 1password, none)
	ProviderType string `json:"provider_type"`
	// Password for encrypted provider (optional, can be set via environment variable)
	// TODO Review environment variable for this
	Password string `json:"password,omitempty"`
}

// setupSecretsResponse represents the response for initializing a secrets provider
//
//	@Description	Response after initializing a secrets provider
type setupSecretsResponse struct {
	// Type of the secrets provider that was setup
	ProviderType string `json:"provider_type"`
	// Success message
	Message string `json:"message"`
}

// getSecretsProviderResponse represents the response for getting secrets provider details
//
//	@Description	Response containing secrets provider details
type getSecretsProviderResponse struct {
	// Name of the secrets provider
	Name string `json:"name"`
	// Type of the secrets provider
	ProviderType string `json:"provider_type"`
	// Capabilities of the secrets provider
	Capabilities providerCapabilitiesResponse `json:"capabilities"`
}

// providerCapabilitiesResponse represents the capabilities of a secrets provider
//
//	@Description	Capabilities of a secrets provider
type providerCapabilitiesResponse struct {
	// Whether the provider can read secrets
	CanRead bool `json:"can_read"`
	// Whether the provider can write secrets
	CanWrite bool `json:"can_write"`
	// Whether the provider can delete secrets
	CanDelete bool `json:"can_delete"`
	// Whether the provider can list secrets
	CanList bool `json:"can_list"`
	// Whether the provider can cleanup all secrets
	CanCleanup bool `json:"can_cleanup"`
}

// listSecretsResponse represents the response for listing secrets
//
//	@Description	Response containing a list of secret keys
type listSecretsResponse struct {
	// List of secret keys
	Keys []secretKeyResponse `json:"keys"`
}

// secretKeyResponse represents a secret key with optional description
//
//	@Description	Secret key information
type secretKeyResponse struct {
	// Secret key name
	Key string `json:"key"`
	// Optional description of the secret
	Description string `json:"description,omitempty"`
}

// createSecretRequest represents the request for creating a secret
//
//	@Description	Request to create a new secret
type createSecretRequest struct {
	// Secret key name
	Key string `json:"key"`
	// Secret value
	Value string `json:"value"`
}

// createSecretResponse represents the response for creating a secret
//
//	@Description	Response after creating a secret
type createSecretResponse struct {
	// Secret key that was created
	Key string `json:"key"`
	// Success message
	Message string `json:"message"`
}

// updateSecretRequest represents the request for updating a secret
//
//	@Description	Request to update an existing secret
type updateSecretRequest struct {
	// New secret value
	Value string `json:"value"`
}

// updateSecretResponse represents the response for updating a secret
//
//	@Description	Response after updating a secret
type updateSecretResponse struct {
	// Secret key that was updated
	Key string `json:"key"`
	// Success message
	Message string `json:"message"`
}
