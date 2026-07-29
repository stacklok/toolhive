// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
)

func newTestPluginsClient(t *testing.T, server *httptest.Server) PluginsClient {
	t.Helper()
	client, err := NewPluginsClient(server.URL, true, nil)
	require.NoError(t, err)
	return client
}

func TestPluginsClient_GetPlugin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		namespace  string
		pluginName string
		handler    http.HandlerFunc
		wantPlugin *thvregistry.Plugin
		wantErr    bool
	}{
		{
			name:       "success",
			namespace:  "io.github.user",
			pluginName: "my-plugin",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v0.1/x/dev.toolhive/plugins/io.github.user/my-plugin", r.URL.Path)
				require.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Plugin{
					Namespace:   "io.github.user",
					Name:        "my-plugin",
					Version:     "1.0.0",
					Description: "A test plugin",
				})
				require.NoError(t, err)
			},
			wantPlugin: &thvregistry.Plugin{
				Namespace:   "io.github.user",
				Name:        "my-plugin",
				Version:     "1.0.0",
				Description: "A test plugin",
			},
		},
		{
			name:       "not found",
			namespace:  "io.github.user",
			pluginName: "nonexistent",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("plugin not found"))
			},
			wantErr: true,
		},
		{
			name:       "server error",
			namespace:  "io.github.user",
			pluginName: "my-plugin",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			},
			wantErr: true,
		},
		{
			name:       "path escaping",
			namespace:  "io.github.user/special",
			pluginName: "my plugin",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// Verify that the path components are properly escaped
				require.Equal(t, "/v0.1/x/dev.toolhive/plugins/io.github.user%2Fspecial/my%20plugin", r.URL.RawPath)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Plugin{
					Namespace: "io.github.user/special",
					Name:      "my plugin",
					Version:   "1.0.0",
				})
				require.NoError(t, err)
			},
			wantPlugin: &thvregistry.Plugin{
				Namespace: "io.github.user/special",
				Name:      "my plugin",
				Version:   "1.0.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestPluginsClient(t, server)
			plugin, err := client.GetPlugin(t.Context(), tt.namespace, tt.pluginName)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantPlugin, plugin)
		})
	}
}

func TestPluginsClient_GetPluginVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		namespace  string
		pluginName string
		version    string
		handler    http.HandlerFunc
		wantPlugin *thvregistry.Plugin
		wantErr    bool
	}{
		{
			name:       "success",
			namespace:  "io.github.user",
			pluginName: "my-plugin",
			version:    "2.0.0",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v0.1/x/dev.toolhive/plugins/io.github.user/my-plugin/versions/2.0.0", r.URL.Path)
				require.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Plugin{
					Namespace:   "io.github.user",
					Name:        "my-plugin",
					Version:     "2.0.0",
					Description: "Version 2",
				})
				require.NoError(t, err)
			},
			wantPlugin: &thvregistry.Plugin{
				Namespace:   "io.github.user",
				Name:        "my-plugin",
				Version:     "2.0.0",
				Description: "Version 2",
			},
		},
		{
			name:       "version not found",
			namespace:  "io.github.user",
			pluginName: "my-plugin",
			version:    "99.0.0",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("version not found"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestPluginsClient(t, server)
			plugin, err := client.GetPluginVersion(t.Context(), tt.namespace, tt.pluginName, tt.version)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantPlugin, plugin)
		})
	}
}

