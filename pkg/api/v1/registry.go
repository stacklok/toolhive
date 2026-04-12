// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// RegistryAuthRequiredCode is the machine-readable error code returned in the
// structured JSON 503 response when registry authentication is missing.
// Desktop clients (Studio) match on this value to display the correct UI.
const RegistryAuthRequiredCode = "registry_auth_required"

// registryErrorResponse is the JSON body for structured HTTP 503 error responses.
type registryErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeRegistryAuthRequiredError writes a structured JSON 503 response.
func writeRegistryAuthRequiredError(w http.ResponseWriter) {
	body := registryErrorResponse{
		Code:    RegistryAuthRequiredCode,
		Message: "Registry authentication required. POST to /api/v1beta/registry/auth/login to authenticate.",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(body)
}

// isRegistryAuthError checks if an error is a registry auth required error.
func isRegistryAuthError(err error) bool {
	return errors.Is(err, auth.ErrRegistryAuthRequired)
}

// newSecretsProvider creates a secrets provider from the given config provider.
func newSecretsProvider(configProvider config.Provider) (secrets.Provider, error) {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("getting secrets provider type: %w", err)
	}
	return secrets.CreateSecretProvider(providerType)
}

// registryAuthLogin handles POST /registry/auth/login.
//
//	@Summary		Registry login
//	@Description	Trigger an interactive OAuth flow to authenticate with the configured registry. Only available in serve mode.
//	@Tags			registry
//	@Produce		json
//	@Success		200	{object}	map[string]string	"Authenticated successfully"
//	@Failure		400	{string}	string				"Bad Request - Registry OAuth not configured"
//	@Failure		500	{string}	string				"Internal Server Error"
//	@Router			/api/v1beta/registry/auth/login [post]
func (rr *RegistryRoutes) registryAuthLogin(w http.ResponseWriter, r *http.Request) {
	secretsProvider, err := newSecretsProvider(rr.configProvider)
	if err != nil {
		slog.Error("failed to create secrets provider", "error", err)
		http.Error(w, "Failed to create secrets provider", http.StatusInternalServerError)
		return
	}

	if err := auth.Login(r.Context(), rr.configProvider, secretsProvider, auth.LoginOptions{}); err != nil {
		if isRegistryAuthError(err) {
			http.Error(w, "Registry OAuth not configured; call PUT /api/v1beta/registry/default with a client ID and "+
				"issuer URL first", http.StatusBadRequest)
			return
		}
		slog.Error("registry login failed", "error", err)
		http.Error(w, "Login failed", http.StatusInternalServerError)
		return
	}

	regpkg.ResetDefaultStore()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "authenticated"})
}

// registryAuthLogout handles POST /registry/auth/logout.
//
//	@Summary		Registry logout
//	@Description	Clear cached OAuth tokens for the configured registry. Only available in serve mode.
//	@Tags			registry
//	@Produce		json
//	@Success		200	{object}	map[string]string	"Logged out successfully"
//	@Failure		400	{string}	string				"Bad Request - Registry OAuth not configured"
//	@Failure		500	{string}	string				"Internal Server Error"
//	@Router			/api/v1beta/registry/auth/logout [post]
func (rr *RegistryRoutes) registryAuthLogout(w http.ResponseWriter, r *http.Request) {
	secretsProvider, err := newSecretsProvider(rr.configProvider)
	if err != nil {
		slog.Error("failed to create secrets provider", "error", err)
		http.Error(w, "Failed to create secrets provider", http.StatusInternalServerError)
		return
	}

	if err := auth.Logout(r.Context(), rr.configProvider, secretsProvider); err != nil {
		if isRegistryAuthError(err) {
			http.Error(w, "Registry OAuth not configured; call PUT /api/v1beta/registry/default with a client ID and "+
				"issuer URL first", http.StatusBadRequest)
			return
		}
		slog.Error("registry logout failed", "error", err)
		http.Error(w, "Logout failed", http.StatusInternalServerError)
		return
	}

	regpkg.ResetDefaultStore()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
}

// connectivityError represents a registry connectivity/timeout error
type connectivityError struct {
	URL string
	Err error
}

func (e *connectivityError) Error() string {
	return fmt.Sprintf("registry at %s is unreachable: %v", e.URL, e.Err)
}

func (e *connectivityError) Unwrap() error {
	return e.Err
}

