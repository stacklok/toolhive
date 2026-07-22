// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	envoycore "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoytls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoyauth "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	envoydiscovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func checkRequest(host, method, path string) *envoyauth.CheckRequest {
	return &envoyauth.CheckRequest{
		Attributes: &envoyauth.AttributeContext{
			Request: &envoyauth.AttributeContext_Request{
				Http: &envoyauth.AttributeContext_HttpRequest{
					Method: method,
					Path:   path,
					Host:   host,
				},
			},
		},
	}
}

func mustAuthzServer(t *testing.T, reader upstreamtoken.TokenReader) *egressbroker.AuthorizationServer {
	t.Helper()
	inj := mustInjector(t, reader)
	srv, err := egressbroker.NewAuthorizationServer(inj)
	require.NoError(t, err)
	return srv
}

func TestAuthorizationServerCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("allowlisted request → OK with Authorization header set", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{
				"github": {AccessToken: "gho_secret"},
			}, nil, nil)

		resp, err := mustAuthzServer(t, reader).Check(ctx,
			checkRequest("api.github.com:443", "GET", "/repos/foo"))
		require.NoError(t, err)
		assert.Equal(t, int32(code.Code_OK), resp.GetStatus().GetCode())

		ok := resp.GetOkResponse()
		require.NotNil(t, ok)
		require.Len(t, ok.GetHeaders(), 1)
		assert.Equal(t, egressbroker.AuthorizationHeader, ok.GetHeaders()[0].GetHeader().GetKey())
		assert.Equal(t, "Bearer gho_secret", ok.GetHeaders()[0].GetHeader().GetValue())
		// The response must not leak the token anywhere else.
		assert.Empty(t, ok.GetResponseHeadersToAdd())
		assert.Nil(t, resp.GetDeniedResponse())
	})

	t.Run("non-allowlisted host → deny, no header mutation", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		// Token reader never consulted (D5 ordering proven at injector level).

		resp, err := mustAuthzServer(t, reader).Check(ctx,
			checkRequest("evil.example.com:443", "GET", "/"))
		require.NoError(t, err)
		assert.Equal(t, int32(code.Code_PERMISSION_DENIED), resp.GetStatus().GetCode())
		assert.Nil(t, resp.GetOkResponse(), "deny must never carry header material")
		require.NotNil(t, resp.GetDeniedResponse())
	})

	t.Run("credential unavailable → deny", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{}, nil, nil)

		resp, err := mustAuthzServer(t, reader).Check(ctx,
			checkRequest("api.github.com", "GET", "/repos/foo"))
		require.NoError(t, err)
		assert.Equal(t, int32(code.Code_PERMISSION_DENIED), resp.GetStatus().GetCode())
		assert.Nil(t, resp.GetOkResponse())
	})

	t.Run("missing destination → deny (fail closed)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)

		for name, req := range map[string]*envoyauth.CheckRequest{
			"no http attributes": {},
			"empty host":         checkRequest("", "GET", "/"),
			"empty method":       checkRequest("api.github.com", "", "/"),
		} {
			resp, err := mustAuthzServer(t, reader).Check(ctx, req)
			require.NoError(t, err, name)
			assert.Equal(t, int32(code.Code_INVALID_ARGUMENT), resp.GetStatus().GetCode(), name)
			assert.Nil(t, resp.GetOkResponse(), name)
		}
	})

	t.Run("deny body carries the reason, never credential material", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{}, []string{"github"}, nil)

		resp, err := mustAuthzServer(t, reader).Check(ctx,
			checkRequest("api.github.com", "GET", "/repos/foo"))
		require.NoError(t, err)
		denied := resp.GetDeniedResponse()
		require.NotNil(t, denied)
		assert.Contains(t, denied.GetBody(), "re-consent")
		assert.NotContains(t, denied.GetBody(), "gho_")
	})
}

func mustSDSServer(t *testing.T) (*egressbroker.SecretDiscoveryServer, *egressbroker.BumpCA) {
	t.Helper()
	ca, err := egressbroker.GenerateBumpCA("sds-test", time.Now())
	require.NoError(t, err)
	srv, err := egressbroker.NewSecretDiscoveryServer(ca, mustParse(t, testPolicyYAML))
	require.NoError(t, err)
	return srv, ca
}

func TestSecretDiscoveryServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("mints a verifiable bump cert for an allowlisted host", func(t *testing.T) {
		t.Parallel()
		srv, ca := mustSDSServer(t)
		resp, err := srv.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"host:api.github.com"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResources(), 1)

		var secret envoytls.Secret
		require.NoError(t, resp.GetResources()[0].UnmarshalTo(&secret))
		certPEM := secret.GetTlsCertificate().GetCertificateChain().GetInlineBytes()
		keyPEM := secret.GetTlsCertificate().GetPrivateKey().GetInlineBytes()
		require.NotEmpty(t, certPEM)
		require.NotEmpty(t, keyPEM)

		block, _ := pem.Decode(certPEM)
		require.NotNil(t, block)
		leaf, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		caBlock, _ := pem.Decode(ca.CertPEM())
		require.NotNil(t, caBlock)
		caCert, err := x509.ParseCertificate(caBlock.Bytes)
		require.NoError(t, err)
		roots := x509.NewCertPool()
		roots.AddCert(caCert)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "api.github.com"})
		require.NoError(t, err, "SDS-served cert must verify against the tenant bump CA")
	})

	t.Run("refuses non-allowlisted host (fail closed, no cert)", func(t *testing.T) {
		t.Parallel()
		srv, _ := mustSDSServer(t)
		_, err := srv.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"host:evil.example.com"},
		})
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("refuses unknown resource names and empty requests", func(t *testing.T) {
		t.Parallel()
		srv, _ := mustSDSServer(t)
		_, err := srv.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"ca-key"},
		})
		require.Error(t, err, "only host: resources are servable; the CA key is never served")
		_, err = srv.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("wildcard host pattern matches subdomain cert requests", func(t *testing.T) {
		t.Parallel()
		srv, _ := mustSDSServer(t)
		resp, err := srv.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"host:raw.githubusercontent.com"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResources(), 1)
	})

	t.Run("constructor validation", func(t *testing.T) {
		t.Parallel()
		policy := mustParse(t, testPolicyYAML)
		ca, err := egressbroker.GenerateBumpCA("x", time.Now())
		require.NoError(t, err)
		_, err = egressbroker.NewSecretDiscoveryServer(nil, policy)
		require.Error(t, err)
		_, err = egressbroker.NewSecretDiscoveryServer(ca, nil)
		require.Error(t, err)
	})

	var _ = envoycore.HeaderValue{} // keep import used if assertions change
}
