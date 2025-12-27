// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

// RegisterClientHandler handles POST /oauth2/register requests.
// It implements RFC 7591 Dynamic Client Registration for public clients
// with loopback redirect URIs only.
func (r *Router) RegisterClientHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Parse request body
	var dcrReq DCRRequest
	if err := json.NewDecoder(req.Body).Decode(&dcrReq); err != nil {
		r.writeDCRError(w, http.StatusBadRequest, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "invalid JSON request body",
		})
		return
	}

	// Validate request
	validated, dcrErr := ValidateDCRRequest(&dcrReq)
	if dcrErr != nil {
		r.writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Generate client ID
	clientID := uuid.NewString()

	// Create fosite client
	defaultClient := &fosite.DefaultClient{
		ID:            clientID,
		RedirectURIs:  validated.RedirectURIs,
		ResponseTypes: validated.ResponseTypes,
		GrantTypes:    validated.GrantTypes,
		Scopes:        []string{"openid", "profile", "email"},
		Public:        true,
	}

	// Wrap in LoopbackClient for RFC 8252 Section 7.3 dynamic port matching
	client := NewLoopbackClient(defaultClient)

	// Register client
	r.storage.RegisterClient(client)

	r.logger.InfoContext(ctx, "registered new DCR client",
		slog.String("client_id", clientID),
		slog.String("client_name", validated.ClientName),
	)

	// Build response
	response := DCRResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        time.Now().Unix(),
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		r.logger.ErrorContext(ctx, "failed to encode DCR response",
			slog.String("error", err.Error()),
		)
	}
}

// writeDCRError writes a DCR error response per RFC 7591 Section 3.2.2.
func (*Router) writeDCRError(w http.ResponseWriter, statusCode int, dcrErr *DCRError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(dcrErr)
}
