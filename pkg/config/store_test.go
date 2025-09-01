package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalStore_Load(t *testing.T) {
	t.Parallel()

	t.Run("load with empty path uses default", func(t *testing.T) {
		t.Parallel()

		store := NewLocalStore("")

		// Mock the getConfigPath function to return a temporary path
		tempConfig := t.TempDir() + "/config.yaml"
		originalPathGenerator := getConfigPath
		getConfigPath = func() (string, error) {
			return tempConfig, nil
		}
		defer func() { getConfigPath = originalPathGenerator }()

		config, err := store.Load(context.Background())
		require.NoError(t, err)
		require.NotNil(t, config)

		// Should create default config
		assert.Equal(t, "", config.Secrets.ProviderType)
		assert.False(t, config.Secrets.SetupCompleted)
	})
}

func TestNewConfigStoreWithDetector(t *testing.T) {
	t.Parallel()

	t.Run("always creates local store", func(t *testing.T) {
		t.Parallel()

		store, err := NewConfigStoreWithDetector("", nil)
		require.NoError(t, err)

		_, ok := store.(*LocalStore)
		assert.True(t, ok, "Expected LocalStore")
	})
}

func TestNewConfigStore(t *testing.T) {
	t.Parallel()

	store, err := NewConfigStore()
	require.NoError(t, err)

	_, ok := store.(*LocalStore)
	assert.True(t, ok, "Expected LocalStore")
}