func TestPluginsClient_ListPlugins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        *PluginsListOptions
		handler     http.HandlerFunc
		wantCount   int
		wantErr     bool
		wantPlugins []*thvregistry.Plugin
	}{
		{
			name: "single page",
			opts: nil,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(pluginsListResponse{
					Plugins: []*thvregistry.Plugin{
						{Namespace: "io.github.a", Name: "plugin-1", Version: "1.0.0"},
						{Namespace: "io.github.b", Name: "plugin-2", Version: "1.0.0"},
					},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 2, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount: 2,
			wantPlugins: []*thvregistry.Plugin{
				{Namespace: "io.github.a", Name: "plugin-1", Version: "1.0.0"},
				{Namespace: "io.github.b", Name: "plugin-2", Version: "1.0.0"},
			},
		},
		{
			name: "pagination across multiple pages",
			opts: &PluginsListOptions{Limit: 1},
			handler: func() http.HandlerFunc {
				callCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					callCount++
					w.Header().Set("Content-Type", "application/json")

					cursor := r.URL.Query().Get("cursor")
					var resp pluginsListResponse

					switch {
					case cursor == "" && callCount == 1:
						resp = pluginsListResponse{
							Plugins: []*thvregistry.Plugin{
								{Namespace: "io.github.a", Name: "plugin-1", Version: "1.0.0"},
							},
							Metadata: struct {
								Count      int    `json:"count"`
								NextCursor string `json:"nextCursor"`
							}{Count: 1, NextCursor: "page2"},
						}
					case cursor == "page2":
						resp = pluginsListResponse{
							Plugins: []*thvregistry.Plugin{
								{Namespace: "io.github.b", Name: "plugin-2", Version: "1.0.0"},
							},
							Metadata: struct {
								Count      int    `json:"count"`
								NextCursor string `json:"nextCursor"`
							}{Count: 1, NextCursor: ""},
						}
					default:
						w.WriteHeader(http.StatusBadRequest)
						return
					}

					err := json.NewEncoder(w).Encode(resp)
					require.NoError(t, err)
				}
			}(),
			wantCount: 2,
			wantPlugins: []*thvregistry.Plugin{
				{Namespace: "io.github.a", Name: "plugin-1", Version: "1.0.0"},
				{Namespace: "io.github.b", Name: "plugin-2", Version: "1.0.0"},
			},
		},
		{
			name: "empty result",
			opts: nil,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(pluginsListResponse{
					Plugins: []*thvregistry.Plugin{},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 0, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount:   0,
			wantPlugins: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestPluginsClient(t, server)
			result, err := client.ListPlugins(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Plugins, tt.wantCount)
			if tt.wantPlugins != nil {
				require.Equal(t, tt.wantPlugins, result.Plugins)
			}
		})
	}
}

func TestPluginsClient_SearchPlugins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		handler   http.HandlerFunc
		wantCount int
		wantErr   bool
	}{
		{
			name:  "success with results",
			query: "kubernetes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "kubernetes", r.URL.Query().Get("search"))
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(pluginsListResponse{
					Plugins: []*thvregistry.Plugin{
						{Namespace: "io.github.user", Name: "k8s-plugin", Version: "1.0.0", Description: "Kubernetes plugin"},
					},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 1, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount: 1,
		},
		{
			name:  "empty result",
			query: "nonexistent",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(pluginsListResponse{
					Plugins: []*thvregistry.Plugin{},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 0, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestPluginsClient(t, server)
			result, err := client.SearchPlugins(t.Context(), tt.query)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Plugins, tt.wantCount)
		})
	}
}

func TestPluginsClient_ListPluginVersions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v0.1/x/dev.toolhive/plugins/io.github.user/my-plugin/versions", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(pluginsListResponse{
			Plugins: []*thvregistry.Plugin{
				{Namespace: "io.github.user", Name: "my-plugin", Version: "1.0.0"},
				{Namespace: "io.github.user", Name: "my-plugin", Version: "2.0.0"},
				{Namespace: "io.github.user", Name: "my-plugin", Version: "3.0.0"},
			},
			Metadata: struct {
				Count      int    `json:"count"`
				NextCursor string `json:"nextCursor"`
			}{Count: 3, NextCursor: ""},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newTestPluginsClient(t, server)
	result, err := client.ListPluginVersions(t.Context(), "io.github.user", "my-plugin")
	require.NoError(t, err)
	require.Len(t, result.Plugins, 3)
	require.Equal(t, "1.0.0", result.Plugins[0].Version)
	require.Equal(t, "2.0.0", result.Plugins[1].Version)
	require.Equal(t, "3.0.0", result.Plugins[2].Version)
}

