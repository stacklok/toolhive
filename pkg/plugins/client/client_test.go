// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/identitytoken"
)

// newTestClient returns a *Client pointed at the given test server.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return NewClient(srv.URL)
}

func TestList(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		opts       plugins.ListOptions
		wantQuery  map[string]string
		response   listResponse
		statusCode int
		wantErr    bool
	}{
		{
			name: "no filters",
			opts: plugins.ListOptions{},
			response: listResponse{Plugins: []plugins.InstalledPlugin{
				{
					Metadata:    plugins.PluginMetadata{Name: "my-plugin", Version: "1.0.0"},
					Scope:       plugins.ScopeUser,
					Status:      plugins.InstallStatusInstalled,
					InstalledAt: now,
				},
			}},
			statusCode: http.StatusOK,
		},
		{
			name: "with all filters",
			opts: plugins.ListOptions{
				Scope:       plugins.ScopeProject,
				ClientApp:   "claude-code",
				ProjectRoot: "/home/user/proj",
				Group:       "my-group",
			},
			wantQuery: map[string]string{
				"scope":        "project",
				"client":       "claude-code",
				"project_root": "/home/user/proj",
				"group":        "my-group",
			},
			response:   listResponse{Plugins: []plugins.InstalledPlugin{}},
			statusCode: http.StatusOK,
		},
		{
			name:       "server error",
			opts:       plugins.ListOptions{},
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, pluginsBasePath, r.URL.Path)

				for k, v := range tt.wantQuery {
					assert.Equal(t, v, r.URL.Query().Get(k), "query param %s", k)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "something went wrong", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.List(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response.Plugins, got)
		})
	}
}

func TestInstall(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		opts       plugins.InstallOptions
		wantBody   installRequest
		response   installResponse
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name: "success",
			opts: plugins.InstallOptions{
				Name:    "my-plugin",
				Version: "1.0.0",
				Scope:   plugins.ScopeUser,
				Clients: []string{"claude-code"},
				Force:   true,
			},
			wantBody: installRequest{
				Name:    "my-plugin",
				Version: "1.0.0",
				Scope:   plugins.ScopeUser,
				Clients: []string{"claude-code"},
				Force:   true,
			},
			response: installResponse{Plugin: plugins.InstalledPlugin{
				Metadata:    plugins.PluginMetadata{Name: "my-plugin", Version: "1.0.0"},
				Scope:       plugins.ScopeUser,
				Status:      plugins.InstallStatusInstalled,
				InstalledAt: now,
			}},
			statusCode: http.StatusCreated,
		},
		{
			name:       "bad request",
			opts:       plugins.InstallOptions{Name: ""},
			statusCode: http.StatusBadRequest,
			wantErr:    true,
			wantCode:   http.StatusBadRequest,
		},
		{
			name:       "conflict",
			opts:       plugins.InstallOptions{Name: "existing-plugin"},
			statusCode: http.StatusConflict,
			wantErr:    true,
			wantCode:   http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, pluginsBasePath, r.URL.Path)

				if tt.wantBody.Name != "" {
					var got installRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
					assert.Equal(t, tt.wantBody, got)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "error", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.Install(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response.Plugin, got.Plugin)
		})
	}
}

