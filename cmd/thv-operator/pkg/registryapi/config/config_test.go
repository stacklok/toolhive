package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "default",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-configmap",
						},
						Key: "registry.json",
					},
				},
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Equal(t, "registry name is required", err.Error())
	assert.Nil(t, config)
}

func TestBuildConfig_NoRegistries(t *testing.T) {
	t.Parallel()
	// Test that empty registries array returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{}, // Empty registries should cause an error
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one registry must be specified")
	assert.Nil(t, config)
}

func TestBuildConfig_MissingSource(t *testing.T) {
	t.Parallel()
	// Test that a registry with no source type returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "default",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					// No source type specified - should cause an error
				},
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one source type")
	assert.Nil(t, config)
}

func TestBuildConfig_EmptyRegistryNameInConfig(t *testing.T) {
	t.Parallel()
	// Test that an empty registry name in config returns the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "", // Empty name should cause an error
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-configmap",
						},
						Key: "registry.json",
					},
				},
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry name is required")
	assert.Nil(t, config)
}

func TestBuildConfig_DuplicateRegistryNames(t *testing.T) {
	t.Parallel()
	// Test that duplicate registry names return the correct error
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "duplicate",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-configmap",
						},
						Key: "registry.json",
					},
				},
				{
					Name:   "duplicate", // Duplicate name should cause an error
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "test-configmap2",
						},
						Key: "registry.json",
					},
				},
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate registry name")
	assert.Nil(t, config)
}

func TestBuildConfig_ConfigMapSource(t *testing.T) {
	t.Parallel()
	// Test suite for ConfigMap source validation

	t.Run("valid configmap source", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "registry-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "default", config.Registries[0].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[0].Format)
		require.NotNil(t, config.Registries[0].File)
		assert.Equal(t, filepath.Join(RegistryJSONFilePath, "default", RegistryJSONFileName), config.Registries[0].File.Path)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git:    &mcpv1alpha1.GitSource{}, // Nil Git source should cause an error
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

	t.Run("empty git repository", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "", // Empty repository should cause an error
						},
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							// No branch, tag, or commit specified - should cause an error
						},
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Branch:     "main",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "default", config.Registries[0].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[0].Format)
		require.NotNil(t, config.Registries[0].Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Registries[0].Git.Repository)
		assert.Equal(t, "main", config.Registries[0].Git.Branch)
		assert.Empty(t, config.Registries[0].Git.Tag)
		assert.Empty(t, config.Registries[0].Git.Commit)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
	})

	t.Run("valid git source with tag", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "git@github.com:example/repo.git",
							Tag:        "v1.2.3",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "default", config.Registries[0].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[0].Format)
		require.NotNil(t, config.Registries[0].Git)
		assert.Equal(t, "git@github.com:example/repo.git", config.Registries[0].Git.Repository)
		assert.Empty(t, config.Registries[0].Git.Branch)
		assert.Equal(t, "v1.2.3", config.Registries[0].Git.Tag)
		assert.Empty(t, config.Registries[0].Git.Commit)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
	})

	t.Run("valid git source with commit", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Commit:     "abc123def456",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "default", config.Registries[0].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[0].Format)
		require.NotNil(t, config.Registries[0].Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Registries[0].Git.Repository)
		assert.Empty(t, config.Registries[0].Git.Branch)
		assert.Empty(t, config.Registries[0].Git.Tag)
		assert.Equal(t, "abc123def456", config.Registries[0].Git.Commit)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						API:    &mcpv1alpha1.APISource{}, // Nil API source should cause an error
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

	t.Run("empty api endpoint", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						API: &mcpv1alpha1.APISource{
							Endpoint: "", // Empty endpoint should cause an error
						},
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						API: &mcpv1alpha1.APISource{
							Endpoint: "https://api.example.com/registry",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "default", config.Registries[0].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[0].Format)
		require.NotNil(t, config.Registries[0].API)
		assert.Equal(t, "https://api.example.com/registry", config.Registries[0].API.Endpoint)
		// Verify that other source types are nil
		assert.Nil(t, config.Registries[0].File)
		assert.Nil(t, config.Registries[0].Git)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
	})
}

