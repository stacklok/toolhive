// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// maxDCRBodySize is the maximum allowed size for DCR request bodies (64KB).
// This prevents DoS attacks via extremely large payloads while being generous
// enough for legitimate requests with multiple redirect URIs.
const maxDCRBodySize = 64 * 1024

// RegisterClientHandler handles POST /oauth/register requests.
// It implements RFC 7591 Dynamic Client Registration for public clients
// with loopback redirect URIs only.
func (h *Handler) RegisterClientHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Limit request body size to prevent DoS attacks
	req.Body = http.MaxBytesReader(w, req.Body, maxDCRBodySize)

	// Validate Content-Type header (RFC 7591 requires application/json)
	contentType := req.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		writeDCRError(w, http.StatusBadRequest, &registration.DCRError{
			Error:            registration.DCRErrorInvalidClientMetadata,
			ErrorDescription: "Content-Type must be application/json",
		})
		return
	}

	// Parse request body
	var dcrReq registration.DCRRequest
	if err := json.NewDecoder(req.Body).Decode(&dcrReq); err != nil {
		writeDCRError(w, http.StatusBadRequest, &registration.DCRError{
			Error:            registration.DCRErrorInvalidClientMetadata,
			ErrorDescription: "invalid JSON request body",
		})
		return
	}

	// Validate request
	validated, dcrErr := registration.ValidateDCRRequest(&dcrReq)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Validate requested scopes against server's supported scopes
	scopes, dcrErr := registration.ValidateScopes(dcrReq.Scope, h.config.ScopesSupported)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Generate client ID
	clientID := uuid.NewString()

	// Create fosite client using factory.
	fositeClient, err := registration.New(registration.Config{
		ID:            clientID,
		RedirectURIs:  validated.RedirectURIs,
		Public:        true,
		GrantTypes:    validated.GrantTypes,
		ResponseTypes: validated.ResponseTypes,
		Scopes:        scopes,
		Audience:      h.config.AllowedAudiences,
	})
	if err != nil {
		slog.Error("failed to create client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to create client",
		})
		return
	}

	// Register client
	if err := h.storage.RegisterClient(ctx, fositeClient); err != nil {
		slog.Error("failed to register client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to register client",
		})
		return
	}

	// INFO-level: one success record per DCR registration is useful audit
	// signal and low enough cardinality to keep at INFO. client_id,
	// software_id, token_endpoint_auth_method, and scopes are public client
	// metadata per RFC 7591 and not credentials.
	//
	// Note: the "issuer" attribute here identifies the THIS server (the
	// ToolHive-embedded AS that is performing the registration), not the
	// upstream AS being registered against. That distinction is important
	// when correlating these logs with the resolver's logs in
	// pkg/authserver/runner/dcr.go, which use "issuer" to mean the
	// upstream AS. The two uses live at opposite ends of the DCR flow.
	// No "upstream" attribute is emitted because the /oauth/register
	// endpoint has no upstream concept.
	logAttrs := []any{
		"client_id", clientID,
		"software_id", validated.SoftwareID,
		"token_endpoint_auth_method", validated.TokenEndpointAuthMethod,
		"scopes", scopes,
	}
	if issuer := h.issuer(); issuer != "" {
		logAttrs = append(logAttrs, "issuer", issuer)
	}
	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Info("registered new DCR client", logAttrs...)

	// Build response per RFC 7591 Section 3.2.1.
	// Scope reflects the scopes actually granted to this client (from
	// ValidateScopes above), not all server-supported scopes. This lets
	// the client know exactly which scopes it can request.
	response := registration.DCRResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        time.Now().Unix(),
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
		Scope:                   registration.FormatScopes(scopes),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode DCR response", "error", err)
	}
}

// writeDCRError writes a DCR error response per RFC 7591 Section 3.2.2.
func writeDCRError(w http.ResponseWriter, statusCode int, dcrErr *registration.DCRError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	// Encoding errors are not recoverable (headers already written), log for diagnostics
	if err := json.NewEncoder(w).Encode(dcrErr); err != nil {
		slog.Debug("failed to encode DCR error response", "error", err)
	}
}
