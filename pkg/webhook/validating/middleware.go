// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validating

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/webhook"
)

// MiddlewareType is the type constant for the validating webhook middleware.
const MiddlewareType = "validating-webhook"

// FactoryMiddlewareParams extends MiddlewareParams with context for the factory.
type FactoryMiddlewareParams struct {
	MiddlewareParams
	// ServerName is the name of the ToolHive instance.
	ServerName string `json:"server_name"`
	// Transport is the transport type (e.g., sse, stdio).
	Transport string `json:"transport"`
}

// Middleware wraps validating webhook functionality for the factory pattern.
type Middleware struct {
	handler types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	return nil
}

type clientExecutor struct {
	client *webhook.Client
	config webhook.Config
}

// CreateMiddleware is the factory function for validating webhook middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params FactoryMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal validating webhook middleware parameters: %w", err)
	}

	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid validating webhook configuration: %w", err)
	}

	// Create clients for each webhook
	var executors []clientExecutor
	for i, whCfg := range params.Webhooks {
		client, err := webhook.NewClient(whCfg, webhook.TypeValidating, nil) // HMAC secret not yet plumbed
		if err != nil {
			return fmt.Errorf("failed to create client for webhook[%d] (%q): %w", i, whCfg.Name, err)
		}
		executors = append(executors, clientExecutor{client: client, config: whCfg})
	}

	mw := &Middleware{
		handler: createValidatingHandler(executors, params.ServerName, params.Transport),
	}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

func createValidatingHandler(executors []clientExecutor, serverName, transport string) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip if it's not a parsed MCP request (middleware runs after mcp parser)
			parsedMCP := mcp.GetParsedMCPRequest(r.Context())
			if parsedMCP == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Read the request body to get the raw MCP request
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				sendErrorResponse(w, http.StatusInternalServerError, "Failed to read request body", parsedMCP.ID)
				return
			}
			// Restore the request body for downstream handlers
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			// Build the webhook request payload
			reqUID := uuid.New().String()
			whReq := &webhook.Request{
				Version:    webhook.APIVersion,
				UID:        reqUID,
				Timestamp:  time.Now().UTC(),
				MCPRequest: json.RawMessage(bodyBytes),
				Context: &webhook.RequestContext{
					ServerName: serverName,
					SourceIP:   readSourceIP(r),
					Transport:  transport,
				},
			}

			// Add Principal if authenticated
			if identity, ok := auth.IdentityFromContext(r.Context()); ok {
				whReq.Principal = identity.GetPrincipalInfo()
			}

			// Call each webhook in order
			for _, exec := range executors {
				whName := exec.config.Name

				resp, err := exec.client.Call(r.Context(), whReq)
				if err != nil {
					if webhook.IsAlwaysDenyError(err) {
						slog.Info("Validating webhook denied request due to HTTP 422 response",
							"webhook", whName, "error", err)
						sendErrorResponse(w, http.StatusForbidden, "Request denied by policy", parsedMCP.ID)
						return
					}

					// Handle error based on failure policy
					if exec.config.FailurePolicy == webhook.FailurePolicyIgnore {
						slog.Warn("Validating webhook error ignored due to fail-open policy",
							"webhook", whName, "error", err)
						continue
					}

					slog.Error("Validating webhook error caused request denial",
						"webhook", whName, "error", err)
					sendErrorResponse(w, http.StatusForbidden, "Request denied by policy", parsedMCP.ID)
					return
				}

				if !resp.Allowed {
					slog.Info("Validating webhook denied request", "webhook", whName, "reason", resp.Reason, "message", resp.Message)

					// Prevent information leaks by ignoring the webhook's message
					sendErrorResponse(w, http.StatusForbidden, "Request denied by policy", parsedMCP.ID)
					return
				}
			}

			// All webhooks allowed or ignored errors
			next.ServeHTTP(w, r)
		})
	}
}

func readSourceIP(r *http.Request) string {
	// Let runner handle X-Forwarded-For if TrustProxyHeaders is set.
	// For now, simple RemoteAddr.
	return r.RemoteAddr
}

func sendErrorResponse(w http.ResponseWriter, statusCode int, message string, msgID interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	id, err := mcp.ConvertToJSONRPC2ID(msgID)
	if err != nil {
		id = jsonrpc2.ID{} // Use empty ID if conversion fails
	}

	// Return a JSON-RPC 2.0 error so MCP clients can parse the denial.
	// The HTTP status code signals the error at the transport level; the JSON-RPC body carries the detail.
	errResp := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(int64(statusCode), message),
	}
	_ = json.NewEncoder(w).Encode(errResp)
}
