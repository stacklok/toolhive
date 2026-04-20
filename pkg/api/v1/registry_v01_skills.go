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
	"github.com/stacklok/toolhive/pkg/registry/api"
)

// listSkillsV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills
//
//	@Summary		List available registry skills
//	@Description	Get a paginated list of skills from the registry. Supports optional full-text search and pagination.
//	@Tags			registry-skills
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			q				query		string	false	"Search filter — matches against skill name, namespace, and description"
//	@Param			page			query		integer	false	"Page number, 1-based (default: 1)"
//	@Param			limit			query		integer	false	"Items per page, max 200 (default: 50)"
//	@Success		200				{object}	skillsV01Response
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/x/dev.toolhive/skills [get]
func listSkillsV01(w http.ResponseWriter, r *http.Request) {
	provider, ok := getRegistryProvider(w)
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
	page, limit := parsePaginationV01(r)
	total := len(skills)
	start, end := paginateSlice(total, page, limit)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skillsV01Response{
		Skills: skills[start:end],
		Metadata: paginationV01Metadata{
			Total: total,
			Page:  page,
			Limit: limit,
		},
	}); err != nil {
		slog.Error("failed to encode skills response", "error", err)
	}
}

// getSkillV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills/{namespace}/{skillName}
//
//	@Summary		Get a registry skill
//	@Description	Retrieve a single skill by its namespace and name from the registry.
//	@Tags			registry-skills
//	@Produce		json
//	@Param			registryName	path		string	true	"Registry name (currently ignored, uses the default provider)"
//	@Param			namespace		path		string	true	"Skill namespace in reverse-DNS format (e.g. io.github.stacklok)"
//	@Param			skillName		path		string	true	"Skill name"
//	@Success		200				{object}	types.Skill
//	@Failure		404				{object}	registryErrorResponse	"Skill not found"
//	@Failure		500				{object}	registryErrorResponse	"Internal server error"
//	@Failure		503				{object}	registryErrorResponse	"Registry authentication required or upstream registry unavailable"
//	@Router			/registry/{registryName}/v0.1/x/dev.toolhive/skills/{namespace}/{skillName} [get]
func getSkillV01(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	skillName := chi.URLParam(r, "skillName")

	provider, ok := getRegistryProvider(w)
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

// skillsV01Response is the response body for the v0.1 skills list endpoint.
//
//	@Description	Paginated list of skills from the registry
type skillsV01Response struct {
	// Skills is the list of skills on the current page
	Skills []types.Skill `json:"skills"`
	// Metadata contains pagination information
	Metadata paginationV01Metadata `json:"metadata"`
}
