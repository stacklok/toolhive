// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/config"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/auth"
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
		if errors.Is(err, auth.ErrRegistryAuthRequired) {
			writeRegistryAuthRequiredError(w)
			return nil, false
		}
		var unavailableErr *regpkg.UnavailableError
		if errors.As(err, &unavailableErr) {
			slog.Error("upstream registry unavailable", "error", err)
			writeRegistryUnavailableError(w, unavailableErr)
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
func parsePaginationV01(r *http.Request) (page, limit int) {
	page = 1
	limit = v01DefaultLimit
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	// Cap page so (page-1)*limit cannot overflow int in the caller.
	if maxPage := math.MaxInt / limit; page > maxPage {
		page = maxPage
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > v01MaxLimit {
				v = v01MaxLimit
			}
			limit = v
		}
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

// paginationV01Metadata holds pagination metadata for v0.1 list responses.
type paginationV01Metadata struct {
	// Total is the total number of items matching the query
	Total int `json:"total"`
	// Page is the current page number (1-based)
	Page int `json:"page"`
	// Limit is the maximum number of items per page
	Limit int `json:"limit"`
}
