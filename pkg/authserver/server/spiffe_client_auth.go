// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"log/slog"
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

// maxClientCertificateChainLength bounds the work go-spiffe and Go's chain
// builder do on a client-supplied chain, whose intermediates are used before
// any signature is trusted. A leaf plus three intermediates covers nested SPIRE.
const maxClientCertificateChainLength = 4

var (
	errClientCertificateRequired = errors.New("client certificate is required")
	errInvalidX509SVIDLeaf       = errors.New("invalid X.509-SVID leaf")
	errClientChainTooLong        = errors.New("client certificate chain is too long")

	oidExtensionKeyUsage         = asn1.ObjectIdentifier{2, 5, 29, 15}
	oidExtensionSubjectAltName   = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidExtensionBasicConstraints = asn1.ObjectIdentifier{2, 5, 29, 19}
)

// SPIFFEClientResolver resolves a verified SPIFFE identity to its configured
// OAuth client. It is the seam that lets the client-authentication strategy
// here reach the association registry and storage constructed in package
// authserver, which this package cannot import (authserver imports server).
// clientID is an exact selector. The X.509 arm always passes the request's
// client_id, which it requires. Only the JWT arm may pass an empty clientID,
// asking the resolver to derive the configured client from the verified
// SPIFFE identity.
// One signature covers both X.509 and JWT credentials, with method as an
// explicit discriminator, so the two arms share a single resolution path
// instead of each inventing its own. spiffeID is passed explicitly rather
// than pulled from ctx, so an identity resolved from the wrong source cannot
// be mistaken for a verified one.
type SPIFFEClientResolver func(
	ctx context.Context, spiffeID, clientID string, method spiffeauth.SPIFFEAuthenticationMethod,
) (fosite.Client, error)

// SPIFFEX509ClientMatcher reports whether clientID names an association that
// enables spiffe_x509. This package cannot import the association registry.
type SPIFFEX509ClientMatcher func(clientID string) bool

// newSPIFFEClientAuthenticationStrategy picks one verifier per request, never
// by trial: a SPIFFE JWT assertion type selects the JWT arm (the certificate is
// ignored); otherwise a client_id enabled for spiffe_x509 selects the X.509 arm,
// which never falls back; otherwise the default strategy runs and a peer
// certificate is transport identity only. Without a resolver everything goes to
// the default strategy.
func newSPIFFEClientAuthenticationStrategy(
	defaultStrategy fosite.ClientAuthenticationStrategy,
	issuer string,
	jwtBundleSource jwtbundle.Source,
	x509BundleSource x509bundle.Source,
	resolver SPIFFEClientResolver,
	x509Client SPIFFEX509ClientMatcher,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		if resolver == nil {
			return defaultStrategy(ctx, r, form)
		}
		// Check every value: form.Get would let a SPIFFE assertion type hidden
		// behind an earlier value reach the default strategy. A duplicated
		// client_assertion_type is rejected by authenticateSPIFFEJWTClient.
		if slices.Contains(form["client_assertion_type"], spiffeauth.SPIFFEJWTAssertionType) {
			return authenticateSPIFFEJWTClient(ctx, r, form, issuer, jwtBundleSource, resolver)
		}
		// Every client_id value is checked, so a duplicate naming an X.509
		// association still reaches the exact-client_id check.
		if x509Client != nil && slices.ContainsFunc(form["client_id"], x509Client) {
			return authenticateSPIFFEX509Client(ctx, r, form, x509BundleSource, resolver)
		}
		if leafHasSPIFFEURI(r) {
			slog.Debug("SPIFFE client certificate ignored as transport identity")
		}
		return defaultStrategy(ctx, r, form)
	}
}

// leafHasSPIFFEURI reports whether the request arrived on a TLS connection whose
// peer leaf certificate carries a URI SAN with the spiffe scheme. It does not
// validate the certificate; it only reports whether one was presented.
func leafHasSPIFFEURI(r *http.Request) bool {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0] == nil {
		return false
	}
	for _, uri := range r.TLS.PeerCertificates[0].URIs {
		if uri != nil && uri.Scheme == "spiffe" {
			return true
		}
	}
	return false
}

