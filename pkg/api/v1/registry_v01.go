// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
)

const (
	v01DefaultLimit = 50
	v01MaxLimit     = 200
)

// RegistryV01Router creates a router for the v0.1 registry API.
// It combines server endpoints and skills extension endpoints under
// a common {registryName}/v0.1 prefix.
// The {registryName} path param is currently ignored (always uses the default provider).
func RegistryV01Router() http.Handler {
	r := chi.NewRouter()
	r.Route("/{registryName}/v0.1", func(r chi.Router) {
		r.Get("/servers", listServersV01)
		r.Get("/servers/{serverName}/versions/latest", getServerV01)
		r.Get("/x/dev.toolhive/skills", listSkillsV01)
		r.Get("/x/dev.toolhive/skills/{namespace}/{skillName}", getSkillV01)
		r.Get("/x/dev.toolhive/plugins", listPluginsV01)
		r.Get("/x/dev.toolhive/plugins/{namespace}/{pluginName}", getPluginV01)
	})
	return r
}

// getRegistryProvider returns the default registry provider configured for
// non-interactive (serve) mode to prevent browser-based OAuth flows from
// HTTP request handlers. Returns false and writes a structured JSON error
// response if the provider cannot be obtained.
func getRegistryProvider(w http.ResponseWriter) (regpkg.Provider, bool) {
	provider, err := regpkg.GetDefaultProviderWithConfig(
		config.NewProvider(),
		regpkg.WithInteractive(false),
	)
	if err != nil {
		if writeProviderError(w, err) {
			return nil, false
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to get registry provider")
		slog.Error("failed to get registry provider", "error", err)
		return nil, false
	}
	return provider, true
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

// parsePaginationV01 extracts page and limit query parameters from the request.
// Returns 1-based page and clamped limit (default 50, max 200).
//
// Both the embedded browse API's `page` parameter and the registry-server
// clients' opaque `cursor` parameter are accepted, so clients from either
// contract page correctly. A valid `cursor` takes precedence over `page`;
// cursors emitted by these handlers are the next page number.
func parsePaginationV01(r *http.Request) (page, limit int) {
	page = 1
	limit = v01DefaultLimit

	// Parse both values before computing the overflow cap.
	// A valid cursor (registry-server shape) takes precedence over page
	// (embedded browse shape); an absent or invalid cursor falls back to page.
	if v, err := strconv.Atoi(r.URL.Query().Get("cursor")); err == nil && v > 0 {
		page = v
	} else if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > v01MaxLimit {
				v = v01MaxLimit
			}
			limit = v
		}
	}

	// Cap page so (page-1)*limit cannot overflow int.
	if maxPage := math.MaxInt / limit; page > maxPage {
		page = maxPage
	}

	return page, limit
}

// paginateSlice returns start and end indices for paginating a slice of the
// given total length. The returned start and end are safe to use directly
// as slice bounds.
func paginateSlice(total, page, limit int) (start, end int) {
	start = (page - 1) * limit
	if start > total {
		start = total
	}
	end = start + limit
	if end > total {
		end = total
	}
	return start, end
}

// searchQueryV01 returns the full-text search filter from the request. Both the
// embedded browse API's `q` parameter and the registry-server clients' `search`
// parameter are accepted; `search` takes precedence when both are set.
func searchQueryV01(r *http.Request) string {
	if s := r.URL.Query().Get("search"); s != "" {
		return s
	}
	return r.URL.Query().Get("q")
}

// paginationV01Metadata holds pagination metadata for v0.1 list responses.
type paginationV01Metadata struct {
	// Total is the total number of items matching the query
	Total int `json:"total"`
	// Page is the current page number (1-based)
	Page int `json:"page"`
	// Limit is the maximum number of items per page
	Limit int `json:"limit"`
	// Count is the number of items returned in this response. Emitted to match
	// the registry-server contract that pkg/registry/api clients decode.
	Count int `json:"count"`
	// NextCursor is the opaque token for the next page, empty on the last page.
	// Emitted to match the registry-server contract so auto-paginating clients
	// stop correctly. The value is the next page number.
	NextCursor string `json:"nextCursor,omitempty"`
}

// newPaginationV01Metadata builds the response metadata for a page. It reports
// both the embedded API's total/page/limit and the registry-server's
// count/nextCursor so that either client shape can page through the results.
func newPaginationV01Metadata(total, page, limit, returned int) paginationV01Metadata {
	md := paginationV01Metadata{
		Total: total,
		Page:  page,
		Limit: limit,
		Count: returned,
	}
	if (page-1)*limit+returned < total {
		md.NextCursor = strconv.Itoa(page + 1)
	}
	return md
}
