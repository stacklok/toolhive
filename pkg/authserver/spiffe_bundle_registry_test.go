// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/federation"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

type spiffeBundleRegistryRoundTripper func(*http.Request) (*http.Response, error)

func (f spiffeBundleRegistryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewSPIFFEBundleRegistry(t *testing.T) {
	t.Parallel()

	registry, err := NewSPIFFEBundleRegistry(nil)
	require.NoError(t, err)
	require.Nil(t, registry)

	registry, err = NewSPIFFEBundleRegistry(&SPIFFETrustConfig{})
	require.Nil(t, registry)
	require.ErrorContains(t, err, "must be constructed")
}

func TestSPIFFEBundleRegistryServesOnlyDeclaredDomains(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	registry := newTestSPIFFEBundleRegistry(t, trustDomain, []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509})
	endpoint := registry.endpointSources[0]
	endpoint.bundle.Store(testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "x509", ""))
	endpoint.lifecycleMu.Lock()
	endpoint.lastSuccess = time.Now()
	endpoint.lifecycleMu.Unlock()
	registry.started = true

	x509Bundle, err := registry.GetX509BundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	require.Equal(t, trustDomain, x509Bundle.TrustDomain())
	_, err = registry.GetJWTBundleForTrustDomain(trustDomain)
	require.ErrorContains(t, err, "not enabled")

	undeclared := spiffeid.RequireTrustDomainFromString("undeclared.example.org")
	_, err = registry.GetX509BundleForTrustDomain(undeclared)
	require.ErrorContains(t, err, "no SPIFFE bundle configured")
	_, err = registry.GetJWTBundleForTrustDomain(undeclared)
	require.ErrorContains(t, err, "no SPIFFE bundle configured")

	require.NoError(t, registry.Close())
	require.NoError(t, registry.Close())
}

func TestSPIFFEBundleRegistryStartReadinessAndCleanup(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	tests := []struct {
		name    string
		methods []SPIFFEAuthenticationMethod
		bundle  *spiffebundle.Bundle
		wantErr string
	}{
		{
			name:    "X.509 only with X.509 authority",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			bundle:  testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "x509", ""),
		},
		{
			name:    "JWT only with JWT authority",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
			bundle:  testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "", "jwt"),
		},
		{
			name:    "X.509 enabled without X.509 authority",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			bundle:  testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "", "jwt"),
			wantErr: "no X.509 authorities",
		},
		{
			name:    "JWT enabled without JWT authority",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
			bundle:  testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "x509", ""),
			wantErr: "no JWT authorities",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			served := &testSPIFFEBundleSource{bundle: tt.bundle}
			handler, err := federation.NewHandler(trustDomain, served)
			require.NoError(t, err)
			server := httptest.NewTLSServer(handler)
			t.Cleanup(server.Close)

			registry := newTestSPIFFEBundleRegistry(t, trustDomain, tt.methods)
			registry.endpointSources[0].endpoint.Scheme = "https"
			registry.endpointSources[0].endpoint.Host = server.Listener.Addr().String()
			registry.endpointSources[0].client = server.Client()
			err = registry.Start(context.Background())
			if tt.wantErr == "" {
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, registry.Close()) })
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			_, err = registry.GetX509BundleForTrustDomain(trustDomain)
			require.ErrorContains(t, err, "not started")
			require.NoError(t, registry.Close())
		})
	}
}

func TestSPIFFEBundleRegistryFailedStartRemainsNotStarted(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	failed := newTestSPIFFEBundleRegistry(t, trustDomain, []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509})
	failed.endpointSources[0].client = &http.Client{Transport: spiffeBundleRegistryRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unavailable")
	})}
	err := failed.Start(context.Background())
	require.ErrorContains(t, err, "fetch SPIFFE bundle endpoint")
	_, err = failed.GetX509BundleForTrustDomain(trustDomain)
	require.ErrorContains(t, err, "not started")
	require.NoError(t, failed.Close())
}

func newTestSPIFFEBundleRegistry(
	t *testing.T,
	trustDomain spiffeid.TrustDomain,
	methods []SPIFFEAuthenticationMethod,
) *SPIFFEBundleRegistry {
	t.Helper()
	trust, err := NewSPIFFETrustConfig(
		[]SPIFFETrustDomainRunConfig{{
			Name:        "test",
			TrustDomain: trustDomain.String(),
			Methods:     methods,
			BundleSource: SPIFFEBundleSourceRunConfig{
				Type:     SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundles.example.org"},
			},
		}},
		&InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "test",
			Principal:      "spiffe://example.org/ns/default/workload",
			ClientID:       "client",
			Methods:        methods,
			Resources:      []string{"https://mcp.example.org"},
			Audiences:      []string{"mcp"},
			Scopes:         []string{"openid"},
			GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
			TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
		}}},
		[]string{"openid"}, []string{"https://mcp.example.org", "mcp"},
	)
	require.NoError(t, err)
	registry, err := NewSPIFFEBundleRegistry(trust)
	require.NoError(t, err)
	return registry
}
