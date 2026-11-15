// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package clientcredentials

import (
	"cmp"
	"context"
	"net/url"
	"testing"

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
type recordingHandler struct{ called bool }

func (*recordingHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return true
}
func (*recordingHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}
func (h *recordingHandler) HandleTokenEndpointRequest(context.Context, fosite.AccessRequester) error {
	h.called = true
	return nil
}
func (*recordingHandler) PopulateTokenEndpointResponse(context.Context, fosite.AccessRequester, fosite.AccessResponder) error {
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
		},
		{
			name:      "ordinary confidential client is unauthorized",
			grants:    ccGrant,
			client:    &fosite.DefaultClient{ID: "client", Public: false},
			form:      oneResource,
			wantError: fosite.ErrUnauthorizedClient,
		},
		{
			name:      "unwrapped static SPIFFE client is unauthorized",
			grants:    ccGrant,
			client:    newClient(t, clientOpts{unwrapped: true}),
			form:      oneResource,
			wantError: fosite.ErrUnauthorizedClient,
		},
		{
			name:      "association grant is rejected",
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
