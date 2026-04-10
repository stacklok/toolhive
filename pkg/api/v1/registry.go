// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	registry "github.com/stacklok/toolhive-core/registry/types"
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
// The "code" field allows clients (e.g. Studio) to distinguish between
// "registry_auth_required" and "registry_unavailable" conditions.
type registryErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeRegistryAuthRequiredError writes a structured JSON 503 response.
// HTTP 503 is correct: the incoming client (Studio) is authenticated to the thv serve API,
// but thv serve itself lacks a valid registry credential. This is a server-side dependency
// issue, not a client auth failure (which would be 401).
func writeRegistryAuthRequiredError(w http.ResponseWriter) {
	body := registryErrorResponse{
		Code:    RegistryAuthRequiredCode,
		Message: "Registry authentication required. POST to /api/v1beta/registry/auth/login to authenticate.",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(body)
}

// RegistryUnavailableCode is the machine-readable error code returned in the
// structured JSON 503 response when the upstream registry is unreachable.
const RegistryUnavailableCode = "registry_unavailable"

// writeRegistryUnavailableError writes a structured JSON 503 response when the
// upstream registry cannot be reached or returns an unexpected error (e.g. 404).
func writeRegistryUnavailableError(w http.ResponseWriter, unavailableErr *regpkg.UnavailableError) {
	body := registryErrorResponse{
		Code:    RegistryUnavailableCode,
		Message: unavailableErr.Error(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(body)
}

// resolveAuthStatus returns the auth_status and auth_type strings for API responses
// by delegating to the AuthManager.
func (rr *RegistryRoutes) resolveAuthStatus() (authStatus, authType string) {
	authMgr := regpkg.NewAuthManager(rr.configProvider)
	return authMgr.GetAuthStatus()
}

// resolveAuthConfig returns the non-secret OAuth configuration for API responses,
// or nil if no OAuth auth is configured.
func (rr *RegistryRoutes) resolveAuthConfig() *regpkg.OAuthPublicConfig {
	authMgr := regpkg.NewAuthManager(rr.configProvider)
	return authMgr.GetOAuthPublicConfig()
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
// It triggers an interactive OAuth flow that opens the user's browser.
// This endpoint is only available in serve mode and is designed for desktop
// clients (e.g. Studio) where the user has a local browser. Headless or
// remote deployments should pre-configure credentials via the CLI instead.
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

	// Reset the singleton provider so subsequent registry calls pick up the new token.
	regpkg.ResetDefaultProvider()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "authenticated"})
}

// registryAuthLogout handles POST /registry/auth/logout.
// It clears cached OAuth tokens for the configured registry.
// This endpoint is only available in serve mode.
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

	// Reset the singleton provider so subsequent registry calls reflect the logged-out state.
	regpkg.ResetDefaultProvider()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
}

const (
	// defaultRegistryName is the name of the default registry
	defaultRegistryName = "default"
)

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

	// Check if this is a RegistryError with timeout or unreachable errors
	var regErr *config.RegistryError
	if errors.As(err, &regErr) {
		return errors.Is(regErr.Err, config.ErrRegistryTimeout) ||
			errors.Is(regErr.Err, config.ErrRegistryUnreachable)
	}

	// Check for context deadline exceeded (timeout) - direct check for legacy support
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

	// Check if this is a RegistryError with validation failure
	var regErr *config.RegistryError
	if errors.As(err, &regErr) {
		return errors.Is(regErr.Err, config.ErrRegistryValidationFailed)
	}

	return false
}

// RegistryType represents the type of registry
type RegistryType string

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile RegistryType = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL RegistryType = "url"
	// RegistryTypeAPI represents an MCP Registry API endpoint
	RegistryTypeAPI RegistryType = "api"
	// RegistryTypeDefault represents a built-in registry
	RegistryTypeDefault RegistryType = "default"
)

// getRegistryInfo returns the registry type and the source
func (rr *RegistryRoutes) getRegistryInfo() (RegistryType, string) {
	registryType, source := rr.configService.GetRegistryInfo()
	return RegistryType(registryType), source
}

