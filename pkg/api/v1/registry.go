package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
)

const (
	// defaultRegistryName is the name of the default registry
	defaultRegistryName = "default"
)

// RegistryRoutes defines the routes for the registry API.
type RegistryRoutes struct{}

// RegistryRouter creates a new router for the registry API.
func RegistryRouter() http.Handler {
	routes := RegistryRoutes{}

	r := chi.NewRouter()
	r.Get("/", routes.listRegistries)
	r.Post("/", routes.addRegistry)
	r.Get("/{name}", routes.getRegistry)
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
	reg, err := registry.GetRegistry()
	if err != nil {
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	registries := []registryInfo{
		{
			Name:        defaultRegistryName,
			Version:     reg.Version,
			LastUpdated: reg.LastUpdated,
			ServerCount: len(reg.Servers),
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

	reg, err := registry.GetRegistry()
	if err != nil {
		http.Error(w, "Failed to get registry", http.StatusInternalServerError)
		return
	}

	response := getRegistryResponse{
		Name:        defaultRegistryName,
		Version:     reg.Version,
		LastUpdated: reg.LastUpdated,
		ServerCount: len(reg.Servers),
		Registry:    reg,
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

	servers, err := registry.ListServers()
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

	server, err := registry.GetServer(serverName)
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
	// Full registry data
	Registry *registry.Registry `json:"registry"`
}

// listServersResponse represents the response for listing servers in a registry
//
//	@Description	Response containing a list of servers
type listServersResponse struct {
	// List of servers in the registry
	Servers []*registry.ImageMetadata `json:"servers"`
}

// getServerResponse represents the response for getting a server from a registry
//
//	@Description	Response containing server details
type getServerResponse struct {
	// Server details
	Server *registry.ImageMetadata `json:"server"`
}
