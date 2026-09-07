// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// maxSPIFFEJWTAssertionRemainingValidity permits the recommended five-minute
// JWT-SVID lifetime plus the one-minute clock skew tolerated by go-spiffe.
const maxSPIFFEJWTAssertionRemainingValidity = 6 * time.Minute

var (
	errClientCertificateRequired = errors.New("client certificate is required")
	errInvalidX509SVIDLeaf       = errors.New("invalid X.509-SVID leaf")

	oidExtensionKeyUsage         = asn1.ObjectIdentifier{2, 5, 29, 15}
	oidExtensionSubjectAltName   = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidExtensionBasicConstraints = asn1.ObjectIdentifier{2, 5, 29, 19}
)

// SPIFFEClientResolver resolves a verified SPIFFE identity to its configured
// OAuth client. It is the seam that lets the client-authentication strategy
// here reach the association registry and storage constructed in package
// authserver, which this package cannot import (authserver imports server).
// clientID is an optional exact selector; an empty value asks the resolver to
// derive the configured client from the verified SPIFFE identity.
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
		// An ambient X.509-SVID and a SPIFFE JWT-SVID assertion are distinct
		// credentials. Reject the combination before either authentication path
		// can run, including when no SPIFFE resolver is configured.
		if _, ok := spiffeauth.SPIFFEIDFromContext(ctx); ok &&
			slices.Contains(form["client_assertion_type"], spiffeauth.SPIFFEJWTAssertionType) {
			return nil, fosite.ErrInvalidClient
		}
		// Without a SPIFFE resolver, requests that do not combine an ambient
		// X.509-SVID with a SPIFFE JWT-SVID assertion go to the default strategy
		// untouched. This preserves shared-dispatcher behavior for entirely
		// non-SPIFFE requests (e.g. RFC 7523 private-key JWT).
		if resolver == nil {
			return defaultStrategy(ctx, r, form)
		}
		// In the absence of an ambient X.509 identity, an explicit SPIFFE assertion
		// type selects JWT authentication. A repeated form key must be checked in
		// full: form.Get would only see the first value, letting a SPIFFE assertion
		// type hidden behind an earlier value slip through to the default strategy.
		// A duplicated client_assertion_type is rejected as ambiguous by
		// authenticateSPIFFEJWTClient itself, once SPIFFE is actually selected.
		if slices.Contains(form["client_assertion_type"], spiffeauth.SPIFFEJWTAssertionType) {
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
// identity with another client credential (HTTP Basic, a client secret, or a
// client assertion form field). SPIFFE X.509 authentication must be the sole
// credential in the request, for the same reason as its JWT sibling: allowing
// a second credential to also be present would let an attacker probe which the
// server accepts.
func rejectedSPIFFEX509Request(r *http.Request, form url.Values) bool {
	return hasBasicAuthorization(r) || form.Has("client_secret") ||
		form.Has("client_assertion") || form.Has("client_assertion_type")
}

// verifySPIFFEX509 re-derives and verifies the SPIFFE ID from the request's
// TLS peer certificate chain. It retains ToolHive's stricter leaf profile: the
// leaf must contain a critical key-usage extension with digital signature (and
// neither certificate- nor CRL-signing usage), a basic-constraints extension
// marking it as a non-CA, and a subject-alternative-name extension. The SAN must
// be critical when the subject is empty. The leaf must also have a non-root,
// canonical SPIFFE ID path. If an extended-key-usage extension is present, it
// must include both server and client authentication. The pinned go-spiffe
// verifier validates the chain against the bundle selected from the leaf's trust
// domain. A missing or malformed certificate is always rejected; there is no
// fallback to public or secret-based client authentication.
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
		leaf.KeyUsage&(x509.KeyUsageCertSign|x509.KeyUsageCRLSign) != 0 ||
		!validSPIFFEX509RequiredExtensions(leaf) ||
		!validSPIFFEX509ExtendedKeyUsage(leaf) {
		return spiffeid.ID{}, errInvalidX509SVIDLeaf
	}
	verifiedID, _, err := x509svid.Verify(r.TLS.PeerCertificates, source)
	if err != nil {
		return spiffeid.ID{}, err
	}
	if verifiedID != id {
		return spiffeid.ID{}, errInvalidX509SVIDLeaf
	}
	return id, nil
}

func validSPIFFEX509RequiredExtensions(certificate *x509.Certificate) bool {
	keyUsage, ok := certificateExtension(certificate, oidExtensionKeyUsage)
	if !ok || !keyUsage.Critical {
		return false
	}
	if _, ok := certificateExtension(certificate, oidExtensionBasicConstraints); !ok ||
		!certificate.BasicConstraintsValid || certificate.IsCA {
		return false
	}
	subjectAltName, ok := certificateExtension(certificate, oidExtensionSubjectAltName)
	if !ok {
		return false
	}

	var subject pkix.RDNSequence
	rest, err := asn1.Unmarshal(certificate.RawSubject, &subject)
	if err != nil || len(rest) != 0 {
		return false
	}
	return len(subject) != 0 || subjectAltName.Critical
}

func certificateExtension(certificate *x509.Certificate, oid asn1.ObjectIdentifier) (pkix.Extension, bool) {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(oid) {
			return extension, true
		}
	}
	return pkix.Extension{}, false
}

func validSPIFFEX509ExtendedKeyUsage(certificate *x509.Certificate) bool {
	if len(certificate.ExtKeyUsage) == 0 && len(certificate.UnknownExtKeyUsage) == 0 {
		return true
	}
	return slices.Contains(certificate.ExtKeyUsage, x509.ExtKeyUsageServerAuth) &&
		slices.Contains(certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
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
	clientID, ok := optionalExactNonEmptyFormValue(form, "client_id")
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
	if err != nil || !validSPIFFEJWTIdentityClaims(svid, issuer) {
		return nil, fosite.ErrInvalidClient
	}
	if svid.Expiry.After(time.Now().Add(maxSPIFFEJWTAssertionRemainingValidity)) {
		return nil, fosite.ErrInvalidClient
	}
	client, err := resolver(ctx, svid.ID.String(), clientID, spiffeauth.SPIFFEAuthenticationMethodJWT)
	if !validResolvedSPIFFEClient(client, err, clientID) {
		return nil, fosite.ErrInvalidClient
	}
	return client, nil
}

// validSPIFFEJWTIdentityClaims reports whether svid carries exactly the AS as
// its sole audience. It does not require an `iss` claim: RFC 7519 states iss
// is OPTIONAL, and the SPIFFE JWT-SVID spec does not mandate it either — a
// conformant SVID signed by a bundle-trusted key can omit it entirely.
func validSPIFFEJWTIdentityClaims(svid *jwtsvid.SVID, audience string) bool {
	return svid != nil && len(svid.Audience) == 1 && svid.Audience[0] == audience
}

func validResolvedSPIFFEClient(client fosite.Client, err error, requestedClientID string) bool {
	return err == nil && client != nil && (requestedClientID == "" || client.GetID() == requestedClientID)
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

// optionalExactNonEmptyFormValue returns an absent field as an empty value.
// A supplied field must contain exactly one non-whitespace value, which is
// returned without normalization.
func optionalExactNonEmptyFormValue(form url.Values, key string) (string, bool) {
	if !form.Has(key) {
		return "", true
	}
	return exactNonEmptyFormValue(form, key)
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
