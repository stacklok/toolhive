// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oautherr"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// registrationProbeTimeout bounds the request that checks a cached
// registration, so an unresponsive upstream delays resolution by at most this
// long before the cached registration is used as-is.
const registrationProbeTimeout = 10 * time.Second

// authorizationProbeDrainLimit caps how much of an authorization endpoint's
// response body the probe reads before closing it. The body is never
// inspected; draining only lets the connection be reused.
const authorizationProbeDrainLimit = 1 << 20

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

// errProbeRedirectRefused stops the token-endpoint probe from following a
// redirect, which would re-send the client credentials to another origin.
var errProbeRedirectRefused = errors.New("dcr: token endpoint returned a redirect; refusing to follow")

// errProbeUnsupportedAuthMethod reports a cached registration whose token
// endpoint auth method the probe cannot authenticate with.
var errProbeUnsupportedAuthMethod = errors.New("dcr: token endpoint auth method cannot be probed")

// errProbeDisabled reports that the caller configured ProbeNone, so the cached
// registration is used without asking the upstream.
var errProbeDisabled = errors.New("dcr: registration probe disabled by configuration")

// verifyCachedRegistration asks the upstream whether it still recognises the
// cached client, using the strategy req.RegistrationProbe selects. An
// upstream can drop a registration that the resolver still caches: RFC 7591
// lets client_secret_expires_at be 0 while the server evicts idle clients
// anyway (a ToolHive auth server does exactly this), or it can lose its
// client storage. The browser is the only party that sees the resulting
// authorize error, so the resolver must ask.
//
// ProbeTokenEndpoint (the default) is verifyViaTokenEndpoint;
// ProbeAuthorizationEndpoint is verifyViaAuthorizationEndpoint. Each returns
// registrationUnverified for every outcome that is not a verdict (transport
// failures, 5xx, rate limiting, shapes the strategy cannot read), and callers
// then keep the cached registration so an unreachable upstream never blocks
// startup. ProbeNone returns registrationUnverified with errProbeDisabled
// without contacting the upstream.
//
// The returned error is non-nil only for an unverified status and says why;
// it never carries the upstream's response body (see probeErrorAttrs).
func verifyCachedRegistration(ctx context.Context, req *Request, cached *Resolution) (registrationStatus, error) {
	switch probe := req.RegistrationProbe.effective(); probe {
	case ProbeTokenEndpoint:
		return verifyViaTokenEndpoint(ctx, req, cached)
	case ProbeAuthorizationEndpoint:
		return verifyViaAuthorizationEndpoint(ctx, req, cached)
	case ProbeNone:
		return registrationUnverified, errProbeDisabled
	default:
		// validateResolveInputs rejects unknown values before resolution starts.
		return registrationUnverified, fmt.Errorf("dcr: unknown registration probe %q", string(probe))
	}
}

