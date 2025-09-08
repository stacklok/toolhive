package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_CreateStorage(t *testing.T) {
	t.Parallel()

	t.Run("Default to LocalStorage", func(t *testing.T) {
		t.Parallel()
		config := &Config{}
		storage, err := config.CreateStorage()
		require.NoError(t, err)
		assert.IsType(t, &LocalStorage{}, storage)
		assert.Equal(t, 30*time.Minute, config.TTL)
	})

	t.Run("Explicit LocalStorage", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "local",
			TTL:         1 * time.Hour,
		}
		storage, err := config.CreateStorage()
		require.NoError(t, err)
		assert.IsType(t, &LocalStorage{}, storage)
		assert.Equal(t, 1*time.Hour, config.TTL)
	})

	t.Run("Redis without config returns error", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "redis",
		}
		_, err := config.CreateStorage()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "redis configuration required")
	})

	t.Run("Unknown storage type returns error", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "unknown",
		}
		_, err := config.CreateStorage()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown storage type")
	})

	t.Run("Valkey is treated as Redis", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "valkey",
		}
		_, err := config.CreateStorage()
		// Should fail with same error as Redis without config
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "redis configuration required")
	})
}

func TestCreateManagerFromConfig(t *testing.T) {
	t.Parallel()

	t.Run("Create manager with LocalStorage", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "local",
			TTL:         1 * time.Hour,
		}
		manager, err := CreateManagerFromConfig(config)
		require.NoError(t, err)
		require.NotNil(t, manager)
		defer manager.Stop()

		// Test that manager works
		err = manager.AddWithID("test-session")
		assert.NoError(t, err)
		
		session, ok := manager.Get("test-session")
		assert.True(t, ok)
		assert.Equal(t, "test-session", session.ID())
	})

	t.Run("Create manager with invalid config", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "invalid",
		}
		manager, err := CreateManagerFromConfig(config)
		assert.Error(t, err)
		assert.Nil(t, manager)
	})
}

func TestCreateTypedManagerFromConfig(t *testing.T) {
	t.Parallel()

	t.Run("Create typed manager for SSE sessions", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "local",
			TTL:         30 * time.Minute,
		}
		manager, err := CreateTypedManagerFromConfig(config, SessionTypeSSE)
		require.NoError(t, err)
		require.NotNil(t, manager)
		defer manager.Stop()

		// Test that manager creates correct session type
		err = manager.AddWithID("sse-session")
		assert.NoError(t, err)
		
		session, ok := manager.Get("sse-session")
		assert.True(t, ok)
		assert.Equal(t, SessionTypeSSE, session.Type())
	})

	t.Run("Create typed manager for MCP sessions", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "local",
			TTL:         45 * time.Minute,
		}
		manager, err := CreateTypedManagerFromConfig(config, SessionTypeMCP)
		require.NoError(t, err)
		require.NotNil(t, manager)
		defer manager.Stop()

		// Test that manager creates correct session type
		err = manager.AddWithID("mcp-session")
		assert.NoError(t, err)
		
		session, ok := manager.Get("mcp-session")
		assert.True(t, ok)
		assert.Equal(t, SessionTypeMCP, session.Type())
	})

	t.Run("Create typed manager with invalid config", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			StorageType: "redis", // Will fail without Redis config
		}
		manager, err := CreateTypedManagerFromConfig(config, SessionTypeStreamable)
		assert.Error(t, err)
		assert.Nil(t, manager)
	})
}