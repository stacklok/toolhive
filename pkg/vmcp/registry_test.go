package vmcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	testModifiedName = "Modified"
)

func TestNewImmutableRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name          string
		backends      []Backend
		expectedCount int
	}{
		{
			name: "single backend",
			backends: []Backend{
				{
					ID:            "backend-1",
					Name:          "GitHub MCP",
					BaseURL:       "http://localhost:8080",
					TransportType: "streamable-http",
					HealthStatus:  BackendHealthy,
					AuthConfig:    &authtypes.BackendAuthStrategy{Type: "unauthenticated"},
					Metadata:      map[string]string{"env": "production"},
				},
			},
			expectedCount: 1,
		},
		{
			name: "multiple backends",
			backends: []Backend{
				{ID: "github-mcp", Name: "GitHub MCP", HealthStatus: BackendHealthy},
				{ID: "jira-mcp", Name: "Jira MCP", HealthStatus: BackendHealthy},
				{ID: "slack-mcp", Name: "Slack MCP", HealthStatus: BackendDegraded},
			},
			expectedCount: 3,
		},
		{
			name: "all health statuses",
			backends: []Backend{
				{ID: "healthy", HealthStatus: BackendHealthy},
				{ID: "degraded", HealthStatus: BackendDegraded},
				{ID: "unhealthy", HealthStatus: BackendUnhealthy},
				{ID: "unknown", HealthStatus: BackendUnknown},
				{ID: "unauthenticated", HealthStatus: BackendUnauthenticated},
			},
			expectedCount: 5,
		},
		{
			name: "all transport types",
			backends: []Backend{
				{ID: "http", TransportType: "http"},
				{ID: "sse", TransportType: "sse"},
				{ID: "streamable", TransportType: "streamable-http"},
			},
			expectedCount: 3,
		},
		{
			name:          "empty slice",
			backends:      []Backend{},
			expectedCount: 0,
		},
		{
			name:          "nil slice",
			backends:      nil,
			expectedCount: 0,
		},
		{
			name: "nil metadata maps",
			backends: []Backend{
				{
					ID:         "backend-1",
					AuthConfig: nil,
					Metadata:   nil,
				},
			},
			expectedCount: 1,
		},
		{
			name: "minimal fields",
			backends: []Backend{
				{ID: "minimal"},
			},
			expectedCount: 1,
		},
		{
			name: "duplicate IDs - last wins",
			backends: []Backend{
				{ID: "dup", Name: "First", Metadata: map[string]string{"v": "1"}},
				{ID: "dup", Name: "Second", Metadata: map[string]string{"v": "2"}},
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewImmutableRegistry(tt.backends)

			require.NotNil(t, registry)
			assert.Equal(t, tt.expectedCount, registry.Count())

			// Additional validations for specific test cases
			if tt.name == "all transport types" {
				httpBackend := registry.Get(ctx, "http")
				require.NotNil(t, httpBackend)
				assert.Equal(t, "http", httpBackend.TransportType)

				sseBackend := registry.Get(ctx, "sse")
				require.NotNil(t, sseBackend)
				assert.Equal(t, "sse", sseBackend.TransportType)

				streamableBackend := registry.Get(ctx, "streamable")
				require.NotNil(t, streamableBackend)
				assert.Equal(t, "streamable-http", streamableBackend.TransportType)
			}

			if tt.name == "nil metadata maps" {
				backend := registry.Get(ctx, "backend-1")
				require.NotNil(t, backend)
				assert.Nil(t, backend.AuthConfig)
				assert.Nil(t, backend.Metadata)
			}

			if tt.name == "duplicate IDs - last wins" {
				backend := registry.Get(ctx, "dup")
				require.NotNil(t, backend)
				assert.Equal(t, "Second", backend.Name)
				assert.Equal(t, "2", backend.Metadata["v"])
			}
		})
	}
}

