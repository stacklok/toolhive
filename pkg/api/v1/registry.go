package v1

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
)

const (
	// defaultRegistryName is the name of the default registry
	defaultRegistryName = "default"
)

// RegistryType represents the type of registry
type RegistryType string

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile RegistryType = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL RegistryType = "url"
	// RegistryTypeDefault represents a built-in registry
	RegistryTypeDefault RegistryType = "default"
)

// getRegistryInfo returns the registry type and the source
func getRegistryInfo() (RegistryType, string) {
	url, localPath, _, registryType := config.GetRegistryConfig()

	switch registryType {
	case "url":
		return RegistryTypeURL, url
	case "file":
		return RegistryTypeFile, localPath
	default:
		// Default built-in registry
		return RegistryTypeDefault, ""
	}
}

// getCurrentProvider returns the current registry provider
func getCurrentProvider(w http.ResponseWriter) (registry.Provider, bool) {
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		http.Error(w, "Failed to get registry provider", http.StatusInternalServerError)
		logger.Errorf("Failed to get registry provider: %v", err)
		return nil, false
	}
	return provider, true
}

// RegistryRoutes defines the routes for the registry API.
type RegistryRoutes struct{}

// RegistryRouter creates a new router for the registry API.
func RegistryRouter(_ registry.Provider) http.Handler {
	routes := RegistryRoutes{}

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
func (*RegistryRoutes) listRegistries(w http.ResponseWriter, _ *http.Request) {
	provider, ok := getCurrentProvider(w)
	if !ok {
		return
	}

	reg, err := provider.GetRegistry()
	if err != nil {
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := getRegistryInfo()

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
func (*RegistryRoutes) getRegistry(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Only "default" registry is supported currently
	if name != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := getCurrentProvider(w)
	if !ok {
		return
	}

	reg, err := provider.GetRegistry()
	if err != nil {
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registryType, source := getRegistryInfo()

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
func (*RegistryRoutes) updateRegistry(w http.ResponseWriter, r *http.Request) {
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

	// Validate that only one of URL or LocalPath is provided
	if req.URL != nil && req.LocalPath != nil {
		http.Error(w, "Cannot specify both URL and local path", http.StatusBadRequest)
		return
	}

	var responseType string
	var message string

	// Handle reset to default (no URL or LocalPath specified)
	if req.URL == nil && req.LocalPath == nil {
		if err := config.UnsetRegistry(); err != nil {
			logger.Errorf("Failed to unset registry: %v", err)
			http.Error(w, "Failed to reset registry configuration", http.StatusInternalServerError)
			return
		}
		responseType = "default"
		message = "Registry configuration reset to default"
	} else if req.URL != nil {
		// Handle URL update
		allowPrivateIP := false
		if req.AllowPrivateIP != nil {
			allowPrivateIP = *req.AllowPrivateIP
		}

		if err := config.SetRegistryURL(*req.URL, allowPrivateIP); err != nil {
			logger.Errorf("Failed to set registry URL: %v", err)
			http.Error(w, fmt.Sprintf("Failed to set registry URL: %v", err), http.StatusBadRequest)
			return
		}
		responseType = "url"
		message = fmt.Sprintf("Successfully set registry URL: %s", *req.URL)
	} else if req.LocalPath != nil {
		// Handle local path update
		if err := config.SetRegistryFile(*req.LocalPath); err != nil {
			logger.Errorf("Failed to set registry file: %v", err)
			http.Error(w, fmt.Sprintf("Failed to set registry file: %v", err), http.StatusBadRequest)
			return
		}
		responseType = "file"
		message = fmt.Sprintf("Successfully set local registry file: %s", *req.LocalPath)
	}

	// Reset the default provider to pick up configuration changes
	registry.ResetDefaultProvider()

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
func (*RegistryRoutes) listServers(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")

	// Only "default" registry is supported currently
	if registryName != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := getCurrentProvider(w)
	if !ok {
		return
	}

	servers, err := provider.ListServers()
	if err != nil {
		logger.Errorf("Failed to list servers: %v", err)
		http.Error(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := listServersResponse{Servers: servers}
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
func (*RegistryRoutes) getServer(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	serverName := chi.URLParam(r, "serverName")

	// Only "default" registry is supported currently
	if registryName != defaultRegistryName {
		http.Error(w, "Registry not found", http.StatusNotFound)
		return
	}

	provider, ok := getCurrentProvider(w)
	if !ok {
		return
	}

	server, err := provider.GetServer(serverName)
	if err != nil {
		logger.Errorf("Failed to get server '%s': %v", serverName, err)
		http.Error(w, "ImageMetadata not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := getServerResponse{Server: server}
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
	// Source of the registry (URL, file path, or "default")
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
	// Source of the registry (URL, file path, or "default")
	Source string `json:"source"`
	// Full registry data
	Registry *registry.Registry `json:"registry"`
}

// listServersResponse represents the response for listing servers in a registry
//
//	@Description	Response containing a list of servers
type listServersResponse struct {
	// List of registries
	Servers []*registry.ImageMetadata `json:"servers"`
}

// getServerResponse represents the response for getting a server from a registry
//
//	@Description	Response containing server details
type getServerResponse struct {
	// Server details
	Server *registry.ImageMetadata `json:"server"`
}

// UpdateRegistryRequest represents the request for updating a registry
//
//	@Description	Request containing registry configuration updates
type UpdateRegistryRequest struct {
	// Registry URL (for remote registries)
	URL *string `json:"url,omitempty"`
	// Local registry file path
	LocalPath *string `json:"local_path,omitempty"`
	// Allow private IP addresses for registry URL
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