// verifyViaTokenEndpoint redeems a random authorization code with the cached
// client credentials. Servers that authenticate the client before validating
// the grant (as fosite does, for public and confidential clients alike)
// answer invalid_client when the registration is gone and invalid_grant when
// it is alive. No token can be issued for the random code. RFC 6749 §4.1.3
// only requires confidential clients to authenticate, so for a public client
// some servers validate the code first and answer invalid_grant for any
// client_id; against those servers this probe cannot detect a deleted
// registration (ProbeAuthorizationEndpoint can, when the server follows RFC
// 6749 §4.1.2.1). private_key_jwt clients are not probed: the probe does not
// sign for them.
//
// The configured token endpoint wins over the cached one, mirroring
// applyExplicitEndpointOverrides, so a tampered cached endpoint cannot
// receive the client secret when the operator configured the endpoint.
func verifyViaTokenEndpoint(ctx context.Context, req *Request, cached *Resolution) (registrationStatus, error) {
	authStyle, ok := probeAuthStyle(cached)
	if !ok {
		return registrationUnverified, errProbeUnsupportedAuthMethod
	}

	tokenEndpoint, operatorSupplied := req.TokenEndpoint, true
	if tokenEndpoint == "" {
		tokenEndpoint = cached.TokenEndpoint
		operatorSupplied = networking.AuthorityMatchesAny(tokenEndpoint, req.DiscoveryURL, req.RegistrationEndpoint)
	}
	client, err := newProbeHTTPClient(req, tokenEndpoint, "token_endpoint", !operatorSupplied)
	if err != nil {
		return registrationUnverified, fmt.Errorf("dcr: build token endpoint client: %w", err)
	}
	// The request carries client credentials; never re-send them to another origin.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errProbeRedirectRefused
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

// verifyViaAuthorizationEndpoint sends an authorization request for the cached
// client and reads the first response without following redirects. Per RFC
// 6749 §4.1.2.1 the server MUST NOT redirect when the client_id or
// redirect_uri is invalid and informs the user directly instead, so a direct
// 4xx is the signal that the registration is gone, while any 2xx (a login or
// consent page) or 3xx (to the IdP, or back to redirect_uri with an error
// such as invalid_scope) shows the client is known. The request carries no
// credentials: only the public client_id, the registered redirect_uri, the
// configured scopes and a throwaway state and PKCE challenge, so the server
// records at most an abandoned authorization request.
//
// Not every 4xx is a verdict: 404 and 405 describe the endpoint rather than
// the client, and 429 is rate limiting. Those, 5xx and transport failures are
// unverified. A server that renders its unknown-client error with 200, or
// redirects to its own error page, reads as alive; the cached registration is
// then kept exactly as without a probe. The response body is never read for
// content or logged.
//
// The configured authorization endpoint wins over the cached one, mirroring
// verifyViaTokenEndpoint.
func verifyViaAuthorizationEndpoint(ctx context.Context, req *Request, cached *Resolution) (registrationStatus, error) {
	authzEndpoint, operatorSupplied := req.AuthorizationEndpoint, true
	if authzEndpoint == "" {
		authzEndpoint = cached.AuthorizationEndpoint
		operatorSupplied = networking.AuthorityMatchesAny(authzEndpoint, req.DiscoveryURL, req.RegistrationEndpoint)
	}
	client, err := newProbeHTTPClient(req, authzEndpoint, "authorization_endpoint", !operatorSupplied)
	if err != nil {
		return registrationUnverified, fmt.Errorf("dcr: build authorization endpoint client: %w", err)
	}
	// A redirect is the "known client" answer; read it, do not follow it.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	redirectURI := cached.RedirectURI
	if redirectURI == "" {
		if redirectURI, err = resolveUpstreamRedirectURI(req.RedirectURI, req.Issuer); err != nil {
			return registrationUnverified, fmt.Errorf("dcr: authorization endpoint probe: %w", err)
		}
	}
	probeURL, err := authorizationProbeURL(authzEndpoint, cached.ClientID, redirectURI, req.Scopes)
	if err != nil {
		return registrationUnverified, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, registrationProbeTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		return registrationUnverified, fmt.Errorf("dcr: authorization endpoint probe: %w", err)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return registrationUnverified, fmt.Errorf("dcr: authorization endpoint probe: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, authorizationProbeDrainLimit))
		_ = resp.Body.Close()
	}()

	status := classifyAuthorizationProbe(resp.StatusCode)
	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: authorization endpoint probe answered",
		"client_id", cached.ClientID,
		"status", resp.StatusCode,
		"registration_live", status == registrationLive,
		"registration_rejected", status == registrationRejected,
	)
	if status != registrationUnverified {
		return status, nil
	}
	return status, fmt.Errorf("dcr: authorization endpoint answered HTTP %d", resp.StatusCode)
}

// authorizationProbeURL builds the probe's authorization request URL,
// preserving any query the configured endpoint already carries (some
// providers key tenants that way).
func authorizationProbeURL(endpoint, clientID, redirectURI string, scopes []string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("dcr: parse authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", rand.Text())
	q.Set("code_challenge", oauth2.S256ChallengeFromVerifier(rand.Text()+rand.Text()))
	q.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// classifyAuthorizationProbe maps the authorization endpoint's first response
// to a registrationStatus; see verifyViaAuthorizationEndpoint for the rules.
func classifyAuthorizationProbe(statusCode int) registrationStatus {
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusBadRequest:
		return registrationLive
	case statusCode == http.StatusNotFound,
		statusCode == http.StatusMethodNotAllowed,
		statusCode == http.StatusTooManyRequests:
		return registrationUnverified
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		return registrationRejected
	default:
		return registrationUnverified
	}
}

// probeErrorAttrs renders why a probe was unverified as log attributes. An
// OAuth error response is reduced to its HTTP status and RFC 6749 error
// code: oauth2.RetrieveError's message includes the server's
// error_description, or its raw response body when there is no error code,
// and none of that belongs in our logs. Other errors (transport failures,
// refused redirects, an unusable endpoint, a non-verdict status from the
// authorization endpoint) originate on our side and go through
// SanitizeErrorForLog.
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

// newProbeHTTPClient builds the guarded client for a probe against endpoint,
// validated under label. It reuses the resolver's SSRF policy
// (newGuardedDCRClient); callers set CheckRedirect for their strategy.
func newProbeHTTPClient(req *Request, endpoint, label string, serverSupplied bool) (*http.Client, error) {
	if err := validateUpstreamEndpointURL(endpoint, label); err != nil {
		return nil, err
	}
	host, err := hostFromURL(endpoint)
	if err != nil {
		return nil, err
	}
	return newGuardedDCRClient(host, req.AllowPrivateIPs, req.ServerSuppliedEndpoints || serverSupplied, req.CAFilePath)
}

// classifyProbe turns the token-endpoint probe's exchange error into a
// registrationStatus. Only a structured RFC 6749 error code is a verdict;
// transient shapes (5xx, 429, non-OAuth bodies) and transport errors are
// unverified.
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
