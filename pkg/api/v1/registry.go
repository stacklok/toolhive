package v1

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/logger"
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
func (*RegistryRoutes) listRegistries(_ http.ResponseWriter, _ *http.Request) {
	logger.Debug("Listing registries")
}

//	 addRegistry
//
//		@Summary		Add a registry
//		@Description	Add a new registry
//		@Tags			registry
//		@Accept			json
//		@Produce		json
//		@Param			request	body		addRegistryRequest	true	"Add registry request"
//		@Success		201		{object}	addRegistryResponse
//		@Failure		400		{string}	string	"Bad Request"
//		@Failure		409		{string}	string	"Conflict"
//		@Router			/api/v1beta/registry [post]
func (*RegistryRoutes) addRegistry(_ http.ResponseWriter, _ *http.Request) {
	logger.Debug("Adding registry")
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
func (*RegistryRoutes) getRegistry(_ http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	logger.Debugf("Getting registry: %s", name)
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
func (*RegistryRoutes) removeRegistry(_ http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	logger.Debugf("Removing registry: %s", name)
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
func (*RegistryRoutes) listServers(_ http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	logger.Debugf("Listing servers for registry: %s", registryName)
}

//	 getServer
//
//		@Summary		Get a server from a registry
//		@Description	Get details of a specific server in a registry
//		@Tags			registry
//		@Produce		json
//		@Param			name		path		string	true	"Registry name"
//		@Param			serverName	path		string	true	"Server name"
//		@Success		200	{object}	getServerResponse
//		@Failure		404	{string}	string	"Not Found"
//		@Router			/api/v1beta/registry/{name}/servers/{serverName} [get]
func (*RegistryRoutes) getServer(_ http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	serverName := chi.URLParam(r, "serverName")
	logger.Debugf("Getting server '%s' from registry '%s'", serverName, registryName)
}
