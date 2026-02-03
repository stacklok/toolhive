// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive-core/httperr"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
)

const (
	// maxAPILogLines is the maximum number of log lines returned by API endpoints
	maxAPILogLines = 1000
)

// WorkloadRoutes defines the routes for workload management.
type WorkloadRoutes struct {
	workloadManager  workloads.Manager
	containerRuntime runtime.Runtime
	debugMode        bool
	groupManager     groups.Manager
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
	debugMode bool,
) http.Handler {
	workloadService := NewWorkloadService(
		workloadManager,
		groupManager,
		containerRuntime,
		debugMode,
	)

	routes := WorkloadRoutes{
		workloadManager:  workloadManager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
		groupManager:     groupManager,
		workloadService:  workloadService,
	}

	r := chi.NewRouter()
	r.Get("/", apierrors.ErrorHandler(routes.listWorkloads))
	r.Post("/", apierrors.ErrorHandler(routes.createWorkload))
	r.Post("/stop", apierrors.ErrorHandler(routes.stopWorkloadsBulk))
	r.Post("/restart", apierrors.ErrorHandler(routes.restartWorkloadsBulk))
	r.Post("/delete", apierrors.ErrorHandler(routes.deleteWorkloadsBulk))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getWorkload))
	r.Post("/{name}/edit", apierrors.ErrorHandler(routes.updateWorkload))
	r.Post("/{name}/stop", apierrors.ErrorHandler(routes.stopWorkload))
	r.Post("/{name}/restart", apierrors.ErrorHandler(routes.restartWorkload))
	r.Get("/{name}/status", apierrors.ErrorHandler(routes.getWorkloadStatus))
	r.Get("/{name}/logs", apierrors.ErrorHandler(routes.getLogsForWorkload))
	r.Get("/{name}/proxy-logs", apierrors.ErrorHandler(routes.getProxyLogsForWorkload))
	r.Get("/{name}/export", apierrors.ErrorHandler(routes.exportWorkload))
	r.Delete("/{name}", apierrors.ErrorHandler(routes.deleteWorkload))

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
func (s *WorkloadRoutes) listWorkloads(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	listAll := r.URL.Query().Get("all") == "true"
	groupFilter := r.URL.Query().Get("group")

	workloadList, err := s.workloadManager.ListWorkloads(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	// Apply group filtering if specified
	if groupFilter != "" {
		if err := validation.ValidateGroupName(groupFilter); err != nil {
			return httperr.WithCode(
				fmt.Errorf("invalid group name: %w", err),
				http.StatusBadRequest,
			)
		}
		workloadList, err = workloads.FilterByGroup(workloadList, groupFilter)
		if err != nil {
			return err // groups.ErrGroupNotFound already has 404 status code
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(workloadListResponse{Workloads: workloadList}); err != nil {
		return fmt.Errorf("failed to marshal workload list: %w", err)
	}
	return nil
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
func (s *WorkloadRoutes) getWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	// Load the workload configuration
	runConfig, err := runner.LoadState(ctx, name)
	if err != nil {
		return httperr.WithCode(
			fmt.Errorf("workload configuration not found: %w", err),
			http.StatusNotFound,
		)
	}

	config := runConfigToCreateRequest(runConfig)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(config); err != nil {
		return fmt.Errorf("failed to marshal workload configuration: %w", err)
	}
	return nil
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
func (s *WorkloadRoutes) stopWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	// Use the bulk method with a single workload
	// Use background context since this is async operation
	_, err = s.workloadManager.StopWorkloads(context.Background(), []string{name})
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
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
func (s *WorkloadRoutes) restartWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	// Use the bulk method with a single workload
	// Note: In the API, we always assume that the restart is a background operation
	// Use background context since this is async operation
	_, err = s.workloadManager.RestartWorkloads(context.Background(), []string{name}, false)
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
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
func (s *WorkloadRoutes) deleteWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Check if workload exists first
	_, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	// Use the bulk method with a single workload
	// Use background context since this is an async operation
	_, err = s.workloadManager.DeleteWorkloads(context.Background(), []string{name})
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
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
func (s *WorkloadRoutes) createWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("failed to decode request: %w", err),
			http.StatusBadRequest,
		)
	}

	// Validate that image or URL is provided
	if req.Image == "" && req.URL == "" {
		return httperr.WithCode(
			fmt.Errorf("either 'image' or 'url' field is required"),
			http.StatusBadRequest,
		)
	}

	// Validate workload name (strict validation, no sanitization)
	// The JSON decoder sets req.Name to "" by default, so we need to validate it
	if err := wt.ValidateWorkloadName(req.Name); err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}

	// check if the workload already exists
	if req.Name != "" {
		exists, err := s.workloadManager.DoesWorkloadExist(ctx, req.Name)
		if err != nil {
			return fmt.Errorf("failed to check if workload exists: %w", err)
		}
		if exists {
			return httperr.WithCode(
				fmt.Errorf("workload with name %s already exists", req.Name),
				http.StatusConflict,
			)
		}
	}

	// Create the workload using shared logic
	runConfig, err := s.workloadService.CreateWorkloadFromRequest(ctx, &req)
	if err != nil {
		return err // ErrImageNotFound (404) and ErrInvalidRunConfig (400) already have status codes
	}

	// Return name so that the client will get the auto-generated name.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	resp := createWorkloadResponse{
		Name: runConfig.ContainerName,
		Port: runConfig.Port,
	}
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		return fmt.Errorf("failed to marshal workload details: %w", err)
	}
	return nil
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
func (s *WorkloadRoutes) updateWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Parse request body
	var updateReq updateRequest
	if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid JSON: %w", err),
			http.StatusBadRequest,
		)
	}

	// Check if workload exists and get its current port
	existingWorkload, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) already has status code
	}

	// Convert updateRequest to createRequest with the existing workload name
	createReq := createRequest{
		updateRequest: updateReq,
		Name:          name, // Use the name from URL path, not from request body
	}

	// UpdateWorkloadFromRequest uses the request context for synchronous operations
	// (validation, building config). The manager's UpdateWorkload method creates its own
	// background context with timeout for the async operation, so we don't need to create
	// one here.
	runConfig, err := s.workloadService.UpdateWorkloadFromRequest(ctx, name, &createReq, existingWorkload.Port)
	if err != nil {
		return err // ErrImageNotFound (404) and ErrInvalidRunConfig (400) already have status codes
	}

	// Return the same response format as create
	w.Header().Set("Content-Type", "application/json")
	resp := createWorkloadResponse{
		Name: runConfig.ContainerName,
		Port: runConfig.Port,
	}
	if err = json.NewEncoder(w).Encode(resp); err != nil {
		return fmt.Errorf("failed to marshal workload details: %w", err)
	}
	return nil
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
func (s *WorkloadRoutes) stopWorkloadsBulk(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("failed to decode request: %w", err),
			http.StatusBadRequest,
		)
	}

	if err := validateBulkOperationRequest(req); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	// Use background context since this is async operation (handles partial failures gracefully)
	_, err = s.workloadManager.StopWorkloads(context.Background(), workloadNames)
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
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
func (s *WorkloadRoutes) restartWorkloadsBulk(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("failed to decode request: %w", err),
			http.StatusBadRequest,
		)
	}

	if err := validateBulkOperationRequest(req); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	// Note: In the API, we always assume that the restart is a background operation.
	// Use background context since this is async operation (handles partial failures gracefully)
	_, err = s.workloadManager.RestartWorkloads(context.Background(), workloadNames, false)
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
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
func (s *WorkloadRoutes) deleteWorkloadsBulk(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	var req bulkOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("failed to decode request: %w", err),
			http.StatusBadRequest,
		)
	}

	if err := validateBulkOperationRequest(req); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	workloadNames, err := s.workloadService.GetWorkloadNamesFromRequest(ctx, req)
	if err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	// Note that this is an asynchronous operation.
	// The request is not blocked on completion.
	_, err = s.workloadManager.DeleteWorkloads(context.Background(), workloadNames)
	if err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// getLogsForWorkload
