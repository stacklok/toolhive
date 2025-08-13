package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	thverrors "github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
)

// WorkloadRoutes defines the routes for workload management.
type WorkloadRoutes struct {
	workloadManager  workloads.Manager
	containerRuntime runtime.Runtime
	debugMode        bool
	groupManager     groups.Manager
}

//	@title			ToolHive API
//	@version		1.0
//	@description	This is the ToolHive API workload.
//	@workloads		[ { "url": "http://localhost:8080/api/v1beta" } ]
//	@basePath		/api/v1beta

// WorkloadRouter creates a new WorkloadRoutes instance.
func WorkloadRouter(
	workloadManager workloads.Manager,
	containerRuntime runtime.Runtime,
	groupManager groups.Manager,
	debugMode bool,
) http.Handler {
	routes := WorkloadRoutes{
		workloadManager:  workloadManager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
		groupManager:     groupManager,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listWorkloads)
	r.Post("/", routes.createWorkload)
	r.Post("/stop", routes.stopWorkloadsBulk)
	r.Post("/restart", routes.restartWorkloadsBulk)
	r.Post("/delete", routes.deleteWorkloadsBulk)
	r.Get("/{name}", routes.getWorkload)
	r.Post("/{name}/edit", routes.updateWorkload)
	r.Post("/{name}/stop", routes.stopWorkload)
	r.Post("/{name}/restart", routes.restartWorkload)
	r.Get("/{name}/logs", routes.getLogsForWorkload)
	r.Get("/{name}/export", routes.exportWorkload)
	r.Delete("/{name}", routes.deleteWorkload)

	return r
}

//	 listWorkloads
//		@Summary		List all workloads
//		@Description	Get a list of all running workloads, optionally filtered by group
//		@Tags			workloads
//		@Produce		json
//		@Param			all	query		bool	false	"List all workloads, including stopped ones"
//		@Param			group	query		string	false	"Filter workloads by group name"
//		@Success		200	{object}	workloadListResponse
//		@Failure		404	{string}	string	"Group not found"
//		@Router			/api/v1beta/workloads [get]
func (s *WorkloadRoutes) listWorkloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	listAll := r.URL.Query().Get("all") == "true"
	groupFilter := r.URL.Query().Get("group")

	workloadList, err := s.workloadManager.ListWorkloads(ctx, listAll)
	if err != nil {
		logger.Errorf("Failed to list workloads: %v", err)
		http.Error(w, "Failed to list workloads", http.StatusInternalServerError)
		return
	}

	// Apply group filtering if specified
	if groupFilter != "" {
		if err := validation.ValidateGroupName(groupFilter); err != nil {
			http.Error(w, "Invalid group name: "+err.Error(), http.StatusBadRequest)
			return
		}
		workloadList, err = workloads.FilterByGroup(workloadList, groupFilter)
		if err != nil {
			if thverrors.IsGroupNotFound(err) {
				http.Error(w, "Group not found", http.StatusNotFound)
			} else {
				logger.Errorf("Failed to filter workloads by group: %v", err)
				http.Error(w, "Failed to list workloads in group", http.StatusInternalServerError)
			}
			return
		}
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
//	@Success		200		{object}	createRequest
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name} [get]
func (s *WorkloadRoutes) getWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		} else if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to get workload: %v", err)
		http.Error(w, "Failed to get workload", http.StatusInternalServerError)
		return
	}

	// Load the workload configuration
	runConfig, err := runner.LoadState(ctx, name)
	if err != nil {
		logger.Errorf("Failed to load workload configuration for %s: %v", name, err)
		http.Error(w, "Workload configuration not found", http.StatusNotFound)
		return
	}

	config := runConfigToCreateRequest(runConfig)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(config); err != nil {
		http.Error(w, "Failed to marshal workload configuration", http.StatusInternalServerError)
		return
	}
}

