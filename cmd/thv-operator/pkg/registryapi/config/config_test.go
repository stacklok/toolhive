// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	manager := NewConfigManager(mcpRegistry)
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

	manager := NewConfigManager(mcpRegistry)
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

	manager := NewConfigManager(mcpRegistry)
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

	manager := NewConfigManager(mcpRegistry)
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

	manager := NewConfigManager(mcpRegistry)
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
						Name:   "configmap-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified one
		assert.Equal(t, "configmap-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].File)
		assert.Equal(t, filepath.Join(RegistryJSONFilePath, "configmap-registry", RegistryJSONFileName), config.Registries[1].File.Path)
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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

		manager := NewConfigManager(mcpRegistry)
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

		manager := NewConfigManager(mcpRegistry)
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
							Path:       "registry.json",
							// No branch, tag, or commit specified - should cause an error
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git branch, tag, and commit are mutually exclusive")
		assert.Nil(t, config)
	})

	t.Run("no git path specified", func(t *testing.T) {
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
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "path is required")
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
						Name:   "git-branch-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Branch:     "main",
							Path:       "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified git registry
		assert.Equal(t, "git-branch-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Registries[1].Git.Repository)
		assert.Equal(t, "main", config.Registries[1].Git.Branch)
		assert.Empty(t, config.Registries[1].Git.Tag)
		assert.Empty(t, config.Registries[1].Git.Commit)
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "git-tag-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "git@github.com:example/repo.git",
							Tag:        "v1.2.3",
							Path:       "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified git registry
		assert.Equal(t, "git-tag-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].Git)
		assert.Equal(t, "git@github.com:example/repo.git", config.Registries[1].Git.Repository)
		assert.Empty(t, config.Registries[1].Git.Branch)
		assert.Equal(t, "v1.2.3", config.Registries[1].Git.Tag)
		assert.Empty(t, config.Registries[1].Git.Commit)
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "git-commit-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Commit:     "abc123def456",
							Path:       "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified git registry
		assert.Equal(t, "git-commit-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].Git)
		assert.Equal(t, "https://github.com/example/repo.git", config.Registries[1].Git.Repository)
		assert.Empty(t, config.Registries[1].Git.Branch)
		assert.Empty(t, config.Registries[1].Git.Tag)
		assert.Equal(t, "abc123def456", config.Registries[1].Git.Commit)
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})
}

func TestBuildConfig_GitAuth(t *testing.T) {
	t.Parallel()
	// Test suite for Git authentication configuration

	t.Run("valid git source with auth", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "private-git-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/private-repo.git",
							Branch:     "main",
							Path:       "registry.json",
							Auth: &mcpv1alpha1.GitAuthConfig{
								Username: "git",
								PasswordSecretRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "git-credentials",
									},
									Key: "token",
								},
							},
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// Second registry should be the user-specified git registry with auth
		assert.Equal(t, "private-git-registry", config.Registries[1].Name)
		require.NotNil(t, config.Registries[1].Git)
		require.NotNil(t, config.Registries[1].Git.Auth)
		assert.Equal(t, "git", config.Registries[1].Git.Auth.Username)
		assert.Equal(t, "/secrets/git-credentials/token", config.Registries[1].Git.Auth.PasswordFile)
	})

	t.Run("git auth with default key", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "private-git-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/private-repo.git",
							Branch:     "main",
							Path:       "registry.json",
							Auth: &mcpv1alpha1.GitAuthConfig{
								Username: "git",
								PasswordSecretRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "git-credentials",
									},
									// Key is empty - should default to "password"
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 2)
		require.NotNil(t, config.Registries[1].Git.Auth)
		assert.Equal(t, "/secrets/git-credentials/password", config.Registries[1].Git.Auth.PasswordFile)
	})

	t.Run("git auth missing username", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "private-git-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/private-repo.git",
							Branch:     "main",
							Path:       "registry.json",
							Auth: &mcpv1alpha1.GitAuthConfig{
								// Username is empty - should cause an error
								PasswordSecretRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "git-credentials",
									},
									Key: "token",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git auth username is required")
		assert.Nil(t, config)
	})

	t.Run("git auth missing password secret name", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "private-git-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/private-repo.git",
							Branch:     "main",
							Path:       "registry.json",
							Auth: &mcpv1alpha1.GitAuthConfig{
								Username: "git",
								PasswordSecretRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "", // Empty name should cause an error
									},
									Key: "token",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "git auth password secret reference name is required")
		assert.Nil(t, config)
	})

	t.Run("git source without auth is valid", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "public-git-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/public-repo.git",
							Branch:     "main",
							Path:       "registry.json",
							// No Auth specified - should be valid
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.Len(t, config.Registries, 2)
		require.NotNil(t, config.Registries[1].Git)
		assert.Nil(t, config.Registries[1].Git.Auth)
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

		manager := NewConfigManager(mcpRegistry)
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

		manager := NewConfigManager(mcpRegistry)
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
						Name:   "api-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified API registry
		assert.Equal(t, "api-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].API)
		assert.Equal(t, "https://api.example.com/registry", config.Registries[1].API.Endpoint)
		// Verify that other source types are nil
		assert.Nil(t, config.Registries[1].File)
		assert.Nil(t, config.Registries[1].Git)
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "sync-policy-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified one
		assert.Nil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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

		manager := NewConfigManager(mcpRegistry)
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
						Name:   "sync-policy-valid-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified one
		require.NotNil(t, config.Registries[1].SyncPolicy)
		assert.Equal(t, "30m", config.Registries[1].SyncPolicy.Interval)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "filter-nil-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Filter should be nil when not provided for the user-specified registry
		assert.Nil(t, config.Registries[1].Filter)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "filter-names-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should have the filter
		require.NotNil(t, config.Registries[1].Filter)
		require.NotNil(t, config.Registries[1].Filter.Names)
		assert.Equal(t, []string{"server-*", "tool-*"}, config.Registries[1].Filter.Names.Include)
		assert.Equal(t, []string{"*-deprecated", "*-test"}, config.Registries[1].Filter.Names.Exclude)
		// Tags should be nil when not provided
		assert.Nil(t, config.Registries[1].Filter.Tags)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "filter-tags-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: "https://github.com/example/repo.git",
							Branch:     "main",
							Path:       "registry.json",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should have the filter
		require.NotNil(t, config.Registries[1].Filter)
		require.NotNil(t, config.Registries[1].Filter.Tags)
		assert.Equal(t, []string{"stable", "production", "v1.*"}, config.Registries[1].Filter.Tags.Include)
		assert.Equal(t, []string{"beta", "alpha", "experimental"}, config.Registries[1].Filter.Tags.Exclude)
		// Names should be nil when not provided
		assert.Nil(t, config.Registries[1].Filter.Names)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "filter-both-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should have the filter
		require.NotNil(t, config.Registries[1].Filter)
		// Both name filters and tags should be present
		require.NotNil(t, config.Registries[1].Filter.Names)
		assert.Equal(t, []string{"mcp-*"}, config.Registries[1].Filter.Names.Include)
		assert.Equal(t, []string{"*-internal"}, config.Registries[1].Filter.Names.Exclude)
		require.NotNil(t, config.Registries[1].Filter.Tags)
		assert.Equal(t, []string{"latest", "stable"}, config.Registries[1].Filter.Tags.Include)
		assert.Equal(t, []string{"dev", "test"}, config.Registries[1].Filter.Tags.Exclude)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "filter-empty-registry",
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

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should have the filter
		require.NotNil(t, config.Registries[1].Filter)
		// Empty lists should still be set
		require.NotNil(t, config.Registries[1].Filter.Names)
		assert.Empty(t, config.Registries[1].Filter.Names.Include)
		assert.Empty(t, config.Registries[1].Filter.Names.Exclude)
		require.NotNil(t, config.Registries[1].Filter.Tags)
		assert.Empty(t, config.Registries[1].Filter.Tags.Include)
		assert.Empty(t, config.Registries[1].Filter.Tags.Exclude)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
					Path:       "registry.json",
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
						Path:       "registry.json",
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

	manager := NewConfigManager(mcpRegistry)
	config, err := manager.BuildConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "test-registry", config.RegistryName)
	// Should have 3 registries: default kubernetes + 2 user-specified
	require.Len(t, config.Registries, 3)

	// First registry should be the default kubernetes registry
	assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
	require.NotNil(t, config.Registries[0].Kubernetes)

	// Verify second registry (first user-specified)
	assert.Equal(t, "registry1", config.Registries[1].Name)
	require.NotNil(t, config.Registries[1].File)
	assert.Equal(t, filepath.Join(RegistryJSONFilePath, "registry1", RegistryJSONFileName), config.Registries[1].File.Path)
	require.NotNil(t, config.Registries[1].SyncPolicy)
	assert.Equal(t, "1h", config.Registries[1].SyncPolicy.Interval)
	assert.Nil(t, config.Registries[1].Filter)

	// Verify third registry (second user-specified)
	assert.Equal(t, "registry2", config.Registries[2].Name)
	require.NotNil(t, config.Registries[2].Git)
	assert.Equal(t, "https://github.com/example/repo.git", config.Registries[2].Git.Repository)
	require.NotNil(t, config.Registries[2].SyncPolicy)
	assert.Equal(t, "30m", config.Registries[2].SyncPolicy.Interval)
	require.NotNil(t, config.Registries[2].Filter)
	require.NotNil(t, config.Registries[2].Filter.Names)
	assert.Equal(t, []string{"server-*"}, config.Registries[2].Filter.Names.Include)
	assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
}

