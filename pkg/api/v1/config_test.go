package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

func TestConfigRouter(t *testing.T) {
	t.Parallel()

	// Create a test config provider to avoid using the singleton
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := config.NewPathProvider(configPath)

	routes := NewConfigRoutesWithProvider(provider)
	router := configRouterWithRoutes(routes)
	assert.NotNil(t, router)
}

func TestGetUsageMetricsStatus(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name                  string
		initialDisabledStatus bool
		expectedEnabled       bool
	}{
		{
			name:                  "usage metrics enabled",
			initialDisabledStatus: false,
			expectedEnabled:       true,
		},
		{
			name:                  "usage metrics disabled",
			initialDisabledStatus: true,
			expectedEnabled:       false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a temporary config directory for this test
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

			// Ensure the directory exists
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			// Create a test config provider
			configProvider := config.NewPathProvider(configPath)

			// Set initial status
			err = configProvider.UpdateConfig(func(cfg *config.Config) {
				cfg.DisableUsageMetrics = tt.initialDisabledStatus
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/usage-metrics", nil)
			w := httptest.NewRecorder()

			routes := NewConfigRoutesWithProvider(configProvider)
			routes.getUsageMetricsStatus(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp getUsageMetricsStatusResponse
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedEnabled, resp.Enabled)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		})
	}
}

func TestUpdateUsageMetricsStatus_ValidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name            string
		requestBody     updateUsageMetricsStatusRequest
		expectedCode    int
		expectedEnabled bool
	}{
		{
			name: "disable usage metrics",
			requestBody: updateUsageMetricsStatusRequest{
				Enabled: false,
			},
			expectedCode:    http.StatusOK,
			expectedEnabled: false,
		},
		{
			name: "enable usage metrics",
			requestBody: updateUsageMetricsStatusRequest{
				Enabled: true,
			},
			expectedCode:    http.StatusOK,
			expectedEnabled: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a temporary config directory for this test
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

			// Ensure the directory exists
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			// Create a test config provider
			configProvider := config.NewPathProvider(configPath)

			body, err := json.Marshal(tt.requestBody)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPut, "/usage-metrics", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			routes := NewConfigRoutesWithProvider(configProvider)
			routes.updateUsageMetricsStatus(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if w.Code == http.StatusOK {
				var resp updateUsageMetricsStatusResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEnabled, resp.Enabled)
				assert.NotEmpty(t, resp.Message)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				// Verify the config was actually updated (inverted because config uses DisableUsageMetrics)
				cfg := configProvider.GetConfig()
				assert.Equal(t, !tt.expectedEnabled, cfg.DisableUsageMetrics)
			}
		})
	}
}

func TestUpdateUsageMetricsStatus_InvalidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		requestBody  interface{}
		expectedCode int
	}{
		{
			name:         "invalid json body",
			requestBody:  "invalid json",
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "empty body",
			requestBody:  "",
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a temporary config directory for this test
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

			// Ensure the directory exists
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			// Create a test config provider
			configProvider := config.NewPathProvider(configPath)

			var body []byte
			if str, ok := tt.requestBody.(string); ok {
				body = []byte(str)
			} else {
				body, err = json.Marshal(tt.requestBody)
				require.NoError(t, err)
			}

			req := httptest.NewRequest(http.MethodPut, "/usage-metrics", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			routes := NewConfigRoutesWithProvider(configProvider)
			routes.updateUsageMetricsStatus(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
		})
	}
}