// isConnectivityError checks if an error is related to connectivity/timeout
func isConnectivityError(err error) bool {
	if err == nil {
		return false
	}
	var regErr *config.RegistryError
	if errors.As(err, &regErr) {
		return errors.Is(regErr.Err, config.ErrRegistryTimeout) ||
			errors.Is(regErr.Err, config.ErrRegistryUnreachable)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// isValidationError checks if an error is related to validation failure
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	var regErr *config.RegistryError
	if errors.As(err, &regErr) {
		return errors.Is(regErr.Err, config.ErrRegistryValidationFailed)
	}
	return false
}

// RegistryRoutes defines the routes for the registry API.
type RegistryRoutes struct {
	configProvider config.Provider
	configService  regpkg.Configurator
	serveMode      bool
}

// NewRegistryRoutes creates a new RegistryRoutes with the default config provider
func NewRegistryRoutes() *RegistryRoutes {
	p := config.NewProvider()
	return &RegistryRoutes{
		configProvider: p,
		configService:  regpkg.NewConfiguratorWithProvider(p),
	}
}

// NewRegistryRoutesWithProvider creates a new RegistryRoutes with a custom config provider
func NewRegistryRoutesWithProvider(provider config.Provider) *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: provider,
		configService:  regpkg.NewConfiguratorWithProvider(provider),
	}
}

// NewRegistryRoutesForServe creates RegistryRoutes configured for serve mode.
func NewRegistryRoutesForServe() *RegistryRoutes {
	p := config.NewProvider()
	return &RegistryRoutes{
		configProvider: p,
		configService:  regpkg.NewConfiguratorWithProvider(p),
		serveMode:      true,
	}
}

// RegistryRouter creates a new router for the registry API.
// Old data-serving routes (GET /, GET /{name}, GET /{name}/servers, etc.)
// have been removed; those are now served by the registry v0.1 proxy.
// Only config management (PUT/DELETE) and auth routes remain.
func RegistryRouter(serveMode bool) http.Handler {
	routes := func() *RegistryRoutes {
		if serveMode {
			return NewRegistryRoutesForServe()
		}
		return NewRegistryRoutes()
	}()

	r := chi.NewRouter()
	r.Put("/{name}", routes.updateRegistry)
	r.Delete("/{name}", routes.removeRegistry)

	if serveMode {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", routes.registryAuthLogin)
			r.Post("/logout", routes.registryAuthLogout)
		})
	}

	return r
}

