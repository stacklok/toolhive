// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
)

// secretExpiryBuffer is the lead time before expiry at which we proactively
// renew the client secret (RFC 7592). Renewal is attempted when the secret
// expires within this window, not only after expiry.
const secretExpiryBuffer = 24 * time.Hour

// clientUpdateRequest is the body sent in a RFC 7592 §2.2 PUT request.
// Per the spec, all client metadata fields that were provided during
// registration must be included in the update request body.
type clientUpdateRequest struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// clientUpdateResponse is the body returned by a RFC 7592 §2.1 response.
// The provider may rotate the registration_access_token; if present we must
// replace the stored one.
type clientUpdateResponse struct {
	// Required fields mirrored from registration response
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"` //nolint:gosec // G117: field holds sensitive data

	// Expiry fields
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at,omitempty"`

	// Management fields — registration_access_token may be rotated
	RegistrationAccessToken string `json:"registration_access_token,omitempty"` //nolint:gosec
	RegistrationClientURI   string `json:"registration_client_uri,omitempty"`
}

// isSecretExpiredOrExpiringSoon returns true when the cached client secret is
// already expired or will expire within secretExpiryBuffer.
// A zero CachedSecretExpiry means the secret never expires, so this returns false.
func (h *Handler) isSecretExpiredOrExpiringSoon() bool {
	expiry := h.config.CachedSecretExpiry
	if expiry.IsZero() {
		return false // Non-expiring secret
	}
	return time.Now().After(expiry.Add(-secretExpiryBuffer))
}

// renewClientSecret attempts to renew the client secret using RFC 7592 §2.2.
// It retrieves the stored registration_access_token and sends a PUT request
// to the registration_client_uri with the current client metadata.
//
// On success the handler's config is updated with the new secret, expiry, and
// (if rotated) the new registration_access_token.
//
// Callers should log a warning and continue if renewal fails — the existing
// secret may still be valid for some time, or the provider may not support renewal.
func (h *Handler) renewClientSecret(ctx context.Context) error {
	if err := h.validateRenewalPrerequisites(); err != nil {
		return err
	}

	// Retrieve the registration access token from the secret manager
	regAccessToken, err := h.secretProvider.GetSecret(ctx, h.config.CachedRegTokenRef)
	if err != nil {
		return fmt.Errorf("failed to retrieve registration access token: %w", err)
	}

	slog.Debug("Attempting RFC 7592 client secret renewal",
		"registration_client_uri", h.config.CachedRegClientURI)

	// Validate the registration_client_uri before using it
	if err := validateRegistrationClientURI(h.config.CachedRegClientURI); err != nil {
		return fmt.Errorf("invalid registration_client_uri: %w", err)
	}

	// Build the update request body with the current client metadata.
	// Per RFC 7592 §2.2, the request MUST include all client metadata fields
	// that were provided during the initial registration.
	updateReq := clientUpdateRequest{
		ClientID:                h.config.CachedClientID,
		ClientName:              "ToolHive MCP Client",
		RedirectURIs:            []string{fmt.Sprintf("http://localhost:%d/callback", h.config.CallbackPort)},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: h.config.CachedTokenEndpointAuthMethod,
	}

	reqBody, err := json.Marshal(updateReq)
	if err != nil {
		return fmt.Errorf("failed to marshal client update request: %w", err)
	}

	// Create PUT request per RFC 7592 §2.2
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		h.config.CachedRegClientURI,
		strings.NewReader(string(reqBody)),
	)
	if err != nil {
		return fmt.Errorf("failed to create client update request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+regAccessToken) //nolint:gosec // G117

	// Execute the request
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("client update request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("Failed to close renewal response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("client update request returned HTTP %d: %s", resp.StatusCode, string(errorBody))
	}

	// Parse the renewal response
	const maxResponseSize = 1024 * 1024 // 1 MB
	var updateResp clientUpdateResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&updateResp); err != nil {
		return fmt.Errorf("failed to decode client update response: %w", err)
	}

	if updateResp.ClientID == "" {
		return fmt.Errorf("client update response missing client_id")
	}
	if updateResp.ClientSecret == "" {
		return fmt.Errorf("client update response missing client_secret")
	}

	return h.persistRenewedSecret(updateResp)
}

func (h *Handler) validateRenewalPrerequisites() error {
	if h.config.CachedRegClientURI == "" {
		return fmt.Errorf("registration_client_uri missing; cannot renew secret (RFC 7592 unsupported)")
	}
	if h.config.CachedRegTokenRef == "" {
		return fmt.Errorf("registration_access_token missing; cannot renew secret (RFC 7592 unsupported)")
	}
	if h.secretProvider == nil {
		return fmt.Errorf("secret provider not configured; cannot retrieve registration access token")
	}
	return nil
}

func (h *Handler) persistRenewedSecret(updateResp clientUpdateResponse) error {
	if h.clientCredentialsPersister == nil {
		return fmt.Errorf("client credentials persister not configured; cannot save renewed secret")
	}

	var newExpiry time.Time
	if updateResp.ClientSecretExpiresAt > 0 {
		newExpiry = time.Unix(updateResp.ClientSecretExpiresAt, 0)
	}

	// Use the rotated registration_access_token if provided; fall back to existing.
	newRegToken := updateResp.RegistrationAccessToken
	newRegURI := updateResp.RegistrationClientURI
	if newRegURI == "" {
		newRegURI = h.config.CachedRegClientURI
	}

	if err := h.clientCredentialsPersister(
		updateResp.ClientID,
		updateResp.ClientSecret,
		newExpiry,
		newRegToken,
		newRegURI,
		h.config.CachedTokenEndpointAuthMethod,
	); err != nil {
		return fmt.Errorf("failed to persist renewed client secret: %w", err)
	}

	slog.Info("Successfully renewed client secret via RFC 7592",
		"client_id", updateResp.ClientID,
		"new_expiry_zero", newExpiry.IsZero(),
		"reg_token_rotated", newRegToken != "")

	return nil
}

// validateRegistrationClientURI validates that the registration_client_uri is
// a valid HTTPS URL (or localhost for development).
func validateRegistrationClientURI(registrationClientURI string) error {
	if registrationClientURI == "" {
		return fmt.Errorf("registration_client_uri is empty")
	}

	parsedURL, err := url.Parse(registrationClientURI)
	if err != nil {
		return fmt.Errorf("invalid registration_client_uri URL: %w", err)
	}

	if parsedURL.Scheme != "https" && !networking.IsLocalhost(parsedURL.Host) {
		return fmt.Errorf("registration_client_uri must use HTTPS: %s", registrationClientURI)
	}

	return nil
}
