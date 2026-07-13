// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive-core/httperr"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// PluginsRoutes defines the routes for plugin management.
type PluginsRoutes struct {
	pluginService plugins.PluginService
}

// PluginsRouter creates a new router for plugin management endpoints.
func PluginsRouter(pluginService plugins.PluginService) http.Handler {
	routes := PluginsRoutes{
		pluginService: pluginService,
	}

	r := chi.NewRouter()
	r.Get("/", apierrors.ErrorHandler(routes.listPlugins))
	r.Post("/", apierrors.ErrorHandler(routes.installPlugin))
	r.Delete("/{name}", apierrors.ErrorHandler(routes.uninstallPlugin))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getPluginInfo))
	r.Post("/validate", apierrors.ErrorHandler(routes.validatePlugin))
	r.Post("/build", apierrors.ErrorHandler(routes.buildPlugin))
	r.Post("/push", apierrors.ErrorHandler(routes.pushPlugin))
	r.Get("/builds", apierrors.ErrorHandler(routes.listBuilds))
	r.Delete("/builds/{tag}", apierrors.ErrorHandler(routes.deleteBuild))
	r.Get("/content", apierrors.ErrorHandler(routes.getPluginContent))

	return r
}

// listPlugins returns a list of installed plugins.
//
//	@Summary		List all installed plugins
//	@Description	Get a list of all installed plugins
//	@Tags			plugins
//	@Produce		json
//	@Param			scope	query		string	false	"Filter by scope (user or project)"	Enums(user, project)
//	@Param			client	query		string	false	"Filter by client app"
//	@Param			project_root	query	string	false	"Filter by project root path"
//	@Param			group	query		string	false	"Filter by group name"
//	@Success		200		{object}	pluginListResponse
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins [get]
func (s *PluginsRoutes) listPlugins(w http.ResponseWriter, r *http.Request) error {
	scope := plugins.Scope(r.URL.Query().Get("scope"))
	projectRoot := r.URL.Query().Get("project_root")
	client := r.URL.Query().Get("client")
	group := r.URL.Query().Get("group")

	result, err := s.pluginService.List(r.Context(), plugins.ListOptions{
		Scope:       scope,
		ClientApp:   client,
		ProjectRoot: projectRoot,
		Group:       group,
	})
	if err != nil {
		return err
	}

	if result == nil {
		result = []plugins.InstalledPlugin{}
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(pluginListResponse{Plugins: result})
}

// installPlugin installs a plugin from a remote source.
//
//	@Summary		Install a plugin
//	@Description	Install a plugin from a remote source
//	@Tags			plugins
//	@Accept			json
//	@Produce		json
//	@Param			request	body		installPluginRequest	true	"Install request"
//	@Success		201		{object}	installPluginResponse
//	@Header			201		{string}	Location	"URI of the installed plugin resource"
//	@Failure		400		{string}	string		"Bad Request"
//	@Failure		401		{string}	string		"Unauthorized (registry refused credentials)"
//	@Failure		404		{string}	string		"Not Found (artifact not present in registry)"
//	@Failure		409		{string}	string		"Conflict"
//	@Failure		429		{string}	string		"Too Many Requests (registry rate limit)"
//	@Failure		500		{string}	string		"Internal Server Error"
//	@Failure		502		{string}	string		"Bad Gateway (upstream registry failure)"
//	@Failure		504		{string}	string		"Gateway Timeout (upstream pull timed out)"
//	@Router			/api/v1beta/plugins [post]
func (s *PluginsRoutes) installPlugin(w http.ResponseWriter, r *http.Request) error {
	var req installPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.pluginService.Install(r.Context(), plugins.InstallOptions{
		Name:        req.Name,
		Version:     req.Version,
		Scope:       req.Scope,
		ProjectRoot: req.ProjectRoot,
		Clients:     req.Clients,
		Force:       req.Force,
		Group:       req.Group,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", fmt.Sprintf("/api/v1beta/plugins/%s", result.Plugin.Metadata.Name))
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(installPluginResponse{Plugin: result.Plugin})
}

// uninstallPlugin removes an installed plugin.
//
//	@Summary		Uninstall a plugin
//	@Description	Remove an installed plugin
//	@Tags			plugins
//	@Param			name	path		string	true	"Plugin name"
//	@Param			scope	query		string	false	"Scope to uninstall from (user or project)"	Enums(user, project)
//	@Param			project_root	query	string	false	"Project root path for project-scoped plugins"
//	@Success		204		{string}	string	"No Content"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/{name} [delete]
func (s *PluginsRoutes) uninstallPlugin(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")

	if err := plugins.ValidatePluginName(name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := plugins.Scope(r.URL.Query().Get("scope"))
	projectRoot := r.URL.Query().Get("project_root")

	if err := s.pluginService.Uninstall(r.Context(), plugins.UninstallOptions{
		Name:        name,
		Scope:       scope,
		ProjectRoot: projectRoot,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getPluginInfo returns detailed information about a plugin.
//
//	@Summary		Get plugin details
//	@Description	Get detailed information about a specific plugin
//	@Tags			plugins
//	@Produce		json
//	@Param			name	path		string	true	"Plugin name"
//	@Param			scope	query		string	false	"Filter by scope (user or project)"	Enums(user, project)
//	@Param			project_root	query	string	false	"Project root path for project-scoped plugins"
//	@Success		200		{object}	plugins.PluginInfo
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/{name} [get]
func (s *PluginsRoutes) getPluginInfo(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")

	if err := plugins.ValidatePluginName(name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := plugins.Scope(r.URL.Query().Get("scope"))
	projectRoot := r.URL.Query().Get("project_root")

	info, err := s.pluginService.Info(r.Context(), plugins.InfoOptions{
		Name:        name,
		Scope:       scope,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(info)
}

// validatePlugin checks whether a plugin definition is valid.
//
//	@Summary		Validate a plugin
//	@Description	Validate a plugin definition
//	@Tags			plugins
//	@Accept			json
//	@Produce		json
//	@Param			request	body		validatePluginRequest	true	"Validate request"
//	@Success		200		{object}	plugins.ValidationResult
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/validate [post]
func (s *PluginsRoutes) validatePlugin(w http.ResponseWriter, r *http.Request) error {
	var req validatePluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	if req.Path == "" {
		return httperr.WithCode(
			fmt.Errorf("path is required"),
			http.StatusBadRequest,
		)
	}

	result, err := s.pluginService.Validate(r.Context(), req.Path)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
}

// buildPlugin builds a plugin from a local directory into an OCI artifact.
//
//	@Summary		Build a plugin
//	@Description	Build a plugin from a local directory
//	@Tags			plugins
//	@Accept			json
//	@Produce		json
//	@Param			request	body		buildPluginRequest	true	"Build request"
//	@Success		200		{object}	plugins.BuildResult
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/build [post]
func (s *PluginsRoutes) buildPlugin(w http.ResponseWriter, r *http.Request) error {
	var req buildPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	if req.Path == "" {
		return httperr.WithCode(
			fmt.Errorf("path is required"),
			http.StatusBadRequest,
		)
	}

	result, err := s.pluginService.Build(r.Context(), plugins.BuildOptions{
		Path: req.Path,
		Tag:  req.Tag,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
}

// pushPlugin pushes a built plugin artifact to a remote registry.
//
//	@Summary		Push a plugin
//	@Description	Push a built plugin artifact to a remote registry
//	@Tags			plugins
//	@Accept			json
//	@Param			request	body	pushPluginRequest	true	"Push request"
//	@Success		204		{string}	string	"No Content"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/push [post]
func (s *PluginsRoutes) pushPlugin(w http.ResponseWriter, r *http.Request) error {
	var req pushPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	if req.Reference == "" {
		return httperr.WithCode(
			fmt.Errorf("reference is required"),
			http.StatusBadRequest,
		)
	}

	if err := s.pluginService.Push(r.Context(), plugins.PushOptions{
		Reference: req.Reference,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// listBuilds returns a list of locally-built OCI plugin artifacts.
//
//	@Summary		List locally-built plugin artifacts
//	@Description	Get a list of all locally-built OCI plugin artifacts in the local store
//	@Tags			plugins
//	@Produce		json
//	@Success		200		{object}	buildListResponse
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/builds [get]
func (s *PluginsRoutes) listBuilds(w http.ResponseWriter, r *http.Request) error {
	builds, err := s.pluginService.ListBuilds(r.Context())
	if err != nil {
		return err
	}

	if builds == nil {
		builds = []plugins.LocalBuild{}
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(pluginBuildListResponse{Builds: builds})
}

// deleteBuild removes a locally-built OCI plugin artifact from the local store.
//
//	@Summary		Delete a locally-built plugin artifact
//	@Description	Remove a locally-built OCI plugin artifact and its blobs from the local store
//	@Tags			plugins
//	@Param			tag	path		string	true	"Artifact tag"
//	@Success		204	{string}	string	"No Content"
//	@Failure		404	{string}	string	"Not Found"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/plugins/builds/{tag} [delete]
func (s *PluginsRoutes) deleteBuild(w http.ResponseWriter, r *http.Request) error {
	tag := chi.URLParam(r, "tag")
	if err := s.pluginService.DeleteBuild(r.Context(), tag); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getPluginContent retrieves the plugin.json body and file listing from an OCI artifact.
//
//	@Summary		Get plugin content
//	@Description	Retrieve the plugin.json body and file listing from an artifact
//	@Description	without installing it. Accepts OCI refs, git refs, or local tags.
//	@Tags			plugins
//	@Produce		json
//	@Param			ref	query		string	true	"OCI reference or local build tag"
//	@Success		200	{object}	plugins.PluginContent
//	@Failure		400	{string}	string	"Bad Request"
//	@Failure		401	{string}	string	"Unauthorized (registry refused credentials)"
//	@Failure		404	{string}	string	"Not Found (artifact not present in registry)"
//	@Failure		429	{string}	string	"Too Many Requests (registry rate limit)"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Failure		502	{string}	string	"Bad Gateway (upstream registry or git resolver failure)"
//	@Failure		504	{string}	string	"Gateway Timeout (upstream pull timed out)"
//	@Router			/api/v1beta/plugins/content [get]
func (s *PluginsRoutes) getPluginContent(w http.ResponseWriter, r *http.Request) error {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		return httperr.WithCode(
			fmt.Errorf("ref query parameter is required"),
			http.StatusBadRequest,
		)
	}

	content, err := s.pluginService.GetContent(r.Context(), plugins.ContentOptions{
		Reference: ref,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(content)
}