// authenticateSPIFFEX509Client verifies the client certificate presented on the
// mutually authenticated connection against the configured trust-domain bundle
// and resolves the verified SPIFFE ID to its configured OAuth client. The ID is
// derived only from the verified leaf. It fails closed on any malformed request
// field, missing or invalid certificate (including a malformed SPIFFE ID), or
// resolver rejection, and never logs the certificate.
func authenticateSPIFFEX509Client(
	ctx context.Context,
	r *http.Request,
	form url.Values,
	source x509bundle.Source,
	resolver SPIFFEClientResolver,
) (fosite.Client, error) {
	// client_id is deliberately required on this arm (RFC 8705 §2: the client
	// includes client_id with mTLS client authentication), unlike the JWT arm,
	// where the resolver can derive the client from the verified SVID. Routing
	// selected this arm by client_id; reject a duplicated client_id so the
	// exact client cannot be steered.
	clientID, ok := exactNonEmptyFormValue(form, "client_id")
	if !ok {
		slog.Debug("SPIFFE X.509 client authentication rejected",
			"stage", "ambiguous client_id")
		return nil, fosite.ErrInvalidRequest
	}
	if rejectedSPIFFEX509Request(r, form) {
		slog.Debug("SPIFFE X.509 client authentication rejected",
			"stage", "mixed credentials", "client_id", clientID)
		return nil, fosite.ErrInvalidClient
	}
	verifiedID, err := verifySPIFFEX509(r, source)
	if err != nil {
		slog.Debug("SPIFFE X.509 client authentication rejected",
			"stage", "certificate verification", "error", err, "client_id", clientID)
		return nil, fosite.ErrInvalidClient
	}
	client, err := resolver(ctx, verifiedID.String(), clientID, spiffeauth.SPIFFEAuthenticationMethodX509)
	if !validResolvedSPIFFEClient(client, err, clientID) {
		slog.Debug("SPIFFE X.509 client authentication rejected",
			"stage", "resolver", "error", err, "client_id", clientID, "spiffe_id", verifiedID.String())
		return nil, fosite.ErrInvalidClient
	}
	return client, nil
}

// rejectedSPIFFEX509Request reports whether a request mixes the ambient mTLS
// identity with another client credential (HTTP Basic, a client secret, or a
// client assertion form field). A SPIFFE client's sole credential must be its
// SVID, so a second credential makes the request ambiguous about which one
// authenticates the client.
func rejectedSPIFFEX509Request(r *http.Request, form url.Values) bool {
	return hasBasicAuthorization(r) || form.Has("client_secret") ||
		form.Has("client_assertion") || form.Has("client_assertion_type")
}

// verifySPIFFEX509 re-derives and verifies the SPIFFE ID from the request's
// TLS peer certificate chain. On top of the pinned go-spiffe verifier it
// enforces ToolHive's stricter leaf profile: a critical key-usage extension
// with digital signature, a basic-constraints extension that is valid, a
// subject-alternative-name extension (critical when the subject is empty), a
// non-root, canonical SPIFFE ID path, and, if an extended-key-usage extension
// is present, both server and client authentication. x509svid.Verify validates
// the chain against the bundle selected from the leaf's trust domain and also
// rejects a leaf that is a CA or has certificate- or CRL-signing key usage,
// and a leaf without exactly one URI SAN. A chain longer than
// maxClientCertificateChainLength is rejected first. A missing or malformed certificate
// is always rejected; there is no fallback to public or secret-based client
// authentication.
func verifySPIFFEX509(r *http.Request, source x509bundle.Source) (spiffeid.ID, error) {
	if source == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return spiffeid.ID{}, errClientCertificateRequired
	}
	if len(r.TLS.PeerCertificates) > maxClientCertificateChainLength {
		return spiffeid.ID{}, errClientChainTooLong
	}
	leaf := r.TLS.PeerCertificates[0]
	id, err := spiffeauth.SPIFFEIDFromCertificate(leaf)
	if err != nil {
		return spiffeid.ID{}, err
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return spiffeid.ID{}, fmt.Errorf("%w: key usage lacks digitalSignature", errInvalidX509SVIDLeaf)
	}
	if err := validSPIFFEX509RequiredExtensions(leaf); err != nil {
		return spiffeid.ID{}, err
	}
	if err := validSPIFFEX509ExtendedKeyUsage(leaf); err != nil {
		return spiffeid.ID{}, err
	}
	if _, _, err := x509svid.Verify(r.TLS.PeerCertificates, source); err != nil {
		return spiffeid.ID{}, err
	}
	return id, nil
}

