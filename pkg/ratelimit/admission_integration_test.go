// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	transportmocks "github.com/stacklok/toolhive/pkg/transport/types/mocks"
	"github.com/stacklok/toolhive/pkg/webhook"
	"github.com/stacklok/toolhive/pkg/webhook/mutating"
	"github.com/stacklok/toolhive/pkg/webhook/validating"
)

func TestAdmissionPolicyChainRegardlessOfContentType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, contentType, prefix string }{
		{"json", "application/json", ""},
		{"plain", "text/plain", ""},
		{"missing", "", ""},
		{"BOM_ignore", "text/plain", string(mcp.UTF8BOM)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mutations, validations atomic.Int32
			mutationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req webhook.Request
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mutations.Add(1)
				assert.Contains(t, string(req.MCPRequest), `"name":"echo"`)
				_ = json.NewEncoder(w).Encode(webhook.MutatingResponse{
					Response:  webhook.Response{Version: webhook.APIVersion, UID: req.UID, Allowed: true},
					PatchType: "json_patch", Patch: json.RawMessage(`[{"op":"add","path":"/mcp_request/params/arguments/reviewed","value":true}]`),
				})
			}))
			t.Cleanup(mutationServer.Close)
			validationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req webhook.Request
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				validations.Add(1)
				var call struct {
					Params struct {
						Name      string
						Arguments struct {
							Input    string
							Reviewed bool
						}
					}
				}
				if !assert.NoError(t, json.Unmarshal(req.MCPRequest, &call)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "echo", call.Params.Name)
				assert.True(t, call.Params.Arguments.Reviewed, "policy must see the mutation")
				_ = json.NewEncoder(w).Encode(webhook.Response{
					Version: webhook.APIVersion, UID: req.UID, Allowed: call.Params.Arguments.Input != "blocked",
				})
			}))
			t.Cleanup(validationServer.Close)

			runner := transportmocks.NewMockMiddlewareRunner(gomock.NewController(t))
			var mutate, validate types.Middleware
			runner.EXPECT().AddMiddleware(mutating.MiddlewareType, gomock.Any()).Do(func(_ string, mw types.Middleware) { mutate = mw })
			runner.EXPECT().AddMiddleware(validating.MiddlewareType, gomock.Any()).Do(func(_ string, mw types.Middleware) { validate = mw })
			for _, wh := range []struct {
				url     string
				factory func(*types.MiddlewareConfig, types.MiddlewareRunner) error
			}{
				{mutationServer.URL, mutating.CreateMiddleware}, {validationServer.URL, validating.CreateMiddleware},
			} {
				parameters, err := json.Marshal(map[string]any{"webhooks": []webhook.Config{{
					Name: "policy", URL: wh.url, Timeout: webhook.DefaultTimeout,
					FailurePolicy: webhook.FailurePolicyIgnore, TLSConfig: &webhook.TLSConfig{InsecureSkipVerify: true},
				}}})
				require.NoError(t, err)
				require.NoError(t, wh.factory(&types.MiddlewareConfig{Parameters: parameters}, runner))
			}
			client, _ := newTestClient(t)
			limiter, err := NewLimiter(client, "test", "admission", &v1beta1.RateLimitConfig{
				Shared: &v1beta1.RateLimitBucket{MaxTokens: 2, RefillPeriod: metav1.Duration{Duration: time.Hour}},
			})
			require.NoError(t, err)
			nextCalls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), `"reviewed":true`)
				assert.Equal(t, tc.contentType, r.Header.Get("Content-Type"), "do not relabel the media type")
				assert.EqualValues(t, len(body), r.ContentLength)
				w.WriteHeader(http.StatusNoContent)
			})
			chain := mcp.ParsingMiddleware(rateLimitHandler(limiter)(mutate.Handler()(validate.Handler()(next))))
			for _, step := range []struct {
				input  string
				status int
			}{
				{"allowed", http.StatusNoContent}, {"blocked", http.StatusForbidden}, {"allowed", http.StatusTooManyRequests},
			} {
				body := tc.prefix + fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"input":%q}}}`, step.input)
				req := httptest.NewRequest(http.MethodPost, "/custom-rpc", strings.NewReader(body))
				if tc.contentType != "" {
					req.Header.Set("Content-Type", tc.contentType)
				}
				rec := httptest.NewRecorder()
				chain.ServeHTTP(rec, req)
				assert.Equal(t, step.status, rec.Code, "body: %s", rec.Body.String())
			}
			assert.Equal(t, 1, nextCalls)
			assert.EqualValues(t, 2, mutations.Load())
			assert.EqualValues(t, 2, validations.Load())
		})
	}
}
