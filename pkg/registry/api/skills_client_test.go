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

func newTestSkillsClient(t *testing.T, server *httptest.Server) SkillsClient {
	t.Helper()
	client, err := NewSkillsClient(server.URL, true, nil)
	require.NoError(t, err)
	return client
}

func TestSkillsClient_GetSkill(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		skillName string
		handler   http.HandlerFunc
		wantSkill *thvregistry.Skill
		wantErr   bool
	}{
		{
			name:      "success",
			namespace: "io.github.user",
			skillName: "my-skill",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v0.1/x/dev.toolhive/skills/io.github.user/my-skill", r.URL.Path)
				require.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Skill{
					Namespace:   "io.github.user",
					Name:        "my-skill",
					Version:     "1.0.0",
					Description: "A test skill",
				})
				require.NoError(t, err)
			},
			wantSkill: &thvregistry.Skill{
				Namespace:   "io.github.user",
				Name:        "my-skill",
				Version:     "1.0.0",
				Description: "A test skill",
			},
		},
		{
			name:      "not found",
			namespace: "io.github.user",
			skillName: "nonexistent",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("skill not found"))
			},
			wantErr: true,
		},
		{
			name:      "server error",
			namespace: "io.github.user",
			skillName: "my-skill",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			},
			wantErr: true,
		},
		{
			name:      "path escaping",
			namespace: "io.github.user/special",
			skillName: "my skill",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// Verify that the path components are properly escaped
				require.Equal(t, "/v0.1/x/dev.toolhive/skills/io.github.user%2Fspecial/my%20skill", r.URL.RawPath)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Skill{
					Namespace: "io.github.user/special",
					Name:      "my skill",
					Version:   "1.0.0",
				})
				require.NoError(t, err)
			},
			wantSkill: &thvregistry.Skill{
				Namespace: "io.github.user/special",
				Name:      "my skill",
				Version:   "1.0.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSkillsClient(t, server)
			skill, err := client.GetSkill(t.Context(), tt.namespace, tt.skillName)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantSkill, skill)
		})
	}
}

func TestSkillsClient_GetSkillVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		skillName string
		version   string
		handler   http.HandlerFunc
		wantSkill *thvregistry.Skill
		wantErr   bool
	}{
		{
			name:      "success",
			namespace: "io.github.user",
			skillName: "my-skill",
			version:   "2.0.0",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v0.1/x/dev.toolhive/skills/io.github.user/my-skill/versions/2.0.0", r.URL.Path)
				require.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Skill{
					Namespace:   "io.github.user",
					Name:        "my-skill",
					Version:     "2.0.0",
					Description: "Version 2",
				})
				require.NoError(t, err)
			},
			wantSkill: &thvregistry.Skill{
				Namespace:   "io.github.user",
				Name:        "my-skill",
				Version:     "2.0.0",
				Description: "Version 2",
			},
		},
		{
			name:      "version not found",
			namespace: "io.github.user",
			skillName: "my-skill",
			version:   "99.0.0",
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

			client := newTestSkillsClient(t, server)
			skill, err := client.GetSkillVersion(t.Context(), tt.namespace, tt.skillName, tt.version)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantSkill, skill)
		})
	}
}

