// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package deviceflow

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	authstorage "github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testClientID  = "test-client"
	testDeviceURI = "https://as.example.com"
)

// newTestFositeConfig returns a minimal *fosite.Config supplying the
// AccessTokenLifespan/RefreshTokenLifespan the deviceFlowConfig interface needs.
func newTestFositeConfig() *fosite.Config {
	return &fosite.Config{
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		GlobalSecret:         []byte("01234567890123456789012345678901"),
	}
}

// newTestStrategyAndStorage builds a real fosite CoreStrategy (JWT access
// tokens over an HMAC core, mirroring createProvider in server_impl.go) and
// uses storage.NewMemoryStorage() as the CoreStorage, so PopulateTokenEndpointResponse
// exercises real token issuance rather than a stub.
func newTestStrategyAndStorage(t *testing.T, cfg *fosite.Config) (oauth2.CoreStrategy, *authstorage.MemoryStorage) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (any, error) { return rsaKey, nil },
		compose.NewOAuth2HMACStrategy(cfg),
		cfg,
	)
	stor := authstorage.NewMemoryStorage()
	return &compose.CommonStrategy{CoreStrategy: jwtStrategy}, stor
}

func newDeviceCodeRequester(clientGrantTypes fosite.Arguments, deviceCode string) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeDeviceCode}
	req.Client = &fosite.DefaultClient{
		ID:         testClientID,
		GrantTypes: clientGrantTypes,
		Scopes:     fosite.Arguments{"openid", "profile"},
		Public:     false,
	}
	req.Form = map[string][]string{"device_code": {deviceCode}}
	return req
}

func TestCanHandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()
	h := &Handler{}

	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeDeviceCode}
	assert.True(t, h.CanHandleTokenEndpointRequest(context.Background(), req))

	other := fosite.NewAccessRequest(&session.Session{})
	other.GrantTypes = fosite.Arguments{"authorization_code"}
	assert.False(t, h.CanHandleTokenEndpointRequest(context.Background(), other))
}

func TestCanSkipClientAuth(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	assert.False(t, h.CanSkipClientAuth(context.Background(), nil))
}

func fullGrantTypes() fosite.Arguments {
	return fosite.Arguments{oauthproto.GrantTypeDeviceCode, oauthproto.GrantTypeRefreshToken}
}

func TestHandleTokenEndpointRequest_Pending(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	require.NoError(t, stor.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: testClientID,
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "authorization_pending", rfcErr.ErrorField)
}

func TestHandleTokenEndpointRequest_Denied(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	require.NoError(t, stor.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: testClientID,
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	require.NoError(t, stor.MarkDeviceRequestDenied(context.Background(), "dc-1"))
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "access_denied", rfcErr.ErrorField)
}

func TestHandleTokenEndpointRequest_UnknownDeviceCode(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fullGrantTypes(), "does-not-exist")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "invalid_grant", rfcErr.ErrorField)
}

func TestHandleTokenEndpointRequest_WrongClient(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	require.NoError(t, stor.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: "someone-else",
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	// Same code as "unknown" — must not leak that the device_code exists for
	// a different client.
	assert.Equal(t, "invalid_grant", rfcErr.ErrorField)
}

func TestHandleTokenEndpointRequest_Expired(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockStor := mocks.NewMockDeviceCodeStorage(ctrl)
	mockStor.EXPECT().LoadDeviceRequestByDeviceCode(gomock.Any(), "dc-1").Return(nil, authstorage.ErrExpired)

	h := &Handler{DeviceStorage: mockStor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "expired_token", rfcErr.ErrorField)
}

func TestHandleTokenEndpointRequest_SlowDown(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	require.NoError(t, stor.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: testClientID,
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	require.NoError(t, stor.UpdateDeviceRequestLastPolledAt(context.Background(), "dc-1", time.Now()))
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Hour}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "slow_down", rfcErr.ErrorField)

	// The poll must still have been recorded even though it was too fast.
	device, loadErr := stor.LoadDeviceRequestByDeviceCode(context.Background(), "dc-1")
	require.NoError(t, loadErr)
	assert.WithinDuration(t, time.Now(), device.LastPolledAt, 5*time.Second)
}

