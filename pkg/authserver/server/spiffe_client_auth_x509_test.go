// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

func TestSPIFFEX509ClientAuthentication(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	certificate, source := newTestX509SVID(t, spiffeID)
	serverAuthOnlyCertificate, serverAuthOnlySource := newTestX509SVID(t, spiffeID, x509.ExtKeyUsageServerAuth)
	resolvedClient := &fosite.DefaultClient{ID: "spiffe-client"}

	tests := []struct {
		name             string
		ctx              context.Context
		form             url.Values
		authorize        string
		certificate      *x509.Certificate
		source           x509bundle.Source
		resolve          SPIFFEClientResolver
		wantClient       fosite.Client
		wantErrIs        error
		wantHint         string
		wantResolverCall bool
	}{
		{
			name:        "valid X.509 SVID resolves associated client",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			resolve: func(_ context.Context, identity, clientID string, method spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				assert.Equal(t, spiffeID.String(), identity)
				assert.Equal(t, "spiffe-client", clientID)
				assert.Equal(t, spiffeauth.SPIFFEAuthenticationMethodX509, method)
				return resolvedClient, nil
			},
			wantClient:       resolvedClient,
			wantResolverCall: true,
		},
		{
			name:        "server-auth-only X.509 SVID fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: serverAuthOnlyCertificate,
			source:      serverAuthOnlySource,
			wantErrIs:   fosite.ErrInvalidClient,
			wantHint:    "SPIFFE X.509 client authentication failed",
		},
		{
			name: "context identity mismatch fails closed",
			ctx: spiffeauth.ContextWithSPIFFEID(
				context.Background(), spiffeid.RequireFromString("spiffe://example.org/workload/other"),
			),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
			wantHint:    "SPIFFE X.509 client authentication failed",
		},
		{
			name:        "missing explicit client ID fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidRequest,
		},
		{
			name:        "resolver failure fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			resolve: func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				return nil, errors.New("association not found")
			},
			wantErrIs:        fosite.ErrInvalidClient,
			wantHint:         "SPIFFE X.509 client authentication failed",
			wantResolverCall: true,
		},
		{
			name:        "resolver returns different client fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			resolve: func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				return &fosite.DefaultClient{ID: "other-client"}, nil
			},
			wantErrIs:        fosite.ErrInvalidClient,
			wantHint:         "SPIFFE X.509 client authentication failed",
			wantResolverCall: true,
		},
		{
			name:        "invalid certificate chain fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      x509bundle.NewSet(),
			wantErrIs:   fosite.ErrInvalidClient,
			wantHint:    "SPIFFE X.509 client authentication failed",
		},
		{
			name:        "missing bundle source fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			wantErrIs:   fosite.ErrInvalidClient,
			wantHint:    "SPIFFE X.509 client authentication failed",
		},
		{
			name:      "no certificate on connection fails closed",
			ctx:       spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:      url.Values{"client_id": {"spiffe-client"}},
			source:    source,
			wantErrIs: fosite.ErrInvalidClient,
			wantHint:  "SPIFFE X.509 client authentication failed",
		},
		{
			name:        "Basic authorization mixed with mTLS identity fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}},
			authorize:   "Basic credentials",
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name:        "client secret mixed with mTLS identity fails closed",
			ctx:         spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form:        url.Values{"client_id": {"spiffe-client"}, "client_secret": {""}},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolverCalled := false
			resolve := tt.resolve
			if resolve == nil {
				resolve = func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
					t.Fatal("resolver called")
					return nil, nil
				}
			}
			wrapped := func(ctx context.Context, identity, clientID string, method spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				resolverCalled = true
				return resolve(ctx, identity, clientID, method)
			}
			strategy := newSPIFFEClientAuthenticationStrategy(
				func(_ context.Context, _ *http.Request, _ url.Values) (fosite.Client, error) {
					t.Fatal("default strategy called")
					return nil, nil
				},
				testIssuer, nil, tt.source, wrapped,
			)
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil).WithContext(tt.ctx)
			if tt.certificate != nil {
				req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tt.certificate}}
			}
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}

			client, err := strategy(tt.ctx, req, tt.form)

			assert.Equal(t, tt.wantResolverCall, resolverCalled)
			if tt.wantErrIs != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErrIs)
				if tt.wantHint != "" {
					var rfcErr *fosite.RFC6749Error
					require.ErrorAs(t, err, &rfcErr)
					assert.Equal(t, tt.wantHint, rfcErr.HintField)
				}
				assert.Nil(t, client)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tt.wantClient, client)
		})
	}
}

// newTestX509SVID builds a leaf certificate carrying id as its sole SPIFFE URI
// SAN, signed by a freshly generated CA, and the x509bundle.Source trusting
// that CA for id's trust domain. keyUsages defaults to client auth so callers
// exercising the happy path do not need to spell it out.
func newTestX509SVID(t *testing.T, id spiffeid.ID, keyUsages ...x509.ExtKeyUsage) (*x509.Certificate, x509bundle.Source) {
	t.Helper()

	if len(keyUsages) == 0 {
		keyUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test workload"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  keyUsages,
		URIs:         []*url.URL{id.URL()},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	return leaf, x509bundle.NewSet(x509bundle.FromX509Authorities(id.TrustDomain(), []*x509.Certificate{ca}))
}
