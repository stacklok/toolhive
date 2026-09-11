// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// maxDeviceCodeGenerationAttempts bounds retries when a freshly generated
// device_code/user_code pair collides with an existing pending request.
// Collision probability is astronomically small (32 bytes / 8 chars of
// crypto/rand output each); this only guards against exhausting the request
// with retries if storage is somehow degenerate.
const maxDeviceCodeGenerationAttempts = 5

// userCodeCharset is RFC 8628's suggested character set for the human-typed
// user_code: uppercase letters and digits with the visually ambiguous
// characters I, O, 0, and 1 removed, so a user reading the code off a screen
// cannot mistype it as a different valid character.
const userCodeCharset = "BCDFGHJKLMNPQRSTVWXZ0123456789"

// userCodeGroupLength is the length of each hyphen-separated group in the
// generated user_code (RFC 8628's example format is XXXX-XXXX).
const userCodeGroupLength = 4

// deviceAuthorizationResponse is the RFC 8628 Section 3.2 response body.
type deviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval,omitempty"`
}

// DeviceAuthorizationHandler handles POST /oauth/device_authorization
// requests (RFC 8628 Section 3.1). It is only mounted when the server
// config enables the device-code grant — see OAuthRoutes.
//
// The request body is application/x-www-form-urlencoded, per RFC 8628
// Section 3.1; the response body is JSON, per Section 3.2.
func (h *Handler) DeviceAuthorizationHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	req.Body = http.MaxBytesReader(w, req.Body, MaxDCRBodySize)
	if err := req.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "failed to parse request body")
		return
	}

	clientID := req.PostForm.Get("client_id")
	if clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "The client_id parameter is required.")
		return
	}

	client, err := h.storage.GetClient(ctx, clientID)
	if err != nil {
		slog.Debug("device authorization: unknown client", "client_id", clientID, "error", err)
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "The client_id is unknown.")
		return
	}
	if !client.GetGrantTypes().Has(oauthproto.GrantTypeDeviceCode) {
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client",
			"The OAuth 2.0 Client is not allowed to use authorization grant 'urn:ietf:params:oauth:grant-type:device_code'.")
		return
	}

	requestedScopes := strings.Fields(req.PostForm.Get("scope"))
	scopes, _, dcrErr := registration.ValidateScopes(requestedScopes, h.config.ScopesSupported)
	if dcrErr != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", dcrErr.ErrorDescription)
		return
	}

	device, err := h.storeNewDeviceRequest(ctx, clientID, scopes, client.GetAudience())
	if err != nil {
		slog.Error("device authorization: failed to store device request", "client_id", clientID, "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to create device authorization request")
		return
	}

	verificationURI := h.issuer() + "/oauth/device"
	response := deviceAuthorizationResponse{
		DeviceCode:              device.DeviceCode,
		UserCode:                device.UserCode,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?user_code=" + url.QueryEscape(device.UserCode),
		ExpiresIn:               int(storage.DefaultDeviceRequestTTL.Seconds()),
		Interval:                int(h.deviceCodeInterval.Seconds()),
	}

	slog.Debug("issued device authorization request", "client_id", clientID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode device authorization response", "error", err)
	}
}

// storeNewDeviceRequest generates a fresh device_code/user_code pair and
// persists it as pending, retrying on a colliding code up to
// maxDeviceCodeGenerationAttempts times.
func (h *Handler) storeNewDeviceRequest(
	ctx context.Context, clientID string, scopes, audience []string,
) (*storage.DeviceRequest, error) {
	var lastErr error
	for attempt := 0; attempt < maxDeviceCodeGenerationAttempts; attempt++ {
		deviceCode, err := generateDeviceCode()
		if err != nil {
			return nil, fmt.Errorf("generate device_code: %w", err)
		}
		userCode, err := generateUserCode()
		if err != nil {
			return nil, fmt.Errorf("generate user_code: %w", err)
		}
		device := &storage.DeviceRequest{
			DeviceCode: deviceCode,
			UserCode:   userCode,
			ClientID:   clientID,
			Scopes:     scopes,
			Audience:   audience,
			Status:     storage.DeviceRequestStatusPending,
			Interval:   h.deviceCodeInterval,
			CreatedAt:  time.Now(),
		}
		err = h.storage.StoreDeviceRequest(ctx, device)
		if err == nil {
			return device, nil
		}
		if !errors.Is(err, storage.ErrAlreadyExists) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("exhausted %d attempts generating a unique device/user code: %w",
		maxDeviceCodeGenerationAttempts, lastErr)
}

// generateDeviceCode returns a high-entropy, opaque device_code: 32 bytes of
// crypto/rand, base64url-encoded without padding.
func generateDeviceCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// generateUserCode returns a short, human-typeable code in RFC 8628's
// example "XXXX-XXXX" format, drawn from userCodeCharset via crypto/rand so
// it carries enough entropy to resist online guessing within the device
// request's TTL.
func generateUserCode() (string, error) {
	const length = userCodeGroupLength * 2
	raw := make([]byte, length)
	idx := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	charsetLen := byte(len(userCodeCharset))
	for i, b := range raw {
		idx[i] = userCodeCharset[b%charsetLen]
	}
	return string(idx[:userCodeGroupLength]) + "-" + string(idx[userCodeGroupLength:]), nil
}

// writeOAuthError writes a bare RFC 6749-shaped JSON error body. Unlike
// writeDCRError (registration.DCRError, RFC 7591 Section 3.2.2's dedicated
// shape), this endpoint has no fosite.AccessRequester to hand to
// h.provider.WriteAccessError/WriteAuthorizeError, so it mirrors their JSON
// shape directly.
func writeOAuthError(w http.ResponseWriter, statusCode int, errorCode, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	body := struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description,omitempty"`
	}{Error: errorCode, ErrorDescription: description}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("failed to encode OAuth error response", "error", err)
	}
}
