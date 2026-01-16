// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/logger"
)

// TokenHandler handles POST /oauth/token requests.
// It processes token requests using fosite's access request/response flow.
func (h *Handler) TokenHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Create a placeholder session for the token request.
	// All parameters are empty because Fosite's NewAccessRequest will:
	// 1. Extract the authorization code from the request
	// 2. Retrieve the stored authorize session from storage (created in CallbackHandler)
	// 3. Use the stored session's claims (subject, tsid, client_id) for token generation
	// This session object is only used as a deserialization template.
	sess := session.New("", "", "")

	// Parse and validate the access request
	accessRequest, err := h.provider.NewAccessRequest(ctx, req, sess)
	if err != nil {
		logger.Errorw("failed to create access request",
			"error", err.Error(),
		)
		h.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// RFC 8707: Handle resource parameter for audience claim.
	// The resource parameter allows clients to specify which protected resource (MCP server)
	// the token is intended for. This value becomes the "aud" claim in the JWT.
	//
	// TODO: Add proper RFC 8707 validation before granting audience:
	// - Validate URI format (absolute URI, no fragment, http/https scheme only)
	// - Validate against a whitelist of allowed MCP server resources
	// - Validate client is authorized to request tokens for this resource
	// - For auth code grant, verify resource matches the original authorization
	// - Return "invalid_target" error for invalid/unauthorized resources
	if resource := accessRequest.GetRequestForm().Get("resource"); resource != "" {
		logger.Debugw("granting audience from resource parameter",
			"resource", resource,
		)
		accessRequest.GrantAudience(resource)
	}

	// Generate the access response (tokens)
	response, err := h.provider.NewAccessResponse(ctx, accessRequest)
	if err != nil {
		logger.Errorw("failed to create access response",
			"error", err.Error(),
		)
		h.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// Write the token response
	h.provider.WriteAccessResponse(ctx, w, accessRequest, response)
}
