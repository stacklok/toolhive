package v1

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// WorkloadRoutes defines the routes for workload management.
type WorkloadRoutes struct {
	manager          workloads.Manager
	containerRuntime runtime.Runtime
	debugMode        bool
}

//	@title			ToolHive API
//	@version		1.0
//	@description	This is the ToolHive API workload.
//	@workloads		[ { "url": "http://localhost:8080/api/v1" } ]
//	@basePath		/api/v1

// WorkloadRouter creates a new WorkloadRoutes instance.
func WorkloadRouter(
	manager workloads.Manager,
	containerRuntime runtime.Runtime,
	debugMode bool,
) http.Handler {
	routes := WorkloadRoutes{
		manager:          manager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listWorkloads)
	r.Post("/", routes.createWorkload)
	r.Get("/{name}", routes.getWorkload)
	r.Post("/{name}/stop", routes.stopWorkload)
	r.Post("/{name}/restart", routes.restartWorkload)
	r.Delete("/{name}", routes.deleteWorkload)
	return r
}

//	 listWorkloads
//		@Summary		List all workloads
//		@Description	Get a list of all running workloads
//		@Tags			workloads
//		@Produce		json
//		@Success		200	{object}	workloadListResponse
//		@Router			/api/v1beta/workloads [get]
func (s *WorkloadRoutes) listWorkloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	listAll := r.URL.Query().Get("all") == "true"
	workloadList, err := s.manager.ListWorkloads(ctx, listAll)
	if err != nil {
		logger.Errorf("Failed to list workloads: %v", err)
		http.Error(w, "Failed to list workloads", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(workloadListResponse{Workloads: workloadList})
	if err != nil {
		http.Error(w, "Failed to marshal workload list", http.StatusInternalServerError)
		return
	}
}

// getWorkload
//
//	@Summary		Get workload details
//	@Description	Get details of a specific workload
//	@Tags			workloads
//	@Produce		json
//	@Param			name	path		string	true	"Workload name"
//	@Success		200		{object}	workloads.Workload
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name} [get]
func (s *WorkloadRoutes) getWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	workload, err := s.manager.GetWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, workloads.ErrContainerNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to list workloads: %v", err)
		http.Error(w, "Failed to list workloads", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(workload)
	if err != nil {
		http.Error(w, "Failed to marshal workload details", http.StatusInternalServerError)
		return
	}
}

// stopWorkload
//
//	@Summary		Stop a workload
//	@Description	Stop a running workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		204		{string}	string	"No Content"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/stop [post]
func (s *WorkloadRoutes) stopWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	err := s.manager.StopWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, workloads.ErrContainerNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to stop workload: %v", err)
		http.Error(w, "Failed to stop workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteWorkload
//
//	@Summary		Delete a workload
//	@Description	Delete a workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		204		{string}	string	"No Content"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name} [delete]
func (s *WorkloadRoutes) deleteWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	err := s.manager.DeleteWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, workloads.ErrContainerNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to delete workload: %v", err)
		http.Error(w, "Failed to delete workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// restartWorkload
//
//	@Summary		Restart a workload
//	@Description	Restart a running workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		204		{string}	string	"No Content"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/restart [post]
func (s *WorkloadRoutes) restartWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	err := s.manager.RestartWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, workloads.ErrContainerNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to restart workload: %v", err)
		http.Error(w, "Failed to restart workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createWorkload
//
//	@Summary		Create a new workload
//	@Description	Create and start a new workload
//	@Tags			workloads
//	@Accept			json
//	@Produce		json
//	@Param			request	body		createRequest	true	"Create workload request"
//	@Success		201		{object}	createWorkloadResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		409		{string}	string	"Conflict"
//	@Router			/api/v1beta/workloads [post]
func (s *WorkloadRoutes) createWorkload(w http.ResponseWriter, r *http.Request) {
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
		req.Host,
		s.debugMode,
		req.Volumes,
		runSecrets,
		req.AuthzConfig,
		"",    // auditConfigPath - will be added in future PR
		false, // enableAudit - will be added in future PR
		req.PermissionProfile,
		transport.LocalhostIPv4, // Seems like a reasonable default for now.
		req.OIDC.Issuer,
		req.OIDC.Audience,
		req.OIDC.JwksURL,
		req.OIDC.ClientID,
		"",    // otelEndpoint - not exposed through API yet
		"",    // otelServiceName - not exposed through API yet
		0.1,   // otelSamplingRate - default value
		nil,   // otelHeaders - not exposed through API yet
		false, // otelInsecure - not exposed through API yet
		false, // otelEnablePrometheusMetricsPath - not exposed through API yet
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
	if _, err := runConfig.WithPorts(0, req.TargetPort); err != nil {
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

	err := s.manager.RunWorkloadDetached(runConfig)
	if err != nil {
		logger.Errorf("Failed to start workload: %v", err)
		http.Error(w, "Failed to start workload", http.StatusInternalServerError)
		return
	}

	// Return name so that the client will get the auto-generated name.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	resp := createWorkloadResponse{
		Name: runConfig.ContainerName,
		Port: runConfig.Port,
	}
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to marshal workload details", http.StatusInternalServerError)
		return
	}
}

// Response type definitions.

// workloadListResponse represents the response for listing workloads
//
//	@Description	Response containing a list of workloads
type workloadListResponse struct {
	// List of container information for each workload
	Workloads []workloads.Workload `json:"workloads"`
}

// createRequest represents the request to create a new workload
//
//	@Description	Request to create a new workload
type createRequest struct {
	// Name of the workload
	Name string `json:"name"`
	// Docker image to use
	Image string `json:"image"`
	// Host to bind to
	Host string `json:"host"`
	// Command arguments to pass to the container
	CmdArguments []string `json:"cmd_arguments"`
	// Port to expose from the container
	TargetPort int `json:"target_port"`
	// Environment variables to set in the container
	EnvVars []string `json:"env_vars"`
	// Secret parameters to inject
	Secrets []secrets.SecretParameter `json:"secrets"`
	// Volume mounts
	Volumes []string `json:"volumes"`
	// Transport configuration
	Transport string `json:"transport"`
	// Authorization configuration
	AuthzConfig string `json:"authz_config"`
	// OIDC configuration options
	OIDC oidcOptions `json:"oidc"`
	// Permission profile to apply
	PermissionProfile string `json:"permission_profile"`
}

// oidcOptions represents OIDC configuration options
//
//	@Description	OIDC configuration for workload authentication
type oidcOptions struct {
	// OIDC issuer URL
	Issuer string `json:"issuer"`
	// Expected audience
	Audience string `json:"audience"`
	// JWKS URL for key verification
	JwksURL string `json:"jwks_url"`
	// OAuth2 client ID
	ClientID string `json:"client_id"`
}

// createWorkloadResponse represents the response for workload creation
//
//	@Description	Response after successfully creating a workload
type createWorkloadResponse struct {
	// Name of the created workload
	Name string `json:"name"`
	// Port the workload is listening on
	Port int `json:"port"`
}