func TestUninstall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       plugins.UninstallOptions
		wantPath   string
		wantQuery  map[string]string
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success",
			opts:       plugins.UninstallOptions{Name: "my-plugin"},
			wantPath:   pluginsBasePath + "/my-plugin",
			statusCode: http.StatusNoContent,
		},
		{
			name: "with scope and project root",
			opts: plugins.UninstallOptions{
				Name:        "my-plugin",
				Scope:       plugins.ScopeProject,
				ProjectRoot: "/home/user/proj",
			},
			wantPath: pluginsBasePath + "/my-plugin",
			wantQuery: map[string]string{
				"scope":        "project",
				"project_root": "/home/user/proj",
			},
			statusCode: http.StatusNoContent,
		},
		{
			name:       "not found",
			opts:       plugins.UninstallOptions{Name: "missing"},
			wantPath:   pluginsBasePath + "/missing",
			statusCode: http.StatusNotFound,
			wantErr:    true,
			wantCode:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, tt.wantPath, r.URL.Path)

				for k, v := range tt.wantQuery {
					assert.Equal(t, v, r.URL.Query().Get(k), "query param %s", k)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "not found", tt.statusCode)
					return
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			err := c.Uninstall(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestInfo(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		opts       plugins.InfoOptions
		wantPath   string
		response   plugins.PluginInfo
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:     "success",
			opts:     plugins.InfoOptions{Name: "my-plugin"},
			wantPath: pluginsBasePath + "/my-plugin",
			response: plugins.PluginInfo{
				Metadata: plugins.PluginMetadata{Name: "my-plugin", Version: "1.0.0"},
				InstalledPlugin: &plugins.InstalledPlugin{
					Metadata:    plugins.PluginMetadata{Name: "my-plugin", Version: "1.0.0"},
					Scope:       plugins.ScopeUser,
					Status:      plugins.InstallStatusInstalled,
					InstalledAt: now,
				},
			},
			statusCode: http.StatusOK,
		},
		{
			name:       "not found",
			opts:       plugins.InfoOptions{Name: "missing"},
			wantPath:   pluginsBasePath + "/missing",
			statusCode: http.StatusNotFound,
			wantErr:    true,
			wantCode:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, tt.wantPath, r.URL.Path)

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "not found", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.Info(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response, *got)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		wantBody   validateRequest
		response   plugins.ValidationResult
		statusCode int
		wantErr    bool
	}{
		{
			name:       "valid plugin",
			path:       "/home/user/my-plugin",
			wantBody:   validateRequest{Path: "/home/user/my-plugin"},
			response:   plugins.ValidationResult{Valid: true},
			statusCode: http.StatusOK,
		},
		{
			name:     "invalid plugin",
			path:     "/home/user/bad-plugin",
			wantBody: validateRequest{Path: "/home/user/bad-plugin"},
			response: plugins.ValidationResult{
				Valid:  false,
				Errors: []string{"missing name field"},
			},
			statusCode: http.StatusOK,
		},
		{
			name:       "bad request",
			path:       "",
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, pluginsBasePath+"/validate", r.URL.Path)

				if tt.wantBody.Path != "" {
					var got validateRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
					assert.Equal(t, tt.wantBody, got)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "bad request", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.Validate(t.Context(), tt.path)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response, *got)
		})
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       plugins.BuildOptions
		wantBody   buildRequest
		response   plugins.BuildResult
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			opts:       plugins.BuildOptions{Path: "/home/user/my-plugin", Tag: "v1.0.0"},
			wantBody:   buildRequest{Path: "/home/user/my-plugin", Tag: "v1.0.0"},
			response:   plugins.BuildResult{Reference: "ghcr.io/org/my-plugin:v1.0.0"},
			statusCode: http.StatusOK,
		},
		{
			name:       "bad request",
			opts:       plugins.BuildOptions{},
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, pluginsBasePath+"/build", r.URL.Path)

				if tt.wantBody.Path != "" {
					var got buildRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
					assert.Equal(t, tt.wantBody, got)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "bad request", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.Build(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response, *got)
		})
	}
}

