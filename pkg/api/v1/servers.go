package v1

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
)

// ServerRoutes defines the routes for server management.
type ServerRoutes struct {
	manager lifecycle.Manager
}

// ServerRouter creates a new ServerRoutes instance.
func ServerRouter(manager lifecycle.Manager) http.Handler {
	routes := ServerRoutes{manager: manager}
	r := chi.NewRouter()
	r.Get("/", routes.listServers)
	r.Get("/{name}", routes.getServer)
	r.Post("/{name}/stop", routes.stopServer)
	r.Post("/{name}/restart", routes.restartServer)
	r.Delete("/{name}", routes.deleteServer)
	return r
}

func (s *ServerRoutes) listServers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	listAll := r.URL.Query().Get("all") == "true"
	servers, err := s.manager.ListContainers(ctx, listAll)
	if err != nil {
		logger.Errorf("Failed to list servers: %v", err)
		http.Error(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	err = json.NewEncoder(w).Encode(serverListResponse{Servers: servers})
	if err != nil {
		http.Error(w, "Failed to marshal server list", http.StatusInternalServerError)
		return
	}
}

func (s *ServerRoutes) getServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	server, err := s.manager.GetContainer(ctx, name)
	if err != nil {
		if errors.Is(err, lifecycle.ErrContainerNotFound) {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to list servers: %v", err)
		http.Error(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	err = json.NewEncoder(w).Encode(server)
	if err != nil {
		http.Error(w, "Failed to marshal server details", http.StatusInternalServerError)
		return
	}
}

func (s *ServerRoutes) stopServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	err := s.manager.StopContainer(ctx, name)
	if err != nil {
		if errors.Is(err, lifecycle.ErrContainerNotFound) {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to stop server: %v", err)
		http.Error(w, "Failed to stop server", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ServerRoutes) deleteServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	forceDelete := r.URL.Query().Get("force") == "true"
	err := s.manager.DeleteContainer(ctx, name, forceDelete)
	if err != nil {
		if errors.Is(err, lifecycle.ErrContainerNotFound) {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to delete server: %v", err)
		http.Error(w, "Failed to delete server", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ServerRoutes) restartServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	err := s.manager.RestartContainer(ctx, name)
	if err != nil {
		if errors.Is(err, lifecycle.ErrContainerNotFound) {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to restart server: %v", err)
		http.Error(w, "Failed to restart server", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Response type definitions.
// TODO: Generate these from OpenAPI specs.
type serverListResponse struct {
	Servers []runtime.ContainerInfo `json:"servers"`
}
