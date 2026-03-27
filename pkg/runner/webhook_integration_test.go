// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/webhook"
	statusesmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

// TestWebhookMiddlewareChainIntegration tests the full execution of the webhook middleware chain
// populated by PopulateMiddlewareConfigs in the runner.
func TestWebhookMiddlewareChainIntegration(t *testing.T) {
	t.Parallel()

	// 1. Set up a mutating webhook server that adds a new argument field
	mutatingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		// Apply a JSONPatch to add "dept" = "engineering"
		patch := []map[string]interface{}{
			{
				"op":    "add",
				"path":  "/mcp_request/params/arguments/dept",
				"value": "engineering",
			},
		}
		patchJSON, _ := json.Marshal(patch)

		resp := webhook.MutatingResponse{
			Response: webhook.Response{
				Version: webhook.APIVersion,
				UID:     req.UID,
				Allowed: true,
			},
			PatchType: "json_patch",
			Patch:     patchJSON,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mutatingServer.Close)

	// 2. Set up a validating webhook server that asserts the field is present and allows the request
	validatingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		// Parse the incoming MCP Request (which should have been mutated)
		var mcpReq map[string]interface{}
		require.NoError(t, json.Unmarshal(req.MCPRequest, &mcpReq))

		params, ok := mcpReq["params"].(map[string]interface{})
		require.True(t, ok)
		args, ok := params["arguments"].(map[string]interface{})
		require.True(t, ok)

		// Check if the mutating webhook successfully added the parameter
		assert.Equal(t, "engineering", args["dept"])

		resp := webhook.Response{
			Version: webhook.APIVersion,
			UID:     req.UID,
			Allowed: true,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(validatingServer.Close)

	// 3. Configure the runner config
	runConfig := NewRunConfig()
	runConfig.Name = "test-server"
	runConfig.MutatingWebhooks = []webhook.Config{
		{
			Name:          "test-mutating-webhook",
			URL:           mutatingServer.URL,
			Timeout:       webhook.DefaultTimeout,
			FailurePolicy: webhook.FailurePolicyFail,
			TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
		},
	}
	runConfig.ValidatingWebhooks = []webhook.Config{
		{
			Name:          "test-validating-webhook",
			URL:           validatingServer.URL,
			Timeout:       webhook.DefaultTimeout,
			FailurePolicy: webhook.FailurePolicyFail,
			TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
		},
	}

	// 4. Populate Middleware Configs
	err := PopulateMiddlewareConfigs(runConfig)
	require.NoError(t, err)

	// 5. Initialize the Runner (this parses the configs into actual middlewares)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runner := NewRunner(runConfig, mockStatusManager)

	for _, mwConfig := range runConfig.MiddlewareConfigs {
		factory, ok := runner.supportedMiddleware[mwConfig.Type]
		require.True(t, ok)
		err := factory(&mwConfig, runner)
		require.NoError(t, err)
	}

	// Ensure the middlewares were created
	require.NotEmpty(t, runner.middlewares)

	// 6. Build the HTTP handler chain. Middlewares are applied backwards to wrap the handler.
	var finalBody []byte
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0", "id": 1, "result": {}}`))
	})

	for i := len(runner.middlewares) - 1; i >= 0; i-- {
		handler = runner.middlewares[i].Handler()(handler)
	}

	// 7. Make a test request through the middleware chain
	reqBody := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"db","arguments":{"query":"SELECT *"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// 8. Assertions
	require.Equal(t, http.StatusOK, rr.Code)

	// Verify the final body received by the innermost handler (the mock MCP server) has the mutated structure
	var parsedFinalBody map[string]interface{}
	require.NoError(t, json.Unmarshal(finalBody, &parsedFinalBody))

	params := parsedFinalBody["params"].(map[string]interface{})
	args := params["arguments"].(map[string]interface{})

	// Ensure the original field was kept and the mutated one was added
	assert.Equal(t, "SELECT *", args["query"])
	assert.Equal(t, "engineering", args["dept"])
}