func TestPluginsClient_ErrorHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErrIs  error
	}{
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       "unauthorized",
			wantErrIs:  ErrRegistryUnauthorized,
		},
		{
			name:       "403 forbidden",
			statusCode: http.StatusForbidden,
			body:       "forbidden",
			wantErrIs:  ErrRegistryUnauthorized,
		},
		{
			name:       "404 not found",
			statusCode: http.StatusNotFound,
			body:       "not found",
			wantErrIs:  nil,
		},
		{
			name:       "500 server error does not unwrap to unauthorized",
			statusCode: http.StatusInternalServerError,
			body:       "internal server error",
			wantErrIs:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := newTestPluginsClient(t, server)
			_, err := client.GetPlugin(t.Context(), "io.github.user", "my-plugin")
			require.Error(t, err)

			var httpErr *RegistryHTTPError
			require.True(t, errors.As(err, &httpErr), "expected *RegistryHTTPError, got %T", err)
			require.Equal(t, tt.statusCode, httpErr.StatusCode)
			require.Contains(t, httpErr.Body, tt.body)

			if tt.wantErrIs != nil {
				require.True(t, errors.Is(err, tt.wantErrIs),
					"expected errors.Is(%v, %v) to be true", err, tt.wantErrIs)
			} else {
				require.False(t, errors.Is(err, ErrRegistryUnauthorized),
					"expected errors.Is(%v, ErrRegistryUnauthorized) to be false", err)
			}
		})
	}
}

func TestPluginsClient_MalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	client := newTestPluginsClient(t, server)
	_, err := client.GetPlugin(t.Context(), "io.github.user", "my-plugin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode response")
}

func TestPluginsClient_TrailingSlashInBaseURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The path should not have a double slash
		require.NotContains(t, r.URL.Path, "//")
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(thvregistry.Plugin{
			Namespace: "io.github.user",
			Name:      "my-plugin",
			Version:   "1.0.0",
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	// Create client with trailing slash
	client, err := NewPluginsClient(server.URL+"/", true, nil)
	require.NoError(t, err)

	plugin, err := client.GetPlugin(t.Context(), "io.github.user", "my-plugin")
	require.NoError(t, err)
	require.Equal(t, "io.github.user", plugin.Namespace)
}

func TestPluginsClient_ListPluginsWithSearch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-query", r.URL.Query().Get("search"))
		require.Equal(t, "50", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(pluginsListResponse{
			Plugins: []*thvregistry.Plugin{
				{Namespace: "io.github.user", Name: "test-plugin", Version: "1.0.0"},
			},
			Metadata: struct {
				Count      int    `json:"count"`
				NextCursor string `json:"nextCursor"`
			}{Count: 1, NextCursor: ""},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newTestPluginsClient(t, server)
	result, err := client.ListPlugins(t.Context(), &PluginsListOptions{
		Search: "test-query",
		Limit:  50,
	})
	require.NoError(t, err)
	require.Len(t, result.Plugins, 1)
	require.Equal(t, "test-plugin", result.Plugins[0].Name)
}

func TestPluginsClient_RegistryHTTPErrorUnwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		wantErrIs  error
	}{
		{name: "401 wraps unauthorized", statusCode: http.StatusUnauthorized, wantErrIs: ErrRegistryUnauthorized},
		{name: "403 wraps unauthorized", statusCode: http.StatusForbidden, wantErrIs: ErrRegistryUnauthorized},
		{name: "404 unwraps to nil", statusCode: http.StatusNotFound, wantErrIs: nil},
		{name: "500 unwraps to nil", statusCode: http.StatusInternalServerError, wantErrIs: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &RegistryHTTPError{
				StatusCode: tt.statusCode,
				Body:       "test body",
				URL:        "http://example.com/test",
			}
			require.Equal(t, tt.wantErrIs, err.Unwrap())
			require.Contains(t, err.Error(), fmt.Sprintf("status %d", tt.statusCode))
		})
	}
}
