// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
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
	resolver SPIFFEClientResolver,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		// No SPIFFE trust configured: this server genuinely does not do SPIFFE,
		// so neither arm applies and every request goes to the default strategy.
		if resolver == nil {
			return defaultStrategy(ctx, r, form)
		}
		// An explicit assertion type takes precedence over an ambient mTLS identity.
		// A repeated form key must be checked in full: form.Get would only see the
		// first value, letting a SPIFFE assertion type hidden behind an earlier
		// value slip through to the default strategy.
		if slices.Contains(form["client_assertion_type"], spiffeauth.SPIFFEJWTAssertionType) {
			return authenticateSPIFFEJWTClient(ctx, r, form, issuer, jwtBundleSource, resolver)
		}
		if _, ok := spiffeauth.SPIFFEIDFromContext(ctx); ok {
			return nil, fosite.ErrInvalidClient.WithHint("SPIFFE X.509 client authentication is not implemented")
		}
		return defaultStrategy(ctx, r, form)
	}
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
