package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

func TestBuildConfig_EmptyRegistryName(t *testing.T) {
	t.Parallel()
	// Test that an empty registry name returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "", // Empty name should cause an error
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
				Format: mcpv1alpha1.RegistryFormatToolHive,
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Equal(t, "registry name is required", err.Error())
	assert.Nil(t, config)
}

func TestBuildConfig_MissingSource(t *testing.T) {
	t.Parallel()
	// Test that a nil/empty source returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type: "", // Empty type should cause an error
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source type is required")
	assert.Nil(t, config)
}

func TestBuildConfig_EmptyFormat(t *testing.T) {
	t.Parallel()
	// Test that an empty source format returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
				Format: "", // Empty format should cause an error
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source format is required")
	assert.Nil(t, config)
}

func TestBuildConfig_UnsupportedSourceType(t *testing.T) {
	t.Parallel()
	// Test that an unsupported source type returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   "unsupported-type", // This should trigger the default case
				Format: mcpv1alpha1.RegistryFormatToolHive,
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported source type: unsupported-type")
	assert.Nil(t, config)
}

func TestBuildConfig_ConfigMapSource(t *testing.T) {
	t.Parallel()
	// Test suite for ConfigMap source validation

	t.Run("nil configmap source object", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "registry-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		// Currently this succeeds even with nil ConfigMap
		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Source.Format)
		require.NotNil(t, config.Source.File)
		assert.Equal(t, SourceTypeFile, config.Source.Type)
		assert.Equal(t, RegistryJSONFilePath, config.Source.File.Path)
	})

	t.Run("valid configmap source", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "registry-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Source.Format)
		assert.Equal(t, SourceTypeFile, config.Source.Type)
		assert.Equal(t, RegistryJSONFilePath, config.Source.File.Path)
	})
}

func TestBuildConfig_GitSource(t *testing.T) {
	t.Parallel()
	// Test suite for Git source validation

	t.Run("nil git source object", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					Git:    nil, // Nil Git source should cause an error
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git source configuration is required")
		assert.Nil(t, config)
	})

	t.Run("empty git repository", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					Git: &mcpv1alpha1.GitSource{
						Repository: "", // Empty repository should cause an error
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git repository is required")
		assert.Nil(t, config)
	})

	t.Run("no git reference specified", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					Git: &mcpv1alpha1.GitSource{
						Repository: "https://github.com/example/repo.git",
						// No branch, tag, or commit specified - should cause an error
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git branch, tag, and commit are mutually exclusive")
		assert.Nil(t, config)
	})

	t.Run("valid git source with branch", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatUpstream,
					Git: &mcpv1alpha1.GitSource{
						Repository: "https://github.com/example/repo.git",
						Branch:     "main",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatUpstream, config.Source.Format)
		assert.Equal(t, SourceTypeGit, config.Source.Type)
		require.NotNil(t, config.Source.Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Source.Git.Repository)
		assert.Equal(t, "main", config.Source.Git.Branch)
		assert.Empty(t, config.Source.Git.Tag)
		assert.Empty(t, config.Source.Git.Commit)
	})

	t.Run("valid git source with tag", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					Git: &mcpv1alpha1.GitSource{
						Repository: "git@github.com:example/repo.git",
						Tag:        "v1.2.3",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Source.Format)
		assert.Equal(t, SourceTypeGit, config.Source.Type)
		require.NotNil(t, config.Source.Git)
		assert.Equal(t, "git@github.com:example/repo.git", config.Source.Git.Repository)
		assert.Empty(t, config.Source.Git.Branch)
		assert.Equal(t, "v1.2.3", config.Source.Git.Tag)
		assert.Empty(t, config.Source.Git.Commit)
	})

	t.Run("valid git source with commit", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatUpstream,
					Git: &mcpv1alpha1.GitSource{
						Repository: "https://github.com/example/repo.git",
						Commit:     "abc123def456",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatUpstream, config.Source.Format)
		assert.Equal(t, SourceTypeGit, config.Source.Type)
		require.NotNil(t, config.Source.Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Source.Git.Repository)
		assert.Empty(t, config.Source.Git.Branch)
		assert.Empty(t, config.Source.Git.Tag)
		assert.Equal(t, "abc123def456", config.Source.Git.Commit)
	})
}

