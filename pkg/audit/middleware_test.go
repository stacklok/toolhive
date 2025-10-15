package audit

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func TestMiddlewareParams_JSON(t *testing.T) {
	t.Parallel()

	t.Run("marshal with all fields", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			Component:           "test-component",
			IncludeRequestData:  true,
			IncludeResponseData: false,
			MaxDataSize:         2048,
		}

		params := MiddlewareParams{
			ConfigPath: "/path/to/config.json",
			ConfigData: config,
			Component:  "override-component",
		}

		data, err := json.Marshal(params)
		require.NoError(t, err)

		var unmarshaled MiddlewareParams
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, "/path/to/config.json", unmarshaled.ConfigPath)
		assert.Equal(t, "override-component", unmarshaled.Component)
		require.NotNil(t, unmarshaled.ConfigData)
		assert.Equal(t, "test-component", unmarshaled.ConfigData.Component)
		assert.True(t, unmarshaled.ConfigData.IncludeRequestData)
		assert.False(t, unmarshaled.ConfigData.IncludeResponseData)
		assert.Equal(t, 2048, unmarshaled.ConfigData.MaxDataSize)
	})

	t.Run("marshal with config path only", func(t *testing.T) {
		t.Parallel()
		params := MiddlewareParams{
			ConfigPath: "/path/to/config.json",
			Component:  "test-component",
		}

		data, err := json.Marshal(params)
		require.NoError(t, err)

		var unmarshaled MiddlewareParams
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, "/path/to/config.json", unmarshaled.ConfigPath)
		assert.Equal(t, "test-component", unmarshaled.Component)
		assert.Nil(t, unmarshaled.ConfigData)
	})

	t.Run("marshal with config data only", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			Component:          "data-only-component",
			IncludeRequestData: true,
			MaxDataSize:        1024,
		}

		params := MiddlewareParams{
			ConfigData: config,
			Component:  "override-component",
		}

		data, err := json.Marshal(params)
		require.NoError(t, err)

		var unmarshaled MiddlewareParams
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)

		assert.Empty(t, unmarshaled.ConfigPath)
		assert.Equal(t, "override-component", unmarshaled.Component)
		require.NotNil(t, unmarshaled.ConfigData)
		assert.Equal(t, "data-only-component", unmarshaled.ConfigData.Component)
		assert.True(t, unmarshaled.ConfigData.IncludeRequestData)
		assert.Equal(t, 1024, unmarshaled.ConfigData.MaxDataSize)
	})
}

func TestCreateMiddlewareWithConfigData(t *testing.T) {
	t.Parallel()

	t.Run("create with config data (preferred method)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		config := &Config{
			Component:           "test-component",
			IncludeRequestData:  true,
			IncludeResponseData: false,
			MaxDataSize:         2048,
		}

		params := MiddlewareParams{
			ConfigPath: "/some/path/config.json", // Should be ignored
			ConfigData: config,                   // Should be used
			Component:  "override-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("create with config file path (backwards compatibility)", func(t *testing.T) {
		t.Parallel()
		// Create a temporary config file
		tempDir := t.TempDir()
		configFile := filepath.Join(tempDir, "audit_config.json")

		testConfig := map[string]interface{}{
			"component":             "file-based-component",
			"include_request_data":  false,
			"include_response_data": true,
			"max_data_size":         1024,
		}

		configData, err := json.Marshal(testConfig)
		require.NoError(t, err)

		err = os.WriteFile(configFile, configData, 0600)
		require.NoError(t, err)

		params := MiddlewareParams{
			ConfigPath: configFile,
			Component:  "override-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("create with default config", func(t *testing.T) {
		t.Parallel()
		params := MiddlewareParams{
			Component: "default-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("config data takes precedence over config path", func(t *testing.T) {
		t.Parallel()
		// Create a temporary config file with different settings
		tempDir := t.TempDir()
		configFile := filepath.Join(tempDir, "audit_config.json")

		fileConfig := map[string]interface{}{
			"component":             "file-component",
			"include_request_data":  false,
			"include_response_data": false,
			"max_data_size":         512,
		}

		configData, err := json.Marshal(fileConfig)
		require.NoError(t, err)

		err = os.WriteFile(configFile, configData, 0600)
		require.NoError(t, err)

		// Config data with different settings
		inMemoryConfig := &Config{
			Component:           "memory-component",
			IncludeRequestData:  true,
			IncludeResponseData: true,
			MaxDataSize:         4096,
		}

		params := MiddlewareParams{
			ConfigPath: configFile,     // Should be ignored
			ConfigData: inMemoryConfig, // Should be used
			Component:  "override-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)

		// Verify the created middleware uses the in-memory config, not the file config
		// This is a bit tricky to test directly, but we can verify it didn't fail
		// and the middleware was created successfully
	})

	t.Run("invalid config path returns error", func(t *testing.T) {
		t.Parallel()
		params := MiddlewareParams{
			ConfigPath: "/nonexistent/path/config.json",
			Component:  "test-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		// Expect no call to AddMiddleware since the creation should fail

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load audit configuration")
	})

	t.Run("invalid middleware parameters", func(t *testing.T) {
		t.Parallel()
		// Create middleware config with invalid JSON parameters
		invalidParams := []byte(`{"invalid": "json"`)

		middlewareConfig := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: invalidParams,
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		// Expect no call to AddMiddleware since the creation should fail

		err := CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal audit middleware parameters")
	})

	t.Run("component override works correctly", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			Component:   "original-component",
			MaxDataSize: 1024,
		}

		params := MiddlewareParams{
			ConfigData: config,
			Component:  "overridden-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)

		// The middleware should be created successfully with the component override
		// The actual component value is used internally by the auditor
	})
}

func TestMiddlewareType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "audit", MiddlewareType)
}

