// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/stacklok/toolhive-core/registry/types"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/api"
)

// listPluginsV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/plugins
//
//	@Summary		List available registry plugins
//	@Description	Get a paginated list of plugins from the registry. Supports optional full-text search and pagination.
//	@Tags			registry-plugins
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			q				query		string	false	"Search filter — matches against plugin name, namespace, and description"
//	@Param			page			query		integer	false	"Page number, 1-based (default: 1)"
//	@Param			limit			query		integer	false	"Items per page, max 200 (default: 50)"
//	@Success		200				{object}	pluginsV01Response
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/x/dev.toolhive/plugins [get]
func listPluginsV01(w http.ResponseWriter, r *http.Request) {
	provider, ok := getRegistryProvider(w)
	if !ok {
		return
	}

	plugins, err := provider.ListAvailablePlugins()
	if err != nil {
		slog.Error("failed to list plugins", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to list plugins")
		return
	}
	if plugins == nil {
		plugins = []types.Plugin{}
	}

	// Apply search filter
	if q := r.URL.Query().Get("q"); q != "" {
		plugins = filterPluginsV01(plugins, q)
	}

	// Paginate
	page, limit := parsePaginationV01(r)
	total := len(plugins)
	start, end := paginateSlice(total, page, limit)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(pluginsV01Response{
		Plugins: plugins[start:end],
		Metadata: paginationV01Metadata{
			Total: total,
			Page:  page,
			Limit: limit,
		},
	}); err != nil {
		slog.Error("failed to encode plugins response", "error", err)
	}
}

// getPluginV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/plugins/{namespace}/{pluginName}
//
//	@Summary		Get a registry plugin
//	@Description	Retrieve a single plugin by its namespace and name from the registry.
//	@Tags			registry-plugins
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			namespace		path		string	true	"Plugin namespace in reverse-DNS format (e.g. io.github.stacklok)"
//	@Param			pluginName		path		string	true	"Plugin name"
//	@Success		200				{object}	types.Plugin
//	@Failure		404				{object}	registryErrorResponse	"Plugin not found"
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/x/dev.toolhive/plugins/{namespace}/{pluginName} [get]
func getPluginV01(w http.ResponseWriter, r *http.Request) {
	provider, ok := getRegistryProvider(w)
	if !ok {
		return
	}
	getPluginV01WithProvider(w, r, provider)
}

// getPluginV01WithProvider contains the Get-plugin logic against an explicit
// provider, separated from getRegistryProvider so tests can inject a mock
// provider without touching the process-wide singleton.
func getPluginV01WithProvider(w http.ResponseWriter, r *http.Request, provider regpkg.Provider) {
	namespace := chi.URLParam(r, "namespace")
	pluginName := chi.URLParam(r, "pluginName")

	plugin, err := provider.GetPlugin(namespace, pluginName)
	if err != nil {
		// Map upstream HTTP errors to appropriate responses
		var httpErr *api.RegistryHTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusNotFound:
				writeJSONError(w, http.StatusNotFound, "not_found", "Plugin not found")
				return
			case http.StatusUnauthorized, http.StatusForbidden:
				writeRegistryAuthRequiredError(w)
				return
			}
		}
		slog.Error("failed to get plugin", "namespace", namespace, "name", pluginName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to get plugin")
		return
	}
	if plugin == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "Plugin not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(plugin); err != nil {
		slog.Error("failed to encode plugin response", "error", err)
	}
}

func filterPluginsV01(plugins []types.Plugin, query string) []types.Plugin {
	q := strings.ToLower(query)
	result := make([]types.Plugin, 0)
	for _, p := range plugins {
		if strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.Namespace), q) ||
			strings.Contains(strings.ToLower(p.Description), q) {
			result = append(result, p)
		}
	}
	return result
}

// pluginsV01Response is the response body for the v0.1 plugins list endpoint.
//
//	@Description	Paginated list of plugins from the registry
type pluginsV01Response struct {
	// Plugins is the list of plugins on the current page
	Plugins []types.Plugin `json:"plugins"`
	// Metadata contains pagination information
	Metadata paginationV01Metadata `json:"metadata"`
}