func TestBackendRegistry_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Setup registry for tests
	backends := []Backend{
		{
			ID:            "github-mcp",
			Name:          "GitHub MCP",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
			HealthStatus:  BackendHealthy,
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					Audience: "github-api",
				},
			},
			Metadata: map[string]string{"env": "production"},
		},
		{
			ID:           "jira-mcp",
			Name:         "Jira MCP",
			HealthStatus: BackendHealthy,
		},
	}
	registry := NewImmutableRegistry(backends)

	tests := []struct {
		name     string
		id       string
		wantNil  bool
		validate func(*testing.T, *Backend)
	}{
		{
			name:    "existing backend",
			id:      "github-mcp",
			wantNil: false,
			validate: func(t *testing.T, b *Backend) {
				t.Helper()
				assert.Equal(t, "github-mcp", b.ID)
				assert.Equal(t, "GitHub MCP", b.Name)
				assert.Equal(t, "http://localhost:8080", b.BaseURL)
				assert.Equal(t, "streamable-http", b.TransportType)
				assert.Equal(t, BackendHealthy, b.HealthStatus)
				assert.NotNil(t, b.AuthConfig)
				assert.Equal(t, "token_exchange", b.AuthConfig.Type)
				assert.Equal(t, "github-api", b.AuthConfig.TokenExchange.Audience)
				assert.Equal(t, "production", b.Metadata["env"])
			},
		},
		{
			name:    "non-existent backend",
			id:      "non-existent",
			wantNil: true,
		},
		{
			name:    "empty ID",
			id:      "",
			wantNil: true,
		},
		{
			name:    "case-sensitive lookup",
			id:      "GITHUB-MCP",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := registry.Get(ctx, tt.id)

			if tt.wantNil {
				assert.Nil(t, backend)
			} else {
				require.NotNil(t, backend)
				if tt.validate != nil {
					tt.validate(t, backend)
				}
			}
		})
	}

	t.Run("returns independent copies", func(t *testing.T) {
		t.Parallel()

		backend1 := registry.Get(ctx, "github-mcp")
		backend2 := registry.Get(ctx, "github-mcp")

		require.NotNil(t, backend1)
		require.NotNil(t, backend2)
		assert.Equal(t, backend1.ID, backend2.ID)
		assert.NotSame(t, backend1, backend2)

		// Modifying one should not affect the other
		backend1.Name = testModifiedName
		assert.Equal(t, "GitHub MCP", backend2.Name)
	})

	t.Run("concurrent reads", func(t *testing.T) {
		t.Parallel()

		done := make(chan bool)
		for i := 0; i < 10; i++ {
			go func() {
				backend := registry.Get(ctx, "github-mcp")
				assert.NotNil(t, backend)
				assert.Equal(t, "github-mcp", backend.ID)
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func TestBackendRegistry_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns all backends", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
			{ID: "backend-2", Name: "Backend 2"},
			{ID: "backend-3", Name: "Backend 3"},
		}
		registry := NewImmutableRegistry(backends)

		result := registry.List(ctx)

		assert.Len(t, result, 3)

		ids := make(map[string]bool)
		for _, b := range result {
			ids[b.ID] = true
		}
		assert.Contains(t, ids, "backend-1")
		assert.Contains(t, ids, "backend-2")
		assert.Contains(t, ids, "backend-3")
	})

	t.Run("returns modifiable copy", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{{ID: "backend-1", Name: "Backend 1"}}
		registry := NewImmutableRegistry(backends)

		list1 := registry.List(ctx)
		list1[0].Name = testModifiedName
		_ = append(list1, Backend{ID: "new"})

		list2 := registry.List(ctx)
		assert.Len(t, list2, 1)
		assert.Equal(t, "Backend 1", list2[0].Name)
	})

	t.Run("preserves all fields", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{
				ID:            "github-mcp",
				Name:          "GitHub MCP",
				TransportType: "streamable-http",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "token_exchange",
					TokenExchange: &authtypes.TokenExchangeConfig{
						Audience: "github-api",
					},
				},
				Metadata: map[string]string{"env": "production"},
			},
		}
		registry := NewImmutableRegistry(backends)

		result := registry.List(ctx)

		require.Len(t, result, 1)
		assert.Equal(t, "github-mcp", result[0].ID)
		assert.Equal(t, "github-api", result[0].AuthConfig.TokenExchange.Audience)
		assert.Equal(t, "production", result[0].Metadata["env"])
	})

	t.Run("empty registry", func(t *testing.T) {
		t.Parallel()

		registry := NewImmutableRegistry([]Backend{})
		result := registry.List(ctx)

		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("concurrent list operations", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1"},
			{ID: "backend-2"},
		}
		registry := NewImmutableRegistry(backends)

		done := make(chan bool)
		for i := 0; i < 10; i++ {
			go func() {
				result := registry.List(ctx)
				assert.Len(t, result, 2)
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func TestBackendRegistry_Count(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name     string
		backends []Backend
		want     int
	}{
		{
			name:     "empty registry",
			backends: []Backend{},
			want:     0,
		},
		{
			name:     "single backend",
			backends: []Backend{{ID: "backend-1"}},
			want:     1,
		},
		{
			name: "multiple backends",
			backends: []Backend{
				{ID: "backend-1"},
				{ID: "backend-2"},
				{ID: "backend-3"},
				{ID: "backend-4"},
				{ID: "backend-5"},
			},
			want: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewImmutableRegistry(tt.backends)

			assert.Equal(t, tt.want, registry.Count())

			// Count should match List length
			list := registry.List(ctx)
			assert.Equal(t, len(list), registry.Count())
		})
	}

	t.Run("concurrent count operations", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1"},
			{ID: "backend-2"},
			{ID: "backend-3"},
		}
		registry := NewImmutableRegistry(backends)

		done := make(chan bool)
		for i := 0; i < 10; i++ {
			go func() {
				assert.Equal(t, 3, registry.Count())
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func TestBackendToTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		backend  *Backend
		wantNil  bool
		validate func(*testing.T, *BackendTarget)
	}{
		{
			name: "complete backend",
			backend: &Backend{
				ID:            "github-mcp",
				Name:          "GitHub MCP",
				BaseURL:       "http://localhost:8080",
				TransportType: "streamable-http",
				HealthStatus:  BackendHealthy,
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "token_exchange",
					TokenExchange: &authtypes.TokenExchangeConfig{
						Audience: "github-api",
						Scopes:   []string{"repo"},
					},
				},
				Metadata: map[string]string{"env": "production"},
			},
			wantNil: false,
			validate: func(t *testing.T, target *BackendTarget) {
				t.Helper()
				assert.Equal(t, "github-mcp", target.WorkloadID)
				assert.Equal(t, "GitHub MCP", target.WorkloadName)
				assert.Equal(t, "http://localhost:8080", target.BaseURL)
				assert.Equal(t, "streamable-http", target.TransportType)
				assert.Equal(t, BackendHealthy, target.HealthStatus)
				assert.NotNil(t, target.AuthConfig)
				assert.Equal(t, "token_exchange", target.AuthConfig.Type)
				assert.Equal(t, "github-api", target.AuthConfig.TokenExchange.Audience)
				assert.Equal(t, "production", target.Metadata["env"])
				assert.False(t, target.SessionAffinity)
			},
		},
		{
			name: "preserves metadata",
			backend: &Backend{
				ID: "test",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "Bearer secret",
					},
				},
				Metadata: map[string]string{"env": "staging", "region": "us-west-2", "version": "2.0.0"},
			},
			wantNil: false,
			validate: func(t *testing.T, target *BackendTarget) {
				t.Helper()
				assert.NotNil(t, target.AuthConfig)
				// Removed timeout assertion - not part of typed config
				// Removed retries assertion - not part of typed config
				assert.Equal(t, "staging", target.Metadata["env"])
				assert.Equal(t, "us-west-2", target.Metadata["region"])
				assert.Equal(t, "2.0.0", target.Metadata["version"])
			},
		},
		{
			name: "minimal backend",
			backend: &Backend{
				ID: "minimal",
			},
			wantNil: false,
			validate: func(t *testing.T, target *BackendTarget) {
				t.Helper()
				assert.Equal(t, "minimal", target.WorkloadID)
				assert.Empty(t, target.WorkloadName)
				assert.Empty(t, target.BaseURL)
				assert.Empty(t, target.TransportType)
				assert.Equal(t, BackendHealthStatus(""), target.HealthStatus)
				assert.Nil(t, target.AuthConfig)

				assert.Nil(t, target.Metadata)
			},
		},
		{
			name:    "nil backend",
			backend: nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target := BackendToTarget(tt.backend)

			if tt.wantNil {
				assert.Nil(t, target)
			} else {
				require.NotNil(t, target)
				if tt.validate != nil {
					tt.validate(t, target)
				}
			}
		})
	}

	t.Run("health status conversion", func(t *testing.T) {
		t.Parallel()

		statuses := []BackendHealthStatus{
			BackendHealthy,
			BackendDegraded,
			BackendUnhealthy,
			BackendUnknown,
			BackendUnauthenticated,
		}

		for _, status := range statuses {
			backend := &Backend{
				ID:           "test",
				HealthStatus: status,
			}

			target := BackendToTarget(backend)

			require.NotNil(t, target)
			assert.Equal(t, status, target.HealthStatus)
		}
	})
}