func TestPush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       plugins.PushOptions
		wantBody   pushRequest
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success",
			opts:       plugins.PushOptions{Reference: "ghcr.io/org/my-plugin:v1.0.0", NoSign: true},
			wantBody:   pushRequest{Reference: "ghcr.io/org/my-plugin:v1.0.0", NoSign: true},
			statusCode: http.StatusNoContent,
		},
		{
			// Guards the DTO trap: the signing fields must reach the wire
			// request, not just PushOptions.
			name: "forwards identity token",
			opts: plugins.PushOptions{
				Reference:     "ghcr.io/org/my-plugin:v1.0.0",
				IdentityToken: "a.b.c",
			},
			wantBody: pushRequest{
				Reference:     "ghcr.io/org/my-plugin:v1.0.0",
				IdentityToken: "a.b.c",
			},
			statusCode: http.StatusNoContent,
		},
		{
			name:       "not found",
			opts:       plugins.PushOptions{Reference: "ghcr.io/org/missing:v1", NoSign: true},
			statusCode: http.StatusNotFound,
			wantErr:    true,
			wantCode:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, pluginsBasePath+"/push", r.URL.Path)

				if tt.wantBody.Reference != "" {
					var got pushRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
					assert.Equal(t, tt.wantBody, got)
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "not found", tt.statusCode)
					return
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			err := c.Push(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestListBuilds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   listBuildsResponse
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name: "success",
			response: listBuildsResponse{Builds: []plugins.LocalBuild{
				{Tag: "my-plugin", Digest: "sha256:abc", Name: "my-plugin", Version: "1.0.0"},
			}},
			statusCode: http.StatusOK,
		},
		{
			name:       "empty",
			response:   listBuildsResponse{Builds: []plugins.LocalBuild{}},
			statusCode: http.StatusOK,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantCode:   http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, pluginsBasePath+"/builds", r.URL.Path)

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "error", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.ListBuilds(t.Context())

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response.Builds, got)
		})
	}
}

func TestDeleteBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tag        string
		wantPath   string
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success",
			tag:        "my-plugin",
			wantPath:   pluginsBasePath + "/builds/my-plugin",
			statusCode: http.StatusNoContent,
		},
		{
			name:       "not found",
			tag:        "missing",
			wantPath:   pluginsBasePath + "/builds/missing",
			statusCode: http.StatusNotFound,
			wantErr:    true,
			wantCode:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, tt.wantPath, r.URL.Path)

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "error", tt.statusCode)
					return
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			err := c.DeleteBuild(t.Context(), tt.tag)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestGetContent(t *testing.T) {
	t.Parallel()

	response := plugins.PluginContent{
		Name:        "my-plugin",
		Description: "A test plugin",
		Version:     "1.0.0",
		License:     "Apache-2.0",
		Manifest:    `{"name":"my-plugin"}`,
		Files:       []plugins.PluginFileEntry{{Path: "plugin.json", Size: 42}},
	}

	tests := []struct {
		name       string
		opts       plugins.ContentOptions
		wantQuery  string
		response   plugins.PluginContent
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success with local tag",
			opts:       plugins.ContentOptions{Reference: "my-plugin"},
			wantQuery:  "my-plugin",
			response:   response,
			statusCode: http.StatusOK,
		},
		{
			name:       "success with OCI reference",
			opts:       plugins.ContentOptions{Reference: "ghcr.io/org/my-plugin:v1"},
			wantQuery:  "ghcr.io/org/my-plugin:v1",
			response:   response,
			statusCode: http.StatusOK,
		},
		{
			name:       "server error propagates",
			opts:       plugins.ContentOptions{Reference: "missing"},
			statusCode: http.StatusBadRequest,
			wantErr:    true,
			wantCode:   http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, pluginsBasePath+"/content", r.URL.Path)
				if tt.wantQuery != "" {
					assert.Equal(t, tt.wantQuery, r.URL.Query().Get("ref"))
				}

				if tt.statusCode >= http.StatusBadRequest {
					http.Error(w, "bad request", tt.statusCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.GetContent(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.response, *got)
		})
	}
}

func TestConnectionError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	c := NewClient(srv.URL)
	_, err := c.List(t.Context(), plugins.ListOptions{})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServerUnreachable), "expected ErrServerUnreachable, got: %v", err)
}