// getCurrentProvider returns the current registry provider using the injected config.
// In serve mode, the provider is created with non-interactive auth to prevent
// browser-based OAuth flows from being triggered by API requests.
func (rr *RegistryRoutes) getCurrentProvider(w http.ResponseWriter) (regpkg.Provider, bool) {
	var opts []regpkg.ProviderOption
	if rr.serveMode {
		opts = append(opts, regpkg.WithInteractive(false))
	}
	provider, err := regpkg.GetDefaultProviderWithConfig(rr.configProvider, opts...)
	if err != nil {
		if isRegistryAuthError(err) {
			writeRegistryAuthRequiredError(w)
			return nil, false
		}
		var unavailableErr *regpkg.UnavailableError
		if errors.As(err, &unavailableErr) {
			slog.Error("upstream registry unavailable", "error", err)
			writeRegistryUnavailableError(w, unavailableErr)
			return nil, false
		}
		http.Error(w, "Failed to get registry provider", http.StatusInternalServerError)
		slog.Error("failed to get registry provider", "error", err)
		return nil, false
	}
	return provider, true
}

// RegistryRoutes defines the routes for the registry API.
type RegistryRoutes struct {
	configProvider config.Provider
	configService  regpkg.Configurator
	serveMode      bool
}

// NewRegistryRoutes creates a new RegistryRoutes with the default config provider
func NewRegistryRoutes() *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: config.NewProvider(),
		configService:  regpkg.NewConfigurator(),
	}
}

// NewRegistryRoutesWithProvider creates a new RegistryRoutes with a custom config provider
// This is useful for testing
func NewRegistryRoutesWithProvider(provider config.Provider) *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: provider,
		configService:  regpkg.NewConfiguratorWithProvider(provider),
	}
}

// NewRegistryRoutesForServe creates RegistryRoutes configured for serve mode.
// In serve mode, the registry provider uses non-interactive auth (no browser OAuth).
func NewRegistryRoutesForServe() *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: config.NewProvider(),
		configService:  regpkg.NewConfigurator(),
		serveMode:      true,
	}
}

// RegistryRouter creates a new router for the registry API.
// When serveMode is true, the registry provider uses non-interactive auth,
// ensuring browser-based OAuth flows are never triggered from API requests.
func RegistryRouter(serveMode bool) http.Handler {
	routes := func() *RegistryRoutes {
		if serveMode {
			return NewRegistryRoutesForServe()
		}
		return NewRegistryRoutes()
	}()

	r := chi.NewRouter()
	r.Get("/", routes.listRegistries)
	r.Post("/", routes.addRegistry)
	r.Get("/{name}", routes.getRegistry)
	r.Put("/{name}", routes.updateRegistry)
	r.Delete("/{name}", routes.removeRegistry)

	// Add nested routes for servers within a registry
	r.Route("/{name}/servers", func(r chi.Router) {
		r.Get("/", routes.listServers)
		r.Get("/{serverName}", routes.getServer)
	})

	// Auth routes (serve mode only).
	// This static route takes priority over the /{name} parameter in Chi,
	// so it does not conflict with a registry named "auth".
	if serveMode {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", routes.registryAuthLogin)
			r.Post("/logout", routes.registryAuthLogout)
		})
	}

	return r
}

