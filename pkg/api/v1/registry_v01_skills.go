// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry"
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

// listSkillsV01 handles GET /registry/{registryName}/v0.1/x/dev.toolhive/skills
func listSkillsV01(w http.ResponseWriter, r *http.Request) {
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		slog.Error("failed to get registry provider", "error", err)
		http.Error(w, "Failed to get registry provider", http.StatusInternalServerError)
		return
	}

	skills, err := provider.ListAvailableSkills()
	if err != nil {
		slog.Error("failed to list skills", "error", err)
		http.Error(w, "Failed to list skills", http.StatusInternalServerError)
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

	provider, err := registry.GetDefaultProvider()
	if err != nil {
		slog.Error("failed to get registry provider", "error", err)
		http.Error(w, "Failed to get registry provider", http.StatusInternalServerError)
		return
	}

	skill, err := provider.GetSkill(namespace, skillName)
	if err != nil {
		slog.Error("failed to get skill", "namespace", namespace, "name", skillName, "error", err)
		http.Error(w, "Failed to get skill", http.StatusInternalServerError)
		return
	}
	if skill == nil {
		http.Error(w, "Skill not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skill); err != nil {
		slog.Error("failed to encode skill response", "error", err)
	}
}

func filterSkillsV01(skills []types.Skill, query string) []types.Skill {
	q := strings.ToLower(query)
	var result []types.Skill
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