func TestBuildConfig_PVCSource(t *testing.T) {
	t.Parallel()

	t.Run("valid pvc source with default path", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "pvc-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						PVCRef: &mcpv1alpha1.PVCSource{
							ClaimName: "registry-data-pvc",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "1h",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		assert.Equal(t, "test-registry", config.RegistryName)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified PVC registry
		assert.Equal(t, "pvc-registry", config.Registries[1].Name)
		assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Registries[1].Format)
		require.NotNil(t, config.Registries[1].File)
		// Path: /config/registry/{registryName}/{pvcRef.path}
		assert.Equal(t, filepath.Join(RegistryJSONFilePath, "pvc-registry", RegistryJSONFileName), config.Registries[1].File.Path)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})

	t.Run("valid pvc source with subdirectory path", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "production-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						PVCRef: &mcpv1alpha1.PVCSource{
							ClaimName: "registry-data-pvc",
							Path:      "production/v1/servers.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "30m",
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified PVC registry
		assert.Equal(t, "production-registry", config.Registries[1].Name)
		require.NotNil(t, config.Registries[1].File)
		// Path: /config/registry/{registryName}/{pvcRef.path}
		assert.Equal(t, filepath.Join(RegistryJSONFilePath, "production-registry", "production/v1/servers.json"), config.Registries[1].File.Path)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})

	t.Run("valid pvc source with filter", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "filtered-pvc",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						PVCRef: &mcpv1alpha1.PVCSource{
							ClaimName: "registry-data-pvc",
							Path:      "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "15m",
						},
						Filter: &mcpv1alpha1.RegistryFilter{
							NameFilters: &mcpv1alpha1.NameFilter{
								Include: []string{"prod-*"},
							},
							Tags: &mcpv1alpha1.TagFilter{
								Include: []string{"production"},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		// Should have 2 registries: default kubernetes + user-specified
		require.Len(t, config.Registries, 2)
		// First registry should be the default kubernetes registry
		assert.Equal(t, DefaultRegistryName, config.Registries[0].Name)
		require.NotNil(t, config.Registries[0].Kubernetes)
		// Second registry should be the user-specified PVC registry
		assert.Equal(t, "filtered-pvc", config.Registries[1].Name)
		require.NotNil(t, config.Registries[1].File)
		// Verify filter is preserved
		require.NotNil(t, config.Registries[1].Filter)
		require.NotNil(t, config.Registries[1].Filter.Names)
		assert.Equal(t, []string{"prod-*"}, config.Registries[1].Filter.Names.Include)
		require.NotNil(t, config.Registries[1].Filter.Tags)
		assert.Equal(t, []string{"production"}, config.Registries[1].Filter.Tags.Include)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})
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
						Name:   "db-nil-registry",
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

		manager := NewConfigManager(mcpRegistry)
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
		assert.Equal(t, "prefer", config.Database.SSLMode)
		assert.Equal(t, 10, config.Database.MaxOpenConns)
		assert.Equal(t, 2, config.Database.MaxIdleConns)
		assert.Equal(t, "30m", config.Database.ConnMaxLifetime)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "db-custom-registry",
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

		manager := NewConfigManager(mcpRegistry)
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
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
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
						Name:   "db-partial-registry",
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

		manager := NewConfigManager(mcpRegistry)
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
		assert.Equal(t, "prefer", config.Database.SSLMode)
		assert.Equal(t, 10, config.Database.MaxOpenConns)
		assert.Equal(t, 2, config.Database.MaxIdleConns)
		assert.Equal(t, "30m", config.Database.ConnMaxLifetime)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})
}

