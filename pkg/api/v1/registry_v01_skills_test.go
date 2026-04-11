// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive-core/registry/types"
)

func TestFilterSkillsV01(t *testing.T) {
	t.Parallel()

	skills := []types.Skill{
		{Namespace: "stacklok", Name: "code-review", Description: "Reviews code for issues"},
		{Namespace: "stacklok", Name: "commit", Description: "Creates git commits"},
		{Namespace: "other", Name: "weather", Description: "Weather data"},
	}

	tests := []struct {
		query     string
		wantCount int
	}{
		{"code", 1},
		{"stacklok", 2},
		{"weather", 1},
		{"commits", 1},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			result := filterSkillsV01(skills, tt.query)
			assert.Len(t, result, tt.wantCount)
		})
	}
}

func TestParseSkillsPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantPage  int
		wantLimit int
	}{
		{"defaults", "", 1, skillsDefaultLimit},
		{"custom page", "page=3", 3, skillsDefaultLimit},
		{"custom limit", "limit=10", 1, 10},
		{"both", "page=2&limit=25", 2, 25},
		{"invalid page", "page=-1", 1, skillsDefaultLimit},
		{"limit over max", "limit=999", 1, skillsDefaultLimit},
		{"non-numeric", "page=abc&limit=xyz", 1, skillsDefaultLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/skills?"+tt.query, nil)
			page, limit := parseSkillsPagination(r)
			assert.Equal(t, tt.wantPage, page)
			assert.Equal(t, tt.wantLimit, limit)
		})
	}
}

func TestRegistryV01SkillsRouter_ListSkills(t *testing.T) {
	t.Parallel()

	handler := RegistryV01SkillsRouter()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/skills")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var body skillsV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	// Should return skills from the embedded catalog (may be empty in test env)
	assert.NotNil(t, body.Skills)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}

func TestRegistryV01SkillsRouter_GetSkill_NotFound(t *testing.T) {
	t.Parallel()

	handler := RegistryV01SkillsRouter()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/skills/nonexistent/noskill")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