//
// @Summary      Get logs for a specific workload
// @Description  Retrieve at most 1000 lines of logs for a specific workload by name.
// @Tags         logs
// @Produce      text/plain
// @Param        name  path      string  true  "Workload name"
// @Success      200   {string}  string  "Logs for the specified workload"
// @Failure      400   {string}  string  "Invalid workload name"
// @Failure      404   {string}  string  "Not Found"
// @Router       /api/v1beta/workloads/{name}/logs [get]
func (s *WorkloadRoutes) getLogsForWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate workload name to prevent path traversal
	if err := wt.ValidateWorkloadName(name); err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}

	logs, err := s.workloadManager.GetLogs(ctx, name, false, maxAPILogLines)
	if err != nil {
		return err // ErrWorkloadNotFound (404) already has status code
	}

	w.Header().Set("Content-Type", "text/plain")
	if _, err = w.Write([]byte(logs)); err != nil {
		return fmt.Errorf("failed to write logs response: %w", err)
	}
	return nil
}

// getProxyLogsForWorkload
//
// @Summary      Get proxy logs for a specific workload
// @Description  Retrieve at most 1000 lines of proxy logs for a specific workload by name from the file system.
// @Tags         logs
// @Produce      text/plain
// @Param        name  path      string  true  "Workload name"
// @Success      200   {string}  string  "Proxy logs for the specified workload"
// @Failure      400   {string}  string  "Invalid workload name"
// @Failure      404   {string}  string  "Proxy logs not found for workload"
// @Router       /api/v1beta/workloads/{name}/proxy-logs [get]
func (s *WorkloadRoutes) getProxyLogsForWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate workload name to prevent path traversal
	if err := wt.ValidateWorkloadName(name); err != nil {
		return err // ErrInvalidWorkloadName already has 400 status code
	}

	logs, err := s.workloadManager.GetProxyLogs(ctx, name, maxAPILogLines)
	if err != nil {
		return httperr.WithCode(
			fmt.Errorf("proxy logs not found for workload: %w", err),
			http.StatusNotFound,
		)
	}

	w.Header().Set("Content-Type", "text/plain")
	if _, err = w.Write([]byte(logs)); err != nil {
		return fmt.Errorf("failed to write proxy logs response: %w", err)
	}
	return nil
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
func (s *WorkloadRoutes) getWorkloadStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	workload, err := s.workloadManager.GetWorkload(ctx, name)
	if err != nil {
		return err // ErrWorkloadNotFound (404) or ErrInvalidWorkloadName (400) already have status codes
	}

	response := workloadStatusResponse{
		Status: workload.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return fmt.Errorf("failed to marshal workload status: %w", err)
	}
	return nil
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
func (*WorkloadRoutes) exportWorkload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Load the saved run configuration
	runConfig, err := runner.LoadState(ctx, name)
	if err != nil {
		return err // ErrRunConfigNotFound (404) already has status code
	}

	// Return the configuration as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(runConfig); err != nil {
		return fmt.Errorf("failed to encode workload configuration: %w", err)
	}
	return nil
}
