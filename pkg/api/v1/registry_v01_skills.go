// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	regpkg "github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/api"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

const (
	skillsDefaultLimit = 50
	skillsMaxLimit     = 200
)

// RegistryV01SkillsRouter creates a router for the v0.1 skills extension API.
// Skills live under the x/dev.toolhive extension namespace, matching the
// registry server's route structure: /registry/{name}/v0.1/x/dev.toolhive/skills
// The {registryName} path param is currently ignored (always uses default provider).
func RegistryV01SkillsRouter() http.Handler {
	r := chi.NewRouter()
	r.Route("/{registryName}/v0.1/x/dev.toolhive", func(r chi.Router) {
		r.Get("/skills", listSkillsV01)
		r.Get("/skills/{namespace}/{skillName}", getSkillV01)
	})
	return r
}

// getSkillsProvider returns the default registry provider configured for
// non-interactive (serve) mode to prevent browser-based OAuth flows from
// HTTP request handlers. Returns false and writes a structured JSON error
// response if the provider cannot be obtained.
func getSkillsProvider(w http.ResponseWriter) (regpkg.Provider, bool) {
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

// listSkillsV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills
func listSkillsV01(w http.ResponseWriter, r *http.Request) {
	provider, ok := getSkillsProvider(w)
	if !ok {
		return
	}

	skills, err := provider.ListAvailableSkills()
	if err != nil {
		slog.Error("failed to list skills", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to list skills")
		return
	}
	if skills == nil {
		skills = []types.Skill{}
	}

	// Apply search filter
	if q := r.URL.Query().Get("q"); q != "" {
		skills = filterSkillsV01(skills, q)
	}

	// Paginate
	page, limit := parseSkillsPagination(r)
	total := len(skills)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skillsV01Response{
		Skills: skills[start:end],
		Metadata: skillsV01Metadata{
			Total: total,
			Page:  page,
			Limit: limit,
		},
	}); err != nil {
		slog.Error("failed to encode skills response", "error", err)
	}
}

// getSkillV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills/{namespace}/{skillName}
func getSkillV01(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	skillName := chi.URLParam(r, "skillName")

	provider, ok := getSkillsProvider(w)
	if !ok {
		return
	}

	skill, err := provider.GetSkill(namespace, skillName)
	if err != nil {
		// Map upstream HTTP errors to appropriate responses
		var httpErr *api.RegistryHTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusNotFound:
				writeJSONError(w, http.StatusNotFound, "not_found", "Skill not found")
				return
			case http.StatusUnauthorized, http.StatusForbidden:
				writeRegistryAuthRequiredError(w)
				return
			}
		}
		slog.Error("failed to get skill", "namespace", namespace, "name", skillName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "Failed to get skill")
		return
	}
	if skill == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "Skill not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skill); err != nil {
		slog.Error("failed to encode skill response", "error", err)
	}
}

func filterSkillsV01(skills []types.Skill, query string) []types.Skill {
	q := strings.ToLower(query)
	result := make([]types.Skill, 0)
	for _, s := range skills {
		if strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Namespace), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			result = append(result, s)
		}
	}
	return result
}

func parseSkillsPagination(r *http.Request) (page, limit int) {
	page = 1
	limit = skillsDefaultLimit
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= skillsMaxLimit {
			limit = v
		}
	}
	return page, limit
}

type skillsV01Response struct {
	Skills   []types.Skill     `json:"skills"`
	Metadata skillsV01Metadata `json:"metadata"`
}

type skillsV01Metadata struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
}
