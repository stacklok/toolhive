// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
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
		slog.Error("failed to create access request",
			"error", err,
		)
		h.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// RFC 8707: Handle resource parameter for audience claim.
	// The resource parameter allows clients to specify which protected resource (MCP server)
	// the token is intended for. This value becomes the "aud" claim in the JWT.
	//
	// Note: RFC 8707 allows multiple resource parameters, but we explicitly reject them
	// for security reasons (simpler audience model, clearer token scope).
	resources := accessRequest.GetRequestForm()["resource"]
	if len(resources) > 1 {
		slog.Debug("multiple resource parameters not supported", //nolint:gosec // G706: count is an integer
			"count", len(resources),
		)
		h.provider.WriteAccessError(ctx, w, accessRequest,
			server.ErrInvalidTarget.WithHint("Multiple resource parameters are not supported"))
		return
	}
	if len(resources) == 1 {
		resource := resources[0]
		// Validate URI format per RFC 8707
		if err := server.ValidateAudienceURI(resource); err != nil {
			slog.Debug("invalid resource URI format", //nolint:gosec // G706: resource URI from token request
				"resource", resource,
				"error", err,
			)
			h.provider.WriteAccessError(ctx, w, accessRequest, err)
			return
		}

		// Validate against allowed audiences list
		if err := server.ValidateAudienceAllowed(resource, h.config.AllowedAudiences); err != nil {
			slog.Debug("resource not in allowed audiences", //nolint:gosec // G706: resource URI from token request
				"resource", resource,
				"error", err,
			)
			h.provider.WriteAccessError(ctx, w, accessRequest, err)
			return
		}

		slog.Debug("granting audience from resource parameter", //nolint:gosec // G706: resource URI from token request
			"resource", resource,
		)
		accessRequest.GrantAudience(resource)
	}

	// Generate the access response (tokens)
	response, err := h.provider.NewAccessResponse(ctx, accessRequest)
	if err != nil {
		slog.Error("failed to create access response",
			"error", err,
		)
		h.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// Write the token response
	h.provider.WriteAccessResponse(ctx, w, accessRequest, response)
}
