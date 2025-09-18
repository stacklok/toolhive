package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindExistingContainer_RejectsPartialMatches tests that findExistingContainer
// only returns exact matches and rejects partial matches that might be returned
// by the container runtime's name filter
func TestFindExistingContainer_RejectsPartialMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name           string
		searchName     string
		mockContainers []container.Summary
		expectedID     string
		expectFound    bool
	}{
		{
			name:       "exact match found",
			searchName: "myapp",
			mockContainers: []container.Summary{
				{ID: "exact-match-id", Names: []string{"/myapp"}},
				{ID: "partial-match-id", Names: []string{"/myapp-test"}}, // partial match that should be ignored
			},
			expectedID:  "exact-match-id",
			expectFound: true,
		},
		{
			name:       "no exact match with partial matches present",
			searchName: "app",
			mockContainers: []container.Summary{
				{ID: "partial-1", Names: []string{"/myapp"}},    // contains "app" but not exact
				{ID: "partial-2", Names: []string{"/webapp"}},   // contains "app" but not exact
				{ID: "partial-3", Names: []string{"/app-test"}}, // starts with "app" but not exact
			},
			expectedID:  "",
			expectFound: false,
		},
		{
			name:       "exact match among multiple partial matches",
			searchName: "service",
			mockContainers: []container.Summary{
				{ID: "partial-1", Names: []string{"/service-web"}},
				{ID: "partial-2", Names: []string{"/microservice"}},
				{ID: "exact-match", Names: []string{"/service"}}, // exact match
				{ID: "partial-3", Names: []string{"/service-db"}},
			},
			expectedID:  "exact-match",
			expectFound: true,
		},
		{
			name:       "exact match with leading slash",
			searchName: "worker",
			mockContainers: []container.Summary{
				{ID: "slash-match", Names: []string{"/worker"}},
				{ID: "partial", Names: []string{"/background-worker"}},
			},
			expectedID:  "slash-match",
			expectFound: true,
		},
		{
			name:       "exact match without leading slash",
			searchName: "task",
			mockContainers: []container.Summary{
				{ID: "no-slash-match", Names: []string{"task"}}, // without leading slash
				{ID: "partial", Names: []string{"/task-runner"}},
			},
			expectedID:  "no-slash-match",
			expectFound: true,
		},
		{
			name:           "no containers found",
			searchName:     "nonexistent",
			mockContainers: []container.Summary{},
			expectedID:     "",
			expectFound:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeDockerAPI{
				listFunc: func(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
					// Verify that the name filter is being used
					nameFilters := opts.Filters.Get("name")
					if len(nameFilters) > 0 {
						assert.Equal(t, tt.searchName, nameFilters[0], "Expected name filter to be set correctly")
					}

					// Return mock containers that simulate runtime's partial matching behavior
					return tt.mockContainers, nil
				},
			}

			client := &Client{api: api}
			containerID, err := client.findExistingContainer(ctx, tt.searchName)

			require.NoError(t, err)
			if tt.expectFound {
				assert.Equal(t, tt.expectedID, containerID)
				assert.NotEmpty(t, containerID)
			} else {
				assert.Empty(t, containerID)
			}
		})
	}
}

// TestFindContainerByExactName_RejectsPartialMatches tests that findContainerByExactName
// only returns exact matches and rejects partial matches, even when using both label
// and name filters
func TestFindContainerByExactName_RejectsPartialMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name           string
		searchName     string
		mockContainers []container.Summary
		expectedID     string
		expectFound    bool
	}{
		{
			name:       "exact match with toolhive label",
			searchName: "mcp-server",
			mockContainers: []container.Summary{
				{
					ID:     "exact-match-id",
					Names:  []string{"/mcp-server"},
					Labels: map[string]string{"toolhive": "true"},
				},
				{
					ID:     "partial-match-id",
					Names:  []string{"/mcp-server-backup"},
					Labels: map[string]string{"toolhive": "true"},
				},
			},
			expectedID:  "exact-match-id",
			expectFound: true,
		},
		{
			name:       "no exact match despite partial matches",
			searchName: "web",
			mockContainers: []container.Summary{
				{
					ID:     "partial-1",
					Names:  []string{"/webapp"},
					Labels: map[string]string{"toolhive": "true"},
				},
				{
					ID:     "partial-2",
					Names:  []string{"/web-frontend"},
					Labels: map[string]string{"toolhive": "true"},
				},
			},
			expectedID:  "",
			expectFound: false,
		},
		{
			name:       "exact match among toolhive containers only",
			searchName: "api",
			mockContainers: []container.Summary{
				{
					ID:     "toolhive-exact",
					Names:  []string{"/api"},
					Labels: map[string]string{"toolhive": "true"},
				},
				{
					ID:     "toolhive-partial",
					Names:  []string{"/api-gateway"},
					Labels: map[string]string{"toolhive": "true"},
				},
			},
			expectedID:  "toolhive-exact",
			expectFound: true,
		},
		{
			name:       "multiple exact name matches - returns first toolhive one",
			searchName: "duplicated",
			mockContainers: []container.Summary{
				{
					ID:     "first-toolhive",
					Names:  []string{"/duplicated"},
					Labels: map[string]string{"toolhive": "true"},
				},
				{
					ID:     "second-toolhive",
					Names:  []string{"/duplicated"},
					Labels: map[string]string{"toolhive": "true"},
				},
			},
			expectedID:  "first-toolhive",
			expectFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeDockerAPI{
				listFunc: func(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
					// Verify that both toolhive label filter and name filter are being used
					toolhiveFilter := opts.Filters.Get("label")
					nameFilter := opts.Filters.Get("name")

					assert.Contains(t, toolhiveFilter, "toolhive=true", "Expected toolhive label filter")
					assert.Contains(t, nameFilter, tt.searchName, "Expected name filter to be set")

					// Return mock containers that simulate runtime's partial matching behavior
					return tt.mockContainers, nil
				},
			}

			client := &Client{api: api}
			containerID, err := client.findContainerByExactName(ctx, tt.searchName)

			require.NoError(t, err)
			if tt.expectFound {
				assert.Equal(t, tt.expectedID, containerID)
				assert.NotEmpty(t, containerID)
			} else {
				assert.Empty(t, containerID)
			}
		})
	}
}

// TestPartialMatchingPrevention_IntegrationScenarios tests real-world scenarios
// where partial matching could cause problems
func TestPartialMatchingPrevention_IntegrationScenarios(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Scenario: User has containers "app", "app-web", "app-db"
	// When looking for "app", should only find exact match, not the others
	mockContainers := []container.Summary{
		{ID: "app-main", Names: []string{"/app"}, Labels: map[string]string{"toolhive": "true"}},
		{ID: "app-web-id", Names: []string{"/app-web"}, Labels: map[string]string{"toolhive": "true"}},
		{ID: "app-db-id", Names: []string{"/app-db"}, Labels: map[string]string{"toolhive": "true"}},
		{ID: "webapp-id", Names: []string{"/webapp"}, Labels: map[string]string{"toolhive": "true"}},
	}

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			// Simulate that container runtime returned all containers due to partial matching
			return mockContainers, nil
		},
	}

	client := &Client{api: api}

	// Test findExistingContainer with exact match
	containerID, err := client.findExistingContainer(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, "app-main", containerID, "Should find exact match 'app', not partial matches")

	// Test findContainerByExactName with exact match
	containerID, err = client.findContainerByExactName(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, "app-main", containerID, "Should find exact match 'app', not partial matches")

	// Test that partial match requests don't return wrong containers
	containerID, err = client.findExistingContainer(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, containerID, "Should not find anything for non-existent exact name")
}
