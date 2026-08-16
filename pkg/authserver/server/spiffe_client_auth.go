// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

var (
	errClientCertificateRequired = errors.New("client certificate is required")
	errInvalidX509SVIDLeaf       = errors.New("invalid X.509-SVID leaf")
)

// SPIFFEClientResolver resolves a verified SPIFFE identity to its configured
// OAuth client. It is the seam that lets the client-authentication strategy
// here reach the association registry and storage constructed in package
// authserver, which this package cannot import (authserver imports server).
// One signature covers both X.509 and JWT credentials, with method as an
// explicit discriminator, so the two arms share a single resolution path
// instead of each inventing its own. spiffeID is passed explicitly rather
// than pulled from ctx, so an identity resolved from the wrong source cannot
// be mistaken for a verified one.
type SPIFFEClientResolver func(
	ctx context.Context, spiffeID, clientID string, method spiffeauth.SPIFFEAuthenticationMethod,
) (fosite.Client, error)

func newSPIFFEClientAuthenticationStrategy(
	defaultStrategy fosite.ClientAuthenticationStrategy,
	issuer string,
	jwtBundleSource jwtbundle.Source,
	x509BundleSource x509bundle.Source,
	resolver SPIFFEClientResolver,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		// No SPIFFE trust configured: this server genuinely does not do SPIFFE,
		// so neither arm applies and every request goes to the default strategy.
		if resolver == nil {
			return defaultStrategy(ctx, r, form)
		}
		// An explicit assertion type takes precedence over an ambient mTLS identity.
		if form.Get("client_assertion_type") == spiffeauth.SPIFFEJWTAssertionType {
			return authenticateSPIFFEJWTClient(ctx, r, form, issuer, jwtBundleSource, resolver)
		}
		if claimedID, ok := spiffeauth.SPIFFEIDFromContext(ctx); ok {
			return authenticateSPIFFEX509Client(ctx, r, form, claimedID, x509BundleSource, resolver)
		}
		return defaultStrategy(ctx, r, form)
	}
}

// authenticateSPIFFEX509Client re-verifies the client certificate presented on
// the mutually authenticated connection against the configured trust-domain
// bundle and resolves the result to its configured OAuth client. claimedID is
// the identity the connection-level middleware already extracted from the
// leaf certificate; it is compared against the freshly re-verified identity
// so a claim that outlives its certificate's chain of trust cannot be reused.
// It fails closed on any malformed request field, missing or invalid
// certificate, or resolver rejection, and never logs the certificate.
func authenticateSPIFFEX509Client(
	ctx context.Context,
	r *http.Request,
	form url.Values,
	claimedID spiffeid.ID,
	source x509bundle.Source,
	resolver SPIFFEClientResolver,
) (fosite.Client, error) {
	clientID, ok := exactNonEmptyFormValue(form, "client_id")
	if !ok {
		return nil, fosite.ErrInvalidRequest
	}
	if rejectedSPIFFEX509Request(r, form) {
		return nil, fosite.ErrInvalidClient
	}
	verifiedID, err := verifySPIFFEX509(r, source)
	if err != nil || verifiedID != claimedID {
		return nil, fosite.ErrInvalidClient.WithHint("SPIFFE X.509 client authentication failed")
	}
	client, err := resolver(ctx, verifiedID.String(), clientID, spiffeauth.SPIFFEAuthenticationMethodX509)
	if err != nil || client == nil || client.GetID() != clientID {
		return nil, fosite.ErrInvalidClient.WithHint("SPIFFE X.509 client authentication failed")
	}
	return client, nil
}

// rejectedSPIFFEX509Request reports whether a request mixes the ambient mTLS
// identity with another client credential (HTTP Basic or a client secret form
// field). SPIFFE X.509 authentication must be the sole credential in the
// request, for the same reason as its JWT sibling: allowing a second
// credential to also be present would let an attacker probe which the server
// accepts.
func rejectedSPIFFEX509Request(r *http.Request, form url.Values) bool {
	return hasBasicAuthorization(r) || form.Has("client_secret")
}

