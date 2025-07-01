package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

func TestSecretsRouter(t *testing.T) {
	t.Parallel()
	router := SecretsRouter()
	assert.NotNil(t, router)
}

func TestSetupSecretsProvider_ValidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		requestBody  setupSecretsRequest
		expectedCode int
	}{
		{
			name: "valid none provider setup",
			requestBody: setupSecretsRequest{
				ProviderType: string(secrets.NoneType),
			},
			expectedCode: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(tt.requestBody)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			routes := &SecretsRoutes{}
			routes.setupSecretsProvider(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if w.Code == http.StatusCreated {
				var resp setupSecretsResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				assert.NoError(t, err)
				assert.NotEmpty(t, resp.ProviderType)
				assert.NotEmpty(t, resp.Message)
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestSetupSecretsProvider_InvalidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		requestBody  interface{}
		expectedCode int
		errorMessage string
	}{
		{
			name: "invalid provider type",
			requestBody: setupSecretsRequest{
				ProviderType: "invalid",
			},
			expectedCode: http.StatusBadRequest,
			errorMessage: "Invalid secrets provider type: invalid (valid types: encrypted, 1password, none)",
		},
		{
			name:         "invalid json body",
			requestBody:  "invalid json",
			expectedCode: http.StatusBadRequest,
			errorMessage: "Invalid request body",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body []byte
			var err error
			if str, ok := tt.requestBody.(string); ok {
				body = []byte(str)
			} else {
				body, err = json.Marshal(tt.requestBody)
				require.NoError(t, err)
			}

			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			routes := &SecretsRoutes{}
			routes.setupSecretsProvider(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.errorMessage)
		})
	}
}

func TestCreateSecret_InvalidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		requestBody  interface{}
		expectedCode int
		errorMessage string
	}{
		{
			name: "missing key",
			requestBody: createSecretRequest{
				Key:   "",
				Value: "test-value",
			},
			expectedCode: http.StatusBadRequest,
			errorMessage: "Both 'key' and 'value' are required",
		},
		{
			name: "missing value",
			requestBody: createSecretRequest{
				Key:   "test-key",
				Value: "",
			},
			expectedCode: http.StatusBadRequest,
			errorMessage: "Both 'key' and 'value' are required",
		},
		{
			name:         "invalid json body",
			requestBody:  "invalid json",
			expectedCode: http.StatusBadRequest,
			errorMessage: "Invalid request body",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body []byte
			var err error
			if str, ok := tt.requestBody.(string); ok {
				body = []byte(str)
			} else {
				body, err = json.Marshal(tt.requestBody)
				require.NoError(t, err)
			}

			req := httptest.NewRequest(http.MethodPost, "/default/keys", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			routes := &SecretsRoutes{}
			routes.createSecret(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.errorMessage)
		})
	}
}

func TestUpdateSecret_InvalidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		secretKey    string
		requestBody  interface{}
		expectedCode int
		errorMessage string
	}{
		{
			name:      "empty secret key",
			secretKey: "",
			requestBody: updateSecretRequest{
				Value: "new-value",
			},
			expectedCode: http.StatusBadRequest,
			errorMessage: "Secret key is required",
		},
		{
			name:      "missing value",
			secretKey: "test-key",
			requestBody: updateSecretRequest{
				Value: "",
			},
			expectedCode: http.StatusBadRequest,
			errorMessage: "Value is required",
		},
		{
			name:         "invalid json body",
			secretKey:    "test-key",
			requestBody:  "invalid json",
			expectedCode: http.StatusBadRequest,
			errorMessage: "Invalid request body",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body []byte
			var err error
			if str, ok := tt.requestBody.(string); ok {
				body = []byte(str)
			} else {
				body, err = json.Marshal(tt.requestBody)
				require.NoError(t, err)
			}

			url := "/default/keys/" + tt.secretKey
			req := httptest.NewRequest(http.MethodPut, url, bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")

			// Setup chi context to simulate URL parameters
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("key", tt.secretKey)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()

			routes := &SecretsRoutes{}
			routes.updateSecret(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.errorMessage)
		})
	}
}

