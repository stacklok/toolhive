// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/api"
)

func TestBaseProvider_SkillMethods(t *testing.T) {
	t.Parallel()

	bp := NewBaseProvider(func() (*thvregistry.Registry, error) {
		return &thvregistry.Registry{}, nil
	})

	t.Run("GetSkill returns nil", func(t *testing.T) {
		t.Parallel()
		skill, err := bp.GetSkill("any-namespace", "any-name")
		require.NoError(t, err)
		require.Nil(t, skill)
	})

	t.Run("ListSkills returns empty result", func(t *testing.T) {
		t.Parallel()
		result, err := bp.ListSkills(nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Skills)
	})

	t.Run("SearchSkills returns empty result", func(t *testing.T) {
		t.Parallel()
		result, err := bp.SearchSkills("any-query")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Skills)
	})
}

func TestLocalRegistryProvider_SkillMethods(t *testing.T) {
	t.Parallel()

	provider := NewLocalRegistryProvider()

	t.Run("GetSkill returns nil", func(t *testing.T) {
		t.Parallel()
		skill, err := provider.GetSkill("any-namespace", "any-name")
		require.NoError(t, err)
		require.Nil(t, skill)
	})

	t.Run("ListSkills returns empty result", func(t *testing.T) {
		t.Parallel()
		result, err := provider.ListSkills(&api.SkillsListOptions{Search: "test"})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Skills)
	})

	t.Run("SearchSkills returns empty result", func(t *testing.T) {
		t.Parallel()
		result, err := provider.SearchSkills("any-query")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Skills)
	})
}

func TestAPIRegistryProvider_GetSkill(t *testing.T) {
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
			name:      "returns skill from API",
			namespace: "io.github.user",
			skillName: "my-skill",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills/") {
					// Handle the validation probe for ListServers
					writeEmptyServerList(w)
					return
				}
				assert.Equal(t, "/v0.1/x/dev.toolhive/skills/io.github.user/my-skill", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(thvregistry.Skill{
					Namespace:   "io.github.user",
					Name:        "my-skill",
					Version:     "1.0.0",
					Description: "A test skill",
				})
				assert.NoError(t, err)
			},
			wantSkill: &thvregistry.Skill{
				Namespace:   "io.github.user",
				Name:        "my-skill",
				Version:     "1.0.0",
				Description: "A test skill",
			},
		},
		{
			name:      "returns error on not found",
			namespace: "io.github.user",
			skillName: "nonexistent",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills/") {
					writeEmptyServerList(w)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("skill not found"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			provider, err := NewAPIRegistryProvider(server.URL, true, nil)
			require.NoError(t, err)

			skill, err := provider.GetSkill(tt.namespace, tt.skillName)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantSkill, skill)
		})
	}
}

func TestAPIRegistryProvider_ListSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       *api.SkillsListOptions
		handler    http.HandlerFunc
		wantSkills []*thvregistry.Skill
		wantEmpty  bool
		wantErr    bool
	}{
		{
			name: "returns skills list",
			opts: nil,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills") {
					writeEmptyServerList(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListWireResponse{
					Skills: []*thvregistry.Skill{
						{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
						{Namespace: "io.github.b", Name: "skill-2", Version: "2.0.0"},
					},
					Metadata: skillsListMetadata{Count: 2, NextCursor: ""},
				})
				assert.NoError(t, err)
			},
			wantSkills: []*thvregistry.Skill{
				{Namespace: "io.github.a", Name: "skill-1", Version: "1.0.0"},
				{Namespace: "io.github.b", Name: "skill-2", Version: "2.0.0"},
			},
		},
		{
			name: "returns empty list",
			opts: nil,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills") {
					writeEmptyServerList(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListWireResponse{
					Skills:   []*thvregistry.Skill{},
					Metadata: skillsListMetadata{Count: 0, NextCursor: ""},
				})
				assert.NoError(t, err)
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			provider, err := NewAPIRegistryProvider(server.URL, true, nil)
			require.NoError(t, err)

			result, err := provider.ListSkills(tt.opts)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			if tt.wantEmpty {
				require.Empty(t, result.Skills)
			} else {
				require.Equal(t, tt.wantSkills, result.Skills)
			}
		})
	}
}

func TestAPIRegistryProvider_SearchSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      string
		handler    http.HandlerFunc
		wantSkills []*thvregistry.Skill
		wantEmpty  bool
		wantErr    bool
	}{
		{
			name:  "returns matching skills",
			query: "kubernetes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills") {
					writeEmptyServerList(w)
					return
				}
				assert.Equal(t, "kubernetes", r.URL.Query().Get("search"))
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListWireResponse{
					Skills: []*thvregistry.Skill{
						{Namespace: "io.github.user", Name: "k8s-deploy", Version: "1.0.0", Description: "Kubernetes deploy skill"},
					},
					Metadata: skillsListMetadata{Count: 1, NextCursor: ""},
				})
				assert.NoError(t, err)
			},
			wantSkills: []*thvregistry.Skill{
				{Namespace: "io.github.user", Name: "k8s-deploy", Version: "1.0.0", Description: "Kubernetes deploy skill"},
			},
		},
		{
			name:  "returns empty for no matches",
			query: "nonexistent-query",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills") {
					writeEmptyServerList(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(skillsListWireResponse{
					Skills:   []*thvregistry.Skill{},
					Metadata: skillsListMetadata{Count: 0, NextCursor: ""},
				})
				assert.NoError(t, err)
			},
			wantEmpty: true,
		},
		{
			name:  "returns error on server failure",
			query: "test",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v0.1/x/dev.toolhive/skills") {
					writeEmptyServerList(w)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal server error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			provider, err := NewAPIRegistryProvider(server.URL, true, nil)
			require.NoError(t, err)

			result, err := provider.SearchSkills(tt.query)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			if tt.wantEmpty {
				require.Empty(t, result.Skills)
			} else {
				require.Equal(t, tt.wantSkills, result.Skills)
			}
		})
	}
}

// skillsListWireResponse mirrors the JSON wire format for skills list/search responses.
// This is used in test handlers to produce realistic API responses.
type skillsListWireResponse struct {
	Skills   []*thvregistry.Skill `json:"skills"`
	Metadata skillsListMetadata   `json:"metadata"`
}

type skillsListMetadata struct {
	Count      int    `json:"count"`
	NextCursor string `json:"nextCursor"`
}

// writeEmptyServerList writes a minimal valid ServerListResponse for the
// validation probe that NewAPIRegistryProvider performs (GET /v0.1/servers?limit=1&version=latest).
func writeEmptyServerList(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"servers":[],"metadata":{"count":0,"nextCursor":""}}`))
}
