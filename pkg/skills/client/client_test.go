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
	"github.com/stacklok/toolhive/pkg/skills"
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
				Clients: []string{"claude-code"},
				Force:   true,
			},
			wantBody: installRequest{
				Name:    "my-skill",
				Version: "1.0.0",
				Scope:   skills.ScopeUser,
				Clients: []string{"claude-code"},
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
			// `thv skill info` reports trust state from this response alone, so
			// a provenance field the server records but the wire shape drops is
			// invisible to the user.
			name:     "provenance reaches the info renderer",
			opts:     skills.InfoOptions{Name: "signed-skill"},
			wantPath: skillsBasePath + "/signed-skill",
			response: skills.SkillInfo{
				Metadata: skills.SkillMetadata{Name: "signed-skill", Version: "1.0.0"},
				Provenance: &skills.ProvenanceInfo{
					SignerIdentity:    "/.github/workflows/release.yml",
					CertIssuer:        "https://token.actions.githubusercontent.com",
					RepositoryURI:     "https://github.com/org/signed-skill",
					RepositoryRef:     "refs/tags/v1.0.0",
					RunnerEnvironment: "github-hosted",
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
			// Guards the DTO trap: identity_token must reach the wire request,
			// not just PushOptions.
			name: "forwards identity token",
			opts: skills.PushOptions{
				Reference:     "ghcr.io/org/my-skill:v1.0.0",
				IdentityToken: "a.b.c",
			},
			wantBody: pushRequest{
				Reference:     "ghcr.io/org/my-skill:v1.0.0",
				IdentityToken: "a.b.c",
			},
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

func TestGetContent(t *testing.T) {
	t.Parallel()

	response := skills.SkillContent{
		Name:        "my-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		License:     "Apache-2.0",
		Body:        "# My Skill\nDoes things.",
		Files:       []skills.SkillFileEntry{{Path: "SKILL.md", Size: 42}},
	}

	tests := []struct {
		name       string
		opts       skills.ContentOptions
		wantQuery  string
		response   skills.SkillContent
		statusCode int
		wantErr    bool
		wantCode   int
	}{
		{
			name:       "success with local tag",
			opts:       skills.ContentOptions{Reference: "my-skill"},
			wantQuery:  "my-skill",
			response:   response,
			statusCode: http.StatusOK,
		},
		{
			name:       "success with OCI reference",
			opts:       skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v1"},
			wantQuery:  "ghcr.io/org/my-skill:v1",
			response:   response,
			statusCode: http.StatusOK,
		},
		{
			name:       "server error propagates",
			opts:       skills.ContentOptions{Reference: "missing"},
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
				assert.Equal(t, skillsBasePath+"/content", r.URL.Path)
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
	_, err := c.List(t.Context(), skills.ListOptions{})

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

	_, err := newTestClient(t, srv).Install(t.Context(), skills.InstallOptions{
		Name:          "my-skill",
		Scope:         skills.ScopeProject,
		ProjectRoot:   "/tmp/project",
		AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.True(t, got.AllowUnsigned, "allow_unsigned must reach the server")
}

// TestSyncCarriesAllowUnsigned mirrors TestInstallCarriesAllowUnsigned for
// the sync/adopt path.
func TestSyncCarriesAllowUnsigned(t *testing.T) {
	t.Parallel()

	var got syncRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(skills.SyncResult{})
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(t, srv).Sync(t.Context(), skills.SyncOptions{
		ProjectRoot:   "/tmp/project",
		Adopt:         true,
		AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.True(t, got.AllowUnsigned, "allow_unsigned must reach the server")
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

	t.Run("env overrides the timeout discovery installs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mockEnv := envmocks.NewMockReader(ctrl)
		mockEnv.EXPECT().Getenv(envAPIURL).Return("")
		mockEnv.EXPECT().Getenv(envAPITimeout).Return("3m")

		discover := func(context.Context) (string, []Option) {
			return "http://127.0.0.1:54321", []Option{WithHTTPClient(&http.Client{Timeout: defaultTimeout})}
		}
		c := newDefaultClientWithEnv(t.Context(), mockEnv, discover)
		assert.Equal(t, 3*time.Minute, c.httpClient.Timeout,
			"WithHTTPClient from discovery must not shadow an operator-set timeout")
	})
}

// TestTimeoutIsReportedAsTimeoutNotUnreachable pins the distinction the CLI
// hint depends on: a healthy-but-slow server must not be reported as absent.
func TestTimeoutIsReportedAsTimeoutNotUnreachable(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, WithTimeout(50*time.Millisecond))
	_, err := c.List(t.Context(), skills.ListOptions{})

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
	_, err = c.List(t.Context(), skills.ListOptions{})

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
	_, err := c.List(ctx, skills.ListOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrRequestTimeout)
	assert.NotErrorIs(t, err, ErrServerUnreachable)
}

// TestInstallCarriesTrustStateBackToCaller pins the full wire round-trip for
// the fields the CLI uses to report trust. The CLI is a pure HTTP client, so a
// field the server sets but the response DTO omits is invisible in production
// even though a direct unit test of the printer would still pass — every
// install would silently render as untracked.
func TestInstallCarriesTrustStateBackToCaller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response installResponse
	}{
		{
			name: "signed install reports the pinned identity",
			response: installResponse{
				Skill: skills.InstalledSkill{Metadata: skills.SkillMetadata{Name: "signed-skill"}},
				Provenance: &skills.ProvenanceInfo{
					SignerIdentity: "/.github/workflows/build-skills.yml",
					CertIssuer:     "https://token.actions.githubusercontent.com",
					RepositoryURI:  "https://github.com/stacklok/dockyard",
				},
			},
		},
		{
			name: "certificate ref and runner environment survive the round trip",
			response: installResponse{
				Skill: skills.InstalledSkill{Metadata: skills.SkillMetadata{Name: "pinned-skill"}},
				Provenance: &skills.ProvenanceInfo{
					SignerIdentity:    "/.github/workflows/build-skills.yml",
					CertIssuer:        "https://token.actions.githubusercontent.com",
					RepositoryURI:     "https://github.com/stacklok/dockyard",
					RepositoryRef:     "refs/heads/main",
					RunnerEnvironment: "github-hosted",
				},
			},
		},
		{
			name: "provisional provenance survives the round trip",
			response: installResponse{
				Skill: skills.InstalledSkill{Metadata: skills.SkillMetadata{Name: "git-skill"}},
				Provenance: &skills.ProvenanceInfo{
					SignerIdentity: "someone@example.com",
					CertIssuer:     "https://accounts.google.com",
					Provisional:    true,
				},
			},
		},
		{
			name: "explicit unsigned exception is reported as such",
			response: installResponse{
				Skill:    skills.InstalledSkill{Metadata: skills.SkillMetadata{Name: "unsigned-skill"}},
				Unsigned: true,
			},
		},
		{
			name: "an install with neither reports neither",
			response: installResponse{
				Skill: skills.InstalledSkill{Metadata: skills.SkillMetadata{Name: "plain-skill"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			got, err := newTestClient(t, srv).Install(t.Context(), skills.InstallOptions{Name: "x"})
			require.NoError(t, err)

			assert.Equal(t, tt.response.Provenance, got.Provenance,
				"provenance must survive the HTTP boundary — the CLI has no other source for it")
			assert.Equal(t, tt.response.Unsigned, got.Unsigned)
		})
	}
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
	err := c.Push(t.Context(), skills.PushOptions{
		Reference:     "ghcr.io/org/artifact:v1.0.0",
		IdentityToken: "a.b.c",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, identitytoken.ErrInsecureTransport)
	assert.Zero(t, secondHits, "the redirect target must never be contacted")
	assert.False(t, secondSawToken, "the identity token must never leave the approved origin")
}
