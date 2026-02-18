// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive-core/httperr"
	groupval "github.com/stacklok/toolhive-core/validation/group"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// GroupsRoutes defines the routes for group management.
type GroupsRoutes struct {
	groupManager    groups.Manager
	workloadManager workloads.Manager
	clientManager   client.Manager
}

// GroupsRouter creates a new GroupsRoutes instance.
func GroupsRouter(groupManager groups.Manager, workloadManager workloads.Manager, clientManager client.Manager) http.Handler {
	routes := GroupsRoutes{
		groupManager:    groupManager,
		workloadManager: workloadManager,
		clientManager:   clientManager,
	}

	r := chi.NewRouter()
	r.Get("/", apierrors.ErrorHandler(routes.listGroups))
	r.Post("/", apierrors.ErrorHandler(routes.createGroup))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getGroup))
	r.Delete("/{name}", apierrors.ErrorHandler(routes.deleteGroup))

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
func (s *GroupsRoutes) listGroups(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	groupList, err := s.groupManager.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(groupListResponse{Groups: groupList}); err != nil {
		return fmt.Errorf("failed to marshal group list: %w", err)
	}
	return nil
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
func (s *GroupsRoutes) createGroup(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	var req createGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	// Validate group name
	if err := groupval.ValidateName(req.Name); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid group name: %w", err),
			http.StatusBadRequest,
		)
	}

	err := s.groupManager.Create(ctx, req.Name)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	response := createGroupResponse(req)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return fmt.Errorf("failed to marshal create group response: %w", err)
	}
	return nil
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
func (s *GroupsRoutes) getGroup(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate group name
	if err := groupval.ValidateName(name); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid group name: %w", err),
			http.StatusBadRequest,
		)
	}

	group, err := s.groupManager.Get(ctx, name)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(group); err != nil {
		return fmt.Errorf("failed to marshal group: %w", err)
	}
	return nil
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
func (s *GroupsRoutes) deleteGroup(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Validate group name
	if err := groupval.ValidateName(name); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid group name: %w", err),
			http.StatusBadRequest,
		)
	}

	// Check if this is the default group
	if name == groups.DefaultGroup {
		return httperr.WithCode(
			fmt.Errorf("cannot delete the default group"),
			http.StatusBadRequest,
		)
	}

	// Check if group exists before deleting
	exists, err := s.groupManager.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check group existence: %w", err)
	}

	if !exists {
		return groups.ErrGroupNotFound
	}

	// Get the with-workloads flag from query parameter
	withWorkloads := r.URL.Query().Get("with-workloads") == "true" //nolint:goconst // Query parameter check

	// Get all workloads and filter for the group
	allWorkloads, err := s.workloadManager.ListWorkloads(ctx, true) // listAll=true to include stopped workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	groupWorkloads, err := workloads.FilterByGroup(allWorkloads, name)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by group: %w", err)
	}

	// Handle workloads if any exist
	if len(groupWorkloads) > 0 {
		if err := s.handleWorkloadsForGroupDeletion(ctx, name, groupWorkloads, withWorkloads); err != nil {
			return fmt.Errorf("failed to handle workloads: %w", err)
		}
	}

	// Delete the group
	err = s.groupManager.Delete(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleWorkloadsForGroupDeletion handles workloads when deleting a group
func (s *GroupsRoutes) handleWorkloadsForGroupDeletion(
	ctx context.Context,
	groupName string,
	groupWorkloads []core.Workload,
	withWorkloads bool,
) error {
	// Extract workload names
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	if withWorkloads {
		// Delete all workloads in the group
		complete, err := s.workloadManager.DeleteWorkloads(ctx, workloadNames)
		if err != nil {
			return fmt.Errorf("failed to delete workloads in group %s: %w", groupName, err)
		}

		// Wait for the deletion to complete
		if err := complete(); err != nil {
			return fmt.Errorf("failed to delete workloads in group %s: %w", groupName, err)
		}

		//nolint:gosec // G706: group name from URL parameter for diagnostics
		slog.Debug("deleted workloads from group", "count", len(groupWorkloads), "group", groupName)
	} else {
		// Move workloads to default group
		if err := s.workloadManager.MoveToGroup(ctx, workloadNames, groupName, groups.DefaultGroup); err != nil {
			return fmt.Errorf("failed to move workloads to default group: %w", err)
		}

		// Update client configurations for the moved workloads
		if err := s.updateClientConfigurations(ctx, groupWorkloads, groupName, groups.DefaultGroup); err != nil {
			return fmt.Errorf("failed to update client configurations: %w", err)
		}

		//nolint:gosec // G706: group name from URL parameter for diagnostics
		slog.Debug("moved workloads to default group", "count", len(groupWorkloads), "group", groupName)
	}

	return nil
}

// updateClientConfigurations updates client configurations when workloads are moved between groups
func (s *GroupsRoutes) updateClientConfigurations(
	ctx context.Context,
	groupWorkloads []core.Workload,
	groupFrom string,
	groupTo string,
) error {
	for _, w := range groupWorkloads {
		// Only update client configurations for running workloads
		if w.Status != runtime.WorkloadStatusRunning {
			continue
		}

		if err := s.clientManager.RemoveServerFromClients(ctx, w.Name, groupFrom); err != nil {
			return fmt.Errorf("failed to remove server %s from client configurations: %w", w.Name, err)
		}
		if err := s.clientManager.AddServerToClients(ctx, w.Name, w.URL, string(w.TransportType), groupTo); err != nil {
			return fmt.Errorf("failed to add server %s to client configurations: %w", w.Name, err)
		}
	}

	return nil
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
