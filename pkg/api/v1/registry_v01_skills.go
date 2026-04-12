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
func listSkillsV01(w http.ResponseWriter, r *http.Request) {
	store, ok := getRegistryStore(w)
	if !ok {
		return
	}

	registryName := chi.URLParam(r, "registryName")
	skills, err := store.ListSkills(registryName)
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
	start, end, meta := paginateSlice(len(skills), page, limit)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skillsV01Response{
		Skills:   skills[start:end],
		Metadata: meta,
	}); err != nil {
		slog.Error("failed to encode skills response", "error", err)
	}
}

// getSkillV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills/{namespace}/{skillName}
func getSkillV01(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	skillName := chi.URLParam(r, "skillName")

	store, ok := getRegistryStore(w)
	if !ok {
		return
	}

	registryName := chi.URLParam(r, "registryName")
	skill, err := store.GetSkill(registryName, namespace, skillName)
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

type skillsV01Response struct {
	Skills   []types.Skill         `json:"skills"`
	Metadata paginationV01Metadata `json:"metadata"`
}
