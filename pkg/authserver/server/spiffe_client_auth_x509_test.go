// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
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
	certificate, source := newTestX509SVID(t, spiffeID, func(template *x509.Certificate) {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	})
	resolvedClient := &fosite.DefaultClient{ID: "spiffe-client"}

	tests := []struct {
		name             string
		form             url.Values
		authorize        string
		certificate      *x509.Certificate
		source           x509bundle.Source
		resolve          SPIFFEClientResolver
		wantClient       fosite.Client
		wantErrIs        error
		wantResolverCall bool
	}{
		{
			name:        "valid X.509 SVID resolves associated client",
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
			name:      "missing client certificate fails closed",
			form:      url.Values{"client_id": {"spiffe-client"}},
			source:    source,
			wantErrIs: fosite.ErrInvalidClient,
		},
		{
			name:        "duplicated client ID fails closed",
			form:        url.Values{"client_id": {"spiffe-client", "spiffe-client"}},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidRequest,
		},
		{
			name:        "resolver failure fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			resolve: func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				return nil, errors.New("association not found")
			},
			wantErrIs:        fosite.ErrInvalidClient,
			wantResolverCall: true,
		},
		{
			name:        "resolver returns different client fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      source,
			resolve: func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				return &fosite.DefaultClient{ID: "other-client"}, nil
			},
			wantErrIs:        fosite.ErrInvalidClient,
			wantResolverCall: true,
		},
		{
			name:        "no bundle for the leaf's trust domain fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: certificate,
			source:      x509bundle.NewSet(),
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name:        "Basic authorization mixed with mTLS identity fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}},
			authorize:   "Basic credentials",
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name:        "client secret mixed with mTLS identity fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}, "client_secret": {""}},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name:        "client assertion mixed with mTLS identity fails closed",
			form:        url.Values{"client_id": {"spiffe-client"}, "client_assertion": {"header.payload.signature"}},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name: "client assertion type mixed with mTLS identity fails closed",
			form: url.Values{
				"client_id":             {"spiffe-client"},
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			},
			certificate: certificate,
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
		{
			name:        "malformed SPIFFE ID in leaf fails closed without default strategy",
			form:        url.Values{"client_id": {"spiffe-client"}},
			certificate: &x509.Certificate{URIs: []*url.URL{{Scheme: "spiffe", Host: "example.org", Path: "/a/../b"}}},
			source:      source,
			wantErrIs:   fosite.ErrInvalidClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolverCalled := false
			defaultCalled := false
			resolve := tt.resolve
			if resolve == nil {
				resolve = func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
					return nil, errors.New("unexpected resolver call")
				}
			}
			wrapped := func(ctx context.Context, identity, clientID string, method spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				resolverCalled = true
				return resolve(ctx, identity, clientID, method)
			}
			strategy := newSPIFFEClientAuthenticationStrategy(
				func(_ context.Context, _ *http.Request, _ url.Values) (fosite.Client, error) {
					defaultCalled = true
					return nil, errors.New("unexpected default strategy call")
				},
				testIssuer, nil, tt.source, wrapped, matchClientID("spiffe-client"),
			)
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			if tt.certificate != nil {
				req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tt.certificate}}
			}
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}

			client, err := strategy(context.Background(), req, tt.form)

			assert.Equal(t, tt.wantResolverCall, resolverCalled)
			assert.False(t, defaultCalled, "default strategy must not be called")
			if tt.wantErrIs != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErrIs)
				assert.Nil(t, client)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tt.wantClient, client)
		})
	}
}