func TestImmutabilityGuarantees(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("registry contents unchanged after creation", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1", HealthStatus: BackendHealthy},
		}
		registry := NewImmutableRegistry(backends)

		// Modify the returned backend
		backend := registry.Get(ctx, "backend-1")
		backend.Name = testModifiedName
		backend.HealthStatus = BackendUnhealthy

		// Get again - should be unchanged
		backend2 := registry.Get(ctx, "backend-1")
		assert.Equal(t, "Backend 1", backend2.Name)
		assert.Equal(t, BackendHealthy, backend2.HealthStatus)
	})

	t.Run("list modifications do not affect registry", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
			{ID: "backend-2", Name: "Backend 2"},
		}
		registry := NewImmutableRegistry(backends)

		// Modify the list
		list := registry.List(ctx)
		list[0].Name = testModifiedName
		_ = append(list, Backend{ID: "backend-3"})

		// Registry should be unchanged
		assert.Equal(t, 2, registry.Count())
		backend := registry.Get(ctx, "backend-1")
		assert.Equal(t, "Backend 1", backend.Name)
		assert.Nil(t, registry.Get(ctx, "backend-3"))
	})

	t.Run("original slice modifications do not affect registry", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
		}
		registry := NewImmutableRegistry(backends)

		// Modify original slice
		backends[0].Name = testModifiedName
		_ = append(backends, Backend{ID: "backend-2"})

		// Registry should be unchanged
		backend := registry.Get(ctx, "backend-1")
		assert.Equal(t, "Backend 1", backend.Name)
		assert.Equal(t, 1, registry.Count())
	})
}