func TestBuildConfig_AuthConfig(t *testing.T) {
	t.Parallel()

	t.Run("default auth config when nil", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-nil-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				// AuthConfig not specified, should default to anonymous
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
		assert.Nil(t, config.Auth.OAuth)
	})

	t.Run("explicit anonymous mode", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-anonymous-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeAnonymous,
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
		assert.Nil(t, config.Auth.OAuth)
	})

	t.Run("oauth mode with full config", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-oauth-full-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						ResourceURL:     "https://registry.example.com",
						ScopesSupported: []string{"read", "write", "admin"},
						Realm:           "my-registry",
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "keycloak",
								IssuerURL: "https://keycloak.example.com/realms/myrealm",
								Audience:  "registry-api",
								ClientID:  "registry-client",
								ClientSecretRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "keycloak-secret",
									},
									Key: "client-secret",
								},
								CACertRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "keycloak-ca",
									},
									Key: "ca.crt",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeOAuth, config.Auth.Mode)
		require.NotNil(t, config.Auth.OAuth)
		assert.Equal(t, "https://registry.example.com", config.Auth.OAuth.ResourceURL)
		assert.Equal(t, []string{"read", "write", "admin"}, config.Auth.OAuth.ScopesSupported)
		assert.Equal(t, "my-registry", config.Auth.OAuth.Realm)

		// Should have only the keycloak provider
		require.Len(t, config.Auth.OAuth.Providers, 1)

		// Verify keycloak provider
		assert.Equal(t, "keycloak", config.Auth.OAuth.Providers[0].Name)
		assert.Equal(t, "https://keycloak.example.com/realms/myrealm", config.Auth.OAuth.Providers[0].IssuerURL)
		assert.Equal(t, "registry-api", config.Auth.OAuth.Providers[0].Audience)
		assert.Equal(t, "registry-client", config.Auth.OAuth.Providers[0].ClientID)
		assert.Equal(t, "/secrets/keycloak-secret/client-secret", config.Auth.OAuth.Providers[0].ClientSecretFile)
		assert.Equal(t, "/config/certs/keycloak-ca/ca.crt", config.Auth.OAuth.Providers[0].CACertPath)
	})

	t.Run("oauth mode with multiple providers", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-multi-provider-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "google",
								IssuerURL: "https://accounts.google.com",
								Audience:  "my-app.apps.googleusercontent.com",
							},
							{
								Name:      "azure",
								IssuerURL: "https://login.microsoftonline.com/tenant-id/v2.0",
								Audience:  "api://my-app",
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeOAuth, config.Auth.Mode)
		require.NotNil(t, config.Auth.OAuth)

		// Should have google and azure providers
		require.Len(t, config.Auth.OAuth.Providers, 2)
		assert.Equal(t, "google", config.Auth.OAuth.Providers[0].Name)
		assert.Equal(t, "azure", config.Auth.OAuth.Providers[1].Name)
	})

	t.Run("oauth mode without oauth config returns error", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-oauth-no-config-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode:  mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: nil, // OAuth mode but no OAuth config
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		// OAuth mode without OAuth config should still work (oauth config is optional)
		// and won't add the default kubernetes provider since OAuth is nil
		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeOAuth, config.Auth.Mode)
		assert.Nil(t, config.Auth.OAuth)
	})

	t.Run("empty mode defaults to anonymous", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "auth-empty-mode-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: "", // Empty mode should default to anonymous
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth)
		assert.Equal(t, AuthModeAnonymous, config.Auth.Mode)
	})
}

