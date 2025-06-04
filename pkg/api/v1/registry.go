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

func (*RegistryRoutes) listRegistries(_ http.ResponseWriter, _ *http.Request) {
	logger.Debug("Listing registries")
}

func (*RegistryRoutes) addRegistry(_ http.ResponseWriter, _ *http.Request) {
	logger.Debug("Adding registry")
}

func (*RegistryRoutes) getRegistry(_ http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	logger.Debugf("Getting registry: %s", name)
}

func (*RegistryRoutes) removeRegistry(_ http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	logger.Debugf("Removing registry: %s", name)
}

func (*RegistryRoutes) listServers(_ http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	logger.Debugf("Listing servers for registry: %s", registryName)
}

func (*RegistryRoutes) getServer(_ http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "name")
	serverName := chi.URLParam(r, "serverName")
	logger.Debugf("Getting server '%s' from registry '%s'", serverName, registryName)
}
