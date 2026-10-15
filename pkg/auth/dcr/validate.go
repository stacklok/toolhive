// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oautherr"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// registrationProbeTimeout bounds the token-endpoint request that checks a
// cached registration, so an unresponsive upstream delays resolution by at
// most this long before the cached registration is used as-is.
const registrationProbeTimeout = 10 * time.Second

// registrationStatus is the upstream's verdict on a cached registration.
type registrationStatus int

const (
	// registrationUnverified means the probe was skipped or inconclusive.
	registrationUnverified registrationStatus = iota
	// registrationLive means the upstream authenticated the cached client.
	registrationLive
	// registrationRejected means the upstream no longer recognises the
	// cached client.
	registrationRejected
)

// errProbeRedirectRefused stops the probe from following a redirect, which
// would re-send the client credentials to another origin.
var errProbeRedirectRefused = errors.New("dcr: token endpoint returned a redirect; refusing to follow")

// errProbeUnsupportedAuthMethod reports a cached registration whose token
// endpoint auth method the probe cannot authenticate with.
var errProbeUnsupportedAuthMethod = errors.New("dcr: token endpoint auth method cannot be probed")

// verifyCachedRegistration asks the upstream token endpoint whether it still
// recognises the cached client. An upstream can drop a registration that the
// resolver still caches: RFC 7591 lets client_secret_expires_at be 0 while
// the server evicts idle clients anyway (a ToolHive auth server does exactly
// this), or it can lose its client storage. The browser is the only party
// that sees the resulting authorize error, so the resolver must ask.
//
// The probe redeems a random authorization code with the cached client
// credentials. Servers that authenticate the client before validating the
// grant (as fosite does, for public and confidential clients alike) answer
// invalid_client when the registration is gone and invalid_grant when it is
// alive. No token can be issued for the random code. RFC 6749 §4.1.3 only
// requires confidential clients to authenticate, so for a public client some
// servers validate the code first and answer invalid_grant for any client_id;
// against those servers the probe cannot detect a deleted registration and
// the cached one is kept, as before. Every other outcome, including transport
// failures and private_key_jwt clients (which the probe does not sign for),
// is unverified: callers keep the cached registration so an unreachable
// upstream never blocks startup.
//
// The returned error is non-nil only for an unverified status and says why;
// it never carries the token endpoint's response body (see probeErrorAttrs).
//
// The configured token endpoint wins over the cached one, mirroring
// applyExplicitEndpointOverrides, so a tampered cached endpoint cannot
// receive the client secret when the operator configured the endpoint.
func verifyCachedRegistration(ctx context.Context, req *Request, cached *Resolution) (registrationStatus, error) {
	authStyle, ok := probeAuthStyle(cached)
	if !ok {
		return registrationUnverified, errProbeUnsupportedAuthMethod
	}

	tokenEndpoint, operatorSupplied := req.TokenEndpoint, true
	if tokenEndpoint == "" {
		tokenEndpoint = cached.TokenEndpoint
		operatorSupplied = networking.AuthorityMatchesAny(tokenEndpoint, req.DiscoveryURL, req.RegistrationEndpoint)
	}
	client, err := newProbeHTTPClient(req, tokenEndpoint, !operatorSupplied)
	if err != nil {
		return registrationUnverified, fmt.Errorf("dcr: build token endpoint client: %w", err)
	}

	clientSecret := cached.ClientSecret
	if cached.TokenEndpointAuthMethod == oauthproto.TokenEndpointAuthMethodNone {
		clientSecret = ""
	}
	cfg := &oauth2.Config{
		ClientID:     cached.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: tokenEndpoint, AuthStyle: authStyle},
		RedirectURL:  cached.RedirectURI,
	}
	probeCtx, cancel := context.WithTimeout(ctx, registrationProbeTimeout)
	defer cancel()
	probeCtx = context.WithValue(probeCtx, oauth2.HTTPClient, client)
	_, err = cfg.Exchange(probeCtx, rand.Text(), oauth2.VerifierOption(rand.Text()+rand.Text()))

	status := classifyProbe(err)
	if status != registrationUnverified {
		return status, nil
	}
	return status, err
}

// probeErrorAttrs renders why a probe was unverified as log attributes. An
// OAuth error response is reduced to its HTTP status and RFC 6749 error
// code: oauth2.RetrieveError's message includes the server's
// error_description, or its raw response body when there is no error code,
// and none of that belongs in our logs. Other errors (transport failures,
// refused redirects, an unusable endpoint) originate on our side and go
// through SanitizeErrorForLog.
func probeErrorAttrs(err error) []any {
	if retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		attrs := []any{"error_code", retrieveErr.ErrorCode}
		if retrieveErr.Response != nil {
			attrs = append(attrs, "status", retrieveErr.Response.StatusCode)
		}
		return attrs
	}
	return []any{"error", SanitizeErrorForLog(err)}
}

// probeAuthStyle maps the cached token endpoint auth method onto the oauth2
// client's auth style. It reports false for methods the probe cannot use
// (private_key_jwt needs a signed assertion) and for secret-based methods
// whose secret is missing.
func probeAuthStyle(cached *Resolution) (oauth2.AuthStyle, bool) {
	switch cached.TokenEndpointAuthMethod {
	case oauthproto.TokenEndpointAuthMethodClientSecretBasic:
		return oauth2.AuthStyleInHeader, cached.ClientSecret != ""
	case oauthproto.TokenEndpointAuthMethodClientSecretPost:
		return oauth2.AuthStyleInParams, cached.ClientSecret != ""
	case oauthproto.TokenEndpointAuthMethodNone:
		return oauth2.AuthStyleInParams, true
	default:
		return oauth2.AuthStyleAutoDetect, false
	}
}

// newProbeHTTPClient builds the guarded client for the probe. It reuses the
// resolver's SSRF policy (newGuardedDCRClient) and refuses redirects, because
// the request carries client credentials.
func newProbeHTTPClient(req *Request, tokenEndpoint string, serverSupplied bool) (*http.Client, error) {
	if err := validateUpstreamEndpointURL(tokenEndpoint, "token_endpoint"); err != nil {
		return nil, err
	}
	host, err := hostFromURL(tokenEndpoint)
	if err != nil {
		return nil, err
	}
	client, err := newGuardedDCRClient(host, req.AllowPrivateIPs, req.ServerSuppliedEndpoints || serverSupplied, req.CAFilePath)
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errProbeRedirectRefused
	}
	return client, nil
}

// classifyProbe turns the probe's exchange error into a registrationStatus.
// Only a structured RFC 6749 error code is a verdict; transient shapes (5xx,
// 429, non-OAuth bodies) and transport errors are unverified.
func classifyProbe(err error) registrationStatus {
	if err == nil {
		// The upstream issued tokens for a random code; the client is
		// authenticated, whatever else is wrong with that server.
		return registrationLive
	}
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok || !oautherr.IsPermanentCredentialError(err) {
		return registrationUnverified
	}
	switch retrieveErr.ErrorCode {
	case "invalid_client":
		return registrationRejected
	case "invalid_grant":
		return registrationLive
	default:
		return registrationUnverified
	}
}
