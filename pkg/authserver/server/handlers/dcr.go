// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// MaxDCRBodySize is the maximum allowed size for DCR request bodies (64KB).
// This prevents DoS attacks via extremely large payloads while being generous
// enough for legitimate requests with multiple redirect URIs.
//
// It is exported to serve as the single source of truth for the auth-server
// body-size cap: the embedded auth server (pkg/authserver/runner) derives its
// own request-body limit from this constant so the two cannot drift.
const MaxDCRBodySize = 64 * 1024

// RegisterClientHandler handles POST /oauth/register requests.
// It implements RFC 7591 Dynamic Client Registration for public clients
// with loopback redirect URIs only.
func (h *Handler) RegisterClientHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Limit request body size to prevent DoS attacks
	req.Body = http.MaxBytesReader(w, req.Body, MaxDCRBodySize)

	// Validate Content-Type header (RFC 7591 requires application/json)
	contentType := req.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		writeDCRError(w, http.StatusBadRequest, &registration.DCRError{
			Error:            registration.DCRErrorInvalidClientMetadata,
			ErrorDescription: "Content-Type must be application/json",
		})
		return
	}

	// Parse request body. oauthproto.ScopeList.UnmarshalJSON handles both
	// RFC 7591 wire formats for "scope" (space-delimited string or JSON
	// array) so we accept either shape transparently here.
	var dcrReq oauthproto.DynamicClientRegistrationRequest
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
	scopes, dcrErr := registration.ValidateScopes(dcrReq.Scopes, h.config.ScopesSupported)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Union with the operator-configured scope baseline. RFC 7591 §3.2.1 permits
	// the AS to replace requested client metadata values during registration; we
	// use that to expand the registered scope set so a client whose DCR request
	// narrowed the scope field can still request the baseline at /oauth/authorize.
	// h.config.BaselineClientScopes is validated at startup to be a subset of
	// ScopesSupported, so the union is guaranteed to be a subset of advertised
	// scopes. Operators should keep the baseline narrow (e.g. openid,
	// offline_access) — every DCR-registered client gains the ability to request
	// these scopes at /oauth/authorize regardless of what they registered with.
	if len(h.config.BaselineClientScopes) > 0 {
		effective := registration.UnionScopes(scopes, h.config.BaselineClientScopes)
		if !slices.Equal(effective, scopes) {
			// Baseline-driven expansion is the intended behavior whenever
			// baseline_client_scopes is configured, so per-registration
			// audit lives at Debug rather than Warn. Operator-visible
			// signal that the baseline is in effect comes from a one-time
			// Info log at server startup (NewAuthorizationServerConfig).
			slog.Debug("DCR registered scope set expanded by baseline_client_scopes",
				"client_name", validated.ClientName,
				"requested", scopes,
				"effective", effective,
				"baseline", h.config.BaselineClientScopes,
			)
			scopes = effective
		}
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

	// Successful DCR registration is a normal operational event, not a
	// long-running operation, so it logs at Debug to stay silent at INFO+.
	// client_id, software_id, token_endpoint_auth_method, and scopes are
	// public client metadata per RFC 7591 and not credentials. If audit
	// signal is desired in future, the right home is a dedicated audit-
	// log emission path rather than promoting this record to INFO.
	//
	// Note: the "issuer" attribute here identifies THIS server (the
	// ToolHive-embedded AS that is performing the registration), not the
	// upstream AS being registered against. That distinction is important
	// when correlating these logs with the resolver's logs in
	// pkg/auth/dcr/resolver.go, which use "issuer" to mean the
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
	slog.Debug("registered new DCR client", logAttrs...)

	// Build response per RFC 7591 Section 3.2.1.
	// Scopes reflects the scopes actually granted to this client: the
	// client-supplied scope set was validated against ScopesSupported by
	// ValidateScopes above, then (if configured) unioned with
	// BaselineClientScopes — which is itself guaranteed by startup-time
	// validation to be a subset of ScopesSupported. The unioned set is NOT
	// re-validated here. ScopeList.MarshalJSON emits the RFC 7591 §2
	// space-delimited wire form on the way out.
	response := oauthproto.DynamicClientRegistrationResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        time.Now().Unix(),
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
		Scopes:                  oauthproto.ScopeList(scopes),
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