//	 listRegistries
//
//		@Summary		List registries
//		@Description	Get a list of the current registries
//		@Tags			registry
//		@Produce		json
//		@Success		200	{object}	registryListResponse
//		@Router			/api/v1beta/registry [get]
func (rr *RegistryRoutes) listRegistries(w http.ResponseWriter, _ *http.Request) {
	provider, ok := rr.getCurrentProvider(w)
	if !ok {
		return
	}

	reg, err := provider.GetRegistry()
	if err != nil {
		if isRegistryAuthError(err) {
			writeRegistryAuthRequiredError(w)
			return
		}
		var unavailableErr *regpkg.UnavailableError
		if errors.As(err, &unavailableErr) {
			slog.Error("upstream registry unavailable", "error", err)
			writeRegistryUnavailableError(w, unavailableErr)
			return
		}
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := rr.getRegistryInfo()

	regAuthStatus, regAuthType := rr.resolveAuthStatus()

	registries := []registryInfo{
		{
			Name:        defaultRegistryName,
			Version:     reg.Version,
			LastUpdated: reg.LastUpdated,
			ServerCount: len(reg.Servers),
			Type:        registryType,
			Source:      source,
			AuthStatus:  regAuthStatus,
			AuthType:    regAuthType,
			AuthConfig:  rr.resolveAuthConfig(),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	response := registryListResponse{Registries: registries}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

//	 addRegistry
//
//		@Summary		Add a registry
//		@Description	Add a new registry
//		@Tags			registry
//		@Accept			json
//		@Produce		json
//		@Success		501		{string}	string	"Not Implemented"
//		@Router			/api/v1beta/registry [post]
func (*RegistryRoutes) addRegistry(w http.ResponseWriter, _ *http.Request) {
	// Currently, only the default registry is supported
	// This endpoint returns a 501 Not Implemented status
	http.Error(w, "Adding custom registries is not currently supported", http.StatusNotImplemented)
}

//	 getRegistry
//
//		@Summary		Get a registry
//		@Description	Get details of a specific registry
//		@Tags			registry
//		@Produce		json
//		@Param			name	path		string	true	"Registry name"
//		@Success		200	{object}	getRegistryResponse
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name} [get]
func (rr *RegistryRoutes) getRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Only "default" registry is supported currently
	if name != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := rr.getCurrentProvider(w)
	if !ok {
		return
	}

	reg, err := provider.GetRegistry()
	if err != nil {
		if isRegistryAuthError(err) {
			writeRegistryAuthRequiredError(w)
			return
		}
		var unavailableErr *regpkg.UnavailableError
		if errors.As(err, &unavailableErr) {
			slog.Error("upstream registry unavailable", "error", err)
			writeRegistryUnavailableError(w, unavailableErr)
			return
		}
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := rr.getRegistryInfo()

	regAuthStatus, regAuthType := rr.resolveAuthStatus()

	response := getRegistryResponse{
		Name:        defaultRegistryName,
		Version:     reg.Version,
		LastUpdated: reg.LastUpdated,
		ServerCount: len(reg.Servers),
		Type:        registryType,
		Source:      source,
		AuthStatus:  regAuthStatus,
		AuthType:    regAuthType,
		AuthConfig:  rr.resolveAuthConfig(),
		Registry:    reg,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

//	 updateRegistry
//
//		@Summary		Update registry configuration
//		@Description	Update registry URL or local path for the default registry
//		@Tags			registry
//		@Accept			json
//		@Produce		json
//		@Param			name	path		string					true	"Registry name (must be 'default')"
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
	if name != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	var req UpdateRegistryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate that only one of URL, APIURL, or LocalPath is provided
	if err := validateRegistryRequest(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := regpkg.ActivePolicyGate().CheckUpdateRegistry(r.Context(), updateRegistryConfigFromRequest(&req)); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Process the registry URL/path update.
	var responseType string
	registryType, err := rr.processRegistryUpdate(&req)
	if err != nil {
		// Check if it's a connectivity error - return 504 Gateway Timeout
		var connErr *connectivityError
		if errors.As(err, &connErr) {
			http.Error(w, connErr.Error(), http.StatusGatewayTimeout)
			return
		}
		// Check if it's a validation error - return 502 Bad Gateway
		if isValidationError(err) {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Other errors - return 400 Bad Request
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	responseType = registryType

	// Always overwrite auth: if auth is provided, set it; if not, clear it.
	// This prevents stale tokens from being sent to the wrong registry server.
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

	// Reset the registry provider cache to pick up configuration changes
	regpkg.ResetDefaultProvider()

	// If registry was reset to default, responseType is already "default".
	// Otherwise resolve from config.
	if responseType == "" {
		currentType, _ := rr.getRegistryInfo()
		responseType = string(currentType)
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

	// Determine which registry type to set
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
		allowPrivateIP = false // Not applicable for local files
	} else {
		return "", fmt.Errorf("no valid registry configuration provided")
	}

	// Use the service to set the registry
	registryType, err := rr.configService.SetRegistryFromInput(input, allowPrivateIP)
	if err != nil {
		slog.Error("failed to set registry", "error", err)
		// Check if error is connectivity/timeout related
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
//		@Description	Remove a specific registry
//		@Tags			registry
//		@Produce		json
//		@Param			name	path		string	true	"Registry name"
//		@Success		204	{string}	string	"No Content"
//		@Failure		403	{string}	string	"Forbidden - blocked by policy"
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name} [delete]
func (*RegistryRoutes) removeRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if err := regpkg.ActivePolicyGate().CheckDeleteRegistry(r.Context(), &regpkg.DeleteRegistryConfig{
		Name: name,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Cannot remove the default registry
	if name == defaultRegistryName {
		http.Error(w, "Cannot remove the default registry", http.StatusBadRequest)
		return
	}

	// Since only default registry exists, any other name is not found
	http.Error(w, "Registry not found", http.StatusNotFound)
}

//	 listServers
//
//		@Summary		List servers in a registry
//		@Description	Get a list of servers in a specific registry
//		@Tags			registry
//		@Produce		json
//		@Param			name	path		string	true	"Registry name"
//		@Success		200	{object}	listServersResponse
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name}/servers [get]
func (rr *RegistryRoutes) listServers(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")

	// Only "default" registry is supported currently
	if registryName != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := rr.getCurrentProvider(w)
	if !ok {
		return
	}

	// Get the full registry to access both container and remote servers
	reg, err := provider.GetRegistry()
	if err != nil {
		if isRegistryAuthError(err) {
			writeRegistryAuthRequiredError(w)
			return
		}
		var unavailableErr *regpkg.UnavailableError
		if errors.As(err, &unavailableErr) {
			slog.Error("upstream registry unavailable", "error", err)
			writeRegistryUnavailableError(w, unavailableErr)
			return
		}
		slog.Error("failed to get registry", "error", err)
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	// Build response with both container and remote servers
	response := listServersResponse{
		Servers:       make([]*registry.ImageMetadata, 0, len(reg.Servers)),
		RemoteServers: make([]*registry.RemoteServerMetadata, 0, len(reg.RemoteServers)),
	}

	// Add container servers
	for _, server := range reg.Servers {
		response.Servers = append(response.Servers, server)
	}

	// Add remote servers
	for _, server := range reg.RemoteServers {
		response.RemoteServers = append(response.RemoteServers, server)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

//	 getServer
//
//		@Summary		Get a server from a registry
//		@Description	Get details of a specific server in a registry
//		@Tags			registry
//		@Produce		json
//		@Param			name		path		string	true	"Registry name"
//		@Param			serverName	path		string	true	"ImageMetadata name"
//		@Success		200	{object}	getServerResponse
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name}/servers/{serverName} [get]
func (rr *RegistryRoutes) getServer(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	serverName := chi.URLParam(r, "serverName")

	// URL-decode the server name to handle special characters like forward slashes
	// Chi should decode automatically, but we do it explicitly for safety
	decodedServerName, err := url.QueryUnescape(serverName)
	if err != nil {
		// If decoding fails, use the original name
		decodedServerName = serverName
	}

	// Only "default" registry is supported currently
	if registryName != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := rr.getCurrentProvider(w)
	if !ok {
		return
	}

	// Try to get the server (could be container or remote)
	server, err := provider.GetServer(decodedServerName)
	if err != nil {
		//nolint:gosec // G706: server name from URL parameter for diagnostics
		slog.Error("failed to get server", "server", decodedServerName, "error", err)
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Build response based on server type
	var response getServerResponse
	if server.IsRemote() {
		if remote, ok := server.(*registry.RemoteServerMetadata); ok {
			response = getServerResponse{
				RemoteServer: remote,
				IsRemote:     true,
			}
		}
	} else {
		if img, ok := server.(*registry.ImageMetadata); ok {
			response = getServerResponse{
				Server:   img,
				IsRemote: false,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// Response type definitions.

// registryInfo represents basic information about a registry
//
//	@Description	Basic information about a registry
type registryInfo struct {
	// Name of the registry
	Name string `json:"name"`
	// Version of the registry schema
	Version string `json:"version"`
	// Last updated timestamp
	LastUpdated string `json:"last_updated"`
	// Number of servers in the registry
	ServerCount int `json:"server_count"`
	// Type of registry (file, url, or default)
	Type RegistryType `json:"type"`
	// Source of the registry (URL, file path, or empty string for built-in)
	Source string `json:"source"`
	// AuthStatus is one of: "none", "configured", "authenticated".
	// Intentionally omits omitempty so clients always receive the field,
	// even when the value is "none" (the zero-value equivalent).
	AuthStatus string `json:"auth_status"`
	// AuthType is "oauth", "bearer" (future), or empty string when no auth.
	// Intentionally omits omitempty so clients can distinguish "no auth
	// configured" (empty string) from "field missing" without extra logic.
	AuthType string `json:"auth_type"`
	// AuthConfig contains the non-secret OAuth configuration when auth is configured.
	// Nil when auth_status is "none".
	AuthConfig *regpkg.OAuthPublicConfig `json:"auth_config,omitempty"`
}

// registryListResponse represents the response for listing registries
//
//	@Description	Response containing a list of registries
type registryListResponse struct {
	// List of registries
	Registries []registryInfo `json:"registries"`
}

// getRegistryResponse represents the response for getting a registry
//
//	@Description	Response containing registry details
type getRegistryResponse struct {
	// Name of the registry
	Name string `json:"name"`
	// Version of the registry schema
	Version string `json:"version"`
	// Last updated timestamp
	LastUpdated string `json:"last_updated"`
	// Number of servers in the registry
	ServerCount int `json:"server_count"`
	// Type of registry (file, url, or default)
	Type RegistryType `json:"type"`
	// Source of the registry (URL, file path, or empty string for built-in)
	Source string `json:"source"`
	// AuthStatus is one of: "none", "configured", "authenticated".
	// Intentionally omits omitempty — see registryInfo for rationale.
	AuthStatus string `json:"auth_status"`
	// AuthType is "oauth", "bearer" (future), or empty string when no auth.
	// Intentionally omits omitempty — see registryInfo for rationale.
	AuthType string `json:"auth_type"`
	// AuthConfig contains the non-secret OAuth configuration when auth is configured.
	// Nil when auth_status is "none".
	AuthConfig *regpkg.OAuthPublicConfig `json:"auth_config,omitempty"`
	// Full registry data
	Registry *registry.Registry `json:"registry"`
}

// listServersResponse represents the response for listing servers in a registry
//
//	@Description	Response containing a list of servers
type listServersResponse struct {
	// List of container servers in the registry
	Servers []*registry.ImageMetadata `json:"servers"`
	// List of remote servers in the registry (if any)
	RemoteServers []*registry.RemoteServerMetadata `json:"remote_servers,omitempty"`
}

// getServerResponse represents the response for getting a server from a registry
//
//	@Description	Response containing server details
type getServerResponse struct {
	// Container server details (if it's a container server)
	Server *registry.ImageMetadata `json:"server,omitempty"`
	// Remote server details (if it's a remote server)
	RemoteServer *registry.RemoteServerMetadata `json:"remote_server,omitempty"`
	// Indicates if this is a remote server
	IsRemote bool `json:"is_remote"`
}

// UpdateRegistryRequest represents the request for updating a registry
//
//	@Description	Request containing registry configuration updates
type UpdateRegistryRequest struct {
	// Registry URL (for remote registries)
	URL *string `json:"url,omitempty"`
	// MCP Registry API URL
	APIURL *string `json:"api_url,omitempty"`
	// Local registry file path
	LocalPath *string `json:"local_path,omitempty"`
	// Allow private IP addresses for registry URL or API URL
	AllowPrivateIP *bool `json:"allow_private_ip,omitempty"`
	// OAuth authentication configuration (optional)
	Auth *UpdateRegistryAuthRequest `json:"auth,omitempty"`
}

// UpdateRegistryAuthRequest contains OAuth configuration fields for registry auth.
type UpdateRegistryAuthRequest struct {
	// OIDC issuer URL
	Issuer string `json:"issuer"`
	// OAuth client ID
	ClientID string `json:"client_id"`
	// OAuth audience (optional)
	Audience string `json:"audience,omitempty"`
	// OAuth scopes (optional)
	Scopes []string `json:"scopes,omitempty"`
}

// UpdateRegistryResponse represents the response for updating a registry
//
//	@Description	Response containing update result
type UpdateRegistryResponse struct {
	// Registry type after update
	Type string `json:"type"`
}