func TestDomainTypes_BackendHealthStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constant BackendHealthStatus
		value    string
	}{
		{BackendHealthy, "healthy"},
		{BackendDegraded, "degraded"},
		{BackendUnhealthy, "unhealthy"},
		{BackendUnknown, "unknown"},
		{BackendUnauthenticated, "unauthenticated"},
	}

	for _, tt := range tests {
		assert.Equal(t, BackendHealthStatus(tt.value), tt.constant)
	}

	// Verify all statuses are unique
	seen := make(map[BackendHealthStatus]bool)
	for _, tt := range tests {
		assert.False(t, seen[tt.constant], "duplicate status: %s", tt.constant)
		seen[tt.constant] = true
	}
}

func TestDomainTypes_ConflictResolutionStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constant ConflictResolutionStrategy
		value    string
	}{
		{ConflictStrategyPrefix, "prefix"},
		{ConflictStrategyPriority, "priority"},
		{ConflictStrategyManual, "manual"},
	}

	for _, tt := range tests {
		assert.Equal(t, ConflictResolutionStrategy(tt.value), tt.constant)
	}

	// Verify all strategies are unique
	seen := make(map[ConflictResolutionStrategy]bool)
	for _, tt := range tests {
		assert.False(t, seen[tt.constant], "duplicate strategy: %s", tt.constant)
		seen[tt.constant] = true
	}
}

