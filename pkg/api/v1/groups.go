package v1

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// GroupsRoutes defines the routes for group management.
type GroupsRoutes struct {
	groupManager    groups.Manager
	workloadManager workloads.Manager
}

// GroupsRouter creates a new GroupsRoutes instance.
func GroupsRouter(groupManager groups.Manager, workloadManager workloads.Manager) http.Handler {
	routes := GroupsRoutes{
		groupManager:    groupManager,
		workloadManager: workloadManager,
	}

	r := chi.NewRouter()
	r.Get("/", routes.listGroups)
	r.Post("/", routes.createGroup)
	r.Get("/{name}", routes.getGroup)
	r.Delete("/{name}", routes.deleteGroup)

	return r
}

//	@title			ToolHive API
//	@version		1.0
//	@description	This is the ToolHive API groups.
//	@groups		[ { "url": "http://localhost:8080/api/v1beta" } ]
//	@basePath		/api/v1beta

// listGroups
//
//	@Summary		List all groups
//	@Description	Get a list of all groups
//	@Tags			groups
//	@Produce		json
//	@Success		200	{object}	groupListResponse
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/groups [get]
func (s *GroupsRoutes) listGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupList, err := s.groupManager.List(ctx)
	if err != nil {
		logger.Errorf("Failed to list groups: %v", err)
		http.Error(w, "Failed to list groups", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(groupListResponse{Groups: groupList})
	if err != nil {
		logger.Errorf("Failed to marshal group list: %v", err)
		http.Error(w, "Failed to marshal group list", http.StatusInternalServerError)
		return
	}
}

// createGroup
//
//	@Summary		Create a new group
//	@Description	Create a new group with the specified name
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Param			group	body		createGroupRequest	true	"Group creation request"
//	@Success		201		{object}	createGroupResponse
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		409		{string}	string	"Conflict"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/groups [post]
func (s *GroupsRoutes) createGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode create group request: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate group name
	if err := validation.ValidateGroupName(req.Name); err != nil {
		logger.Errorf("Invalid group name: %v", err)
		http.Error(w, fmt.Sprintf("Invalid group name: %v", err), http.StatusBadRequest)
		return
	}

	err := s.groupManager.Create(ctx, req.Name)
	if err != nil {
		logger.Errorf("Failed to create group: %v", err)
		if errors.IsGroupAlreadyExists(err) {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, "Failed to create group", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	response := createGroupResponse(req)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("Failed to marshal create group response: %v", err)
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}
}

// getGroup
//
//	@Summary		Get group details
//	@Description	Get details of a specific group
//	@Tags			groups
//	@Produce		json
//	@Param			name	path		string	true	"Group name"
//	@Success		200		{object}	groups.Group
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/groups/{name} [get]
func (s *GroupsRoutes) getGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate group name
	if err := validation.ValidateGroupName(name); err != nil {
		logger.Errorf("Invalid group name: %v", err)
		http.Error(w, fmt.Sprintf("Invalid group name: %v", err), http.StatusBadRequest)
		return
	}

	group, err := s.groupManager.Get(ctx, name)
	if err != nil {
		logger.Errorf("Failed to get group %s: %v", name, err)
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(group); err != nil {
		logger.Errorf("Failed to marshal group: %v", err)
		http.Error(w, "Failed to marshal group", http.StatusInternalServerError)
		return
	}
}

// deleteGroup
//
//	@Summary		Delete a group
//	@Description	Delete a group by name.
//	Use with-workloads=true to delete all workloads in the group, otherwise workloads are moved to the default group.
//	@Tags			groups
//	@Param			name	path		string	true	"Group name"
//	@Param			with-workloads	query	bool	false	"Delete all workloads in the group (default: false, moves workloads to default group)"
//	@Success		204		{string}	string	"No Content"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/groups/{name} [delete]
func (s *GroupsRoutes) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate group name
	if err := validation.ValidateGroupName(name); err != nil {
		logger.Errorf("Invalid group name: %v", err)
		http.Error(w, fmt.Sprintf("Invalid group name: %v", err), http.StatusBadRequest)
		return
	}

	// Check if this is the default group
	if name == groups.DefaultGroup {
		http.Error(w, "Cannot delete the default group", http.StatusBadRequest)
		return
	}

	// Check if group exists before deleting
	exists, err := s.groupManager.Exists(ctx, name)
	if err != nil {
		logger.Errorf("Failed to check if group exists %s: %v", name, err)
		http.Error(w, "Failed to check group existence", http.StatusInternalServerError)
		return
	}

	if !exists {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	// Get the with-workloads flag from query parameter
	withWorkloads := r.URL.Query().Get("with-workloads") == "true"

	// Get all workloads in the group
	groupWorkloads, err := s.workloadManager.ListWorkloadsInGroup(ctx, name)
	if err != nil {
		logger.Errorf("Failed to list workloads in group %s: %v", name, err)
		http.Error(w, "Failed to list workloads in group", http.StatusInternalServerError)
		return
	}

	// Handle workloads if any exist
	if len(groupWorkloads) > 0 {
		if withWorkloads {
			// Delete all workloads in the group
			group, err := s.workloadManager.DeleteWorkloads(ctx, groupWorkloads)
			if err != nil {
				logger.Errorf("Failed to delete workloads in group %s: %v", name, err)
				http.Error(w, "Failed to delete workloads in group", http.StatusInternalServerError)
				return
			}

			// Wait for the deletion to complete
			if err := group.Wait(); err != nil {
				logger.Errorf("Failed to delete workloads in group %s: %v", name, err)
				http.Error(w, "Failed to delete workloads in group", http.StatusInternalServerError)
				return
			}

			logger.Infof("Deleted %d workload(s) from group '%s'", len(groupWorkloads), name)
		} else {
			// Move workloads to default group
			if err := s.workloadManager.MoveToDefaultGroup(ctx, groupWorkloads, name); err != nil {
				logger.Errorf("Failed to move workloads to default group: %v", err)
				http.Error(w, "Failed to move workloads to default group", http.StatusInternalServerError)
				return
			}

			logger.Infof("Moved %d workload(s) from group '%s' to default group", len(groupWorkloads), name)
		}
	}

	// Delete the group
	err = s.groupManager.Delete(ctx, name)
	if err != nil {
		logger.Errorf("Failed to delete group %s: %v", name, err)
		http.Error(w, "Failed to delete group", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Response types

type groupListResponse struct {
	// List of groups
	Groups []*groups.Group `json:"groups"`
}

type createGroupRequest struct {
	// Name of the group to create
	Name string `json:"name"`
}

type createGroupResponse struct {
	// Name of the created group
	Name string `json:"name"`
}
