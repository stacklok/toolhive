// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
)

// serversV01Response is the JSON envelope for the paginated server list.
type serversV01Response struct {
	Servers  []*v0.ServerJSON      `json:"servers"`
	Metadata paginationV01Metadata `json:"metadata"`
}

// listServersV01 handles GET /registry/{registryName}/v0.1/servers
func listServersV01(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "registryName")
	if proxyIfNeeded(w, r, registryName) {
		return
	}

	store, ok := getRegistryStore(w)
	if !ok {
		return
	}
	servers, err := store.ListServers(registryName)
	if err != nil {
		slog.Error("failed to list servers", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to list servers")
		return
	}
	if servers == nil {
		servers = []*v0.ServerJSON{}
	}

	// Apply search filter
	if q := r.URL.Query().Get("q"); q != "" {
		servers = filterServersV01(servers, q)
	}

	// Paginate
	page, limit := parsePaginationV01(r)
	start, end, meta := paginateSlice(len(servers), page, limit)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(serversV01Response{
		Servers:  servers[start:end],
		Metadata: meta,
	}); err != nil {
		slog.Error("failed to encode servers response", "error", err)
	}
}

// getServerV01 handles GET /registry/{registryName}/v0.1/servers/{serverName}/versions/latest
func getServerV01(w http.ResponseWriter, r *http.Request) {
	registryName := chi.URLParam(r, "registryName")
	if proxyIfNeeded(w, r, registryName) {
		return
	}

	serverName := chi.URLParam(r, "serverName")

	// URL-decode the server name to handle special characters (server names
	// use reverse-DNS format like io.github.stacklok/fetch, so clients
	// percent-encode the slash).
	decodedName, err := url.PathUnescape(serverName)
	if err != nil {
		decodedName = serverName
	}

	store, ok := getRegistryStore(w)
	if !ok {
		return
	}

	server, err := store.GetServer(registryName, decodedName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "Server not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(server); err != nil {
		slog.Error("failed to encode server response", "error", err)
	}
}

// filterServersV01 returns the subset of servers whose Name or Description
// contains the query string (case-insensitive).
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
