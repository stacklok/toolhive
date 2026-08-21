// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/stacklok/toolhive-core/httperr"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// PluginsRoutes defines the routes for plugin management.
type PluginsRoutes struct {
	pluginService plugins.PluginService
	lockService   plugins.PluginLockService
}

// PluginsRouter creates a new router for plugin management endpoints. If
// pluginService's concrete implementation also satisfies plugins.PluginLockService
// (as pluginsvc.New's does), /sync and /upgrade are served; otherwise both
// return 501.
func PluginsRouter(pluginService plugins.PluginService) http.Handler {
	routes := PluginsRoutes{
		pluginService: pluginService,
	}
	if lockSvc, ok := pluginService.(plugins.PluginLockService); ok {
		routes.lockService = lockSvc
	}

	// Mirrors WorkloadRouter and SkillsRouter: routes that move OCI artifacts
	// get a timeout sized for the transfer, everything else keeps the short
	// one. Without the split these routes inherit the flat 60s applied to
	// standard routers, which severs a cold artifact pull mid-flight.
	stdTimeout := middleware.Timeout(standardRouteTimeout)
	longTimeout := middleware.Timeout(longRunningRouteTimeout)

	r := chi.NewRouter()
	r.With(stdTimeout).Get("/", apierrors.ErrorHandler(routes.listPlugins))
	r.With(longTimeout).Post("/", apierrors.ErrorHandler(routes.installPlugin))
	r.With(stdTimeout).Delete("/{name}", apierrors.ErrorHandler(routes.uninstallPlugin))
	r.With(stdTimeout).Get("/{name}", apierrors.ErrorHandler(routes.getPluginInfo))
	r.With(stdTimeout).Post("/validate", apierrors.ErrorHandler(routes.validatePlugin))
	r.With(longTimeout).Post("/build", apierrors.ErrorHandler(routes.buildPlugin))
	r.With(longTimeout).Post("/push", apierrors.ErrorHandler(routes.pushPlugin))
	r.With(stdTimeout).Get("/builds", apierrors.ErrorHandler(routes.listBuilds))
	r.With(stdTimeout).Delete("/builds/{tag}", apierrors.ErrorHandler(routes.deleteBuild))
	r.With(stdTimeout).Get("/content", apierrors.ErrorHandler(routes.getPluginContent))
	r.With(longTimeout).Post("/sync", apierrors.ErrorHandler(routes.syncPlugins))
	r.With(longTimeout).Post("/upgrade", apierrors.ErrorHandler(routes.upgradePlugins))

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
		Name:          req.Name,
		Version:       req.Version,
		Scope:         req.Scope,
		ProjectRoot:   req.ProjectRoot,
		Clients:       req.Clients,
		Force:         req.Force,
		Group:         req.Group,
		AllowUnsigned: req.AllowUnsigned,
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
//	@Success		200		{object}	pluginBuildListResponse
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

// syncPlugins restores a project's installed plugins to match its lock file.
//
//	@Summary		Sync project plugins from the lock file
//	@Description	Restore a project's installed plugins to match toolhive.lock.yaml
//	@Tags			plugins
//	@Accept			json
//	@Produce		json
//	@Param			request	body		syncPluginsRequest	true	"Sync request"
//	@Success		200		{object}	plugins.SyncResult
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		403		{string}	string	"Forbidden (feature not enabled)"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Failure		501		{string}	string	"Not Implemented"
//	@Router			/api/v1beta/plugins/sync [post]
func (s *PluginsRoutes) syncPlugins(w http.ResponseWriter, r *http.Request) error {
	if s.lockService == nil {
		return httperr.WithCode(errors.New("plugin sync is not supported by this server"), http.StatusNotImplemented)
	}

	var req syncPluginsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.lockService.Sync(r.Context(), plugins.SyncOptions{
		ProjectRoot:   req.ProjectRoot,
		Clients:       req.Clients,
		Prune:         req.Prune,
		Check:         req.Check,
		Adopt:         req.Adopt,
		AllowUnsigned: req.AllowUnsigned,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
}

// upgradePlugins re-resolves a project's lock entries and installs newer
// content where available. With fail_on_changes set, it evaluates the plan
// and returns the outcomes without installing anything — clients derive the
// freshness verdict from the outcome statuses, mirroring sync's check mode.
//
//	@Summary		Upgrade project plugins
//	@Description	Re-resolve a project's lock entries and install newer content where available
//	@Tags			plugins
//	@Accept			json
//	@Produce		json
//	@Param			request	body		upgradePluginsRequest	true	"Upgrade request"
//	@Success		200		{object}	plugins.UpgradeResult
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		403		{string}	string	"Forbidden (feature not enabled)"
//	@Failure		404		{string}	string	"Not Found (a requested name is not in the lock file)"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Failure		501		{string}	string	"Not Implemented"
//	@Router			/api/v1beta/plugins/upgrade [post]
func (s *PluginsRoutes) upgradePlugins(w http.ResponseWriter, r *http.Request) error {
	if s.lockService == nil {
		return httperr.WithCode(errors.New("plugin upgrade is not supported by this server"), http.StatusNotImplemented)
	}

	var req upgradePluginsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.lockService.Upgrade(r.Context(), plugins.UpgradeOptions{
		ProjectRoot:    req.ProjectRoot,
		Names:          req.Names,
		Preview:        req.Preview,
		FailOnChanges:  req.FailOnChanges,
		AllowRefChange: req.AllowRefChange,
		Clients:        req.Clients,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
}
