package v1

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ServerRoutes defines the routes for server management.
type ServerRoutes struct {
	manager          lifecycle.Manager
	containerRuntime runtime.Runtime
	debugMode        bool
}

// ServerRouter creates a new ServerRoutes instance.
func ServerRouter(
	manager lifecycle.Manager,
	containerRuntime runtime.Runtime,
	debugMode bool,
) http.Handler {
	routes := ServerRoutes{
		manager:          manager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listServers)
	r.Post("/", routes.createServer)
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

func (s *ServerRoutes) createServer(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
		return
	}

	// NOTE: None of the k8s-related config logic is included here.
	runSecrets := secrets.SecretParametersToCLI(req.Secrets)
	runConfig := runner.NewRunConfigFromFlags(
		s.containerRuntime,
		req.CmdArguments,
		req.Name,
		s.debugMode,
		req.Volumes,
		runSecrets,
		req.AuthzConfig,
		req.PermissionProfile,
		"localhost", // Seems like a reasonable default for now.
		req.OIDC.Issuer,
		req.OIDC.Audience,
		req.OIDC.JwksURL,
		req.OIDC.ClientID,
	)

	// TODO: De-dupe from `configureRunConfig` in `cmd/thv/app/run_common.go`.
	if req.Transport == "" {
		req.Transport = "stdio"
	}
	if _, err := runConfig.WithTransport(req.Transport); err != nil {
		// TODO: More fine grained error handling.
		http.Error(w, "Unable to configure transport", http.StatusBadRequest)
		return
	}
	// Let the manager handle the port mapping.
	// Configure ports and target host
	if _, err := runConfig.WithPorts(0, 0); err != nil {
		http.Error(w, "Unable to configure ports", http.StatusInternalServerError)
	}

	if runConfig.PermissionProfileNameOrPath == "" {
		runConfig.PermissionProfileNameOrPath = permissions.ProfileNetwork
	}

	// Set permission profile (mandatory)
	if _, err := runConfig.ParsePermissionProfile(); err != nil {
		http.Error(w, "Unable to configure permission profile", http.StatusBadRequest)
	}

	// Process volume mounts
	if err := runConfig.ProcessVolumeMounts(); err != nil {
		http.Error(w, "Unable to configure volume mounts", http.StatusBadRequest)
	}

	// Parse and set environment variables
	if _, err := runConfig.WithEnvironmentVariables(req.EnvVars); err != nil {
		http.Error(w, "Unable to configure ports", http.StatusBadRequest)
	}

	runConfig.Image = req.Image
	runConfig.WithContainerName()
	runConfig.WithStandardLabels()

	// ASSUMPTION MADE: The CLI parses the image and pulls it, but since the
	// same code is called when the process is detached, I do not call it here.
	// Some basic testing has confirmed this, but it may need some further
	// testing with npx/uvx.
	// TODO: Refactor the code out of the CLI.

	err := s.manager.RunContainerDetached(runConfig)
	if err != nil {
		logger.Errorf("Failed to start server: %v", err)
		http.Error(w, "Failed to start server", http.StatusInternalServerError)
		return
	}

	// Return name so that the client will get the auto-generated name.
	resp := createServerResponse{
		Name: runConfig.ContainerName,
		Port: runConfig.Port,
	}
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to marshal server details", http.StatusInternalServerError)
		return
	}
}

// Response type definitions.
// TODO: Generate these from OpenAPI specs.
type serverListResponse struct {
	Servers []runtime.ContainerInfo `json:"servers"`
}

type createRequest struct {
	Name              string                    `json:"name"`
	Image             string                    `json:"image"`
	CmdArguments      []string                  `json:"cmd_arguments"`
	EnvVars           []string                  `json:"env_vars"`
	Secrets           []secrets.SecretParameter `json:"secrets"`
	Volumes           []string                  `json:"volumes"`
	Transport         string                    `json:"transport"`
	AuthzConfig       string                    `json:"authz_config"`
	OIDC              oidcOptions               `json:"oidc"`
	PermissionProfile string                    `json:"permission_profile"`
}

type oidcOptions struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	JwksURL  string `json:"jwks_url"`
	ClientID string `json:"client_id"`
}

type createServerResponse struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}