// stopWorkload
//
//	@Summary		Stop a workload
//	@Description	Stop a running workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/stop [post]
func (s *WorkloadRoutes) stopWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Use the bulk method with a single workload
	_, err := s.workloadManager.StopWorkloads(ctx, []string{name})
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to stop workload: %v", err)
		http.Error(w, "Failed to stop workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// restartWorkload
//
//	@Summary		Restart a workload
//	@Description	Restart a running workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/restart [post]
func (s *WorkloadRoutes) restartWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Use the bulk method with a single workload
	// Note: In the API, we always assume that the restart is a background operation
	_, err := s.workloadManager.RestartWorkloads(ctx, []string{name}, false)
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to restart workload: %v", err)
		http.Error(w, "Failed to restart workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// deleteWorkload
//
//	@Summary		Delete a workload
//	@Description	Delete a workload
//	@Tags			workloads
//	@Param			name	path		string	true	"Workload name"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name} [delete]
func (s *WorkloadRoutes) deleteWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Use the bulk method with a single workload
	_, err := s.workloadManager.DeleteWorkloads(ctx, []string{name})
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to delete workload: %v", err)
		http.Error(w, "Failed to delete workload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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
	ctx := r.Context()
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
		return
	}

	// Create the workload using shared logic
	runConfig, err := s.createWorkloadFromRequest(ctx, &req)
	if err != nil {
		// Error messages already logged in createWorkloadFromRequest
		if errors.Is(err, retriever.ErrImageNotFound) || err.Error() == "MCP server image not found" {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
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

// updateWorkload
//
//	@Summary		Update workload
//	@Description	Update an existing workload configuration
//	@Tags			workloads
//	@Accept			json
//	@Produce		json
//	@Param			name		path		string			true	"Workload name"
//	@Param			request		body		updateRequest	true	"Update workload request"
//	@Success		200			{object}	createWorkloadResponse
//	@Failure		400			{string}	string	"Bad Request"
//	@Failure		404			{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/edit [post]
func (s *WorkloadRoutes) updateWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Parse request body
	var updateReq updateRequest
	if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Check if workload exists
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		logger.Errorf("Failed to get workload: %v", err)
		http.Error(w, "Workload not found", http.StatusNotFound)
		return
	}

	// Convert updateRequest to createRequest with the existing workload name
	createReq := createRequest{
		updateRequest: updateReq,
		Name:          name, // Use the name from URL path, not from request body
	}

	// Stop the existing workload
	if _, err = s.workloadManager.StopWorkloads(ctx, []string{name}); err != nil {
		logger.Errorf("Failed to stop workload %s: %v", name, err)
		http.Error(w, "Failed to stop workload", http.StatusInternalServerError)
		return
	}

	// Delete the existing workload
	if _, err = s.workloadManager.DeleteWorkloads(ctx, []string{name}); err != nil {
		logger.Errorf("Failed to delete workload %s: %v", name, err)
		http.Error(w, "Failed to delete workload", http.StatusInternalServerError)
		return
	}

	// Create the new workload using shared logic
	runConfig, err := s.createWorkloadFromRequest(ctx, &createReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the same response format as create
	w.Header().Set("Content-Type", "application/json")
	resp := createWorkloadResponse{
		Name: runConfig.ContainerName,
		Port: runConfig.Port,
	}
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to marshal workload details", http.StatusInternalServerError)
		return
	}
}

// stopWorkloadsBulk
//
//	@Summary		Stop workloads in bulk
//	@Description	Stop multiple workloads by name or by group
//	@Tags			workloads
//	@Accept			json
//	@Param			request	body		bulkOperationRequest	true	"Bulk stop request (names or group)"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Router			/api/v1beta/workloads/stop [post]
func (s *WorkloadRoutes) stopWorkloadsBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
		return
	}

	if err := validateBulkOperationRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	workloadNames, err := s.getWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	_, err = s.workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to stop workloads: %v", err)
		http.Error(w, "Failed to stop workloads", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// restartWorkloadsBulk
//
//	@Summary		Restart workloads in bulk
//	@Description	Restart multiple workloads by name or by group
//	@Tags			workloads
//	@Accept			json
//	@Param			request	body		bulkOperationRequest	true	"Bulk restart request (names or group)"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Router			/api/v1beta/workloads/restart [post]
func (s *WorkloadRoutes) restartWorkloadsBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
		return
	}

	if err := validateBulkOperationRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	workloadNames, err := s.getWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	// Note: In the API, we always assume that the restart is a background operation.
	_, err = s.workloadManager.RestartWorkloads(ctx, workloadNames, false)
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to restart workloads: %v", err)
		http.Error(w, "Failed to restart workloads", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// deleteWorkloadsBulk
//
//	@Summary		Delete workloads in bulk
//	@Description	Delete multiple workloads by name or by group
//	@Tags			workloads
//	@Accept			json
//	@Param			request	body		bulkOperationRequest	true	"Bulk delete request (names or group)"
//	@Success		202		{string}	string	"Accepted"
//	@Failure		400		{string}	string	"Bad Request"
//	@Router			/api/v1beta/workloads/delete [post]
func (s *WorkloadRoutes) deleteWorkloadsBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
		return
	}

	if err := validateBulkOperationRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	workloadNames, err := s.getWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	_, err = s.workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		if errors.Is(err, wt.ErrInvalidWorkloadName) {
			http.Error(w, "Invalid workload name: "+err.Error(), http.StatusBadRequest)
			return
		}
		logger.Errorf("Failed to delete workloads: %v", err)
		http.Error(w, "Failed to delete workloads", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// getLogsForWorkload
//
// @Summary      Get logs for a specific workload
// @Description  Retrieve at most 100 lines of logs for a specific workload by name.
// @Tags         logs
// @Produce      text/plain
// @Param        name  path      string  true  "Workload name"
// @Success      200   {string}  string  "Logs for the specified workload"
// @Failure      404   {string}  string  "Not Found"
// @Router       /api/v1beta/workloads/{name}/logs [get]
func (s *WorkloadRoutes) getLogsForWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	logs, err := s.workloadManager.GetLogs(ctx, name, false)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get logs: %v", err)
		http.Error(w, "Failed to get logs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, err = w.Write([]byte(logs))
	if err != nil {
		logger.Errorf("Failed to write logs response: %v", err)
		http.Error(w, "Failed to write logs response", http.StatusInternalServerError)
		return
	}
}

// exportWorkload
//
//	@Summary		Export workload configuration
//	@Description	Export a workload's run configuration as JSON
//	@Tags			workloads
//	@Produce		json
//	@Param			name	path		string	true	"Workload name"
//	@Success		200		{object}	runner.RunConfig
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/export [get]
func (*WorkloadRoutes) exportWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Load the saved run configuration
	runConfig, err := runner.LoadState(ctx, name)
	if err != nil {
		if thverrors.IsRunConfigNotFound(err) {
			http.Error(w, "Workload configuration not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to load workload configuration: %v", err)
		http.Error(w, "Failed to load workload configuration", http.StatusInternalServerError)
		return
	}

	// Return the configuration as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(runConfig); err != nil {
		logger.Errorf("Failed to encode workload configuration: %v", err)
		http.Error(w, "Failed to encode workload configuration", http.StatusInternalServerError)
		return
	}
}

// Response type definitions.

// workloadListResponse represents the response for listing workloads
//
//	@Description	Response containing a list of workloads
type workloadListResponse struct {
	// List of container information for each workload
	Workloads []core.Workload `json:"workloads"`
}


// updateRequest represents the request to update an existing workload
//
//	@Description	Request to update an existing workload (name cannot be changed)
type updateRequest struct {
	// Docker image to use
	Image string `json:"image"`
	// Host to bind to
	Host string `json:"host"`
	// Command arguments to pass to the container
	CmdArguments []string `json:"cmd_arguments"`
	// Port to expose from the container
	TargetPort int `json:"target_port"`
	// Environment variables to set in the container
	EnvVars map[string]string `json:"env_vars"`
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
	PermissionProfile *permissions.Profile `json:"permission_profile"`
	// Proxy mode to use
	ProxyMode string `json:"proxy_mode"`
	// Whether network isolation is turned on. This applies the rules in the permission profile.
	NetworkIsolation bool `json:"network_isolation"`
	// Tools filter
	ToolsFilter []string `json:"tools"`
}

// createRequest represents the request to create a new workload
//
//	@Description	Request to create a new workload
type createRequest struct {
	updateRequest
	// Name of the workload
	Name string `json:"name"`
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
	// Token introspection URL for OIDC
	IntrospectionURL string `json:"introspection_url"`
	// OAuth2 client ID
	ClientID string `json:"client_id"`
	// OAuth2 client secret
	ClientSecret string `json:"client_secret"`
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

// bulkOperationRequest represents a request for bulk operations on workloads
type bulkOperationRequest struct {
	// Names of the workloads to operate on
	Names []string `json:"names"`
	// Group name to operate on (mutually exclusive with names)
	Group string `json:"group,omitempty"`
}

// validateBulkOperationRequest validates the bulk operation request
func validateBulkOperationRequest(req bulkOperationRequest) error {
	if len(req.Names) > 0 && req.Group != "" {
		return fmt.Errorf("cannot specify both names and group")
	}
	if len(req.Names) == 0 && req.Group == "" {
		return fmt.Errorf("must specify either names or group")
	}
	return nil
}

// getWorkloadNamesFromRequest gets workload names from either the names field or group
func (s *WorkloadRoutes) getWorkloadNamesFromRequest(ctx context.Context, req bulkOperationRequest) ([]string, error) {
	if len(req.Names) > 0 {
		return req.Names, nil
	}

	if err := validation.ValidateGroupName(req.Group); err != nil {
		return nil, fmt.Errorf("invalid group name: %w", err)
	}

	// Check if the group exists
	exists, err := s.groupManager.Exists(ctx, req.Group)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %v", err)
	}
	if !exists {
		return nil, fmt.Errorf("group '%s' does not exist", req.Group)
	}

	// Get all workload names in the group
	workloadNames, err := s.workloadManager.ListWorkloadsInGroup(ctx, req.Group)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %v", err)
	}

	return workloadNames, nil
}

