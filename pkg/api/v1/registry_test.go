// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

func CreateTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	configPath := filepath.Join(configDir, "config.yaml")
	provider := config.NewPathProvider(configPath)

	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return provider, func() {}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	provider, _ := CreateTestConfigProvider(t, nil)
	routes := NewRegistryRoutesWithProvider(provider)
	assert.NotNil(t, routes)
}

//nolint:paralleltest,tparallel // Subtests share a mock HTTP server
func TestRegistryAPI_PutEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) string
		expectedCode int
		description  string
	}{
		{
			name: "valid local file registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tempFile := filepath.Join(t.TempDir(), "valid-registry.json")
				validJSON := `{"servers": {"test-server": {"command": ["test"], "args": []}}}`
				err := os.WriteFile(tempFile, []byte(validJSON), 0600)
				require.NoError(t, err)
				return `{"local_path":"` + tempFile + `"}`
			},
			expectedCode: http.StatusOK,
			description:  "Valid local file with proper registry structure should be accepted",
		},
		{
			name: "invalid JSON",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"invalid":json}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Invalid JSON should return 400",
		},
		{
			name: "empty body resets registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{}`
			},
			expectedCode: http.StatusOK,
			description:  "Empty request resets registry (returns 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			configProvider := config.NewPathProvider(configPath)
			routes := NewRegistryRoutesWithProvider(configProvider)

			requestBody := tt.setupFunc(t)

			req := httptest.NewRequest("PUT", "/default", strings.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", "default")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			routes.updateRegistry(w, req)

			assert.Equal(t, tt.expectedCode, w.Code, tt.description)

			if w.Code == http.StatusOK {
				var response map[string]interface{}
				err := json.NewDecoder(w.Body).Decode(&response)
				require.NoError(t, err, "Success response should be valid JSON")
			}
		})
	}
}

// denyRegistryGate is a test helper that blocks all registry mutations.
type denyRegistryGate struct {
	registry.NoopPolicyGate
	err error
}

func (g *denyRegistryGate) CheckUpdateRegistry(_ context.Context, _ *registry.UpdateRegistryConfig) error {
	return g.err
}

func (g *denyRegistryGate) CheckDeleteRegistry(_ context.Context, _ *registry.DeleteRegistryConfig) error {
	return g.err
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestUpdateRegistry_BlockedByPolicyGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	sentinel := errors.New("[ToolHive Policy] Registry is managed by organization policy")
	registry.RegisterPolicyGate(&denyRegistryGate{err: sentinel})

	provider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()
	routes := NewRegistryRoutesWithProvider(provider)

	body := `{"url":"https://example.com/registry.json"}`
	req := httptest.NewRequest(http.MethodPut, "/default", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.updateRegistry(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "Blocked PUT should return 403")
	assert.Contains(t, w.Body.String(), "organization policy")
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRemoveRegistry_BlockedByPolicyGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	sentinel := errors.New("[ToolHive Policy] Registry is managed by organization policy")
	registry.RegisterPolicyGate(&denyRegistryGate{err: sentinel})

	provider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()
	routes := NewRegistryRoutesWithProvider(provider)

	req := httptest.NewRequest(http.MethodDelete, "/test-reg", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "test-reg")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.removeRegistry(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "Blocked DELETE should return 403")
	assert.Contains(t, w.Body.String(), "organization policy")
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestUpdateRegistry_AllowedByDefaultGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	registry.RegisterPolicyGate(registry.NoopPolicyGate{})

	provider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()
	routes := NewRegistryRoutesWithProvider(provider)

	body := `{}`
	req := httptest.NewRequest(http.MethodPut, "/default", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.updateRegistry(w, req)

	assert.NotEqual(t, http.StatusForbidden, w.Code,
		"Default gate should not return 403")
}
