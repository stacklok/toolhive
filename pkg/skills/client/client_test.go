// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
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
		opts       skills.ListOptions
		wantQuery  map[string]string
		response   listResponse
		statusCode int
		wantErr    bool
	}{
		{
			name: "no filters",
			opts: skills.ListOptions{},
			response: listResponse{Skills: []skills.InstalledSkill{
				{
					Metadata:    skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
					Scope:       skills.ScopeUser,
					Status:      skills.InstallStatusInstalled,
					InstalledAt: now,
				},
			}},
			statusCode: http.StatusOK,
		},
		{
			name: "with all filters",
			opts: skills.ListOptions{
				Scope:       skills.ScopeProject,
				ClientApp:   "claude-code",
				ProjectRoot: "/home/user/proj",
			},
			wantQuery: map[string]string{
				"scope":        "project",
				"client":       "claude-code",
				"project_root": "/home/user/proj",
			},
			response:   listResponse{Skills: []skills.InstalledSkill{}},
			statusCode: http.StatusOK,
		},
		{
			name:       "server error",
			opts:       skills.ListOptions{},
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, skillsBasePath, r.URL.Path)

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
			assert.Equal(t, tt.response.Skills, got)
		})
	}
}

func TestInstall(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		opts       skills.InstallOptions
		wantBody   installRequest
		response   installResponse
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name: "success",
			opts: skills.InstallOptions{
				Name:    "my-skill",
				Version: "1.0.0",
				Scope:   skills.ScopeUser,
				Client:  "claude-code",
				Force:   true,
			},
			wantBody: installRequest{
				Name:    "my-skill",
				Version: "1.0.0",
				Scope:   skills.ScopeUser,
				Client:  "claude-code",
				Force:   true,
			},
			response: installResponse{Skill: skills.InstalledSkill{
				Metadata:    skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
				Scope:       skills.ScopeUser,
				Status:      skills.InstallStatusInstalled,
				InstalledAt: now,
			}},
			statusCode: http.StatusCreated,
		},
		{
			name:       "bad request",
			opts:       skills.InstallOptions{Name: ""},
			statusCode: http.StatusBadRequest,
			wantErr:    true,
			wantCode:   http.StatusBadRequest,
		},
		{
			name:       "conflict",
			opts:       skills.InstallOptions{Name: "existing-skill"},
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
				assert.Equal(t, skillsBasePath, r.URL.Path)

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
			assert.Equal(t, tt.response.Skill, got.Skill)
		})
	}
}

func TestUninstall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       skills.UninstallOptions
		wantPath   string
		wantQuery  map[string]string
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success",
			opts:       skills.UninstallOptions{Name: "my-skill"},
			wantPath:   skillsBasePath + "/my-skill",
			statusCode: http.StatusNoContent,
		},
		{
			name: "with scope and project root",
			opts: skills.UninstallOptions{
				Name:        "my-skill",
				Scope:       skills.ScopeProject,
				ProjectRoot: "/home/user/proj",
			},
			wantPath: skillsBasePath + "/my-skill",
			wantQuery: map[string]string{
				"scope":        "project",
				"project_root": "/home/user/proj",
			},
			statusCode: http.StatusNoContent,
		},
		{
			name:       "not found",
			opts:       skills.UninstallOptions{Name: "missing"},
			wantPath:   skillsBasePath + "/missing",
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
		opts       skills.InfoOptions
		wantPath   string
		response   skills.SkillInfo
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:     "success",
			opts:     skills.InfoOptions{Name: "my-skill"},
			wantPath: skillsBasePath + "/my-skill",
			response: skills.SkillInfo{
				Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
				InstalledSkill: &skills.InstalledSkill{
					Metadata:    skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
					Scope:       skills.ScopeUser,
					Status:      skills.InstallStatusInstalled,
					InstalledAt: now,
				},
			},
			statusCode: http.StatusOK,
		},
		{
			name:       "not found",
			opts:       skills.InfoOptions{Name: "missing"},
			wantPath:   skillsBasePath + "/missing",
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
		response   skills.ValidationResult
		statusCode int
		wantErr    bool
	}{
		{
			name:       "valid skill",
			path:       "/home/user/my-skill",
			wantBody:   validateRequest{Path: "/home/user/my-skill"},
			response:   skills.ValidationResult{Valid: true},
			statusCode: http.StatusOK,
		},
		{
			name:     "invalid skill",
			path:     "/home/user/bad-skill",
			wantBody: validateRequest{Path: "/home/user/bad-skill"},
			response: skills.ValidationResult{
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
				assert.Equal(t, skillsBasePath+"/validate", r.URL.Path)

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
		opts       skills.BuildOptions
		wantBody   buildRequest
		response   skills.BuildResult
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			opts:       skills.BuildOptions{Path: "/home/user/my-skill", Tag: "v1.0.0"},
			wantBody:   buildRequest{Path: "/home/user/my-skill", Tag: "v1.0.0"},
			response:   skills.BuildResult{Reference: "ghcr.io/org/my-skill:v1.0.0"},
			statusCode: http.StatusOK,
		},
		{
			name:       "bad request",
			opts:       skills.BuildOptions{},
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, skillsBasePath+"/build", r.URL.Path)

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
		opts       skills.PushOptions
		wantBody   pushRequest
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success",
			opts:       skills.PushOptions{Reference: "ghcr.io/org/my-skill:v1.0.0"},
			wantBody:   pushRequest{Reference: "ghcr.io/org/my-skill:v1.0.0"},
			statusCode: http.StatusNoContent,
		},
		{
			name:       "not found",
			opts:       skills.PushOptions{Reference: "ghcr.io/org/missing:v1"},
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
				assert.Equal(t, skillsBasePath+"/push", r.URL.Path)

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

func TestConnectionError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	c := NewClient(srv.URL)
	_, err := c.List(t.Context(), skills.ListOptions{})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServerUnreachable), "expected ErrServerUnreachable, got: %v", err)
}

func TestNewDefaultClient(t *testing.T) {
	t.Parallel()

	t.Run("falls back to default URL when env is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")

		c := newDefaultClientWithEnv(mockEnv)
		assert.Equal(t, defaultBaseURL, c.baseURL)
	})

	t.Run("uses TOOLHIVE_API_URL from env", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("http://localhost:9999")

		c := newDefaultClientWithEnv(mockEnv)
		assert.Equal(t, "http://localhost:9999", c.baseURL)
	})

	t.Run("applies options", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")

		c := newDefaultClientWithEnv(mockEnv, WithTimeout(5*time.Second))
		assert.Equal(t, 5*time.Second, c.httpClient.Timeout)
	})
}

func TestWithHTTPClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 99 * time.Second}
	c := NewClient("http://example.com", WithHTTPClient(custom))
	assert.Equal(t, custom, c.httpClient)
}

func TestURLEncodesSkillNames(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, skillsBasePath+"/my%20skill%2Fv2", r.URL.RawPath)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(skills.SkillInfo{
			Metadata: skills.SkillMetadata{Name: "my skill/v2"},
		}))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.Info(t.Context(), skills.InfoOptions{Name: "my skill/v2"})
	require.NoError(t, err)
	assert.Equal(t, "my skill/v2", got.Metadata.Name)
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
