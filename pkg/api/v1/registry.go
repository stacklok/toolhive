// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/registry"
)

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

	// Check for context deadline exceeded (timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check error message for common connectivity issues
	errStr := err.Error()
	connectivityKeywords := []string{
		"timeout",
		"unreachable",
		"connection refused",
		"connection reset",
		"connection timed out",
		"no route to host",
		"network is unreachable",
		"validation failed",
		"failed to fetch",
	}

	for _, keyword := range connectivityKeywords {
		if strings.Contains(strings.ToLower(errStr), keyword) {
			return true
		}
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
	return getRegistryInfoWithProvider(rr.configProvider)
}

// getRegistryInfoWithProvider returns the registry type and the source using the provided config provider
func getRegistryInfoWithProvider(configProvider config.Provider) (RegistryType, string) {
	cfg := configProvider.GetConfig()

	// Check API URL first (highest priority for live data)
	if cfg.RegistryApiUrl != "" {
		return RegistryTypeAPI, cfg.RegistryApiUrl
	}

	if cfg.RegistryUrl != "" {
		return RegistryTypeURL, cfg.RegistryUrl
	}

	if cfg.LocalRegistryPath != "" {
		return RegistryTypeFile, cfg.LocalRegistryPath
	}

	// Default built-in registry
	return RegistryTypeDefault, ""
}

// getCurrentProvider returns the current registry provider using the injected config
func (rr *RegistryRoutes) getCurrentProvider(w http.ResponseWriter) (regpkg.Provider, bool) {
	provider, err := regpkg.GetDefaultProviderWithConfig(rr.configProvider)
	if err != nil {
		http.Error(w, "Failed to get registry provider", http.StatusInternalServerError)
		logger.Errorf("Failed to get registry provider: %v", err)
		return nil, false
	}
	return provider, true
}

// RegistryRoutes defines the routes for the registry API.
type RegistryRoutes struct {
	configProvider config.Provider
}

// NewRegistryRoutes creates a new RegistryRoutes with the default config provider
func NewRegistryRoutes() *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: config.NewDefaultProvider(),
	}
}

// NewRegistryRoutesWithProvider creates a new RegistryRoutes with a custom config provider
// This is useful for testing
func NewRegistryRoutesWithProvider(provider config.Provider) *RegistryRoutes {
	return &RegistryRoutes{
		configProvider: provider,
	}
}