func TestDeleteSecret_InvalidRequests(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name         string
		secretKey    string
		expectedCode int
		errorMessage string
	}{
		{
			name:         "empty secret key",
			secretKey:    "",
			expectedCode: http.StatusBadRequest,
			errorMessage: "Secret key is required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := "/default/keys/" + tt.secretKey
			req := httptest.NewRequest(http.MethodDelete, url, nil)

			// Setup chi context to simulate URL parameters
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("key", tt.secretKey)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()

			routes := &SecretsRoutes{}
			routes.deleteSecret(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.errorMessage)
		})
	}
}

func TestRequestResponseTypes(t *testing.T) {
	t.Parallel()

	t.Run("setupSecretsRequest", func(t *testing.T) {
		t.Parallel()
		req := setupSecretsRequest{
			ProviderType: "encrypted",
			Password:     "secret",
		}
		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded setupSecretsRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, req.ProviderType, decoded.ProviderType)
		assert.Equal(t, req.Password, decoded.Password)
	})

	t.Run("createSecretRequest", func(t *testing.T) {
		t.Parallel()
		req := createSecretRequest{
			Key:   "test-key",
			Value: "test-value",
		}
		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded createSecretRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, req.Key, decoded.Key)
		assert.Equal(t, req.Value, decoded.Value)
	})

	t.Run("updateSecretRequest", func(t *testing.T) {
		t.Parallel()
		req := updateSecretRequest{
			Value: "new-value",
		}
		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded updateSecretRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, req.Value, decoded.Value)
	})

	t.Run("getSecretsProviderResponse", func(t *testing.T) {
		t.Parallel()
		resp := getSecretsProviderResponse{
			Name:         "test-provider",
			ProviderType: "none",
			Capabilities: providerCapabilitiesResponse{
				CanRead:    false,
				CanWrite:   false,
				CanDelete:  false,
				CanList:    true,
				CanCleanup: true,
			},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded getSecretsProviderResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, resp.Name, decoded.Name)
		assert.Equal(t, resp.ProviderType, decoded.ProviderType)
		assert.Equal(t, resp.Capabilities.CanRead, decoded.Capabilities.CanRead)
		assert.Equal(t, resp.Capabilities.CanWrite, decoded.Capabilities.CanWrite)
		assert.Equal(t, resp.Capabilities.CanDelete, decoded.Capabilities.CanDelete)
		assert.Equal(t, resp.Capabilities.CanList, decoded.Capabilities.CanList)
		assert.Equal(t, resp.Capabilities.CanCleanup, decoded.Capabilities.CanCleanup)
	})

	t.Run("listSecretsResponse", func(t *testing.T) {
		t.Parallel()
		resp := listSecretsResponse{
			Keys: []secretKeyResponse{
				{Key: "key1", Description: "First secret"},
				{Key: "key2", Description: "Second secret"},
			},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded listSecretsResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Len(t, decoded.Keys, 2)
		assert.Equal(t, "key1", decoded.Keys[0].Key)
		assert.Equal(t, "First secret", decoded.Keys[0].Description)
		assert.Equal(t, "key2", decoded.Keys[1].Key)
		assert.Equal(t, "Second secret", decoded.Keys[1].Description)
	})
}

func TestErrorHandling(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	t.Run("malformed json request", func(t *testing.T) {
		t.Parallel()
		malformedJSON := `{"provider_type": "encrypted", "invalid": json}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(malformedJSON))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routes := &SecretsRoutes{}
		routes.setupSecretsProvider(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid request body")
	})

	t.Run("empty request body", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/default/keys", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routes := &SecretsRoutes{}
		routes.createSecret(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing content type header", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider_type": "none"}`))
		// Deliberately not setting Content-Type header
		w := httptest.NewRecorder()

		routes := &SecretsRoutes{}
		routes.setupSecretsProvider(w, req)

		// Should still work as the handler doesn't strictly require content-type
		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

func TestRouterIntegration(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	t.Run("router setup test", func(t *testing.T) {
		t.Parallel()
		router := SecretsRouter()

		// Test POST / endpoint
		setupReq := setupSecretsRequest{
			ProviderType: string(secrets.NoneType),
		}
		body, err := json.Marshal(setupReq)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	})
}

// Test for default constant
func TestConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "default", defaultSecretsProviderName)
}