func TestBuildConfig_SyncPolicy(t *testing.T) {
	t.Parallel()
	// Test suite for SyncPolicy validation

	t.Run("nil sync policy", func(t *testing.T) {
		t.Parallel()
		// Nil sync policy is now optional (moved into registry config)
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: nil, // Nil SyncPolicy is now optional
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		assert.Nil(t, config.Registries[0].SyncPolicy)
	})

	t.Run("empty interval", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "", // Empty interval should cause an error
						},
					},
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "30m", // Valid interval
						},
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		require.NotNil(t, config.Registries[0].SyncPolicy)
		assert.Equal(t, "30m", config.Registries[0].SyncPolicy.Interval)
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
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
						Filter: nil, // Nil filter is optional
					},
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		require.Len(t, config.Registries, 1)
		// Filter should be nil when not provided
		assert.Nil(t, config.Registries[0].Filter)
	})

	t.Run("filter with name filters", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
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
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 1)
		require.NotNil(t, config.Registries[0].Filter)
		require.NotNil(t, config.Registries[0].Filter.Names)
		assert.Equal(t, []string{"server-*", "tool-*"}, config.Registries[0].Filter.Names.Include)
		assert.Equal(t, []string{"*-deprecated", "*-test"}, config.Registries[0].Filter.Names.Exclude)
		// Tags should be nil when not provided
		assert.Nil(t, config.Registries[0].Filter.Tags)
	})

	t.Run("filter with tags", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Branch:     "main",
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
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 1)
		require.NotNil(t, config.Registries[0].Filter)
		require.NotNil(t, config.Registries[0].Filter.Tags)
		assert.Equal(t, []string{"stable", "production", "v1.*"}, config.Registries[0].Filter.Tags.Include)
		assert.Equal(t, []string{"beta", "alpha", "experimental"}, config.Registries[0].Filter.Tags.Exclude)
		// Names should be nil when not provided
		assert.Nil(t, config.Registries[0].Filter.Names)
	})

	t.Run("filter with both name filters and tags", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						API: &mcpv1alpha1.APISource{
							Endpoint: "https://api.example.com/registry",
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
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 1)
		require.NotNil(t, config.Registries[0].Filter)
		// Both name filters and tags should be present
		require.NotNil(t, config.Registries[0].Filter.Names)
		assert.Equal(t, []string{"mcp-*"}, config.Registries[0].Filter.Names.Include)
		assert.Equal(t, []string{"*-internal"}, config.Registries[0].Filter.Names.Exclude)
		require.NotNil(t, config.Registries[0].Filter.Tags)
		assert.Equal(t, []string{"latest", "stable"}, config.Registries[0].Filter.Tags.Include)
		assert.Equal(t, []string{"dev", "test"}, config.Registries[0].Filter.Tags.Exclude)
	})

	t.Run("filter with empty include and exclude lists", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
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
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 1)
		require.NotNil(t, config.Registries[0].Filter)
		// Empty lists should still be set
		require.NotNil(t, config.Registries[0].Filter.Names)
		assert.Empty(t, config.Registries[0].Filter.Names.Include)
		assert.Empty(t, config.Registries[0].Filter.Names.Exclude)
		require.NotNil(t, config.Registries[0].Filter.Tags)
		assert.Empty(t, config.Registries[0].Filter.Tags.Include)
		assert.Empty(t, config.Registries[0].Filter.Tags.Exclude)
	})
}

