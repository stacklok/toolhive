// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/stacklok/toolhive-core/registry/converters"
	types "github.com/stacklok/toolhive-core/registry/types"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/api"
)

// listServersV01 handles GET /registry/{registryName}/v0.1/servers
//
//	@Summary		List available registry servers
//	@Description	Get a paginated list of servers from the registry. Supports optional full-text search and pagination.
//	@Tags			registry-servers
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			q				query		string	false	"Search filter — matches against server name and description"
//	@Param			page			query		integer	false	"Page number, 1-based (default: 1)"
//	@Param			limit			query		integer	false	"Items per page, max 200 (default: 50)"
//	@Success		200				{object}	serversV01Response
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/servers [get]
func listServersV01(w http.ResponseWriter, r *http.Request) {
	provider, ok := getRegistryProvider(w)
	if !ok {
		return
	}

	servers, err := provider.ListServers()
	if err != nil {
		slog.Error("failed to list servers", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to list servers")
		return
	}
	if servers == nil {
		servers = []types.ServerMetadata{}
	}

	// Convert to ServerJSON
	converted := make([]*v0.ServerJSON, 0, len(servers))
	for _, s := range servers {
		sj, convErr := serverMetadataToJSON(s.GetName(), s)
		if convErr != nil {
			slog.Warn("failed to convert server metadata", "name", s.GetName(), "error", convErr)
			continue
		}
		converted = append(converted, sj)
	}

	// Apply search filter
	if q := r.URL.Query().Get("q"); q != "" {
		converted = filterServersV01(converted, q)
	}

	// Paginate
	page, limit := parsePaginationV01(r)
	total := len(converted)
	start, end := paginateSlice(total, page, limit)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(serversV01Response{
		Servers: converted[start:end],
		Metadata: paginationV01Metadata{
			Total: total,
			Page:  page,
			Limit: limit,
		},
	}); err != nil {
		slog.Error("failed to encode servers response", "error", err)
	}
}

// getServerV01 handles GET /registry/{registryName}/v0.1/servers/{serverName}/versions/latest
//
//	@Summary		Get a registry server
//	@Description	Retrieve a single server by name. Names use reverse-DNS format; URL-encode slashes.
//	@Tags			registry-servers
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			serverName		path		string	true	"Server name (URL-encoded reverse-DNS format)"
//	@Success		200				{object}	v0.ServerJSON
//	@Failure		400				{object}	registryErrorResponse	"Invalid server name encoding"
//	@Failure		404				{object}	registryErrorResponse	"Server not found"
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/servers/{serverName}/versions/latest [get]
func getServerV01(w http.ResponseWriter, r *http.Request) {
	serverName := chi.URLParam(r, "serverName")

	// Server names use reverse-DNS format with slashes (e.g. io.github.stacklok/fetch).
	// Clients URL-encode the slash as %2F, so we must decode it here.
	decoded, err := url.PathUnescape(serverName)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "Invalid server name encoding")
		return
	}
	serverName = decoded

	provider, ok := getRegistryProvider(w)
	if !ok {
		return
	}

	server, err := provider.GetServer(serverName)
	if err != nil {
		// Map upstream HTTP errors to appropriate responses
		var httpErr *api.RegistryHTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusNotFound:
				writeJSONError(w, http.StatusNotFound, "not_found", "Server not found")
				return
			case http.StatusUnauthorized, http.StatusForbidden:
				writeRegistryAuthRequiredError(w)
				return
			}
		}
		// Sentinel error from base/API providers
		if errors.Is(err, regpkg.ErrServerNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "Server not found")
			return
		}
		slog.Error("failed to get server", "name", serverName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to get server")
		return
	}
	if server == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "Server not found")
		return
	}

	sj, convErr := serverMetadataToJSON(server.GetName(), server)
	if convErr != nil {
		slog.Error("failed to convert server metadata", "name", serverName, "error", convErr)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to convert server metadata")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sj); err != nil {
		slog.Error("failed to encode server response", "error", err)
	}
}

// serverMetadataToJSON converts a ServerMetadata interface value to the upstream
// ServerJSON format using the appropriate converter from toolhive-core.
func serverMetadataToJSON(name string, md types.ServerMetadata) (*v0.ServerJSON, error) {
	switch m := md.(type) {
	case *types.ImageMetadata:
		return converters.ImageMetadataToServerJSON(name, m)
	case *types.RemoteServerMetadata:
		return converters.RemoteServerMetadataToServerJSON(name, m)
	default:
		return nil, fmt.Errorf("unknown server type: %T", md)
	}
}

// filterServersV01 returns servers whose name or description contains the
// query string (case-insensitive).
func filterServersV01(servers []*v0.ServerJSON, query string) []*v0.ServerJSON {
	q := strings.ToLower(query)
	result := make([]*v0.ServerJSON, 0)
	for _, s := range servers {
		if strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			result = append(result, s)
		}
	}
	return result
}

// serversV01Response is the response body for the v0.1 servers list endpoint.
//
//	@Description	Paginated list of servers from the registry
type serversV01Response struct {
	// Servers is the list of servers on the current page
	Servers []*v0.ServerJSON `json:"servers"`
	// Metadata contains pagination information
	Metadata paginationV01Metadata `json:"metadata"`
}