func TestNewDefaultClient(t *testing.T) {
	t.Parallel()

	// noDiscovery stubs server discovery to report no running server, isolating
	// the test from any real local server (e.g. a running Desktop app).
	noDiscovery := func(context.Context) (string, []Option) { return "", nil }

	// failDiscovery fails the test if discovery is consulted, asserting that an
	// earlier resolution step short-circuited.
	failDiscovery := func(t *testing.T) discoverFunc {
		t.Helper()
		return func(context.Context) (string, []Option) {
			t.Error("discovery should not be called when TOOLHIVE_API_URL is set")
			return "", nil
		}
	}

	t.Run("falls back to default URL when env is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("").AnyTimes()

		c := newDefaultClientWithEnv(t.Context(), mockEnv, noDiscovery)
		assert.Equal(t, defaultBaseURL, c.baseURL)
	})

	t.Run("uses TOOLHIVE_API_URL from env", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("http://localhost:9999")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("").AnyTimes()

		c := newDefaultClientWithEnv(t.Context(), mockEnv, failDiscovery(t))
		assert.Equal(t, "http://localhost:9999", c.baseURL)
	})

	t.Run("uses discovered server when env is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("").AnyTimes()

		discover := func(context.Context) (string, []Option) {
			return "http://127.0.0.1:54321", nil
		}
		c := newDefaultClientWithEnv(t.Context(), mockEnv, discover)
		assert.Equal(t, "http://127.0.0.1:54321", c.baseURL)
	})

	t.Run("applies options", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("").AnyTimes()

		c := newDefaultClientWithEnv(t.Context(), mockEnv, noDiscovery, WithTimeout(5*time.Second))
		assert.Equal(t, 5*time.Second, c.httpClient.Timeout)
	})
}

func TestWithHTTPClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 99 * time.Second}
	c := NewClient("http://example.com", WithHTTPClient(custom))
	assert.Equal(t, custom, c.httpClient)
}

func TestURLEncodesPluginNames(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, pluginsBasePath+"/my%20plugin%2Fv2", r.URL.RawPath)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(plugins.PluginInfo{
			Metadata: plugins.PluginMetadata{Name: "my plugin/v2"},
		}))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.Info(t.Context(), plugins.InfoOptions{Name: "my plugin/v2"})
	require.NoError(t, err)
	assert.Equal(t, "my plugin/v2", got.Metadata.Name)
}

func TestHandleErrorResponseReadFailure(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(&failReader{}),
	}
	err := handleErrorResponse(resp)

	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	assert.Contains(t, err.Error(), "failed to read error response body")
}

type failReader struct{}

func (*failReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated read error")
}

func TestTimeoutFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "unset falls back to the default", value: "", want: 0, ok: false},
		{name: "a duration is honored", value: "45s", want: 45 * time.Second, ok: true},
		{name: "minutes parse", value: "5m", want: 5 * time.Minute, ok: true},
		{name: "surrounding whitespace is tolerated", value: "  90s ", want: 90 * time.Second, ok: true},
		{name: "a bare number is not a duration", value: "60", want: 0, ok: false},
		{name: "garbage degrades to the default", value: "soon", want: 0, ok: false},
		{name: "zero would disable the timeout, so it is ignored", value: "0s", want: 0, ok: false},
		{name: "negative is ignored", value: "-5s", want: 0, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mockEnv := envmocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv(envAPITimeout).Return(tc.value)

			got, ok := timeoutFromEnv(mockEnv)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNewDefaultClientTimeoutPrecedence(t *testing.T) {
	t.Parallel()

	noDiscovery := func(context.Context) (string, []Option) { return "", nil }

	t.Run("defaults when the env is unset", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("")

		c := newDefaultClientWithEnv(t.Context(), mockEnv, noDiscovery)
		assert.Equal(t, defaultTimeout, c.httpClient.Timeout)
	})

	t.Run("env overrides the default", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("2m")

		c := newDefaultClientWithEnv(t.Context(), mockEnv, noDiscovery)
		assert.Equal(t, 2*time.Minute, c.httpClient.Timeout)
	})

	t.Run("an explicit option outranks the env", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("2m")

		c := newDefaultClientWithEnv(t.Context(), mockEnv, noDiscovery, WithTimeout(7*time.Second))
		assert.Equal(t, 7*time.Second, c.httpClient.Timeout)
	})
}

