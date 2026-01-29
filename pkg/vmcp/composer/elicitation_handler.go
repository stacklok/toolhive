// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

//go:generate mockgen -destination=mocks/mock_sdk_elicitation_requester.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/composer SDKElicitationRequester

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// defaultElicitationTimeout is the default timeout for elicitation requests.
	// This is a reasonable time for a human to see and respond to a prompt.
	defaultElicitationTimeout = 5 * time.Minute

	// maxElicitationTimeout is the maximum allowed timeout to prevent resource exhaustion attacks.
	// Per security review: prevents timeout bomb attacks where many elicitations with very long
	// timeouts accumulate in memory. Set to 10 minutes as a reasonable maximum for human response.
	maxElicitationTimeout = 10 * time.Minute

	// maxSchemaSize is the maximum size in bytes for JSON schemas.
	// Per security review: prevents memory exhaustion via enormous schemas (10MB+ each).
	maxSchemaSize = 100 * 1024 // 100KB

	// maxSchemaDepth is the maximum nesting depth for JSON schemas.
	// Per security review: prevents deeply nested schema attacks.
	maxSchemaDepth = 10

	// maxResponseContentSize is the maximum size in bytes for user-provided response content.
	// Per security review: prevents memory exhaustion via large responses (100MB+ each).
	maxResponseContentSize = 1 * 1024 * 1024 // 1MB

	// elicitationActionAccept is the MCP protocol action for user acceptance.
	elicitationActionAccept = "accept"

	// elicitationActionDecline is the MCP protocol action for user decline.
	elicitationActionDecline = "decline"

	// elicitationActionCancel is the MCP protocol action for user cancel.
	elicitationActionCancel = "cancel"
)

var (
	// ErrElicitationTimeout is returned when an elicitation request times out.
	ErrElicitationTimeout = errors.New("elicitation request timed out")

	// ErrElicitationCancelled is returned when the user cancels the elicitation.
	ErrElicitationCancelled = errors.New("elicitation request was cancelled by user")

	// ErrElicitationDeclined is returned when the user declines the elicitation.
	ErrElicitationDeclined = errors.New("elicitation request was declined by user")

	// ErrSchemaTooLarge is returned when the schema exceeds size limits.
	ErrSchemaTooLarge = errors.New("schema too large")

	// ErrSchemaTooDeep is returned when the schema exceeds nesting depth limits.
	ErrSchemaTooDeep = errors.New("schema nesting too deep")

	// ErrContentTooLarge is returned when response content exceeds size limits.
	ErrContentTooLarge = errors.New("response content too large")
)