//	 updateRegistry
//
//		@Summary		Update registry configuration
//		@Description	Update registry URL or local path for a registry
//		@Tags			registry
//		@Accept			json
//		@Produce		json
//		@Param			name	path		string					true	"Registry name"
//		@Param			body	body		UpdateRegistryRequest	true	"Registry configuration"
//		@Success		200		{object}	UpdateRegistryResponse
//		@Failure		400		{string}	string	"Bad Request"
//		@Failure		403		{string}	string	"Forbidden - blocked by policy"
//		@Failure		404		{string}	string	"Not Found"
//		@Failure		502		{string}	string	"Bad Gateway - Registry validation failed"
//		@Failure		504		{string}	string	"Gateway Timeout - Registry unreachable"
//		@Router			/api/v1beta/registry/{name} [put]
func (rr *RegistryRoutes) updateRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Only "default" registry can be updated currently
	if name != "default" {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	var req UpdateRegistryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := validateRegistryRequest(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := regpkg.ActivePolicyGate().CheckUpdateRegistry(r.Context(), updateRegistryConfigFromRequest(&req)); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	var responseType string
	registryType, err := rr.processRegistryUpdate(&req)
	if err != nil {
		var connErr *connectivityError
		if errors.As(err, &connErr) {
			http.Error(w, connErr.Error(), http.StatusGatewayTimeout)
			return
		}
		if isValidationError(err) {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	responseType = registryType

	// Always overwrite auth: if auth is provided, set it; if not, clear it.
	if req.Auth != nil {
		if err := rr.processAuthUpdate(r.Context(), req.Auth); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		authMgr := regpkg.NewAuthManager(rr.configProvider)
		if err := authMgr.UnsetAuth(); err != nil {
			slog.Error("failed to clear registry auth", "error", err)
			http.Error(w, "Failed to clear registry auth", http.StatusInternalServerError)
			return
		}
	}

	regpkg.ResetDefaultStore()

	if responseType == "" {
		currentType, _ := rr.configService.GetRegistryInfo()
		responseType = currentType
	}

	response := UpdateRegistryResponse{
		Type: responseType,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// validateRegistryRequest validates that only one registry type is specified
func validateRegistryRequest(req *UpdateRegistryRequest) error {
	if (req.URL != nil && req.APIURL != nil) ||
		(req.URL != nil && req.LocalPath != nil) ||
		(req.APIURL != nil && req.LocalPath != nil) {
		return fmt.Errorf("cannot specify more than one registry type (url, api_url, or local_path)")
	}
	return nil
}

// updateRegistryConfigFromRequest builds an UpdateRegistryConfig from the
// parsed API request for policy evaluation.
func updateRegistryConfigFromRequest(req *UpdateRegistryRequest) *regpkg.UpdateRegistryConfig {
	cfg := &regpkg.UpdateRegistryConfig{
		HasAuth: req.Auth != nil,
	}
	if req.URL != nil {
		cfg.URL = *req.URL
	}
	if req.APIURL != nil {
		cfg.APIURL = *req.APIURL
	}
	if req.LocalPath != nil {
		cfg.LocalPath = *req.LocalPath
	}
	if req.AllowPrivateIP != nil {
		cfg.AllowPrivateIP = *req.AllowPrivateIP
	}
	return cfg
}

// processAuthUpdate validates and applies OAuth configuration for registry auth.
func (rr *RegistryRoutes) processAuthUpdate(ctx context.Context, authReq *UpdateRegistryAuthRequest) error {
	if authReq.Issuer == "" || authReq.ClientID == "" {
		return fmt.Errorf("auth.issuer and auth.client_id are required")
	}
	authMgr := regpkg.NewAuthManager(rr.configProvider)
	if err := authMgr.SetOAuthAuth(ctx, authReq.Issuer, authReq.ClientID, authReq.Audience, authReq.Scopes); err != nil {
		return fmt.Errorf("failed to configure registry auth: %w", err)
	}
	return nil
}

// processRegistryUpdate processes the registry update based on request type
func (rr *RegistryRoutes) processRegistryUpdate(req *UpdateRegistryRequest) (string, error) {
	// Handle registry reset (unset)
	if req.URL == nil && req.APIURL == nil && req.LocalPath == nil {
		err := rr.configService.UnsetRegistry()
		if err != nil {
			slog.Error("failed to unset registry", "error", err)
			return "", fmt.Errorf("failed to reset registry configuration")
		}
		return "default", nil
	}

	// Determine which input to pass
	var input string
	var allowPrivateIP bool

	if req.URL != nil {
		input = *req.URL
		allowPrivateIP = req.AllowPrivateIP != nil && *req.AllowPrivateIP
	} else if req.APIURL != nil {
		input = *req.APIURL
		allowPrivateIP = req.AllowPrivateIP != nil && *req.AllowPrivateIP
	} else if req.LocalPath != nil {
		input = *req.LocalPath
	}

	registryType, err := rr.configService.SetRegistryFromInput(input, allowPrivateIP)
	if err != nil {
		slog.Error("failed to set registry", "error", err)
		if isConnectivityError(err) {
			return "", &connectivityError{
				URL: input,
				Err: err,
			}
		}
		return "", err
	}

	return registryType, nil
}

//	 removeRegistry
//
//		@Summary		Remove a registry
//		@Description	Remove a specific registry configuration
//		@Tags			registry
//		@Produce		json
//		@Param			name	path		string	true	"Registry name"
//		@Success		204	{string}	string	"No Content"
//		@Failure		403	{string}	string	"Forbidden - blocked by policy"
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name} [delete]
func (rr *RegistryRoutes) removeRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if err := regpkg.ActivePolicyGate().CheckDeleteRegistry(r.Context(), &regpkg.DeleteRegistryConfig{
		Name: name,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Remove the named registry from config
	if err := rr.configProvider.RemoveRegistry(name); err != nil {
		http.Error(w, fmt.Sprintf("Failed to remove registry: %v", err), http.StatusInternalServerError)
		return
	}

	regpkg.ResetDefaultStore()
	w.WriteHeader(http.StatusNoContent)
}

// Request/Response type definitions.

// UpdateRegistryRequest represents the request for updating a registry
//
//	@Description	Request containing registry configuration updates
type UpdateRegistryRequest struct {
	URL            *string                    `json:"url,omitempty"`
	APIURL         *string                    `json:"api_url,omitempty"`
	LocalPath      *string                    `json:"local_path,omitempty"`
	AllowPrivateIP *bool                      `json:"allow_private_ip,omitempty"`
	Auth           *UpdateRegistryAuthRequest `json:"auth,omitempty"`
}

// UpdateRegistryAuthRequest contains OAuth configuration fields for registry auth.
type UpdateRegistryAuthRequest struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Audience string   `json:"audience,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// UpdateRegistryResponse represents the response for updating a registry
//
//	@Description	Response containing update result
type UpdateRegistryResponse struct {
	Type string `json:"type"`
}