// verifySPIFFEX509 re-derives and verifies the SPIFFE ID from the request's
// verified TLS peer certificate. It checks the certificate shape explicitly
// rather than trusting the connection alone: the leaf must not be a CA, must
// carry the digital-signature key usage (and neither certificate- nor
// CRL-signing usage), and its chain must validate against the trust domain's
// configured bundle for client authentication. A missing or malformed
// certificate is always rejected; there is no fallback to public or
// secret-based client authentication.
func verifySPIFFEX509(r *http.Request, source x509bundle.Source) (spiffeid.ID, error) {
	if source == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return spiffeid.ID{}, errClientCertificateRequired
	}
	leaf := r.TLS.PeerCertificates[0]
	id, err := spiffeauth.SPIFFEIDFromCertificate(leaf)
	if err != nil {
		return spiffeid.ID{}, err
	}
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		leaf.KeyUsage&(x509.KeyUsageCertSign|x509.KeyUsageCRLSign) != 0 {
		return spiffeid.ID{}, errInvalidX509SVIDLeaf
	}
	bundle, err := source.GetX509BundleForTrustDomain(id.TrustDomain())
	if err != nil {
		return spiffeid.ID{}, err
	}
	roots := x509.NewCertPool()
	for _, authority := range bundle.X509Authorities() {
		roots.AddCert(authority)
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range r.TLS.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return spiffeid.ID{}, err
	}
	return id, nil
}

// authenticateSPIFFEJWTClient validates a SPIFFE JWT-SVID client assertion and
// resolves it to its configured OAuth client. It fails closed on any
// malformed request field, verification failure, or resolver rejection, and
// never logs the assertion or its claims.
func authenticateSPIFFEJWTClient(
	ctx context.Context,
	r *http.Request,
	form url.Values,
	issuer string,
	jwtBundleSource jwtbundle.Source,
	resolver SPIFFEClientResolver,
) (fosite.Client, error) {
	assertionType, ok := exactNonEmptyFormValue(form, "client_assertion_type")
	if !ok || assertionType != spiffeauth.SPIFFEJWTAssertionType {
		return nil, fosite.ErrInvalidRequest
	}
	assertion, ok := exactNonEmptyFormValue(form, "client_assertion")
	if !ok {
		return nil, fosite.ErrInvalidRequest
	}
	clientID, ok := exactNonEmptyFormValue(form, "client_id")
	if !ok {
		return nil, fosite.ErrInvalidRequest
	}
	if rejectedSPIFFEJWTRequest(r, form, assertion) {
		return nil, fosite.ErrInvalidClient
	}
	if jwtBundleSource == nil {
		return nil, fosite.ErrInvalidClient
	}

	svid, err := jwtsvid.ParseAndValidate(assertion, jwtBundleSource, []string{issuer})
	if err != nil || svid == nil || len(svid.Audience) != 1 || svid.Audience[0] != issuer {
		return nil, fosite.ErrInvalidClient
	}
	client, err := resolver(ctx, svid.ID.String(), clientID, spiffeauth.SPIFFEAuthenticationMethodJWT)
	if err != nil || client == nil || client.GetID() != clientID {
		return nil, fosite.ErrInvalidClient
	}
	return client, nil
}

// exactNonEmptyFormValue returns the sole value for key, rejecting an absent,
// duplicated, or whitespace-only field. Duplicated fields are rejected rather
// than taking the first or last value, since either choice could be steered
// by an attacker who controls only one of the duplicates.
func exactNonEmptyFormValue(form url.Values, key string) (string, bool) {
	values, ok := form[key]
	if !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return values[0], true
}

// rejectedSPIFFEJWTRequest reports whether a request mixes SPIFFE JWT
// authentication with another client credential (HTTP Basic or a client
// secret form field), or carries an oversized assertion. SPIFFE JWT
// authentication must be the sole credential in the request; allowing a
// second one to also be present would let an attacker probe which the
// server accepts.
func rejectedSPIFFEJWTRequest(r *http.Request, form url.Values, assertion string) bool {
	return hasBasicAuthorization(r) || form.Has("client_secret") || len(assertion) > 16*1024
}

func hasBasicAuthorization(r *http.Request) bool {
	for _, authorization := range r.Header.Values("Authorization") {
		fields := strings.Fields(authorization)
		if len(fields) > 0 && strings.EqualFold(fields[0], "Basic") {
			return true
		}
	}
	return false
}