// TestToConfigMapWithContentChecksum tests that ToConfigMapWithContentChecksum creates
// a ConfigMap with the correct checksum annotation based on the YAML content.
func TestToConfigMapWithContentChecksum(t *testing.T) {
	t.Parallel()

	// Create a populated Config object
	config := &Config{
		RegistryName: "checksum-test-registry",
		Registries: []RegistryConfig{
			{
				Name:   "default",
				Format: "toolhive",
				Git: &GitConfig{
					Repository: "https://github.com/example/mcp-servers.git",
					Branch:     "main",
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
	config.Registries[0].SyncPolicy.Interval = "30m"
	configMap3, err := config.ToConfigMapWithContentChecksum(mcpRegistry)
	require.NoError(t, err)
	checksum3Value := configMap3.Annotations[checksum.ContentChecksumAnnotation]
	assert.NotEqual(t, checksumValue, checksum3Value, "Different config should produce different checksum")
}

func TestBuildConfig_MultipleRegistries(t *testing.T) {
	t.Parallel()
	// Test that multiple registries are properly built
	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-registry",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "registry1",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "configmap1",
						},
						Key: "registry.json",
					},
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "1h",
					},
				},
				{
					Name:   "registry2",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					Git: &mcpv1alpha1.GitSource{
						Repository: "https://github.com/example/repo.git",
						Branch:     "main",
					},
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "30m",
					},
					Filter: &mcpv1alpha1.RegistryFilter{
						NameFilters: &mcpv1alpha1.NameFilter{
							Include: []string{"server-*"},
						},
					},
				},
			},
		},
	}

	manager := NewConfigManagerForTesting(mcpRegistry)
	config, err := manager.BuildConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "test-registry", config.RegistryName)
	require.Len(t, config.Registries, 2)

	// Verify first registry
	assert.Equal(t, "registry1", config.Registries[0].Name)
	require.NotNil(t, config.Registries[0].File)
	assert.Equal(t, filepath.Join(RegistryJSONFilePath, "registry1", RegistryJSONFileName), config.Registries[0].File.Path)
	require.NotNil(t, config.Registries[0].SyncPolicy)
	assert.Equal(t, "1h", config.Registries[0].SyncPolicy.Interval)
	assert.Nil(t, config.Registries[0].Filter)

	// Verify second registry
	assert.Equal(t, "registry2", config.Registries[1].Name)
	require.NotNil(t, config.Registries[1].Git)
	assert.Equal(t, "https://github.com/example/repo.git", config.Registries[1].Git.Repository)
	require.NotNil(t, config.Registries[1].SyncPolicy)
	assert.Equal(t, "30m", config.Registries[1].SyncPolicy.Interval)
	require.NotNil(t, config.Registries[1].Filter)
	require.NotNil(t, config.Registries[1].Filter.Names)
	assert.Equal(t, []string{"server-*"}, config.Registries[1].Filter.Names.Include)
}

func TestBuildConfig_DatabaseConfig(t *testing.T) {
	t.Parallel()

	t.Run("default database config when nil", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				// DatabaseConfig not specified, should use defaults
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Database)

		// Verify default values
		assert.Equal(t, "postgres", config.Database.Host)
		assert.Equal(t, 5432, config.Database.Port)
		assert.Equal(t, "db_app", config.Database.User)
		assert.Equal(t, "db_migrator", config.Database.MigrationUser)
		assert.Equal(t, "registry", config.Database.Database)
		assert.Equal(t, "disable", config.Database.SSLMode)
		assert.Equal(t, 10, config.Database.MaxOpenConns)
		assert.Equal(t, 2, config.Database.MaxIdleConns)
		assert.Equal(t, "30m", config.Database.ConnMaxLifetime)
	})

	t.Run("custom database config", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				DatabaseConfig: &mcpv1alpha1.MCPRegistryDatabaseConfig{
					Host:            "custom-postgres.example.com",
					Port:            15432,
					User:            "custom_app_user",
					MigrationUser:   "custom_migrator",
					Database:        "custom_registry_db",
					SSLMode:         "require",
					MaxOpenConns:    25,
					MaxIdleConns:    5,
					ConnMaxLifetime: "1h",
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Database)

		// Verify custom values
		assert.Equal(t, "custom-postgres.example.com", config.Database.Host)
		assert.Equal(t, 15432, config.Database.Port)
		assert.Equal(t, "custom_app_user", config.Database.User)
		assert.Equal(t, "custom_migrator", config.Database.MigrationUser)
		assert.Equal(t, "custom_registry_db", config.Database.Database)
		assert.Equal(t, "require", config.Database.SSLMode)
		assert.Equal(t, 25, config.Database.MaxOpenConns)
		assert.Equal(t, 5, config.Database.MaxIdleConns)
		assert.Equal(t, "1h", config.Database.ConnMaxLifetime)
	})

	t.Run("partial database config uses defaults for missing fields", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				DatabaseConfig: &mcpv1alpha1.MCPRegistryDatabaseConfig{
					Host:     "custom-host",
					Database: "custom-db",
					// Other fields omitted, should use defaults
				},
			},
		}

		manager := NewConfigManagerForTesting(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Database)

		// Verify custom values are used
		assert.Equal(t, "custom-host", config.Database.Host)
		assert.Equal(t, "custom-db", config.Database.Database)

		// Verify defaults are used for omitted fields
		assert.Equal(t, 5432, config.Database.Port)
		assert.Equal(t, "db_app", config.Database.User)
		assert.Equal(t, "db_migrator", config.Database.MigrationUser)
		assert.Equal(t, "disable", config.Database.SSLMode)
		assert.Equal(t, 10, config.Database.MaxOpenConns)
		assert.Equal(t, 2, config.Database.MaxIdleConns)
		assert.Equal(t, "30m", config.Database.ConnMaxLifetime)
	})
}