func TestBuildOAuthProviderConfig_Validation(t *testing.T) {
	t.Parallel()

	t.Run("missing provider name", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "provider-no-name-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "", // Missing name
								IssuerURL: "https://issuer.example.com",
								Audience:  "my-app",
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider name is required")
		assert.Nil(t, config)
	})

	t.Run("missing issuer URL", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "provider-no-issuer-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "my-provider",
								IssuerURL: "", // Missing issuer URL
								Audience:  "my-app",
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider issuer URL is required")
		assert.Nil(t, config)
	})

	t.Run("missing audience", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "provider-no-audience-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "my-provider",
								IssuerURL: "https://issuer.example.com",
								Audience:  "", // Missing audience
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider audience is required")
		assert.Nil(t, config)
	})
}

func TestBuildSecretFilePath(t *testing.T) {
	t.Parallel()

	t.Run("nil secret ref returns empty string", func(t *testing.T) {
		t.Parallel()
		result := buildSecretFilePath(nil)
		assert.Equal(t, "", result)
	})

	t.Run("secret ref with key", func(t *testing.T) {
		t.Parallel()
		secretRef := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "my-secret",
			},
			Key: "my-key",
		}
		result := buildSecretFilePath(secretRef)
		assert.Equal(t, "/secrets/my-secret/my-key", result)
	})

	t.Run("secret ref without key uses default", func(t *testing.T) {
		t.Parallel()
		secretRef := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "my-secret",
			},
			Key: "",
		}
		result := buildSecretFilePath(secretRef)
		assert.Equal(t, "/secrets/my-secret/clientSecret", result)
	})
}