// RegistryRouter creates a new router for the registry API.
func RegistryRouter() http.Handler {
	routes := NewRegistryRoutes()

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
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := rr.getRegistryInfo()

	registries := []registryInfo{
		{
			Name:        defaultRegistryName,
			Version:     reg.Version,
			LastUpdated: reg.LastUpdated,
			ServerCount: len(reg.Servers),
			Type:        registryType,
			Source:      source,
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
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := rr.getRegistryInfo()

	response := getRegistryResponse{
		Name:        defaultRegistryName,
		Version:     reg.Version,
		LastUpdated: reg.LastUpdated,
		ServerCount: len(reg.Servers),
		Type:        registryType,
		Source:      source,
		Registry:    reg,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
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
//		@Failure		404		{string}	string	"Not Found"
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

	// Process the registry update
	responseType, message, err := rr.processRegistryUpdate(&req)
	if err != nil {
		// Check if it's a connectivity error - return 504 Gateway Timeout
		var connErr *connectivityError
		if errors.As(err, &connErr) {
			http.Error(w, connErr.Error(), http.StatusGatewayTimeout)
			return
		}
		// Other errors - return 400 Bad Request
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Reset the default provider to pick up configuration changes
	regpkg.ResetDefaultProvider()
	// Reset the config singleton to clear cached configuration
	config.ResetSingleton()

	response := UpdateRegistryResponse{
		Message: message,
		Type:    responseType,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
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

// processRegistryUpdate processes the registry update based on request type
func (rr *RegistryRoutes) processRegistryUpdate(req *UpdateRegistryRequest) (string, string, error) {
	if req.URL == nil && req.APIURL == nil && req.LocalPath == nil {
		return rr.handleRegistryReset()
	}
	if req.URL != nil {
		return rr.handleRegistryURL(*req.URL, req.AllowPrivateIP)
	}
	if req.APIURL != nil {
		return rr.handleRegistryAPIURL(*req.APIURL, req.AllowPrivateIP)
	}
	if req.LocalPath != nil {
		return rr.handleRegistryLocalPath(*req.LocalPath)
	}
	return "", "", fmt.Errorf("no valid registry configuration provided")
}

// handleRegistryReset resets the registry to default
func (rr *RegistryRoutes) handleRegistryReset() (string, string, error) {
	if err := rr.configProvider.UnsetRegistry(); err != nil {
		logger.Errorf("Failed to unset registry: %v", err)
		return "", "", fmt.Errorf("failed to reset registry configuration")
	}
	return "default", "Registry configuration reset to default", nil
}

// handleRegistryURL updates the registry URL
func (rr *RegistryRoutes) handleRegistryURL(registryURL string, allowPrivateIP *bool) (string, string, error) {
	allow := false
	if allowPrivateIP != nil {
		allow = *allowPrivateIP
	}

	if err := rr.configProvider.SetRegistryURL(registryURL, allow); err != nil {
		logger.Errorf("Failed to set registry URL: %v", err)
		// Check if error is connectivity/timeout related
		if isConnectivityError(err) {
			return "", "", &connectivityError{
				URL: registryURL,
				Err: err,
			}
		}
		return "", "", fmt.Errorf("failed to set registry URL: %w", err)
	}
	return "url", fmt.Sprintf("Successfully set registry URL: %s", registryURL), nil
}

// handleRegistryAPIURL updates the registry API URL
func (rr *RegistryRoutes) handleRegistryAPIURL(apiURL string, allowPrivateIP *bool) (string, string, error) {
	allow := false
	if allowPrivateIP != nil {
		allow = *allowPrivateIP
	}

	if err := rr.configProvider.SetRegistryAPI(apiURL, allow); err != nil {
		logger.Errorf("Failed to set registry API URL: %v", err)
		// Check if error is connectivity/timeout related
		if isConnectivityError(err) {
			return "", "", &connectivityError{
				URL: apiURL,
				Err: err,
			}
		}
		return "", "", fmt.Errorf("failed to set registry API URL: %w", err)
	}
	return "api", fmt.Sprintf("Successfully set registry API URL: %s", apiURL), nil
}

// handleRegistryLocalPath updates the registry local path
func (rr *RegistryRoutes) handleRegistryLocalPath(localPath string) (string, string, error) {
	if err := rr.configProvider.SetRegistryFile(localPath); err != nil {
		logger.Errorf("Failed to set registry file: %v", err)
		return "", "", fmt.Errorf("failed to set registry file: %w", err)
	}
	return "file", fmt.Sprintf("Successfully set local registry file: %s", localPath), nil
}

//	 removeRegistry
//
//		@Summary		Remove a registry
//		@Description	Remove a specific registry
//		@Tags			registry
//		@Produce		json
//		@Param			name	path		string	true	"Registry name"
//		@Success		204	{string}	string	"No Content"
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name} [delete]
func (*RegistryRoutes) removeRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

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
		logger.Errorf("Failed to get registry: %v", err)
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
		logger.Errorf("Failed to encode response: %v", err)
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
		logger.Errorf("Failed to get server '%s': %v", decodedServerName, err)
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
		logger.Errorf("Failed to encode response: %v", err)
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
}

// UpdateRegistryResponse represents the response for updating a registry
//
//	@Description	Response containing update result
type UpdateRegistryResponse struct {
	// Status message
	Message string `json:"message"`
	// Registry type after update
	Type string `json:"type"`
}