func TestHandleTokenEndpointRequest_ClientNotRegisteredForGrant(t *testing.T) {
	t.Parallel()
	stor := authstorage.NewMemoryStorage()
	h := &Handler{DeviceStorage: stor, Config: newTestFositeConfig(), MinInterval: time.Second}
	req := newDeviceCodeRequester(fosite.Arguments{}, "dc-1")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "unauthorized_client", rfcErr.ErrorField)
}

// TestAuthorizedFlow_IssuesTokensAndIsSingleUse covers the authorized path
// end to end: HandleTokenEndpointRequest attaches a session and consumes the
// device_code, PopulateTokenEndpointResponse issues both an access and a
// refresh token with the stored scopes/audience, and a second attempt to
// redeem the same device_code fails with invalid_grant.
func TestAuthorizedFlow_IssuesTokensAndIsSingleUse(t *testing.T) {
	t.Parallel()
	fositeCfg := newTestFositeConfig()
	strategy, coreStorage := newTestStrategyAndStorage(t, fositeCfg)

	require.NoError(t, coreStorage.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: testClientID,
		Scopes: []string{"openid"}, Audience: []string{testDeviceURI},
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	require.NoError(t, coreStorage.MarkDeviceRequestAuthorized(
		context.Background(), "dc-1", "user-1", "Ada Lovelace", "ada@example.com", "session-1"))

	h := &Handler{
		DeviceStorage: coreStorage,
		CoreStorage:   coreStorage,
		Strategy:      strategy,
		Config:        fositeCfg,
		MinInterval:   time.Second,
	}
	req := newDeviceCodeRequester(fullGrantTypes(), "dc-1")

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))
	assert.ElementsMatch(t, []string{"openid"}, []string(req.GetGrantedScopes()))
	assert.ElementsMatch(t, []string{testDeviceURI}, []string(req.GetGrantedAudience()))

	responder := fosite.NewAccessResponse()
	require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, responder))
	assert.NotEmpty(t, responder.GetAccessToken())
	assert.Equal(t, "bearer", responder.GetTokenType())
	assert.NotEmpty(t, responder.GetExtra("refresh_token"))

	// The device_code has been consumed: a second redemption attempt fails.
	req2 := newDeviceCodeRequester(fullGrantTypes(), "dc-1")
	err := h.HandleTokenEndpointRequest(context.Background(), req2)
	require.Error(t, err)
	rfcErr := fosite.ErrorToRFC6749Error(err)
	assert.Equal(t, "invalid_grant", rfcErr.ErrorField)
}

// TestAuthorizedFlow_NoRefreshTokenWithoutGrant confirms a client not
// registered for the refresh_token grant receives an access token only.
func TestAuthorizedFlow_NoRefreshTokenWithoutGrant(t *testing.T) {
	t.Parallel()
	fositeCfg := newTestFositeConfig()
	strategy, coreStorage := newTestStrategyAndStorage(t, fositeCfg)

	require.NoError(t, coreStorage.StoreDeviceRequest(context.Background(), &authstorage.DeviceRequest{
		DeviceCode: "dc-1", UserCode: "AAAA-BBBB", ClientID: testClientID,
		Status: authstorage.DeviceRequestStatusPending, CreatedAt: time.Now(),
	}))
	require.NoError(t, coreStorage.MarkDeviceRequestAuthorized(
		context.Background(), "dc-1", "user-1", "", "", "session-1"))

	h := &Handler{
		DeviceStorage: coreStorage,
		CoreStorage:   coreStorage,
		Strategy:      strategy,
		Config:        fositeCfg,
		MinInterval:   time.Second,
	}
	req := newDeviceCodeRequester(fosite.Arguments{oauthproto.GrantTypeDeviceCode}, "dc-1")

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

	responder := fosite.NewAccessResponse()
	require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, responder))
	assert.NotEmpty(t, responder.GetAccessToken())
	assert.Empty(t, responder.GetExtra("refresh_token"))
}