func TestVerifySPIFFEX509(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	otherID := spiffeid.RequireFromString("spiffe://other.org/workload/my-service")
	digitalSignatureKeyUsage := mustMarshalASN1(t, asn1.BitString{Bytes: []byte{0x80}, BitLength: 1})
	nonCABasicConstraints := mustMarshalASN1(t, struct{}{})
	spiffeIDSubjectAltName := mustMarshalASN1(t, []asn1.RawValue{{
		Class: asn1.ClassContextSpecific,
		Tag:   6,
		Bytes: []byte(spiffeID.String()),
	}})
	now := time.Now()

	type outcome int
	const (
		wantOK outcome = iota
		// wantRequired expects errClientCertificateRequired.
		wantRequired
		// wantLeafErr expects errInvalidX509SVIDLeaf, which is returned before
		// the chain is verified, so the chain is not the rejection reason.
		wantLeafErr
		// wantChainErr expects a rejection from x509svid.Verify.
		wantChainErr
		// wantChainTooLong expects errClientChainTooLong.
		wantChainTooLong
	)
	type buildFunc func(t *testing.T) (*http.Request, x509bundle.Source)

	leaf := func(configure func(*x509.Certificate)) buildFunc {
		return func(t *testing.T) (*http.Request, x509bundle.Source) {
			t.Helper()
			certificate, source := newTestX509SVID(t, spiffeID, configure)
			return &http.Request{TLS: tlsStateWithLeaf(certificate)}, source
		}
	}
	withExtraExtension := func(extension pkix.Extension) func(*x509.Certificate) {
		return func(template *x509.Certificate) {
			template.ExtraExtensions = append(template.ExtraExtensions, extension)
		}
	}
	extKeyUsage := func(known []x509.ExtKeyUsage, unknown []asn1.ObjectIdentifier) buildFunc {
		return leaf(func(template *x509.Certificate) {
			template.ExtKeyUsage = known
			template.UnknownExtKeyUsage = unknown
		})
	}
	bothAuthEKUs := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	unknownEKU := []asn1.ObjectIdentifier{{1, 2, 3, 4}}

	withURIs := func(uris ...*url.URL) buildFunc {
		return leaf(func(template *x509.Certificate) {
			template.URIs = uris
		})
	}

	tests := []struct {
		name  string
		build buildFunc
		want  outcome
	}{
		{name: "valid required extensions", build: leaf(nil)},
		{
			name: "chain longer than the cap is rejected before verification",
			build: func(t *testing.T) (*http.Request, x509bundle.Source) {
				t.Helper()
				certificate, source := newTestX509SVID(t, spiffeID, nil)
				chain := make([]*x509.Certificate, maxClientCertificateChainLength+1)
				for i := range chain {
					chain[i] = certificate
				}
				return &http.Request{TLS: &tls.ConnectionState{PeerCertificates: chain}}, source
			},
			want: wantChainTooLong,
		},
		{
			name: "CA leaf passes the leaf profile and fails chain verification",
			build: leaf(func(template *x509.Certificate) {
				template.IsCA = true
				template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
			}),
			want: wantChainErr,
		},
		// The two rows below pin go-spiffe's single-URI-SAN rule through the whole verify path.
		{
			name: "two SPIFFE URI SANs",
			build: withURIs(spiffeID.URL(),
				&url.URL{Scheme: "spiffe", Host: "example.org", Path: "/workload/other"}),
			want: wantChainErr,
		},
		{
			name:  "non-SPIFFE URI SAN",
			build: withURIs(&url.URL{Scheme: "https", Host: "example.org", Path: "/workload/my-service"}),
			want:  wantChainErr,
		},
		{
			name: "key usage without digital signature",
			build: leaf(func(template *x509.Certificate) {
				template.KeyUsage = x509.KeyUsageKeyEncipherment
			}),
			want: wantLeafErr,
		},
		{
			name: "non-critical key usage",
			build: leaf(withExtraExtension(pkix.Extension{
				Id: oidExtensionKeyUsage, Critical: false, Value: digitalSignatureKeyUsage,
			})),
			want: wantLeafErr,
		},
		{
			name: "missing basic constraints",
			build: leaf(func(template *x509.Certificate) {
				template.BasicConstraintsValid = false
			}),
			want: wantLeafErr,
		},
		{
			name: "non-critical basic constraints with CA false",
			build: leaf(withExtraExtension(pkix.Extension{
				Id: oidExtensionBasicConstraints, Critical: false, Value: nonCABasicConstraints,
			})),
		},
		{
			name: "empty subject with critical SAN",
			build: leaf(func(template *x509.Certificate) {
				template.Subject = pkix.Name{}
			}),
		},
		{
			name: "empty subject with non-critical SAN",
			build: leaf(func(template *x509.Certificate) {
				template.Subject = pkix.Name{}
				withExtraExtension(pkix.Extension{
					Id: oidExtensionSubjectAltName, Critical: false, Value: spiffeIDSubjectAltName,
				})(template)
			}),
			want: wantLeafErr,
		},
		{
			name: "expired leaf",
			build: leaf(func(template *x509.Certificate) {
				template.NotBefore = now.Add(-2 * time.Hour)
				template.NotAfter = now.Add(-10 * time.Minute)
			}),
			want: wantChainErr,
		},
		{name: "absent EKU is accepted", build: extKeyUsage(nil, nil)},
		{name: "server and client auth EKUs are accepted", build: extKeyUsage(bothAuthEKUs, nil)},
		{
			name:  "client-auth-only EKU is rejected",
			build: extKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil),
			want:  wantLeafErr,
		},
		{
			name:  "server-auth-only EKU is rejected",
			build: extKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil),
			want:  wantLeafErr,
		},
		{name: "unknown-only EKU is rejected", build: extKeyUsage(nil, unknownEKU), want: wantLeafErr},
		{
			name:  "server and client auth plus an unknown EKU is accepted",
			build: extKeyUsage(bothAuthEKUs, unknownEKU),
		},
		{
			name: "nil TLS state",
			build: func(t *testing.T) (*http.Request, x509bundle.Source) {
				t.Helper()
				_, source := newTestX509SVID(t, spiffeID, nil)
				return &http.Request{}, source
			},
			want: wantRequired,
		},
		{
			name: "TLS state without peer certificates",
			build: func(t *testing.T) (*http.Request, x509bundle.Source) {
				t.Helper()
				_, source := newTestX509SVID(t, spiffeID, nil)
				return &http.Request{TLS: &tls.ConnectionState{}}, source
			},
			want: wantRequired,
		},
		{
			name: "nil bundle source",
			build: func(t *testing.T) (*http.Request, x509bundle.Source) {
				t.Helper()
				certificate, _ := newTestX509SVID(t, spiffeID, nil)
				return &http.Request{TLS: tlsStateWithLeaf(certificate)}, nil
			},
			want: wantRequired,
		},
		{
			name: "leaf chaining through a presented intermediate is accepted",
			build: func(t *testing.T) (*http.Request, x509bundle.Source) {
				t.Helper()
				chain, source := newTestX509SVIDChain(t, spiffeID)
				return &http.Request{TLS: &tls.ConnectionState{PeerCertificates: chain}}, source
			},
		},
		{
			name: "leaf in a trust domain without a bundle is rejected",
			build: leaf(func(template *x509.Certificate) {
				template.URIs = []*url.URL{otherID.URL()}
			}),
			want: wantChainErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request, source := tt.build(t)

			verifiedID, err := verifySPIFFEX509(request, source)

			switch tt.want {
			case wantRequired:
				require.ErrorIs(t, err, errClientCertificateRequired)
				assert.Equal(t, spiffeid.ID{}, verifiedID)
			case wantLeafErr:
				require.ErrorIs(t, err, errInvalidX509SVIDLeaf)
				assert.Equal(t, spiffeid.ID{}, verifiedID)
			case wantChainTooLong:
				require.ErrorIs(t, err, errClientChainTooLong)
				assert.Equal(t, spiffeid.ID{}, verifiedID)
			case wantChainErr:
				require.Error(t, err)
				assert.NotErrorIs(t, err, errInvalidX509SVIDLeaf)
				assert.NotErrorIs(t, err, errClientCertificateRequired)
				assert.Equal(t, spiffeid.ID{}, verifiedID)
			case wantOK:
				require.NoError(t, err)
				assert.Equal(t, spiffeID, verifiedID)
			}
		})
	}
}