func TestBuildConfig_APISource(t *testing.T) {
	t.Parallel()
	// Test suite for API source validation

	t.Run("nil api source object", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeAPI,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					API:    nil, // Nil API source should cause an error
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "api source configuration is required")
		assert.Nil(t, config)
	})

	t.Run("empty api endpoint", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeAPI,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					API: &mcpv1alpha1.APISource{
						Endpoint: "", // Empty endpoint should cause an error
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "api endpoint is required")
		assert.Nil(t, config)
	})

	t.Run("valid api source with endpoint", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeAPI,
					Format: mcpv1alpha1.RegistryFormatUpstream,
					API: &mcpv1alpha1.APISource{
						Endpoint: "https://api.example.com/registry",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		assert.Equal(t, mcpv1alpha1.RegistryFormatUpstream, config.Source.Format)
		assert.Equal(t, SourceTypeAPI, config.Source.Type)
		require.NotNil(t, config.Source.API)
		assert.Equal(t, "https://api.example.com/registry", config.Source.API.Endpoint)
		// Verify that other source types are nil
		assert.Nil(t, config.Source.File)
		assert.Nil(t, config.Source.Git)
	})
}

func TestBuildConfig_SyncPolicy(t *testing.T) {
	t.Parallel()
	// Test suite for SyncPolicy validation

	t.Run("nil sync policy", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: nil, // Nil SyncPolicy should cause an error
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "sync policy configuration is required")
		assert.Nil(t, config)
	})

	t.Run("empty interval", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "", // Empty interval should cause an error
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "sync policy interval is required")
		assert.Nil(t, config)
	})

	t.Run("valid sync policy with interval", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "30m", // Valid interval
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.NotNil(t, config.SyncPolicy)
		assert.Equal(t, "30m", config.SyncPolicy.Interval)
	})
}

func TestBuildConfig_Filter(t *testing.T) {
	t.Parallel()
	// Test suite for Filter validation

	t.Run("nil filter", func(t *testing.T) {
		t.Parallel()
		// Nil filter should not cause an error
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
				Filter: nil, // Nil filter is optional
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Filter should be nil when not provided
		assert.Nil(t, config.Filter)
	})

	t.Run("filter with name filters", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
				Filter: &mcpv1alpha1.RegistryFilter{
					NameFilters: &mcpv1alpha1.NameFilter{
						Include: []string{"server-*", "tool-*"},
						Exclude: []string{"*-deprecated", "*-test"},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Filter)
		require.NotNil(t, config.Filter.Names)
		assert.Equal(t, []string{"server-*", "tool-*"}, config.Filter.Names.Include)
		assert.Equal(t, []string{"*-deprecated", "*-test"}, config.Filter.Names.Exclude)
		// Tags should be nil when not provided
		assert.Nil(t, config.Filter.Tags)
	})

	t.Run("filter with tags", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeGit,
					Format: mcpv1alpha1.RegistryFormatUpstream,
					Git: &mcpv1alpha1.GitSource{
						Repository: "https://github.com/example/repo.git",
						Branch:     "main",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "30m",
				},
				Filter: &mcpv1alpha1.RegistryFilter{
					Tags: &mcpv1alpha1.TagFilter{
						Include: []string{"stable", "production", "v1.*"},
						Exclude: []string{"beta", "alpha", "experimental"},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Filter)
		require.NotNil(t, config.Filter.Tags)
		assert.Equal(t, []string{"stable", "production", "v1.*"}, config.Filter.Tags.Include)
		assert.Equal(t, []string{"beta", "alpha", "experimental"}, config.Filter.Tags.Exclude)
		// Names should be nil when not provided
		assert.Nil(t, config.Filter.Names)
	})

	t.Run("filter with both name filters and tags", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeAPI,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					API: &mcpv1alpha1.APISource{
						Endpoint: "https://api.example.com/registry",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "2h",
				},
				Filter: &mcpv1alpha1.RegistryFilter{
					NameFilters: &mcpv1alpha1.NameFilter{
						Include: []string{"mcp-*"},
						Exclude: []string{"*-internal"},
					},
					Tags: &mcpv1alpha1.TagFilter{
						Include: []string{"latest", "stable"},
						Exclude: []string{"dev", "test"},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Filter)
		// Both name filters and tags should be present
		require.NotNil(t, config.Filter.Names)
		assert.Equal(t, []string{"mcp-*"}, config.Filter.Names.Include)
		assert.Equal(t, []string{"*-internal"}, config.Filter.Names.Exclude)
		require.NotNil(t, config.Filter.Tags)
		assert.Equal(t, []string{"latest", "stable"}, config.Filter.Tags.Include)
		assert.Equal(t, []string{"dev", "test"}, config.Filter.Tags.Exclude)
	})

	t.Run("filter with empty include and exclude lists", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Source: mcpv1alpha1.MCPRegistrySource{
					Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMap: &mcpv1alpha1.ConfigMapSource{
						Name: "test-configmap",
					},
				},
				SyncPolicy: &mcpv1alpha1.SyncPolicy{
					Interval: "1h",
				},
				Filter: &mcpv1alpha1.RegistryFilter{
					NameFilters: &mcpv1alpha1.NameFilter{
						Include: []string{},
						Exclude: []string{},
					},
					Tags: &mcpv1alpha1.TagFilter{
						Include: []string{},
						Exclude: []string{},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Filter)
		// Empty lists should still be set
		require.NotNil(t, config.Filter.Names)
		assert.Empty(t, config.Filter.Names.Include)
		assert.Empty(t, config.Filter.Names.Exclude)
		require.NotNil(t, config.Filter.Tags)
		assert.Empty(t, config.Filter.Tags.Include)
		assert.Empty(t, config.Filter.Tags.Exclude)
	})
}