func TestDomainTypes_RoutingTable(t *testing.T) {
	t.Parallel()

	t.Run("can be created with all capability types", func(t *testing.T) {
		t.Parallel()

		toolTarget := &BackendTarget{WorkloadID: "github-mcp", BaseURL: "http://localhost:8080"}
		resourceTarget := &BackendTarget{WorkloadID: "storage-mcp", BaseURL: "http://localhost:8081"}
		promptTarget := &BackendTarget{WorkloadID: "llm-mcp", BaseURL: "http://localhost:8082"}

		table := &RoutingTable{
			Tools: map[string]*BackendTarget{
				"create_pr": toolTarget,
				"merge_pr":  toolTarget,
			},
			Resources: map[string]*BackendTarget{
				"file:///config.json":   resourceTarget,
				"file:///settings.yaml": resourceTarget,
			},
			Prompts: map[string]*BackendTarget{
				"code_review": promptTarget,
				"greeting":    promptTarget,
			},
		}

		assert.Len(t, table.Tools, 2)
		assert.Len(t, table.Resources, 2)
		assert.Len(t, table.Prompts, 2)
		assert.Equal(t, "github-mcp", table.Tools["create_pr"].WorkloadID)
		assert.Equal(t, "storage-mcp", table.Resources["file:///config.json"].WorkloadID)
		assert.Equal(t, "llm-mcp", table.Prompts["code_review"].WorkloadID)
	})

	t.Run("can be created with empty maps", func(t *testing.T) {
		t.Parallel()

		table := &RoutingTable{
			Tools:     map[string]*BackendTarget{},
			Resources: map[string]*BackendTarget{},
			Prompts:   map[string]*BackendTarget{},
		}

		assert.NotNil(t, table.Tools)
		assert.Empty(t, table.Tools)
		assert.NotNil(t, table.Resources)
		assert.Empty(t, table.Resources)
		assert.NotNil(t, table.Prompts)
		assert.Empty(t, table.Prompts)
	})
}

func TestBackendTarget_GetBackendCapabilityName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		target              *BackendTarget
		resolvedName        string
		expectedBackendName string
		description         string
	}{
		{
			name: "returns original name when set (prefix strategy)",
			target: &BackendTarget{
				WorkloadID:             "fetch",
				OriginalCapabilityName: "fetch",
			},
			resolvedName:        "fetch_fetch",
			expectedBackendName: "fetch",
			description:         "Tool renamed from 'fetch' to 'fetch_fetch' via prefix strategy",
		},
		{
			name: "returns original name when set (manual strategy)",
			target: &BackendTarget{
				WorkloadID:             "github",
				OriginalCapabilityName: "create_issue",
			},
			resolvedName:        "github_create_issue_custom",
			expectedBackendName: "create_issue",
			description:         "Tool renamed from 'create_issue' to 'github_create_issue_custom' via manual override",
		},
		{
			name: "returns resolved name when original is empty (no conflict)",
			target: &BackendTarget{
				WorkloadID:             "github",
				OriginalCapabilityName: "",
			},
			resolvedName:        "create_issue",
			expectedBackendName: "create_issue",
			description:         "No conflict resolution applied, names match",
		},
		{
			name: "returns resolved name when original is empty (priority strategy winner)",
			target: &BackendTarget{
				WorkloadID:             "github",
				OriginalCapabilityName: "",
			},
			resolvedName:        "list_issues",
			expectedBackendName: "list_issues",
			description:         "Priority strategy kept original name (winner)",
		},
		{
			name: "handles resource URIs",
			target: &BackendTarget{
				WorkloadID:             "files",
				OriginalCapabilityName: "file:///data/config.json",
			},
			resolvedName:        "file:///files/data/config.json",
			expectedBackendName: "file:///data/config.json",
			description:         "Resource URI translated for backend",
		},
		{
			name: "handles prompt names",
			target: &BackendTarget{
				WorkloadID:             "ai",
				OriginalCapabilityName: "code_review",
			},
			resolvedName:        "ai_code_review",
			expectedBackendName: "code_review",
			description:         "Prompt name translated for backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.target.GetBackendCapabilityName(tt.resolvedName)

			assert.Equal(t, tt.expectedBackendName, result,
				"GetBackendCapabilityName should return correct backend name. %s", tt.description)
		})
	}
}

