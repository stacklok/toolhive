// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// setupDeviceVerificationHandler builds a Handler with device flow enabled,
// one mock upstream, and a registered public device_code client. Returns the
// handler and the mock upstream so tests can configure/inspect the exchange.
func setupDeviceVerificationHandler(t *testing.T) (*Handler, *mockIDPProvider) {
	t.Helper()

	stor := storage.NewMemoryStorage()
	ctx := context.Background()

	client := &fosite.DefaultClient{
		ID:         "device-client",
		GrantTypes: []string{oauthproto.GrantTypeDeviceCode},
		Scopes:     []string{"openid"},
		Public:     true,
	}
	require.NoError(t, stor.RegisterClient(ctx, client))

	mockUpstream := &mockIDPProvider{
		providerType:     upstream.ProviderTypeOAuth2,
		authorizationURL: "https://idp.example.com/authorize",
		exchangeResult: &upstream.Identity{
			Tokens: &upstream.Tokens{
				AccessToken: "upstream-access-token",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
			Subject: "upstream-subject-1",
			Name:    "Ada Lovelace",
			Email:   "ada@example.com",
		},
	}

	fositeConfig := &fosite.Config{}
	provider := fosite.NewOAuth2Provider(stor, fositeConfig)
	config := &server.AuthorizationServerConfig{
		Config:            fositeConfig,
		ScopesSupported:   []string{"openid"},
		DeviceFlowEnabled: true,
	}
	h, err := NewHandler(provider, config, stor, []NamedUpstream{{Name: "test-upstream", Provider: mockUpstream}})
	require.NoError(t, err)

	return h, mockUpstream
}

// issueDeviceCode drives POST /oauth/device_authorization for the given
// client and returns the device_code/user_code pair.
func issueDeviceCode(t *testing.T, h *Handler, clientID string) (deviceCode, userCode string) {
	t.Helper()

	form := url.Values{"client_id": {clientID}, "scope": {"openid"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/device_authorization", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.DeviceAuthorizationHandler(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body["device_code"].(string), body["user_code"].(string)
}

var confirmTokenPattern = regexp.MustCompile(`name="confirm_token" value="([^"]+)"`)

func extractConfirmToken(t *testing.T, body string) string {
	t.Helper()
	matches := confirmTokenPattern.FindStringSubmatch(body)
	require.Len(t, matches, 2, "confirm_token not found in body: %s", body)
	return matches[1]
}

func TestDeviceVerificationHandler_RendersForm(t *testing.T) {
	t.Parallel()
	h, _ := setupDeviceVerificationHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/device?user_code=abcd-1234", nil)
	rec := httptest.NewRecorder()

	h.DeviceVerificationHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `value="ABCD-1234"`)
}

func TestDeviceVerificationFlow_ApproveHappyPath(t *testing.T) {
	t.Parallel()
	h, mockUpstream := setupDeviceVerificationHandler(t)
	deviceCode, userCode := issueDeviceCode(t, h, "device-client")

	// Step 1: submit the user_code.
	submitForm := url.Values{"user_code": {userCode}}
	submitReq := httptest.NewRequest(http.MethodPost, "/oauth/device", strings.NewReader(submitForm.Encode()))
	submitReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submitRec := httptest.NewRecorder()
	h.DeviceVerificationSubmitHandler(submitRec, submitReq)

	require.Equal(t, http.StatusFound, submitRec.Code, "body: %s", submitRec.Body.String())
	state := mockUpstream.capturedState
	require.NotEmpty(t, state)

	// Step 2: upstream redirects back to the shared /oauth/callback endpoint.
	callbackReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+state, nil)
	callbackRec := httptest.NewRecorder()
	h.CallbackHandler(callbackRec, callbackReq)

	require.Equal(t, http.StatusOK, callbackRec.Code, "body: %s", callbackRec.Body.String())
	assert.Contains(t, callbackRec.Body.String(), "Ada Lovelace")
	confirmToken := extractConfirmToken(t, callbackRec.Body.String())

	// Step 3: approve.
	confirmForm := url.Values{"confirm_token": {confirmToken}, "action": {"approve"}}
	confirmReq := httptest.NewRequest(http.MethodPost, "/oauth/device/confirm", strings.NewReader(confirmForm.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmRec := httptest.NewRecorder()
	h.DeviceVerificationConfirmHandler(confirmRec, confirmReq)

	require.Equal(t, http.StatusOK, confirmRec.Code, "body: %s", confirmRec.Body.String())
	assert.Contains(t, confirmRec.Body.String(), "Device authorized")

	device, err := h.deviceStorage.LoadDeviceRequestByDeviceCode(context.Background(), deviceCode)
	require.NoError(t, err)
	assert.Equal(t, storage.DeviceRequestStatusAuthorized, device.Status)
	assert.NotEmpty(t, device.ResolvedUserID)
	assert.Equal(t, "Ada Lovelace", device.ResolvedUserName)
	assert.Equal(t, "ada@example.com", device.ResolvedUserEmail)

	// The confirm_token is single-use.
	replayRec := httptest.NewRecorder()
	h.DeviceVerificationConfirmHandler(replayRec, confirmReq)
	assert.Equal(t, http.StatusBadRequest, replayRec.Code)
}

func TestDeviceVerificationFlow_Deny(t *testing.T) {
	t.Parallel()
	h, mockUpstream := setupDeviceVerificationHandler(t)
	deviceCode, userCode := issueDeviceCode(t, h, "device-client")

	submitForm := url.Values{"user_code": {userCode}}
	submitReq := httptest.NewRequest(http.MethodPost, "/oauth/device", strings.NewReader(submitForm.Encode()))
	submitReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submitRec := httptest.NewRecorder()
	h.DeviceVerificationSubmitHandler(submitRec, submitReq)
	require.Equal(t, http.StatusFound, submitRec.Code)

	callbackReq := httptest.NewRequest(http.MethodGet,
		"/oauth/callback?code=upstream-code&state="+mockUpstream.capturedState, nil)
	callbackRec := httptest.NewRecorder()
	h.CallbackHandler(callbackRec, callbackReq)
	require.Equal(t, http.StatusOK, callbackRec.Code)
	confirmToken := extractConfirmToken(t, callbackRec.Body.String())

	confirmForm := url.Values{"confirm_token": {confirmToken}, "action": {"deny"}}
	confirmReq := httptest.NewRequest(http.MethodPost, "/oauth/device/confirm", strings.NewReader(confirmForm.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmRec := httptest.NewRecorder()
	h.DeviceVerificationConfirmHandler(confirmRec, confirmReq)

	require.Equal(t, http.StatusOK, confirmRec.Code)
	assert.Contains(t, confirmRec.Body.String(), "Access denied")

	device, err := h.deviceStorage.LoadDeviceRequestByDeviceCode(context.Background(), deviceCode)
	require.NoError(t, err)
	assert.Equal(t, storage.DeviceRequestStatusDenied, device.Status)
}

func TestDeviceVerificationSubmitHandler_InvalidUserCode(t *testing.T) {
	t.Parallel()
	h, _ := setupDeviceVerificationHandler(t)

	form := url.Values{"user_code": {"ZZZZ-9999"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/device", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.DeviceVerificationSubmitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid or has expired")
}

func TestCallbackHandler_UnknownStateForDeviceLogin(t *testing.T) {
	t.Parallel()
	h, _ := setupDeviceVerificationHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=x&state=unknown-state", nil)
	rec := httptest.NewRecorder()

	h.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeviceVerificationConfirmHandler_UnknownToken(t *testing.T) {
	t.Parallel()
	h, _ := setupDeviceVerificationHandler(t)

	form := url.Values{"confirm_token": {"unknown-token"}, "action": {"approve"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/device/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.DeviceVerificationConfirmHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNormalizeUserCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already formatted", "ABCD-1234", "ABCD-1234"},
		{"lowercase", "abcd-1234", "ABCD-1234"},
		{"no dash", "ABCD1234", "ABCD-1234"},
		{"extra whitespace", "  ABCD-1234  ", "ABCD-1234"},
		{"wrong length left as-is", "ABC", "ABC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeUserCode(tt.in))
		})
	}
}