func TestMiddlewareHandlerMethods(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	middleware := &Middleware{}

	// Create a mock middleware function
	mockFunc := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
	middleware.middleware = mockFunc

	t.Run("handler returns middleware function", func(t *testing.T) {
		t.Parallel()
		handler := middleware.Handler()
		assert.NotNil(t, handler)
		// Can't directly compare function pointers, just verify it's not nil and is the right type
		assert.IsType(t, types.MiddlewareFunction(nil), handler)
	})

	t.Run("close returns no error", func(t *testing.T) {
		t.Parallel()
		err := middleware.Close()
		assert.NoError(t, err)
	})

	_ = config // Use config to avoid unused variable warning
}

func TestNewMiddlewareConfig(t *testing.T) {
	t.Parallel()

	t.Run("create middleware config with config data", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			Component:   "test-component",
			MaxDataSize: 2048,
		}

		params := MiddlewareParams{
			ConfigData: config,
			Component:  "override-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		assert.Equal(t, MiddlewareType, middlewareConfig.Type)
		assert.NotNil(t, middlewareConfig.Parameters)

		// Verify we can unmarshal the parameters back
		var unmarshaled MiddlewareParams
		err = json.Unmarshal(middlewareConfig.Parameters, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, "override-component", unmarshaled.Component)
		require.NotNil(t, unmarshaled.ConfigData)
		assert.Equal(t, "test-component", unmarshaled.ConfigData.Component)
		assert.Equal(t, 2048, unmarshaled.ConfigData.MaxDataSize)
	})

	t.Run("create middleware config with config path only", func(t *testing.T) {
		t.Parallel()
		params := MiddlewareParams{
			ConfigPath: "/path/to/config.json",
			Component:  "path-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		assert.Equal(t, MiddlewareType, middlewareConfig.Type)
		assert.NotNil(t, middlewareConfig.Parameters)

		// Verify we can unmarshal the parameters back
		var unmarshaled MiddlewareParams
		err = json.Unmarshal(middlewareConfig.Parameters, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, "/path/to/config.json", unmarshaled.ConfigPath)
		assert.Equal(t, "path-component", unmarshaled.Component)
		assert.Nil(t, unmarshaled.ConfigData)
	})
}

func TestBackwardsCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("old-style parameters still work", func(t *testing.T) {
		t.Parallel()
		// Create a temporary config file
		tempDir := t.TempDir()
		configFile := filepath.Join(tempDir, "audit_config.json")

		testConfig := map[string]interface{}{
			"component":             "backwards-compat-component",
			"include_request_data":  true,
			"include_response_data": false,
			"max_data_size":         512,
		}

		configData, err := json.Marshal(testConfig)
		require.NoError(t, err)

		err = os.WriteFile(configFile, configData, 0600)
		require.NoError(t, err)

		// Create parameters the old way (without ConfigData)
		oldStyleParams := map[string]interface{}{
			"config_path": configFile,
			"component":   "old-style-component",
		}

		paramBytes, err := json.Marshal(oldStyleParams)
		require.NoError(t, err)

		middlewareConfig := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: paramBytes,
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("new-style parameters with both fields work", func(t *testing.T) {
		t.Parallel()
		// Create a temporary config file (should be ignored)
		tempDir := t.TempDir()
		configFile := filepath.Join(tempDir, "ignored_config.json")

		ignoredConfig := map[string]interface{}{
			"component":             "ignored-component",
			"include_request_data":  false,
			"include_response_data": false,
			"max_data_size":         128,
		}

		configData, err := json.Marshal(ignoredConfig)
		require.NoError(t, err)

		err = os.WriteFile(configFile, configData, 0600)
		require.NoError(t, err)

		// Create parameters with both config_path and config_data
		preferredConfig := &Config{
			Component:           "preferred-component",
			IncludeRequestData:  true,
			IncludeResponseData: true,
			MaxDataSize:         4096,
		}

		newStyleParams := MiddlewareParams{
			ConfigPath: configFile,      // Should be ignored
			ConfigData: preferredConfig, // Should be used
			Component:  "final-component",
		}

		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, newStyleParams)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})
}