// swappableX509BundleSource is an x509bundle.Source whose bundle set can be
// replaced between requests, standing in for a rotating source. It is not safe
// for concurrent use and is only for sequential tests.
type swappableX509BundleSource struct {
	source x509bundle.Source
}

func (s *swappableX509BundleSource) set(source x509bundle.Source) {
	s.source = source
}

func (s *swappableX509BundleSource) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	return s.source.GetX509BundleForTrustDomain(td)
}

func TestSPIFFEX509ClientAuthenticationFollowsBundleRotation(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	certificate, sourceA := newTestX509SVID(t, spiffeID, nil)
	_, sourceB := newTestX509SVID(t, spiffeID, nil)
	live := &swappableX509BundleSource{source: sourceA}

	resolverCalls := 0
	resolvedClient := &fosite.DefaultClient{ID: "spiffe-client"}
	strategy := newSPIFFEClientAuthenticationStrategy(
		func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
			t.Error("default strategy called")
			return nil, nil
		},
		testIssuer, nil, live,
		func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
			resolverCalls++
			return resolvedClient, nil
		},
		matchClientID("spiffe-client"),
	)
	authenticate := func() (fosite.Client, error) {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
		req.TLS = tlsStateWithLeaf(certificate)
		return strategy(context.Background(), req, url.Values{"client_id": {"spiffe-client"}})
	}

	client, err := authenticate()
	require.NoError(t, err)
	assert.Same(t, resolvedClient, client)
	assert.Equal(t, 1, resolverCalls)

	live.set(sourceB)
	client, err = authenticate()
	require.ErrorIs(t, err, fosite.ErrInvalidClient)
	assert.Nil(t, client)
	assert.Equal(t, 1, resolverCalls, "resolver must not run once the signing CA is no longer trusted")

	live.set(sourceA)
	client, err = authenticate()
	require.NoError(t, err)
	assert.Same(t, resolvedClient, client)
	assert.Equal(t, 2, resolverCalls)
}