// validSPIFFEX509RequiredExtensions checks that the leaf has a critical
// key-usage extension, a valid basic-constraints extension, and a SAN
// extension that is critical when the subject is empty. The CA bit is checked
// by x509svid.Verify.
func validSPIFFEX509RequiredExtensions(certificate *x509.Certificate) error {
	keyUsage, ok := certificateExtension(certificate, oidExtensionKeyUsage)
	if !ok || !keyUsage.Critical {
		return fmt.Errorf("%w: key usage extension missing or not critical", errInvalidX509SVIDLeaf)
	}
	if _, ok := certificateExtension(certificate, oidExtensionBasicConstraints); !ok ||
		!certificate.BasicConstraintsValid {
		return fmt.Errorf("%w: basic constraints missing or invalid", errInvalidX509SVIDLeaf)
	}
	subjectAltName, ok := certificateExtension(certificate, oidExtensionSubjectAltName)
	if !ok {
		return fmt.Errorf("%w: subject alternative name missing", errInvalidX509SVIDLeaf)
	}

	var subject pkix.RDNSequence
	rest, err := asn1.Unmarshal(certificate.RawSubject, &subject)
	if err != nil || len(rest) != 0 {
		return fmt.Errorf("%w: subject is malformed", errInvalidX509SVIDLeaf)
	}
	if len(subject) == 0 && !subjectAltName.Critical {
		return fmt.Errorf("%w: subject alternative name not critical for an empty subject", errInvalidX509SVIDLeaf)
	}
	return nil
}

func certificateExtension(certificate *x509.Certificate, oid asn1.ObjectIdentifier) (pkix.Extension, bool) {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(oid) {
			return extension, true
		}
	}
	return pkix.Extension{}, false
}

func validSPIFFEX509ExtendedKeyUsage(certificate *x509.Certificate) error {
	if len(certificate.ExtKeyUsage) == 0 && len(certificate.UnknownExtKeyUsage) == 0 {
		return nil
	}
	if slices.Contains(certificate.ExtKeyUsage, x509.ExtKeyUsageServerAuth) &&
		slices.Contains(certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return nil
	}
	return fmt.Errorf("%w: extended key usage lacks serverAuth or clientAuth", errInvalidX509SVIDLeaf)
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
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "missing or ambiguous client_assertion_type")
		return nil, fosite.ErrInvalidRequest
	}
	assertion, ok := exactNonEmptyFormValue(form, "client_assertion")
	if !ok {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "missing or ambiguous client_assertion")
		return nil, fosite.ErrInvalidRequest
	}
	clientID, ok := optionalExactNonEmptyFormValue(form, "client_id")
	if !ok {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "ambiguous client_id")
		return nil, fosite.ErrInvalidRequest
	}
	if rejectedSPIFFEJWTRequest(r, form, assertion) {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "mixed credentials or oversized assertion", "client_id", clientID)
		return nil, fosite.ErrInvalidClient
	}
	if jwtBundleSource == nil {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "no JWT bundle source configured", "client_id", clientID)
		return nil, fosite.ErrInvalidClient
	}

	svid, err := jwtsvid.ParseAndValidate(assertion, jwtBundleSource, []string{issuer})
	if err != nil {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "assertion verification", "error", err, "client_id", clientID)
		return nil, fosite.ErrInvalidClient
	}
	if !validSPIFFEJWTIdentityClaims(svid, issuer) {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "audience claim", "client_id", clientID, "spiffe_id", svid.ID.String())
		return nil, fosite.ErrInvalidClient
	}
	if svid.Expiry.After(time.Now().Add(maxSPIFFEJWTAssertionRemainingValidity)) {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "assertion lifetime exceeds limit", "client_id", clientID, "spiffe_id", svid.ID.String())
		return nil, fosite.ErrInvalidClient
	}
	client, err := resolver(ctx, svid.ID.String(), clientID, spiffeauth.SPIFFEAuthenticationMethodJWT)
	if !validResolvedSPIFFEClient(client, err, clientID) {
		slog.Debug("SPIFFE JWT-SVID client authentication rejected",
			"stage", "resolver", "error", err, "client_id", clientID, "spiffe_id", svid.ID.String())
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
