package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	thverrors "github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
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
	secretsProvider  secrets.Provider
	workloadService  *WorkloadService
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
	secretsProvider secrets.Provider,
	debugMode bool,
) http.Handler {
	workloadService := NewWorkloadService(
		workloadManager,
		groupManager,
		secretsProvider,
		containerRuntime,
		debugMode,
	)

	routes := WorkloadRoutes{
		workloadManager:  workloadManager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
		groupManager:     groupManager,
		secretsProvider:  secretsProvider,
		workloadService:  workloadService,
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
	r.Get("/{name}/status", routes.getWorkloadStatus)
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

	// check if the workload already exists
	if req.Name != "" {
		exists, err := s.workloadManager.DoesWorkloadExist(ctx, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to check if workload exists: %v", err), http.StatusInternalServerError)
			return
		}
		if exists {
			http.Error(w, fmt.Sprintf("Workload with name %s already exists", req.Name), http.StatusConflict)
			return
		}
	}

	// Create the workload using shared logic
	runConfig, err := s.workloadService.CreateWorkloadFromRequest(ctx, &req)
	if err != nil {
		// Error messages already logged in createWorkloadFromRequest
		if errors.Is(err, retriever.ErrImageNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else if errors.Is(err, retriever.ErrInvalidRunConfig) {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

	runConfig, err := s.workloadService.UpdateWorkloadFromRequest(ctx, name, &createReq)
	if err != nil {
		http.Error(w, "Failed to update workload", http.StatusInternalServerError)
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

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
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

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
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

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
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

// getWorkloadStatus
//
//	@Summary		Get workload status
//	@Description	Get the current status of a specific workload
//	@Tags			workloads
//	@Produce		json
//	@Param			name	path		string	true	"Workload name"
//	@Success		200		{object}	workloadStatusResponse
//	@Failure		404		{string}	string	"Not Found"
//	@Router			/api/v1beta/workloads/{name}/status [get]
func (s *WorkloadRoutes) getWorkloadStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	workload, err := s.workloadManager.GetWorkload(ctx, name)
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

	response := workloadStatusResponse{
		Status: workload.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to marshal workload status", http.StatusInternalServerError)
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
