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

func TestRegistryV01Router_ListServers(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

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

func TestRegistryV01Router_GetServer_NotFound(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// URL-encode a non-existent reverse-DNS server name
	resp, err := http.Get(srv.URL + "/default/v0.1/servers/io.nonexistent%2Fnosuchserver/versions/latest")
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
		{Name: "io.github.stacklok/fetch", Description: "Fetch web content"},
		{Name: "io.github.stacklok/postgres", Description: "PostgreSQL database access"},
		{Name: "io.github.other/weather", Description: "Weather data and forecasts"},
	}

	tests := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"match name", "fetch", 1},
		{"case insensitive", "FETCH", 1},
		{"match description", "database", 1},
		{"match namespace", "stacklok", 2},
		{"match multiple", "weather", 1},
		{"no match", "nonexistent", 0},
		{"partial description", "data", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := filterServersV01(servers, tt.query)
			assert.Len(t, result, tt.wantCount)
		})
	}
}

func TestFilterServersV01_EmptyResult_NotNull(t *testing.T) {
	t.Parallel()

	servers := []*v0.ServerJSON{
		{Name: "io.github.stacklok/test", Description: "A test server"},
	}

	result := filterServersV01(servers, "nonexistent")
	assert.NotNil(t, result, "Filter result should be empty slice, not nil")
	assert.Empty(t, result)

	// Verify JSON encoding produces [] not null
	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestRegistryV01Router_ListServers_PaginationBeyondResults(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

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

func TestPaginateSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int
		page      int
		limit     int
		wantStart int
		wantEnd   int
	}{
		{"first page", 100, 1, 10, 0, 10},
		{"second page", 100, 2, 10, 10, 20},
		{"last partial page", 25, 3, 10, 20, 25},
		{"beyond total", 10, 5, 10, 10, 10},
		{"single item", 1, 1, 10, 0, 1},
		{"empty", 0, 1, 10, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start, end := paginateSlice(tt.total, tt.page, tt.limit)
			assert.Equal(t, tt.wantStart, start)
			assert.Equal(t, tt.wantEnd, end)
		})
	}
}
