package v1

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

// HealthcheckRouter sets up healthcheck route.
func HealthcheckRouter(containerRuntime rt.Runtime) http.Handler {
	routes := &healthcheckRoutes{containerRuntime: containerRuntime}
	r := chi.NewRouter()
	r.Get("/", routes.getHealthcheck)
	return r
}

type healthcheckRoutes struct {
	containerRuntime rt.Runtime
}

//	 getHealthcheck
//		@Summary		Health check
//		@Description	Check if the API is healthy
//		@Tags			system
//		@Success		204	{string}	string	"No Content"
//		@Router			/health [get]
func (h *healthcheckRoutes) getHealthcheck(w http.ResponseWriter, r *http.Request) {
	if err := h.containerRuntime.IsRunning(r.Context()); err != nil {
		// If the container runtime is not running, we return a 503 Service Unavailable status.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// If the container runtime is running, we consider the API healthy.
	w.WriteHeader(http.StatusNoContent)
}
