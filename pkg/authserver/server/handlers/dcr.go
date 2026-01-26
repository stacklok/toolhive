// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/logger"
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

	// Generate client ID
	clientID := uuid.NewString()

	// Create fosite client using factory
	fositeClient, err := registration.New(registration.Config{
		ID:            clientID,
		RedirectURIs:  validated.RedirectURIs,
		Public:        true,
		GrantTypes:    validated.GrantTypes,
		ResponseTypes: validated.ResponseTypes,
	})
	if err != nil {
		logger.Errorw("failed to create client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to create client",
		})
		return
	}

	// Register client
	if err := h.storage.RegisterClient(ctx, fositeClient); err != nil {
		logger.Errorw("failed to register client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to register client",
		})
		return
	}

	logger.Debugw("registered new DCR client",
		"client_id", clientID,
		"client_name", validated.ClientName,
	)

	// Build response
	response := registration.DCRResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        time.Now().Unix(),
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorw("failed to encode DCR response", "error", err)
	}
}

// writeDCRError writes a DCR error response per RFC 7591 Section 3.2.2.
func writeDCRError(w http.ResponseWriter, statusCode int, dcrErr *registration.DCRError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	// Encoding errors are not recoverable (headers already written), log for diagnostics
	if err := json.NewEncoder(w).Encode(dcrErr); err != nil {
		logger.Debugw("failed to encode DCR error response", "error", err)
	}
}