// DynamicRegistry Tests

func TestNewDynamicRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name          string
		backends      []Backend
		expectedCount int
	}{
		{
			name: "single backend",
			backends: []Backend{
				{
					ID:            "backend-1",
					Name:          "GitHub MCP",
					BaseURL:       "http://localhost:8080",
					TransportType: "streamable-http",
					HealthStatus:  BackendHealthy,
					AuthConfig:    &authtypes.BackendAuthStrategy{Type: "unauthenticated"},
					Metadata:      map[string]string{"env": "production"},
				},
			},
			expectedCount: 1,
		},
		{
			name: "multiple backends",
			backends: []Backend{
				{ID: "github-mcp", Name: "GitHub MCP", HealthStatus: BackendHealthy},
				{ID: "jira-mcp", Name: "Jira MCP", HealthStatus: BackendHealthy},
				{ID: "slack-mcp", Name: "Slack MCP", HealthStatus: BackendDegraded},
			},
			expectedCount: 3,
		},
		{
			name:          "empty slice",
			backends:      []Backend{},
			expectedCount: 0,
		},
		{
			name:          "nil slice",
			backends:      nil,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewDynamicRegistry(tt.backends)

			require.NotNil(t, registry)
			assert.Equal(t, tt.expectedCount, registry.Count())
			assert.Equal(t, uint64(0), registry.Version(), "initial version should be 0")

			// Verify backends are accessible
			if tt.expectedCount > 0 {
				backends := registry.List(ctx)
				assert.Len(t, backends, tt.expectedCount)
			}
		})
	}
}

func TestDynamicRegistry_Upsert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("adds new backend", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		backend := Backend{
			ID:           "github-mcp",
			Name:         "GitHub MCP",
			HealthStatus: BackendHealthy,
		}

		err := registry.Upsert(backend)

		require.NoError(t, err)
		assert.Equal(t, 1, registry.Count())
		assert.Equal(t, uint64(1), registry.Version())

		retrieved := registry.Get(ctx, "github-mcp")
		require.NotNil(t, retrieved)
		assert.Equal(t, "GitHub MCP", retrieved.Name)
	})

	t.Run("updates existing backend", func(t *testing.T) {
		t.Parallel()

		initial := []Backend{{ID: "github-mcp", Name: "Original Name"}}
		registry := NewDynamicRegistry(initial)

		updated := Backend{ID: "github-mcp", Name: "Updated Name"}
		err := registry.Upsert(updated)

		require.NoError(t, err)
		assert.Equal(t, 1, registry.Count())
		assert.Equal(t, uint64(1), registry.Version())

		retrieved := registry.Get(ctx, "github-mcp")
		require.NotNil(t, retrieved)
		assert.Equal(t, "Updated Name", retrieved.Name)
	})

	t.Run("idempotent - multiple upserts increment version", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		backend := Backend{ID: "test", Name: "Test"}

		err := registry.Upsert(backend)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), registry.Version())

		err = registry.Upsert(backend)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), registry.Version())

		err = registry.Upsert(backend)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), registry.Version())

		assert.Equal(t, 1, registry.Count())
	})

	t.Run("empty ID returns error", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		backend := Backend{ID: "", Name: "No ID"}

		err := registry.Upsert(backend)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend ID cannot be empty")
		assert.Equal(t, uint64(0), registry.Version())
	})

	t.Run("external modifications do not affect registry", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		backend := Backend{ID: "test", Name: "Original"}

		err := registry.Upsert(backend)
		require.NoError(t, err)

		// Modify the original backend
		backend.Name = "External Modification"

		// Registry should be unchanged (because Upsert received a copy)
		retrieved := registry.Get(ctx, "test")
		require.NotNil(t, retrieved)
		assert.Equal(t, "Original", retrieved.Name)
	})
}