// SDKElicitationRequester is an abstraction for the underlying MCP SDK's elicitation functionality.
//
// This interface wraps the SDK's RequestElicitation method, enabling:
//   - Migration from mark3labs SDK to official SDK without changing workflow code
//   - Mocking for unit tests
//   - Custom implementations for testing
//
// The SDK handles JSON-RPC ID correlation internally - our wrapper doesn't need to track IDs.
type SDKElicitationRequester interface {
	// RequestElicitation sends an elicitation request via the SDK and blocks for response.
	// This wraps the SDK's synchronous RequestElicitation method.
	RequestElicitation(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
}

// DefaultElicitationHandler implements ElicitationProtocolHandler as a thin wrapper around the MCP SDK.
//
// This handler provides:
//   - SDK-agnostic abstraction layer (enables SDK migration)
//   - Security validation (timeout, schema size/depth, content size)
//   - Error transformation (SDK errors → domain errors)
//   - Logging and observability
//
// The handler delegates JSON-RPC ID correlation to the underlying SDK, which handles it internally.
// We only provide validation, transformation, and abstraction.
//
// Per MCP 2025-06-18 spec: Elicitation is synchronous - send request, block, receive response.
//
// Thread-safety: Safe for concurrent calls. The underlying SDK session must be thread-safe.
type DefaultElicitationHandler struct {
	// sdkRequester wraps the MCP SDK's elicitation functionality.
	// This abstraction enables migration to official SDK without changing our code.
	sdkRequester SDKElicitationRequester
}

// NewDefaultElicitationHandler creates a new SDK-agnostic elicitation handler.
//
// The sdkRequester parameter wraps the underlying MCP SDK's RequestElicitation functionality.
// For mark3labs SDK, this would be the MCPServer instance.
// For a future official SDK, this would be replaced without changing workflow code.
func NewDefaultElicitationHandler(sdkRequester SDKElicitationRequester) *DefaultElicitationHandler {
	return &DefaultElicitationHandler{
		sdkRequester: sdkRequester,
	}
}

// RequestElicitation sends an elicitation request to the client and waits for response.
//
// This is a synchronous blocking call that:
//  1. Validates configuration and enforces security limits (timeout, schema size/depth, content size)
//  2. Applies timeout constraints (default 5min, max 1hour)
//  3. Delegates to SDK's RequestElicitation (which handles JSON-RPC ID correlation)
//  4. Validates response content size
//  5. Transforms SDK response to domain type
//
// Per security review: Enforces max timeout (10 minutes), schema size (100KB), schema depth (10 levels),
// and response content size (1MB) to prevent resource exhaustion attacks.
//
// Per MCP 2025-06-18 spec: The SDK handles JSON-RPC ID correlation internally.
// The workflowID and stepID parameters are for logging/tracking only.
//
// Returns ElicitationResponse or error if validation fails, timeout occurs, or user declines/cancels.
func (h *DefaultElicitationHandler) RequestElicitation(
	ctx context.Context,
	workflowID string,
	stepID string,
	config *ElicitationConfig,
) (*ElicitationResponse, error) {
	logger.Debugf("Requesting elicitation for workflow %s, step %s", workflowID, stepID)

	// Validate configuration
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	// Apply and validate timeout (security: prevent timeout bomb attacks)
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultElicitationTimeout
	}
	if timeout > maxElicitationTimeout {
		logger.Warnf("Elicitation timeout %s exceeds maximum %s for step %s, capping to maximum",
			timeout, maxElicitationTimeout, stepID)
		timeout = maxElicitationTimeout
	}

	// Validate schema size and structure (security: prevent memory exhaustion)
	if err := validateSchemaSize(config.Schema); err != nil {
		return nil, fmt.Errorf("invalid schema for step %s: %w", stepID, err)
	}

	// Create timeout context
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create MCP elicitation request per 2025-06-18 spec
	// The SDK will assign JSON-RPC ID and handle correlation internally
	mcpReq := mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message:         config.Message,
			RequestedSchema: config.Schema,
		},
	}

	logger.Debugf("Sending elicitation request for step %s", stepID)

	// Call SDK (synchronous - blocks until response received or timeout)
	// The SDK handles all JSON-RPC ID correlation internally
	result, err := h.sdkRequester.RequestElicitation(reqCtx, mcpReq)
	if err != nil {
		// Check if timeout
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Warnf("Elicitation timed out for step %s after %v", stepID, timeout)
			return nil, fmt.Errorf("%w: step %s", ErrElicitationTimeout, stepID)
		}
		return nil, fmt.Errorf("elicitation request failed for step %s: %w", stepID, err)
	}

	// Validate response content size and depth (security: prevent memory exhaustion and template DoS)
	if result.Action == elicitationActionAccept {
		if err := validateContentSize(result.Content); err != nil {
			return nil, fmt.Errorf("invalid response content for step %s: %w", stepID, err)
		}
		if err := validateContentDepth(result.Content); err != nil {
			return nil, fmt.Errorf("invalid response content depth for step %s: %w", stepID, err)
		}
	}

	logger.Debugf("Received elicitation response for step %s (action: %s)", stepID, result.Action)

	// Transform SDK response to domain type
	// Note: result.Content is of type 'any', convert to map[string]any if present
	var content map[string]any
	if result.Content != nil {
		if contentMap, ok := result.Content.(map[string]any); ok {
			content = contentMap
		} else {
			// Unexpected content type - log and continue
			logger.Warnf("Elicitation response content for step %s is not a map: %T", stepID, result.Content)
		}
	}

	response := &ElicitationResponse{
		Action:     string(result.Action),
		Content:    content,
		ReceivedAt: time.Now(),
	}

	return response, nil
}

// validateConfig validates elicitation configuration.
func validateConfig(config *ElicitationConfig) error {
	if config == nil {
		return fmt.Errorf("elicitation config cannot be nil")
	}
	if config.Message == "" {
		return fmt.Errorf("elicitation message is required")
	}
	if config.Schema == nil {
		return fmt.Errorf("elicitation schema is required")
	}
	return nil
}

// validateSchemaSize validates that the schema doesn't exceed size and depth limits.
// Per security review: Prevents memory exhaustion via enormous schemas.
func validateSchemaSize(schema map[string]any) error {
	// Serialize to measure size
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	// Check size limit
	if len(data) > maxSchemaSize {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrSchemaTooLarge, len(data), maxSchemaSize)
	}

	// Check depth limit (prevents deeply nested attacks)
	return validateSchemaDepth(schema, 0, maxSchemaDepth)
}

// validateSchemaDepth recursively validates schema nesting depth.
func validateSchemaDepth(obj any, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: %d levels (max %d)", ErrSchemaTooDeep, depth, maxDepth)
	}

	switch v := obj.(type) {
	case map[string]any:
		for _, val := range v {
			if err := validateSchemaDepth(val, depth+1, maxDepth); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range v {
			if err := validateSchemaDepth(val, depth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateContentSize validates that response content doesn't exceed size limits.
// Per security review: Prevents memory exhaustion via large responses (100MB+ each).
func validateContentSize(content any) error {
	if content == nil {
		return nil
	}

	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("invalid content: %w", err)
	}

	if len(data) > maxResponseContentSize {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrContentTooLarge, len(data), maxResponseContentSize)
	}

	return nil
}

// validateContentDepth validates that response content doesn't exceed nesting depth limits.
// Per security review: Prevents deeply nested structures in elicitation responses that could
// cause template expansion DoS attacks when referenced in workflow templates.
// Uses the same depth limit as schemas (maxSchemaDepth = 10 levels).
func validateContentDepth(content any) error {
	if content == nil {
		return nil
	}
	return validateSchemaDepth(content, 0, maxSchemaDepth)
}