// createWorkloadFromRequest creates a workload from a request
func (s *WorkloadRoutes) createWorkloadFromRequest(ctx context.Context, req *createRequest) (*runner.RunConfig, error) {
	// Fetch or build the requested image
	imageURL, imageMetadata, err := retriever.GetMCPServer(
		ctx,
		req.Image,
		"", // We do not let the user specify a CA cert path here.
		retriever.VerifyImageWarn,
	)
	if err != nil {
		if errors.Is(err, retriever.ErrImageNotFound) {
			return nil, fmt.Errorf("MCP server image not found")
		}
		return nil, fmt.Errorf("failed to retrieve MCP server image: %v", err)
	}

	// Build RunConfig
	runSecrets := secrets.SecretParametersToCLI(req.Secrets)
	runConfig, err := runner.NewRunConfigBuilder().
		WithRuntime(s.containerRuntime).
		WithCmdArgs(req.CmdArguments).
		WithName(req.Name).
		WithImage(imageURL).
		WithHost(req.Host).
		WithTargetHost(transport.LocalhostIPv4).
		WithDebug(s.debugMode).
		WithVolumes(req.Volumes).
		WithSecrets(runSecrets).
		WithAuthzConfigPath(req.AuthzConfig).
		WithAuditConfigPath("").
		WithPermissionProfile(req.PermissionProfile).
		WithNetworkIsolation(req.NetworkIsolation).
		WithK8sPodPatch("").
		WithProxyMode(types.ProxyMode(req.ProxyMode)).
		WithTransportAndPorts(req.Transport, 0, req.TargetPort).
		WithAuditEnabled(false, "").
		WithOIDCConfig(req.OIDC.Issuer, req.OIDC.Audience, req.OIDC.JwksURL, req.OIDC.ClientID,
			"", "", "", "", "", false).
		WithTelemetryConfig("", false, "", 0.0, nil, false, nil).
		WithToolsFilter(req.ToolsFilter).
		Build(ctx, imageMetadata, req.EnvVars, &runner.DetachedEnvVarValidator{})
	if err != nil {
		logger.Errorf("Failed to build run config: %v", err)
		return nil, fmt.Errorf("invalid configuration: %v", err)
	}

	// Save the workload state
	if err := runConfig.SaveState(ctx); err != nil {
		logger.Errorf("Failed to save workload config: %v", err)
		return nil, fmt.Errorf("failed to save workload config")
	}

	// Start workload
	if err := s.workloadManager.RunWorkloadDetached(ctx, runConfig); err != nil {
		logger.Errorf("Failed to start workload: %v", err)
		return nil, fmt.Errorf("failed to start workload")
	}

	return runConfig, nil
}