// The no-SAN and malformed-subject branches cannot be reached through
// verifySPIFFEX509 with a parsed certificate: a SPIFFE ID requires a URI SAN
// extension and x509.ParseCertificate rejects a malformed subject. They guard
// hand-built certificates.
func TestValidSPIFFEX509RequiredExtensionsHandBuilt(t *testing.T) {
	t.Parallel()

	criticalKeyUsage := pkix.Extension{Id: oidExtensionKeyUsage, Critical: true}
	basicConstraints := pkix.Extension{Id: oidExtensionBasicConstraints}
	subjectAltName := pkix.Extension{Id: oidExtensionSubjectAltName, Critical: true}
	emptySubject := mustMarshalASN1(t, pkix.RDNSequence{})
	tests := []struct {
		name       string
		extensions []pkix.Extension
		rawSubject []byte
		want       bool
	}{
		{
			name:       "all required extensions with empty subject and critical SAN",
			extensions: []pkix.Extension{criticalKeyUsage, basicConstraints, subjectAltName},
			rawSubject: emptySubject,
			want:       true,
		},
		{
			name:       "subject alternative name extension absent",
			extensions: []pkix.Extension{criticalKeyUsage, basicConstraints},
			rawSubject: emptySubject,
		},
		{
			name:       "key usage extension absent",
			extensions: []pkix.Extension{basicConstraints, subjectAltName},
			rawSubject: emptySubject,
		},
		{
			name:       "malformed raw subject",
			extensions: []pkix.Extension{criticalKeyUsage, basicConstraints, subjectAltName},
			rawSubject: []byte{0xff, 0x01},
		},
		{
			name:       "raw subject with trailing bytes",
			extensions: []pkix.Extension{criticalKeyUsage, basicConstraints, subjectAltName},
			rawSubject: append(append([]byte{}, emptySubject...), 0x00),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			certificate := &x509.Certificate{
				Extensions:            tt.extensions,
				RawSubject:            tt.rawSubject,
				BasicConstraintsValid: true,
			}

			assert.Equal(t, tt.want, validSPIFFEX509RequiredExtensions(certificate) == nil)
		})
	}
}

// newTestX509SVIDChain returns a leaf and an intermediate CA, in presentation
// order, and a source that trusts only the root CA that signed the intermediate.
func newTestX509SVIDChain(t *testing.T, id spiffeid.ID) ([]*x509.Certificate, x509bundle.Source) {
	t.Helper()

	now := time.Now()
	newCA := func(serial int64, name string, maxPathLen int, parent *x509.Certificate, parentKey, key *ecdsa.PrivateKey) *x509.Certificate {
		template := &x509.Certificate{
			SerialNumber:          big.NewInt(serial),
			Subject:               pkix.Name{CommonName: name},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.Add(time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			MaxPathLen:            maxPathLen,
			MaxPathLenZero:        maxPathLen == 0,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		}
		if parent == nil {
			parent, parentKey = template, key
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
		require.NoError(t, err)
		certificate, err := x509.ParseCertificate(der)
		require.NoError(t, err)
		return certificate
	}

	rootKey, intermediateKey, leafKey := mustECDSAKey(t), mustECDSAKey(t), mustECDSAKey(t)
	root := newCA(1, "test root CA", 1, nil, nil, rootKey)
	intermediate := newCA(2, "test intermediate CA", 0, root, rootKey, intermediateKey)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "test workload"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{id.URL()},
	}, intermediate, &leafKey.PublicKey, intermediateKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	return []*x509.Certificate{leaf, intermediate},
		x509bundle.NewSet(x509bundle.FromX509Authorities(id.TrustDomain(), []*x509.Certificate{root}))
}

// newTestX509SVID builds a leaf certificate carrying id as its sole SPIFFE URI
// SAN, signed by a freshly generated CA, and the x509bundle.Source trusting
// that CA for id's trust domain. The leaf explicitly contains the required
// key-usage and CA=false basic-constraints extensions and no extended key
// usage. configure, if non-nil, mutates the leaf template before signing so
// tests exercise the actual DER extension representation.
func newTestX509SVID(
	t *testing.T, id spiffeid.ID, configure func(*x509.Certificate),
) (*x509.Certificate, x509bundle.Source) {
	t.Helper()

	caKey := mustECDSAKey(t)
	leafKey := mustECDSAKey(t)
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
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test workload"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{id.URL()},
	}
	if configure != nil {
		configure(leafTemplate)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	return leaf, x509bundle.NewSet(x509bundle.FromX509Authorities(id.TrustDomain(), []*x509.Certificate{ca}))
}

func mustMarshalASN1(t *testing.T, value any) []byte {
	t.Helper()

	der, err := asn1.Marshal(value)
	require.NoError(t, err)
	return der
}
