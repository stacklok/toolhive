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
		{"CODE", 1},        // case-insensitive
		{"Code-Review", 1}, // mixed case
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

func TestRegistryV01SkillsRouter_ListSkills(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
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

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/skills/nonexistent/noskill")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json",
		"Error responses should be JSON")

	var body registryErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "not_found", body.Code)
}

func TestFilterSkillsV01_EmptyResult_NotNull(t *testing.T) {
	t.Parallel()

	skills := []types.Skill{
		{Namespace: "stacklok", Name: "test", Description: "A test skill"},
	}

	result := filterSkillsV01(skills, "nonexistent")
	assert.NotNil(t, result, "Filter result should be empty slice, not nil")
	assert.Empty(t, result)

	// Verify JSON encoding produces [] not null
	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestRegistryV01SkillsRouter_ListSkills_PaginationBeyondResults(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/skills?page=999&limit=10")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body skillsV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Empty(t, body.Skills, "Page beyond results should return empty skills")
	assert.Equal(t, 999, body.Metadata.Page)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}
