// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	regpkg "github.com/stacklok/toolhive/pkg/registry"
)

const (
	// v01DefaultLimit is the default page size for v0.1 paginated endpoints.
	v01DefaultLimit = 50
	// v01MaxLimit is the maximum page size for v0.1 paginated endpoints.
	v01MaxLimit = 200
)

// RegistryV01Router creates a combined router for all v0.1 registry endpoints.
// It mounts both the standard server endpoints and the extension (skills)
// endpoints under /registry/{registryName}/v0.1.
//
// The {registryName} path param selects which registry to query. It is passed
// through to Store methods, which resolve an empty name to the default registry.
func RegistryV01Router() http.Handler {
	r := chi.NewRouter()
	r.Route("/{registryName}/v0.1", func(r chi.Router) {
		// Servers
		r.Get("/servers", listServersV01)
		r.Get("/servers/{serverName}/versions/latest", getServerV01)

		// Skills (extension namespace)
		r.Get("/x/dev.toolhive/skills", listSkillsV01)
		r.Get("/x/dev.toolhive/skills/{namespace}/{skillName}", getSkillV01)
	})
	return r
}

// --- Shared helpers for v0.1 endpoints ---

// paginationV01Metadata is the pagination metadata included in paginated v0.1
// list responses.
type paginationV01Metadata struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
}

// getRegistryStore returns the default registry Store, writing a structured
// JSON error response if the Store cannot be obtained. Returns false when the
// Store is unavailable and the caller should abort.
func getRegistryStore(w http.ResponseWriter) (*regpkg.Store, bool) {
	store, err := regpkg.DefaultStore()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to get registry store")
		slog.Error("failed to get registry store", "error", err)
		return nil, false
	}
	return store, true
}

// parsePaginationV01 parses page and limit query parameters, returning safe
// defaults when the values are missing, invalid, or out of range.
func parsePaginationV01(r *http.Request) (page, limit int) {
	page = 1
	limit = v01DefaultLimit
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= v01MaxLimit {
			limit = v
		}
	}
	return page, limit
}

// paginateSlice computes the start and end indices for a page of results and
// returns the metadata. Callers should slice their data as data[start:end].
func paginateSlice(total, page, limit int) (start, end int, meta paginationV01Metadata) {
	start = (page - 1) * limit
	if start > total {
		start = total
	}
	end = start + limit
	if end > total {
		end = total
	}
	meta = paginationV01Metadata{
		Total: total,
		Page:  page,
		Limit: limit,
	}
	return start, end, meta
}

// writeJSONError writes a structured JSON error response matching the
// registryErrorResponse format used by other registry endpoints.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(registryErrorResponse{
		Code:    code,
		Message: message,
	})
}