// TestTimeoutIsReportedAsTimeoutNotUnreachable pins the distinction the CLI
// hint depends on: a healthy-but-slow server must not be reported as absent.
func TestTimeoutIsReportedAsTimeoutNotUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, WithTimeout(50*time.Millisecond))
	_, err := c.List(t.Context(), plugins.ListOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRequestTimeout)
	assert.NotErrorIs(t, err, ErrServerUnreachable,
		"the server answered the connection; only the response was slow")
}

func TestUnreachableServerIsStillUnreachable(t *testing.T) {
	t.Parallel()

	// Bind and immediately release a port so nothing is listening on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	c := NewClient("http://"+addr, WithTimeout(2*time.Second))
	_, err = c.List(t.Context(), plugins.ListOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServerUnreachable)
	assert.NotErrorIs(t, err, ErrRequestTimeout)
}

// TestCallerCancellationIsNeitherSentinel keeps a user pressing Ctrl-C from
// being reported as a server problem.
func TestCallerCancellationIsNeitherSentinel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	c := NewClient(srv.URL, WithTimeout(30*time.Second))
	_, err := c.List(ctx, plugins.ListOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrRequestTimeout)
	assert.NotErrorIs(t, err, ErrServerUnreachable)
}

// TestInstallCarriesAllowUnsigned round-trips the unsigned exception through
// the client's request body — without this, the CLI flag silently never
// reaches the server (every --allow-unsigned install would 403 telling the
// user to pass the flag they passed).
func TestInstallCarriesAllowUnsigned(t *testing.T) {
	t.Parallel()

	var got installRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(installResponse{})
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(t, srv).Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		Scope:         plugins.ScopeProject,
		ProjectRoot:   "/tmp/project",
		AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.True(t, got.AllowUnsigned, "allow_unsigned must reach the server")
}

// TestInstallReturnsTrustState guards the response half of the same DTO
// boundary: the CLI is a pure HTTP client, so a provenance block dropped
// here would make every signed install print as if it were untracked.
func TestInstallReturnsTrustState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response installResponse
	}{
		{
			name: "signed",
			response: installResponse{
				Plugin: plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Provenance: &plugins.ProvenanceInfo{
					SignerIdentity: "/.github/workflows/release.yml",
					CertIssuer:     "https://token.actions.githubusercontent.com",
				},
			},
		},
		{
			name: "provisional",
			response: installResponse{
				Plugin: plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Provenance: &plugins.ProvenanceInfo{
					SignerIdentity: "/.github/workflows/release.yml",
					Provisional:    true,
				},
			},
		},
		{
			name: "unsigned exception",
			response: installResponse{
				Plugin:   plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Unsigned: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			t.Cleanup(srv.Close)

			result, err := newTestClient(t, srv).Install(t.Context(), plugins.InstallOptions{Name: "my-plugin"})
			require.NoError(t, err)
			assert.Equal(t, tt.response.Provenance, result.Provenance)
			assert.Equal(t, tt.response.Unsigned, result.Unsigned)
		})
	}
}

// TestSyncCarriesAllowUnsigned mirrors TestInstallCarriesAllowUnsigned for
// the sync/adopt path: a flag that dies at the DTO boundary would silently
// make every adoption of an install with no stored bundle fail.
func TestSyncCarriesAllowUnsigned(t *testing.T) {
	t.Parallel()

	var got syncRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(plugins.SyncResult{})
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(t, srv).Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot:   "/tmp/project",
		Adopt:         true,
		AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.True(t, got.AllowUnsigned, "allow_unsigned must reach the server")
}