func TestDynamicRegistry_Remove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("removes existing backend", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{{ID: "github-mcp", Name: "GitHub"}}
		registry := NewDynamicRegistry(backends)

		err := registry.Remove("github-mcp")

		require.NoError(t, err)
		assert.Equal(t, 0, registry.Count())
		assert.Equal(t, uint64(1), registry.Version())
		assert.Nil(t, registry.Get(ctx, "github-mcp"))
	})

	t.Run("idempotent - removing non-existent backend succeeds", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)

		err := registry.Remove("non-existent")

		require.NoError(t, err)
		assert.Equal(t, uint64(1), registry.Version())
	})

	t.Run("multiple removes increment version", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{{ID: "test", Name: "Test"}}
		registry := NewDynamicRegistry(backends)

		err := registry.Remove("test")
		require.NoError(t, err)
		assert.Equal(t, uint64(1), registry.Version())

		err = registry.Remove("test")
		require.NoError(t, err)
		assert.Equal(t, uint64(2), registry.Version())

		assert.Equal(t, 0, registry.Count())
	})

	t.Run("removes one backend among many", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
			{ID: "backend-2", Name: "Backend 2"},
			{ID: "backend-3", Name: "Backend 3"},
		}
		registry := NewDynamicRegistry(backends)

		err := registry.Remove("backend-2")

		require.NoError(t, err)
		assert.Equal(t, 2, registry.Count())
		assert.Nil(t, registry.Get(ctx, "backend-2"))
		assert.NotNil(t, registry.Get(ctx, "backend-1"))
		assert.NotNil(t, registry.Get(ctx, "backend-3"))
	})
}

func TestDynamicRegistry_Version(t *testing.T) {
	t.Parallel()

	t.Run("initial version is zero", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)

		assert.Equal(t, uint64(0), registry.Version())
	})

	t.Run("version increments on upsert", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)

		_ = registry.Upsert(Backend{ID: "b1", Name: "Backend 1"})
		assert.Equal(t, uint64(1), registry.Version())

		_ = registry.Upsert(Backend{ID: "b2", Name: "Backend 2"})
		assert.Equal(t, uint64(2), registry.Version())

		_ = registry.Upsert(Backend{ID: "b1", Name: "Backend 1 Updated"})
		assert.Equal(t, uint64(3), registry.Version())
	})

	t.Run("version increments on remove", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{{ID: "test"}}
		registry := NewDynamicRegistry(backends)

		_ = registry.Remove("test")
		assert.Equal(t, uint64(1), registry.Version())

		_ = registry.Remove("non-existent")
		assert.Equal(t, uint64(2), registry.Version())
	})

	t.Run("version increments monotonically", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		versions := []uint64{}

		// Perform mixed operations
		_ = registry.Upsert(Backend{ID: "b1"})
		versions = append(versions, registry.Version())

		_ = registry.Upsert(Backend{ID: "b2"})
		versions = append(versions, registry.Version())

		_ = registry.Remove("b1")
		versions = append(versions, registry.Version())

		_ = registry.Upsert(Backend{ID: "b3"})
		versions = append(versions, registry.Version())

		_ = registry.Remove("b2")
		versions = append(versions, registry.Version())

		// Verify monotonic increase
		for i := 1; i < len(versions); i++ {
			assert.Greater(t, versions[i], versions[i-1])
		}
	})
}

func TestDynamicRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("concurrent reads", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
			{ID: "backend-2", Name: "Backend 2"},
		}
		registry := NewDynamicRegistry(backends)

		done := make(chan bool)
		for i := 0; i < 50; i++ {
			go func() {
				_ = registry.Get(ctx, "backend-1")
				_ = registry.List(ctx)
				_ = registry.Count()
				_ = registry.Version()
				done <- true
			}()
		}

		for i := 0; i < 50; i++ {
			<-done
		}
	})

	t.Run("concurrent writes", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)

		done := make(chan bool)
		for i := 0; i < 50; i++ {
			go func(id int) {
				backendID := fmt.Sprintf("backend-%d", id)
				_ = registry.Upsert(Backend{ID: backendID, Name: fmt.Sprintf("Backend %d", id)})
				done <- true
			}(i)
		}

		for i := 0; i < 50; i++ {
			<-done
		}

		assert.Equal(t, 50, registry.Count())
		assert.Equal(t, uint64(50), registry.Version())
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		t.Parallel()

		backends := []Backend{
			{ID: "backend-1", Name: "Backend 1"},
			{ID: "backend-2", Name: "Backend 2"},
		}
		registry := NewDynamicRegistry(backends)

		done := make(chan bool)

		// Readers
		for i := 0; i < 25; i++ {
			go func() {
				_ = registry.Get(ctx, "backend-1")
				_ = registry.List(ctx)
				_ = registry.Count()
				done <- true
			}()
		}

		// Writers
		for i := 0; i < 25; i++ {
			go func(id int) {
				backendID := fmt.Sprintf("backend-%d", id)
				_ = registry.Upsert(Backend{ID: backendID, Name: fmt.Sprintf("Backend %d", id)})
				_ = registry.Remove(backendID)
				done <- true
			}(i)
		}

		for i := 0; i < 50; i++ {
			<-done
		}

		// Verify registry is still functional
		assert.GreaterOrEqual(t, registry.Count(), 0)
		assert.Greater(t, registry.Version(), uint64(0))
	})
}

func TestDynamicRegistry_ImmutabilityGuarantees(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("Get returns independent copies", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		_ = registry.Upsert(Backend{ID: "test", Name: "Original"})

		backend1 := registry.Get(ctx, "test")
		backend2 := registry.Get(ctx, "test")

		require.NotNil(t, backend1)
		require.NotNil(t, backend2)
		assert.Equal(t, backend1.ID, backend2.ID)
		assert.NotSame(t, backend1, backend2)

		// Modifying one should not affect the other
		backend1.Name = testModifiedName
		assert.Equal(t, "Original", backend2.Name)
	})

	t.Run("List returns independent copies", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		_ = registry.Upsert(Backend{ID: "test", Name: "Original"})

		list1 := registry.List(ctx)
		list1[0].Name = testModifiedName

		list2 := registry.List(ctx)
		assert.Equal(t, "Original", list2[0].Name)
	})

	t.Run("modifications after Get do not affect registry", func(t *testing.T) {
		t.Parallel()

		registry := NewDynamicRegistry(nil)
		_ = registry.Upsert(Backend{ID: "test", Name: "Original"})

		backend := registry.Get(ctx, "test")
		backend.Name = testModifiedName
		backend.HealthStatus = BackendUnhealthy

		// Registry should be unchanged
		retrieved := registry.Get(ctx, "test")
		require.NotNil(t, retrieved)
		assert.Equal(t, "Original", retrieved.Name)
		assert.NotEqual(t, BackendUnhealthy, retrieved.HealthStatus)
	})
}
