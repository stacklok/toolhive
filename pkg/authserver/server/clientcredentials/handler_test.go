// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package clientcredentials

import (
	"cmp"
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/internal/spiffetest"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testResource = "https://resource.example.com"
	testSPIFFEID = "spiffe://example.org/workload/concrete"
)

// recordingHandler stands in for Fosite's stock handler; no gomock mock of
// fosite.TokenEndpointHandler exists in the repo.
type recordingHandler struct {
	called, populated bool
	// lifespan, when set, is the access-token lifetime the stand-in stamps on
	// the session as the stock handler does.
	lifespan time.Duration
}

func (*recordingHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return true
}
func (*recordingHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}
func (h *recordingHandler) HandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) error {
	h.called = true
	if h.lifespan > 0 {
		requester.GetSession().SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(h.lifespan))
	}
	return nil
}
func (h *recordingHandler) PopulateTokenEndpointResponse(context.Context, fosite.AccessRequester, fosite.AccessResponder) error {
	h.populated = true
	return nil
}

// clientOpts describes a SPIFFE client for tests; zero fields take defaults
// (client_credentials grant, openid scope, testResource, JWT method).
type clientOpts struct {
	clientID  string
	spiffeID  string
	grants    []string
	scopes    []string
	resources []string
	method    spiffeauth.SPIFFEAuthenticationMethod
	expiry    time.Time
	// unwrapped returns the static client without the authenticated principal.
	unwrapped bool
}

func newClient(t *testing.T, o clientOpts) fosite.Client {
	t.Helper()

	opts := []spiffetest.Option{
		spiffetest.WithID(cmp.Or(o.clientID, "client")),
		spiffetest.WithSPIFFEID(cmp.Or(o.spiffeID, testSPIFFEID)),
		spiffetest.WithAuthMethod(cmp.Or(o.method, spiffeauth.SPIFFEAuthenticationMethodJWT)),
		spiffetest.WithGrantTypes(orDefault(o.grants, oauthproto.GrantTypeClientCredentials)...),
		spiffetest.WithScopes(orDefault(o.scopes, "openid")...),
		spiffetest.WithResources(orDefault(o.resources, testResource)...),
	}
	if !o.expiry.IsZero() {
		opts = append(opts, spiffetest.WithCredentialExpiry(o.expiry))
	}
	if o.unwrapped {
		return spiffetest.Client(t, opts...)
	}
	return spiffetest.AuthenticatedClient(t, opts...)
}

// orDefault returns v, or def when v is nil; a non-nil empty slice is kept.
func orDefault(v []string, def ...string) []string {
	if v == nil {
		return def
	}
	return v
}

func TestHandlerHandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()

	ccGrant := fosite.Arguments{oauthproto.GrantTypeClientCredentials}
	defaultClient := clientOpts{}
	oneResource := url.Values{"resource": {testResource}}

	tests := []struct {
		name      string
		grants    fosite.Arguments
		client    fosite.Client
		form      url.Values
		scopes    fosite.Arguments
		wantError error
		wantHint  string
		check     func(*testing.T, *fosite.AccessRequest)
	}{
		{
			name:   "exact grant creates SPIFFE subject session",
			grants: ccGrant,
			client: newClient(t, defaultClient),
			form:   oneResource,
			scopes: fosite.Arguments{"openid"},
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok)
				assert.Equal(t, testSPIFFEID, sess.JWTClaims.Subject)
				assert.Equal(t, "client", sess.JWTClaims.Extra[session.ClientIDClaimKey])
				assert.Equal(t, fosite.Arguments{"openid"}, req.GetGrantedScopes())
				assert.Equal(t, fosite.Arguments{testResource}, req.GetGrantedAudience())
			},
		},
		{
			name:   "omitted scope grants none",
			grants: ccGrant,
			client: newClient(t, defaultClient),
			form:   oneResource,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				assert.Empty(t, req.GetGrantedScopes())
			},
		},
		{
			name:      "excess scope is invalid scope",
			grants:    ccGrant,
			client:    newClient(t, defaultClient),
			form:      oneResource,
			scopes:    fosite.Arguments{"admin"},
			wantError: fosite.ErrInvalidScope,
			wantHint:  "is not permitted for this client",
		},
		{
			name:      "ordinary confidential client is unauthorized",
			grants:    ccGrant,
			client:    &fosite.DefaultClient{ID: "client", Public: false},
			form:      oneResource,
			wantError: fosite.ErrUnauthorizedClient,
			wantHint:  "only to clients authenticated with a SPIFFE credential",
		},
		{
			name:      "unwrapped static SPIFFE client is unauthorized",
			grants:    ccGrant,
			client:    newClient(t, clientOpts{unwrapped: true}),
			form:      oneResource,
			wantError: fosite.ErrUnauthorizedClient,
			wantHint:  "only to clients authenticated with a SPIFFE credential",
		},
		{
			name:      "association without client_credentials is unauthorized",
			grants:    ccGrant,
			client:    newClient(t, clientOpts{grants: []string{oauthproto.GrantTypeTokenExchange}}),
			form:      oneResource,
			wantError: fosite.ErrUnauthorizedClient,
			wantHint:  "not permitted to use the client_credentials grant",
		},
		{
			name:      "grant check precedes scope check",
			grants:    ccGrant,
			client:    newClient(t, clientOpts{grants: []string{oauthproto.GrantTypeTokenExchange}}),
			form:      oneResource,
			scopes:    fosite.Arguments{"admin"},
			wantError: fosite.ErrUnauthorizedClient,
			wantHint:  "not permitted to use the client_credentials grant",
		},
		{
			name:      "client credentials plus another grant is declined",
			grants:    fosite.Arguments{oauthproto.GrantTypeClientCredentials, oauthproto.GrantTypeTokenExchange},
			client:    newClient(t, defaultClient),
			form:      oneResource,
			wantError: fosite.ErrUnknownRequest,
		},
		{
			name:      "other grant is declined",
			grants:    fosite.Arguments{oauthproto.GrantTypeTokenExchange},
			client:    newClient(t, defaultClient),
			form:      oneResource,
			wantError: fosite.ErrUnknownRequest,
		},
		{
			name:      "missing resource is invalid target",
			grants:    ccGrant,
			client:    newClient(t, defaultClient),
			wantError: server.ErrInvalidTarget,
			wantHint:  "Exactly one resource parameter is required",
		},
		{
			name:      "audience only is invalid target",
			grants:    ccGrant,
			client:    newClient(t, defaultClient),
			form:      url.Values{"audience": {testResource}},
			wantError: server.ErrInvalidTarget,
			wantHint:  "audience parameter is not accepted",
		},
		{
			name:      "unauthorized resource is invalid target",
			grants:    ccGrant,
			client:    newClient(t, defaultClient),
			form:      url.Values{"resource": {"https://other.example.com"}},
			wantError: server.ErrInvalidTarget,
			wantHint:  "must exactly match a permitted resource",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stock := &recordingHandler{}
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes, req.Client, req.Form, req.RequestedScope = tt.grants, tt.client, tt.form, tt.scopes
			handler := &Handler{stock: stock, allowedAudiences: []string{testResource}}
			err := handler.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
				assert.Contains(t, fosite.ErrorToRFC6749Error(err).HintField, tt.wantHint)
				assert.False(t, stock.called)
				return
			}
			require.NoError(t, err)
			assert.True(t, stock.called)
			tt.check(t, req)
		})
	}
}

func TestHandlerPopulateTokenEndpointResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		grants     fosite.Arguments
		wantError  error
		wantCalled bool
	}{
		{
			name:       "client_credentials delegates to the stock handler",
			grants:     fosite.Arguments{oauthproto.GrantTypeClientCredentials},
			wantCalled: true,
		},
		{
			name:      "other grant is declined without calling the stock handler",
			grants:    fosite.Arguments{oauthproto.GrantTypeTokenExchange},
			wantError: fosite.ErrUnknownRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stock := &recordingHandler{}
			req := fosite.NewAccessRequest(session.New(testSPIFFEID, "", "client", session.UserClaims{}))
			req.GrantTypes = tt.grants
			err := (&Handler{stock: stock}).PopulateTokenEndpointResponse(context.Background(), req, fosite.NewAccessResponse())
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalled, stock.populated)
		})
	}
}

func TestHandlerCapsAccessTokenAtCredentialExpiry(t *testing.T) {
	t.Parallel()

	const lifespan = time.Hour
	tests := []struct {
		name string
		// credentialIn is the credential's remaining validity.
		credentialIn time.Duration
		// wantIn is the expected access-token lifetime, within a few seconds.
		wantIn    time.Duration
		wantError bool
	}{
		{name: "credential sooner than the access-token lifespan caps the token", credentialIn: 5 * time.Minute, wantIn: 5 * time.Minute},
		{name: "credential outliving the access-token lifespan leaves the lifespan", credentialIn: 24 * time.Hour, wantIn: lifespan},
		{name: "expired credential is refused", credentialIn: -time.Second, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stock := &recordingHandler{lifespan: lifespan}
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeClientCredentials}
			req.Client = newClient(t, clientOpts{expiry: time.Now().Add(tt.credentialIn)})
			req.Form = url.Values{"resource": {testResource}}
			handler := &Handler{stock: stock, allowedAudiences: []string{testResource}}

			err := handler.HandleTokenEndpointRequest(context.Background(), req)

			if tt.wantError {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				assert.Equal(t, "The client credential has expired.", fosite.ErrorToRFC6749Error(err).HintField)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantIn.Seconds(),
				time.Until(req.GetSession().GetExpiresAt(fosite.AccessToken)).Seconds(), 5)
		})
	}
}

func TestHandlerPopulateRefusesElapsedSessionExpiry(t *testing.T) {
	t.Parallel()

	stock := &recordingHandler{}
	sess := session.New(testSPIFFEID, "", "client", session.UserClaims{})
	sess.SetExpiresAt(fosite.AccessToken, time.Now().Add(-time.Second))
	req := fosite.NewAccessRequest(sess)
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeClientCredentials}

	err := (&Handler{stock: stock}).PopulateTokenEndpointResponse(context.Background(), req, fosite.NewAccessResponse())

	require.ErrorIs(t, err, fosite.ErrInvalidGrant)
	assert.False(t, stock.populated)
}
