// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mutating

import (
	"bytes"
	"context"
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

// MiddlewareType is the type constant for the mutating webhook middleware.
const MiddlewareType = "mutating-webhook"

// Middleware wraps mutating webhook functionality for the factory pattern.
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

// CreateMiddleware is the factory function for mutating webhook middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params FactoryMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal mutating webhook middleware parameters: %w", err)
	}

	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid mutating webhook configuration: %w", err)
	}

	// Create clients for each webhook.
	var executors []clientExecutor
	for i, whCfg := range params.Webhooks {
		client, err := webhook.NewClient(whCfg, webhook.TypeMutating, nil) // HMAC secret not yet plumbed
		if err != nil {
			return fmt.Errorf("failed to create client for webhook[%d] (%q): %w", i, whCfg.Name, err)
		}
		executors = append(executors, clientExecutor{client: client, config: whCfg})
	}

	mw := &Middleware{
		handler: createMutatingHandler(executors, params.ServerName, params.Transport),
	}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

func createMutatingHandler(executors []clientExecutor, serverName, transport string) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip if it's not a parsed MCP request (middleware runs after mcp parser).
			parsedMCP := mcp.GetParsedMCPRequest(r.Context())
			if parsedMCP == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Read the request body to get the raw MCP request.
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				sendErrorResponse(w, http.StatusInternalServerError, "Failed to read request body", parsedMCP.ID)
				return
			}
			// Restore the request body immediately; we will replace it after mutations.
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			// currentMCPBody is the MCP JSON-RPC body we thread through the webhook chain.
			// Each successful mutation replaces this with the patched version.
			currentMCPBody := bodyBytes

			// Build the base webhook request context (reused across all webhooks).
			reqContext := &webhook.RequestContext{
				ServerName: serverName,
				SourceIP:   readSourceIP(r),
				Transport:  transport,
			}

			// Resolve principal once (same for all webhooks in this chain).
			var principal *auth.PrincipalInfo
			if identity, ok := auth.IdentityFromContext(r.Context()); ok {
				principal = identity.GetPrincipalInfo()
			}

			// Execute the webhook chain to apply mutations.
			mutatedBody, err := executeMutations(r.Context(), executors, currentMCPBody, reqContext, principal, parsedMCP.ID, w)
			if err != nil {
				// executeMutations handles writing the error to the response implicitly when it returns an error.
				return
			}

			// Replace the request body with the (potentially mutated) MCP body for downstream handlers.
			r.Body = io.NopCloser(bytes.NewBuffer(mutatedBody))
			next.ServeHTTP(w, r)
		})
	}
}

// executeMutations runs the chain of mutating webhooks sequentially.
// It returns the final mutated body, or an error if the chain was aborted.
// If an error occurs that should abort the request, this function writes the error response.
func executeMutations(
	ctx context.Context,
	executors []clientExecutor,
	initialBody []byte,
	reqContext *webhook.RequestContext,
	principal *auth.PrincipalInfo,
	msgID interface{},
	w http.ResponseWriter,
) ([]byte, error) {
	currentBody := initialBody

	for _, exec := range executors {
		mutatedBody, err := executeSingleMutation(ctx, exec, currentBody, reqContext, principal, msgID, w)
		if err != nil {
			return nil, err
		}
		currentBody = mutatedBody
	}

	return currentBody, nil
}