func TestSkillsClient_ListSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       *SkillsListOptions
		handler    http.HandlerFunc
		wantCount  int
		wantErr    bool
		wantSkills []*thvregistry.Skill
	}{
		{
			name: "single page",
			opts: nil,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListResponse{
					Skills: []*thvregistry.Skill{
						{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
						{Namespace: "io.github.b", Name: "skill-2", Version: "1.0.0"},
					},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 2, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount: 2,
			wantSkills: []*thvregistry.Skill{
				{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
				{Namespace: "io.github.b", Name: "skill-2", Version: "1.0.0"},
			},
		},
		{
			name: "pagination across multiple pages",
			opts: &SkillsListOptions{Limit: 1},
			handler: func() http.HandlerFunc {
				callCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					callCount++
					w.Header().Set("Content-Type", "application/json")

					cursor := r.URL.Query().Get("cursor")
					var resp skillsListResponse

					switch {
					case cursor == "" && callCount == 1:
						resp = skillsListResponse{
							Skills: []*thvregistry.Skill{
								{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
							},
							Metadata: struct {
								Count      int    `json:"count"`
								NextCursor string `json:"nextCursor"`
							}{Count: 1, NextCursor: "page2"},
						}
					case cursor == "page2":
						resp = skillsListResponse{
							Skills: []*thvregistry.Skill{
								{Namespace: "io.github.b", Name: "skill-2", Version: "1.0.0"},
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
			wantSkills: []*thvregistry.Skill{
				{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
				{Namespace: "io.github.b", Name: "skill-2", Version: "1.0.0"},
			},
		},
		{
			name: "empty result",
			opts: nil,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListResponse{
					Skills: []*thvregistry.Skill{},
					Metadata: struct {
						Count      int    `json:"count"`
						NextCursor string `json:"nextCursor"`
					}{Count: 0, NextCursor: ""},
				})
				require.NoError(t, err)
			},
			wantCount:  0,
			wantSkills: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSkillsClient(t, server)
			result, err := client.ListSkills(t.Context(), tt.opts)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Skills, tt.wantCount)
			if tt.wantSkills != nil {
				require.Equal(t, tt.wantSkills, result.Skills)
			}
		})
	}
}

func TestSkillsClient_SearchSkills(t *testing.T) {
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
				err := json.NewEncoder(w).Encode(skillsListResponse{
					Skills: []*thvregistry.Skill{
						{Namespace: "io.github.user", Name: "k8s-skill", Version: "1.0.0", Description: "Kubernetes skill"},
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
				err := json.NewEncoder(w).Encode(skillsListResponse{
					Skills: []*thvregistry.Skill{},
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

			client := newTestSkillsClient(t, server)
			result, err := client.SearchSkills(t.Context(), tt.query)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Skills, tt.wantCount)
		})
	}
}

func TestSkillsClient_ListSkillVersions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v0.1/x/dev.toolhive/skills/io.github.user/my-skill/versions", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(skillsListResponse{
			Skills: []*thvregistry.Skill{
				{Namespace: "io.github.user", Name: "my-skill", Version: "1.0.0"},
				{Namespace: "io.github.user", Name: "my-skill", Version: "2.0.0"},
				{Namespace: "io.github.user", Name: "my-skill", Version: "3.0.0"},
			},
			Metadata: struct {
				Count      int    `json:"count"`
				NextCursor string `json:"nextCursor"`
			}{Count: 3, NextCursor: ""},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newTestSkillsClient(t, server)
	result, err := client.ListSkillVersions(t.Context(), "io.github.user", "my-skill")
	require.NoError(t, err)
	require.Len(t, result.Skills, 3)
	require.Equal(t, "1.0.0", result.Skills[0].Version)
	require.Equal(t, "2.0.0", result.Skills[1].Version)
	require.Equal(t, "3.0.0", result.Skills[2].Version)
}

func TestSkillsClient_ErrorHandling(t *testing.T) {
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

			client := newTestSkillsClient(t, server)
			_, err := client.GetSkill(t.Context(), "io.github.user", "my-skill")
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

func TestSkillsClient_MalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	client := newTestSkillsClient(t, server)
	_, err := client.GetSkill(t.Context(), "io.github.user", "my-skill")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode response")
}

func TestSkillsClient_TrailingSlashInBaseURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The path should not have a double slash
		require.NotContains(t, r.URL.Path, "//")
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(thvregistry.Skill{
			Namespace: "io.github.user",
			Name:      "my-skill",
			Version:   "1.0.0",
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	// Create client with trailing slash
	client, err := NewSkillsClient(server.URL+"/", true, nil)
	require.NoError(t, err)

	skill, err := client.GetSkill(t.Context(), "io.github.user", "my-skill")
	require.NoError(t, err)
	require.Equal(t, "io.github.user", skill.Namespace)
}

func TestSkillsClient_ListSkillsWithSearch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-query", r.URL.Query().Get("search"))
		require.Equal(t, "50", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(skillsListResponse{
			Skills: []*thvregistry.Skill{
				{Namespace: "io.github.user", Name: "test-skill", Version: "1.0.0"},
			},
			Metadata: struct {
				Count      int    `json:"count"`
				NextCursor string `json:"nextCursor"`
			}{Count: 1, NextCursor: ""},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newTestSkillsClient(t, server)
	result, err := client.ListSkills(t.Context(), &SkillsListOptions{
		Search: "test-query",
		Limit:  50,
	})
	require.NoError(t, err)
	require.Len(t, result.Skills, 1)
	require.Equal(t, "test-skill", result.Skills[0].Name)
}

func TestRegistryHTTPError_Unwrap(t *testing.T) {
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