func TestBuildCACertFilePath(t *testing.T) {
	t.Parallel()

	t.Run("nil configmap ref returns empty string", func(t *testing.T) {
		t.Parallel()
		result := buildCACertFilePath(nil)
		assert.Equal(t, "", result)
	})

	t.Run("configmap ref with key", func(t *testing.T) {
		t.Parallel()
		configMapRef := &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "my-ca-configmap",
			},
			Key: "custom-ca.pem",
		}
		result := buildCACertFilePath(configMapRef)
		assert.Equal(t, "/config/certs/my-ca-configmap/custom-ca.pem", result)
	})

	t.Run("configmap ref without key uses default", func(t *testing.T) {
		t.Parallel()
		configMapRef := &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "my-ca-configmap",
			},
			Key: "",
		}
		result := buildCACertFilePath(configMapRef)
		assert.Equal(t, "/config/certs/my-ca-configmap/ca.crt", result)
	})
}

func TestBuildOAuthProviderConfig_DirectPaths(t *testing.T) {
	t.Parallel()

	t.Run("direct caCertPath takes precedence over CACertRef", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "direct-ca-path-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:       "my-provider",
								IssuerURL:  "https://issuer.example.com",
								Audience:   "my-app",
								CaCertPath: "/custom/path/to/ca.crt", // Direct path
								CACertRef: &corev1.ConfigMapKeySelector{ // Should be ignored
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "ca-configmap",
									},
									Key: "ca.crt",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth.OAuth)
		require.Len(t, config.Auth.OAuth.Providers, 1)
		// Direct path should be used, not the ref-based path
		assert.Equal(t, "/custom/path/to/ca.crt", config.Auth.OAuth.Providers[0].CACertPath)
	})

	t.Run("CACertRef is used when caCertPath is empty", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "ref-ca-path-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "my-provider",
								IssuerURL: "https://issuer.example.com",
								Audience:  "my-app",
								// CaCertPath not set
								CACertRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "ca-configmap",
									},
									Key: "custom-ca.pem",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth.OAuth)
		require.Len(t, config.Auth.OAuth.Providers, 1)
		// Ref-based path should be used
		assert.Equal(t, "/config/certs/ca-configmap/custom-ca.pem", config.Auth.OAuth.Providers[0].CACertPath)
	})

	t.Run("direct authTokenFile takes precedence over AuthTokenRef", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "direct-token-path-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:          "my-provider",
								IssuerURL:     "https://issuer.example.com",
								Audience:      "my-app",
								AuthTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token", // Direct path
								AuthTokenRef: &corev1.SecretKeySelector{ // Should be ignored
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "token-secret",
									},
									Key: "token",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth.OAuth)
		require.Len(t, config.Auth.OAuth.Providers, 1)
		// Direct path should be used, not the ref-based path
		assert.Equal(t, "/var/run/secrets/kubernetes.io/serviceaccount/token", config.Auth.OAuth.Providers[0].AuthTokenFile)
	})

	t.Run("AuthTokenRef is used when authTokenFile is empty", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "ref-token-path-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:      "my-provider",
								IssuerURL: "https://issuer.example.com",
								Audience:  "my-app",
								// AuthTokenFile not set
								AuthTokenRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "token-secret",
									},
									Key: "my-token",
								},
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth.OAuth)
		require.Len(t, config.Auth.OAuth.Providers, 1)
		// Ref-based path should be used
		assert.Equal(t, "/secrets/token-secret/my-token", config.Auth.OAuth.Providers[0].AuthTokenFile)
	})

	t.Run("provider with all direct paths set", func(t *testing.T) {
		t.Parallel()
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-registry",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "all-direct-paths-registry",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
					},
				},
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{
					Mode: mcpv1alpha1.MCPRegistryAuthModeOAuth,
					OAuth: &mcpv1alpha1.MCPRegistryOAuthConfig{
						Providers: []mcpv1alpha1.MCPRegistryOAuthProviderConfig{
							{
								Name:           "kubernetes-custom",
								IssuerURL:      "https://kubernetes.default.svc",
								Audience:       "my-api",
								CaCertPath:     "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
								AuthTokenFile:  "/var/run/secrets/kubernetes.io/serviceaccount/token",
								AllowPrivateIP: true,
							},
						},
					},
				},
			},
		}

		manager := NewConfigManager(mcpRegistry)
		config, err := manager.BuildConfig()

		require.NoError(t, err)
		require.NotNil(t, config)
		require.NotNil(t, config.Auth.OAuth)
		require.Len(t, config.Auth.OAuth.Providers, 1)

		// Verify the custom kubernetes provider
		customProvider := config.Auth.OAuth.Providers[0]
		assert.Equal(t, "kubernetes-custom", customProvider.Name)
		assert.Equal(t, "https://kubernetes.default.svc", customProvider.IssuerURL)
		assert.Equal(t, "my-api", customProvider.Audience)
		assert.Equal(t, "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt", customProvider.CACertPath)
		assert.Equal(t, "/var/run/secrets/kubernetes.io/serviceaccount/token", customProvider.AuthTokenFile)
		assert.True(t, customProvider.AllowPrivateIP)
	})
}
