// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListServersV01(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/servers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var body serversV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	// Should return servers from the embedded catalog (may be empty in test env)
	assert.NotNil(t, body.Servers)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}

func TestGetServerV01_NotFound(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/servers/nonexistent-server/versions/latest")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json",
		"Error responses should be JSON")

	var body registryErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "not_found", body.Code)
}

func TestFilterServersV01(t *testing.T) {
	t.Parallel()

	servers := []*v0.ServerJSON{
		{Name: "io.github.stacklok/fetch", Description: "Fetches web pages"},
		{Name: "io.github.stacklok/github", Description: "GitHub API integration"},
		{Name: "com.example/weather", Description: "Weather data provider"},
	}

	tests := []struct {
		query     string
		wantCount int
	}{
		{"fetch", 1},
		{"FETCH", 1},       // case-insensitive
		{"stacklok", 2},    // matches name prefix on two servers
		{"weather", 1},     // matches name and description on one server
		{"integration", 1}, // matches description only
		{"nonexistent", 0}, // no match
		{"github", 2},      // matches name on both stacklok servers
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			result := filterServersV01(servers, tt.query)
			assert.Len(t, result, tt.wantCount)
		})
	}
}

func TestFilterServersV01_EmptyResult_NotNull(t *testing.T) {
	t.Parallel()

	servers := []*v0.ServerJSON{
		{Name: "io.github.stacklok/fetch", Description: "Fetches web pages"},
	}

	result := filterServersV01(servers, "nonexistent")
	assert.NotNil(t, result, "Filter result should be empty slice, not nil")
	assert.Empty(t, result)

	// Verify JSON encoding produces [] not null
	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestListServersV01_PaginationBeyondResults(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/default/v0.1/servers?page=999&limit=10")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body serversV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Empty(t, body.Servers, "Page beyond results should return empty servers")
	assert.Equal(t, 999, body.Metadata.Page)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}

func TestParsePaginationV01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantPage  int
		wantLimit int
	}{
		{"defaults", "", 1, v01DefaultLimit},
		{"custom page", "page=3", 3, v01DefaultLimit},
		{"custom limit", "limit=10", 1, 10},
		{"both", "page=2&limit=25", 2, 25},
		{"invalid page", "page=-1", 1, v01DefaultLimit},
		{"limit over max", "limit=999", 1, v01DefaultLimit},
		{"non-numeric", "page=abc&limit=xyz", 1, v01DefaultLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/servers?"+tt.query, nil)
			page, limit := parsePaginationV01(r)
			assert.Equal(t, tt.wantPage, page)
			assert.Equal(t, tt.wantLimit, limit)
		})
	}
}