// TestToConfigMapWithContentChecksum tests that ToConfigMapWithContentChecksum creates
// a ConfigMap with the correct checksum annotation based on the YAML content.
func TestToConfigMapWithContentChecksum(t *testing.T) {
	t.Parallel()

	// Create a populated Config object
	config := &Config{
		RegistryName: "checksum-test-registry",
		Source: &SourceConfig{
			Format: "toolhive",
			Git: &GitConfig{
				Repository: "https://github.com/example/mcp-servers.git",
				Branch:     "main",
			},
		},
		SyncPolicy: &SyncPolicyConfig{
			Interval: "15m",
		},
		Filter: &FilterConfig{
			Names: &NameFilterConfig{
				Include: []string{"mcp-*"},
				Exclude: []string{"*-dev"},
			},
		},
	}

	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
		},
	}

	// Call ToConfigMapWithContentChecksum
	configMap, err := config.ToConfigMapWithContentChecksum(mcpRegistry)

	// Verify no error occurred
	require.NoError(t, err)
	require.NotNil(t, configMap)

	// Verify basic ConfigMap properties
	assert.Equal(t, "checksum-test-registry-registry-server-config", configMap.Name)
	assert.Equal(t, "test-namespace", configMap.Namespace)

	// Verify the checksum annotation exists
	require.NotNil(t, configMap.Annotations)
	checksumValue, exists := configMap.Annotations[checksum.ContentChecksumAnnotation]
	require.True(t, exists, "Expected checksum annotation to exist")
	require.NotEmpty(t, checksumValue, "Checksum should not be empty")

	// Verify the checksum format (should be a hex string)
	// The actual checksum is calculated by ctrlutil.CalculateConfigHash
	assert.Regexp(t, "^[a-f0-9]+$", checksumValue, "Checksum should be a hex string")

	// Verify the Data contains config.yaml
	require.Contains(t, configMap.Data, "config.yaml")
	yamlData := configMap.Data["config.yaml"]
	require.NotEmpty(t, yamlData)

	// Verify YAML content includes expected fields
	assert.Contains(t, yamlData, "registryName: checksum-test-registry")
	assert.Contains(t, yamlData, "repository: https://github.com/example/mcp-servers.git")
	assert.Contains(t, yamlData, "interval: 15m")

	// Test that the same config produces the same checksum
	configMap2, err := config.ToConfigMapWithContentChecksum(mcpRegistry)
	require.NoError(t, err)
	checksum2Value := configMap2.Annotations[checksum.ContentChecksumAnnotation]
	assert.Equal(t, checksumValue, checksum2Value, "Same config should produce same checksum")

	// Test that different config produces different checksum
	config.SyncPolicy.Interval = "30m"
	configMap3, err := config.ToConfigMapWithContentChecksum(mcpRegistry)
	require.NoError(t, err)
	checksum3Value := configMap3.Annotations[checksum.ContentChecksumAnnotation]
	assert.NotEqual(t, checksumValue, checksum3Value, "Different config should produce different checksum")
}