// runConfigToCreateRequest converts a RunConfig to createRequest for API responses
func runConfigToCreateRequest(runConfig *runner.RunConfig) *createRequest {
	// Convert CLI secrets ([]string) back to SecretParameters
	secretParams := make([]secrets.SecretParameter, 0, len(runConfig.Secrets))
	for _, secretStr := range runConfig.Secrets {
		// Parse the CLI format: "<name>,target=<target>"
		if secretParam, err := secrets.ParseSecretParameter(secretStr); err == nil {
			secretParams = append(secretParams, secretParam)
		}
		// Ignore invalid secrets rather than failing the entire conversion
	}

	// Get OIDC fields from RunConfig
	var oidcConfig oidcOptions
	if runConfig.OIDCConfig != nil {
		oidcConfig = oidcOptions{
			Issuer:           runConfig.OIDCConfig.Issuer,
			Audience:         runConfig.OIDCConfig.Audience,
			JwksURL:          runConfig.OIDCConfig.JWKSURL,
			IntrospectionURL: runConfig.OIDCConfig.IntrospectionURL,
			ClientID:         runConfig.OIDCConfig.ClientID,
			ClientSecret:     runConfig.OIDCConfig.ClientSecret,
		}
	}

	authzConfigPath := ""

	return &createRequest{
		updateRequest: updateRequest{
			Image:             runConfig.Image,
			Host:              runConfig.Host,
			CmdArguments:      runConfig.CmdArgs,
			TargetPort:        runConfig.TargetPort,
			EnvVars:           runConfig.EnvVars,
			Secrets:           secretParams,
			Volumes:           runConfig.Volumes,
			Transport:         string(runConfig.Transport),
			AuthzConfig:       authzConfigPath,
			OIDC:              oidcConfig,
			PermissionProfile: runConfig.PermissionProfile,
			ProxyMode:         string(runConfig.ProxyMode),
			NetworkIsolation:  runConfig.IsolateNetwork,
			ToolsFilter:       runConfig.ToolsFilter,
		},
		Name: runConfig.Name,
	}
}