// executeSingleMutation applies a single mutating webhook.
func executeSingleMutation(
	ctx context.Context,
	exec clientExecutor,
	currentBody []byte,
	reqContext *webhook.RequestContext,
	principal *auth.PrincipalInfo,
	msgID interface{},
	w http.ResponseWriter,
) ([]byte, error) {
	whName := exec.config.Name

	whReq := &webhook.Request{
		Version:    webhook.APIVersion,
		UID:        uuid.New().String(),
		Timestamp:  time.Now().UTC(),
		MCPRequest: json.RawMessage(currentBody),
		Context:    reqContext,
		Principal:  principal,
	}

	resp, err := exec.client.CallMutating(ctx, whReq)
	if err != nil {
		if exec.config.FailurePolicy == webhook.FailurePolicyIgnore {
			slog.Warn("Mutating webhook error ignored due to fail-open policy", "webhook", whName, "error", err)
			return currentBody, nil
		}
		slog.Error("Mutating webhook error caused request denial", "webhook", whName, "error", err)
		sendErrorResponse(w, http.StatusInternalServerError, "Webhook error", msgID)
		return nil, err
	}

	if !resp.Allowed {
		slog.Info("Mutating webhook denied request", "webhook", whName, "reason", resp.Reason)
		sendErrorResponse(w, http.StatusInternalServerError, "Request mutation denied by webhook", msgID)
		return nil, fmt.Errorf("webhook denied request")
	}

	if resp.PatchType == "" || len(resp.Patch) == 0 {
		return currentBody, nil
	}

	if resp.PatchType != patchTypeJSONPatch {
		slog.Error("Mutating webhook returned unsupported patch type", "webhook", whName, "patch_type", resp.PatchType)
		if exec.config.FailurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Unsupported patch type from webhook", msgID)
		return nil, fmt.Errorf("unsupported patch type")
	}

	return applyMutationPatch(resp, whReq, whName, exec.config.FailurePolicy, currentBody, msgID, w)
}

func applyMutationPatch(
	resp *webhook.MutatingResponse,
	whReq *webhook.Request,
	whName string,
	failurePolicy webhook.FailurePolicy,
	currentBody []byte,
	msgID interface{},
	w http.ResponseWriter,
) ([]byte, error) {
	var patchOps []JSONPatchOp
	if err := json.Unmarshal(resp.Patch, &patchOps); err != nil {
		slog.Error("Mutating webhook returned malformed patch", "webhook", whName, "error", err)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Malformed patch from webhook", msgID)
		return nil, err
	}

	if err := ValidatePatch(patchOps); err != nil {
		slog.Error("Mutating webhook patch failed validation", "webhook", whName, "error", err)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Invalid patch from webhook", msgID)
		return nil, err
	}

	if !IsPatchScopedToMCPRequest(patchOps) {
		slog.Error("Mutating webhook patch targets fields outside mcp_request — rejected", "webhook", whName)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Patch must be scoped to mcp_request", msgID)
		return nil, fmt.Errorf("patch scope violation")
	}

	envelopeJSON, err := json.Marshal(whReq)
	if err != nil {
		slog.Error("Failed to marshal webhook request envelope", "webhook", whName, "error", err)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Internal error applying patch", msgID)
		return nil, err
	}

	patchedEnvelope, err := ApplyPatch(envelopeJSON, patchOps)
	if err != nil {
		slog.Error("Mutating webhook patch application failed", "webhook", whName, "error", err)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Failed to apply patch from webhook", msgID)
		return nil, err
	}

	mutatedMCPBody, err := extractMCPRequest(patchedEnvelope)
	if err != nil {
		slog.Error("Failed to extract mcp_request", "webhook", whName, "error", err)
		if failurePolicy == webhook.FailurePolicyIgnore {
			return currentBody, nil
		}
		sendErrorResponse(w, http.StatusInternalServerError, "Internal error extracting patched request", msgID)
		return nil, err
	}

	slog.Debug("Mutating webhook applied patch successfully", "webhook", whName)
	return mutatedMCPBody, nil
}

// extractMCPRequest extracts the raw mcp_request bytes from a patched webhook envelope.
func extractMCPRequest(envelope []byte) ([]byte, error) {
	var env struct {
		MCPRequest json.RawMessage `json:"mcp_request"`
	}
	if err := json.Unmarshal(envelope, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshal patched envelope: %w", err)
	}
	if len(env.MCPRequest) == 0 {
		return nil, fmt.Errorf("mcp_request field missing or empty in patched envelope")
	}
	return env.MCPRequest, nil
}

func readSourceIP(r *http.Request) string {
	return r.RemoteAddr
}

//nolint:unparam // statusCode is currently always 500, but kept for API flexibility
func sendErrorResponse(w http.ResponseWriter, statusCode int, message string, msgID interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	id, err := mcp.ConvertToJSONRPC2ID(msgID)
	if err != nil {
		id = jsonrpc2.ID{} // Use empty ID if conversion fails.
	}

	// Return a JSON-RPC 2.0 error so MCP clients can parse the denial.
	errResp := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(int64(statusCode), message),
	}
	_ = json.NewEncoder(w).Encode(errResp)
}