// TestUpgradeCarriesAllowSignerChange pins the upgrade DTO boundary: a flag
// that dies here would silently re-arm the signer-change guard the caller
// just overrode, turning an explicit rotation into a blocked upgrade.
func TestUpgradeCarriesAllowSignerChange(t *testing.T) {
	t.Parallel()

	var got upgradeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(plugins.UpgradeResult{})
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(t, srv).Upgrade(t.Context(), plugins.UpgradeOptions{
		ProjectRoot:       "/tmp/project",
		Names:             []string{"my-plugin"},
		AllowRefChange:    true,
		AllowSignerChange: true,
	})
	require.NoError(t, err)
	assert.True(t, got.AllowSignerChange, "allow_signer_change must reach the server")
	assert.True(t, got.AllowRefChange)
	assert.Equal(t, []string{"my-plugin"}, got.Names)
}

// recordingTransport fails the test if any HTTP request is attempted.
type recordingTransport struct {
	t      *testing.T
	called bool
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.called = true
	rt.t.Errorf("no request must be attempted, but one was sent to %s", req.URL)
	return nil, errors.New("transport must not be reached")
}

// TestPushRefusesIdentityTokenOverRemotePlaintext pins that the credential is
// withheld rather than transmitted: an OIDC identity token is redeemable at
// Fulcio for a signing certificate in the caller's name, so TOOLHIVE_API_URL
// pointing at a plaintext remote host must fail before the token is
// serialized. Asserting on the transport rather than the error is the point —
// an error returned after the request went out would be too late.
func TestPushRefusesIdentityTokenOverRemotePlaintext(t *testing.T) {
	t.Parallel()

	rt := &recordingTransport{t: t}
	c := NewClient("http://api.example.test:8080", WithHTTPClient(&http.Client{Transport: rt}))

	err := c.Push(t.Context(), plugins.PushOptions{
		Reference:     "ghcr.io/org/my-plugin:v1.0.0",
		IdentityToken: "a.b.c",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, identitytoken.ErrInsecureTransport)
	assert.False(t, rt.called)
}

// TestPushUnsignedOverRemotePlaintextIsAllowed: the guard is about the
// credential, not the endpoint. A --no-sign push carries nothing worth
// protecting, so it must not be blocked by the same check.
func TestPushUnsignedOverRemotePlaintextIsAllowed(t *testing.T) {
	t.Parallel()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	require.NoError(t, c.Push(t.Context(), plugins.PushOptions{
		Reference: "ghcr.io/org/my-plugin:v1.0.0",
		NoSign:    true,
	}))
	assert.Equal(t, 1, hits)
}

// TestPushDoesNotFollowRedirectsWithIdentityToken proves the transport guard
// cannot be sidestepped by a redirect. A 307 preserves the method and replays
// the body, so an accepted origin answering with one would hand the
// Fulcio-redeemable token to whatever Location names. Two servers: the first
// is loopback and so passes CheckTransport, the second records any request it
// receives and must receive none.
func TestPushDoesNotFollowRedirectsWithIdentityToken(t *testing.T) {
	t.Parallel()

	var secondHits int
	var secondSawToken bool
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		var got pushRequest
		if json.NewDecoder(r.Body).Decode(&got) == nil && got.IdentityToken != "" {
			secondSawToken = true
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer first.Close()

	c := NewClient(first.URL)
	err := c.Push(t.Context(), plugins.PushOptions{
		Reference:     "ghcr.io/org/artifact:v1.0.0",
		IdentityToken: "a.b.c",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, identitytoken.ErrInsecureTransport)
	assert.Zero(t, secondHits, "the redirect target must never be contacted")
	assert.False(t, secondSawToken, "the identity token must never leave the approved origin")
}
