// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
)

// OAuth grant-type and Entra parameter values for the on-behalf-of flow.
//
//nolint:gosec // G101: these are OAuth protocol identifiers, not credentials.
const (
	// grantTypeJWTBearer is the RFC 7523 JWT bearer grant type. Entra's
	// on-behalf-of flow is the jwt-bearer grant plus requested_token_use.
	grantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"

	// requestedTokenUseOBO is the Entra-specific marker that turns the
	// jwt-bearer grant into an on-behalf-of exchange.
	requestedTokenUseOBO = "on_behalf_of"

	// bearerTokenType is the only token_type the OBO flow issues. The success
	// path requires it (case-insensitively): the token is later presented as a
	// Bearer credential, so accepting any other type would be unsafe.
	bearerTokenType = "Bearer"
)

const (
	// defaultAuthority is the public Microsoft Entra (Azure AD) authority. It is
	// overridable via Config.Authority for sovereign clouds and tests.
	defaultAuthority = "https://login.microsoftonline.com"

	// tokenEndpointPath is the v2.0 token endpoint path appended after the tenant.
	tokenEndpointPath = "oauth2/v2.0/token" //nolint:gosec // G101: URL path segment, not a credential.

	// defaultHTTPTimeout bounds the whole exchange when the caller injects no
	// HTTP client. Generous because federated IdP chains can be slow to respond.
	defaultHTTPTimeout = 30 * time.Second

	// maxResponseBodySize caps how much of the token endpoint response is read,
	// so a pathological server cannot exhaust memory. 1 MiB dwarfs any real
	// token response.
	maxResponseBodySize = 1 << 20 // 1 MiB
)

// maxTenantLen caps the tenant identifier at the DNS name length limit; a
// verified-domain tenant cannot be longer, and a GUID is far shorter.
const maxTenantLen = 253

// Tenant identifier patterns: the Entra v2.0 endpoint addresses a tenant either
// by directory GUID or by verified domain (e.g. contoso.onmicrosoft.com).
// Validating up front turns a typo into a clear wiring-time error instead of an
// opaque 400/404 from Entra on the first exchange.
var (
	tenantGUIDPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	tenantDomainPattern = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
)

// caInteractionCodes are the AADSTS error codes that signal a conditional-access
// / MFA challenge requiring interactive user action. The "interaction_required"
// OAuth error and a non-empty claims challenge are the primary signals; these
// codes catch responses that omit the OAuth error but still demand interaction.
var caInteractionCodes = map[int]struct{}{
	50076: {}, // AADSTS50076: MFA required by the resource.
	50079: {}, // AADSTS50079: MFA enrolment required (proof-up).
}

// Config is the Microsoft Entra confidential-client configuration for the OBO
// exchange. v1 supports client_secret authentication only; certificate and
// workload-identity client auth are planned fast-follows.
type Config struct {
	// TenantID is the Entra tenant (directory) — a GUID or a verified domain.
	// Required.
	TenantID string

	// ClientID is the confidential client's application (client) ID. Required.
	ClientID string

	// ClientSecret is the confidential client's secret. Required in v1.
	ClientSecret string //nolint:gosec // G101: field name, not a hardcoded credential.

	// Authority overrides the Entra authority base URL. Defaults to
	// https://login.microsoftonline.com. Must be https, except http is allowed
	// for loopback hosts so tests can point at an httptest server.
	Authority string

	// HTTPClient is the HTTP client used for the exchange. When nil, a client
	// with a sane default timeout that refuses redirects is used. A
	// caller-injected client SHOULD likewise refuse redirects: the request body
	// carries client_secret and the user assertion, which Go replays on a
	// 307/308 — including to a different host. A legitimate Entra token endpoint
	// never redirects.
	HTTPClient *http.Client
}

// Exchanger performs the Microsoft Entra on-behalf-of token exchange. It
// implements both exchange.Exchanger (token string only) and exchange.TokenExchanger
// (token plus expiry, consumed by the caching layer).
//
// For the Entra provider the audience argument of Exchange / ExchangeToken is
// the OAuth scope to request — typically "<resource>/.default" or an explicit
// delegated scope such as "api://<app-id>/Files.Read". It is sent verbatim as
// the request's scope parameter.
type Exchanger struct {
	tokenURL     string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// Compile-time assertions that Exchanger satisfies both contracts.
var (
	_ exchange.Exchanger      = (*Exchanger)(nil)
	_ exchange.TokenExchanger = (*Exchanger)(nil)
)

// New validates cfg and returns an Exchanger. It fails loudly on missing
// required fields or a malformed authority rather than deferring the failure to
// the first exchange. Configuration errors are returned as plain errors, not as
// *ExchangeError (which classifies exchange-time failures only).
func New(cfg Config) (*Exchanger, error) {
	tokenURL, err := TokenURL(cfg.TenantID, cfg.Authority)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, fmt.Errorf("entra obo: ClientID is required")
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("entra obo: ClientSecret is required")
	}

	return &Exchanger{
		tokenURL:     tokenURL,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		httpClient:   newHTTPClient(cfg.HTTPClient),
	}, nil
}

// TokenURL builds and validates the Entra v2.0 on-behalf-of token endpoint for a
// tenant, applying exactly the rules New uses so the URL it returns is one
// NewFromTokenURL (the proxy-runtime constructor) will also accept. The operator's
// OBO validation calls this to derive the runtime contract's tokenUrl from the
// CRD's tenantId (and optional authority) rather than re-spelling Entra's
// authority/tenant/path structure — keeping that structure defined in one place.
//
// authority defaults to the public Microsoft Entra host when empty; a non-empty
// authority must be an https URL (http only for loopback) with a host and no query
// or fragment. tenantID must be a directory GUID or a verified domain name. Errors
// are plain errors that never contain a credential.
func TokenURL(tenantID, authority string) (string, error) {
	tenant := strings.TrimSpace(tenantID)
	if tenant == "" {
		return "", fmt.Errorf("entra obo: TenantID is required")
	}
	if err := validateTenant(tenant); err != nil {
		return "", err
	}

	if authority == "" {
		authority = defaultAuthority
	}
	if err := validateAuthority(authority); err != nil {
		return "", err
	}

	tokenURL, err := url.JoinPath(authority, tenant, tokenEndpointPath)
	if err != nil {
		return "", fmt.Errorf("entra obo: build token URL: %w", err)
	}
	return tokenURL, nil
}

// NewFromTokenURL returns an Exchanger that posts the OBO exchange to a
// fully-formed tenant token endpoint. It is the constructor for callers that
// already hold the complete token URL — notably the proxy-runtime middleware,
// which receives tokenURL verbatim from the obo.MiddlewareParameters
// contract and must not re-derive Entra's authority/tenant URL structure.
// Use New instead when the caller holds an authority + tenant pair.
//
// tokenURL is validated exactly as New validates its derived endpoint: an
// absolute https URL (http permitted only for loopback hosts, so tests can point
// at an httptest server), with a host and no query string or fragment. clientID
// and clientSecret are required. A nil httpClient gets the same redirect-refusing
// default New uses. Configuration errors are returned as plain errors, not as
// *ExchangeError (which classifies exchange-time failures only).
//
// SECURITY: tokenURL is a trust boundary. The exchange POSTs the client secret
// and the end-user assertion in the request body to tokenURL, so the caller must
// supply only a trusted Entra token endpoint. This is safe because tokenURL comes
// from operator-controlled configuration (the obo.MiddlewareParameters contract), never
// from end-user input, and the unconditional redirect guard (see newHTTPClient)
// stops a 3xx from replaying that body to another host. This constructor
// intentionally does NOT allowlist hosts or enforce a path shape: New already
// trusts an operator-supplied Authority for arbitrary hosts (sovereign clouds),
// and a hard allowlist would reject legitimate sovereign / B2C / CIAM endpoints
// (which use different hosts and token paths) while giving only false assurance.
func NewFromTokenURL(tokenURL, clientID, clientSecret string, httpClient *http.Client) (*Exchanger, error) {
	if strings.TrimSpace(tokenURL) == "" {
		return nil, fmt.Errorf("entra obo: token URL is required")
	}
	if err := validateHTTPSURL(tokenURL, "token URL"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("entra obo: ClientID is required")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("entra obo: ClientSecret is required")
	}

	return &Exchanger{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   newHTTPClient(httpClient),
	}, nil
}

// newHTTPClient returns the client used for the exchange. The redirect guard is
// enforced unconditionally — including over a caller-injected client — because
// the POST body carries the client_secret and user assertion, which Go replays
// on a 307/308 (possibly to a different host) when redirects are followed. A
// real Entra token endpoint never redirects, so refusing them costs nothing.
//
// An injected client is shallow-copied so its Transport/Timeout/Jar are
// preserved while CheckRedirect is overridden, and the caller's own *http.Client
// is never mutated (it may be shared). The copy is a snapshot taken at New time.
func newHTTPClient(injected *http.Client) *http.Client {
	if injected == nil {
		return &http.Client{Timeout: defaultHTTPTimeout, CheckRedirect: refuseRedirects}
	}
	client := *injected
	client.CheckRedirect = refuseRedirects
	return &client
}

// refuseRedirects is a http.Client.CheckRedirect that stops the client at the
// first response instead of following a redirect.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// validateTenant accepts an Entra tenant identifier in the two forms the v2.0
// endpoint uses — a directory GUID or a verified domain — capped at the DNS name
// length. It rejects junk (including the path metacharacters the previous,
// looser check caught) at wiring time.
func validateTenant(tenant string) error {
	if len(tenant) > maxTenantLen {
		return fmt.Errorf("entra obo: TenantID must be at most %d characters", maxTenantLen)
	}
	if tenantGUIDPattern.MatchString(tenant) || tenantDomainPattern.MatchString(tenant) {
		return nil
	}
	return fmt.Errorf("entra obo: TenantID must be a directory GUID or a domain name")
}

// Exchange implements exchange.Exchanger: it returns just the backend-scoped access
// token, discarding the expiry the caching layer would use.
func (e *Exchanger) Exchange(ctx context.Context, userToken, audience string) (string, error) {
	token, err := e.ExchangeToken(ctx, userToken, audience)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// ExchangeToken implements exchange.TokenExchanger: it performs the Entra OBO
// exchange and returns the backend-scoped token together with its expiry.
//
// Failures fall into three error shapes:
//   - Argument validation (empty userToken or audience) returns a plain error,
//     not an *ExchangeError — no exchange was attempted, so there is nothing to
//     classify.
//   - A cancelled or timed-out context returns the wrapped context error
//     (matches with errors.Is(err, context.Canceled / context.DeadlineExceeded)).
//   - An HTTP-level failure returns a typed *ExchangeError; use errors.As to read
//     Kind / Claims, or errors.Is against the package sentinels.
//
// No returned error ever contains the subject token, client secret, or the
// issued access token.
func (e *Exchanger) ExchangeToken(ctx context.Context, userToken, audience string) (exchange.Token, error) {
	if userToken == "" {
		return exchange.Token{}, fmt.Errorf("entra obo: user token is required")
	}
	if audience == "" {
		return exchange.Token{}, fmt.Errorf("entra obo: audience (scope) is required")
	}

	req, err := e.buildRequest(ctx, userToken, audience)
	if err != nil {
		// Unreachable for a New-built Exchanger (the endpoint was validated), but
		// classified rather than panicked on if it ever happens.
		return exchange.Token{}, &ExchangeError{Kind: KindTransient, Description: "unable to build the token request"}
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		// Surface caller-driven cancellation or deadline as-is so it is not
		// misclassified as a retryable server failure. Only when the caller's
		// own context is done: the HTTP client's Timeout also produces an
		// error matching context.DeadlineExceeded, and that is a slow or
		// unreachable token endpoint, which is transient.
		if ctx.Err() != nil {
			return exchange.Token{}, fmt.Errorf("entra obo: %w", ctx.Err())
		}
		// Transport-level failure (DNS, dial, TLS, read). The raw error is
		// dropped deliberately: it is not needed to classify the failure as
		// transient, and dropping it keeps the token endpoint URL out of logs.
		return exchange.Token{}, &ExchangeError{Kind: KindTransient, Description: "unable to reach the token endpoint"}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return exchange.Token{}, &ExchangeError{
			Kind:        KindTransient,
			HTTPStatus:  resp.StatusCode,
			Description: "failed to read token endpoint response",
		}
	}

	return e.parseResponse(resp.StatusCode, body)
}

// buildRequest assembles the Entra OBO POST. The subject token and client
// secret travel in the form body only. It returns an error rather than papering
// over a request-build failure: New validated the endpoint, so this is
// unreachable in practice, but the caller classifies any failure instead of
// risking a nil-pointer dereference.
func (e *Exchanger) buildRequest(ctx context.Context, userToken, audience string) (*http.Request, error) {
	form := url.Values{}
	form.Set("grant_type", grantTypeJWTBearer)
	form.Set("client_id", e.clientID)
	form.Set("client_secret", e.clientSecret)
	form.Set("assertion", userToken)
	form.Set("scope", audience)
	form.Set("requested_token_use", requestedTokenUseOBO)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// tokenResponse is the wire shape of an Entra token endpoint reply. It covers
// both the success and error payloads: per RFC 6749 Section 5.2 an "error" can
// appear even with a 2xx status, so the body is decoded once and the error
// fields are inspected regardless of status.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
	Claims           string `json:"claims"`
}

// parseResponse turns a decoded token endpoint reply into a Token or a typed
// error. It is a method only so the success path can stamp the expiry against
// the wall clock; it holds no per-request state.
func (*Exchanger) parseResponse(status int, body []byte) (exchange.Token, error) {
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		// Unparseable body: classify by status alone, without echoing the body.
		kind := KindUnknown
		if status >= http.StatusInternalServerError {
			kind = KindTransient
		}
		return exchange.Token{}, &ExchangeError{
			Kind:        kind,
			HTTPStatus:  status,
			Description: "malformed token endpoint response",
		}
	}

	// Treat a non-2xx status OR an error field in a 2xx body as a failure.
	if status < 200 || status > 299 || tr.Error != "" {
		return exchange.Token{}, classifyHTTPError(status, &tr)
	}

	if tr.AccessToken == "" {
		return exchange.Token{}, &ExchangeError{
			Kind:        KindUnknown,
			HTTPStatus:  status,
			Description: "token response missing access_token",
		}
	}

	// RFC 6749 Section 5.1 / RFC 8693 Section 2.2.1 require token_type. The token
	// is presented as a Bearer credential, so reject a missing or non-Bearer
	// type rather than trusting a mangled 2xx.
	if !strings.EqualFold(tr.TokenType, bearerTokenType) {
		return exchange.Token{}, &ExchangeError{
			Kind:        KindUnknown,
			HTTPStatus:  status,
			Description: "token response has a missing or non-Bearer token_type",
		}
	}

	var expiry time.Time
	if tr.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return exchange.Token{AccessToken: tr.AccessToken, Expiry: expiry}, nil
}

// classifyHTTPError maps an Entra error reply onto an *ExchangeError. A 5xx is
// transient regardless of body; otherwise the OAuth error code and claims
// challenge drive the classification.
func classifyHTTPError(status int, tr *tokenResponse) *ExchangeError {
	kind := func() ErrorKind {
		switch {
		case status >= http.StatusInternalServerError:
			return KindTransient
		case isInteractionRequired(tr):
			return KindInteractionRequired
		case tr.Error == "invalid_grant":
			return KindInvalidGrant
		case tr.Error == "invalid_client" || tr.Error == "unauthorized_client":
			return KindInvalidClient
		default:
			return KindUnknown
		}
	}()

	return &ExchangeError{
		Kind:        kind,
		OAuthError:  tr.Error,
		Description: tr.ErrorDescription,
		Claims:      tr.Claims,
		HTTPStatus:  status,
	}
}

// isInteractionRequired reports whether an Entra error reply is a
// conditional-access / MFA / claims challenge. The canonical signals are the
// "interaction_required" OAuth error and a non-empty claims challenge; the
// AADSTS code set is a fallback for replies that carry neither.
func isInteractionRequired(tr *tokenResponse) bool {
	if tr.Error == "interaction_required" || tr.Claims != "" {
		return true
	}
	for _, code := range tr.ErrorCodes {
		if _, ok := caInteractionCodes[code]; ok {
			return true
		}
	}
	return false
}

// validateAuthority enforces that the authority is an absolute https URL (http
// permitted only for loopback hosts, so tests can use httptest), with a host and
// no fragment or query string.
func validateAuthority(raw string) error {
	return validateHTTPSURL(raw, "Authority")
}

// validateHTTPSURL enforces that raw is an absolute https URL (http permitted
// only for loopback hosts, so tests can use httptest), with a host and no
// fragment or query string. label names the field in error messages so the same
// check serves both the authority (New) and the full token URL (NewFromTokenURL).
func validateHTTPSURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("entra obo: %s is not a valid URL: %w", label, err)
	}
	if u.Host == "" {
		return fmt.Errorf("entra obo: %s must include a host", label)
	}
	if u.Fragment != "" {
		return fmt.Errorf("entra obo: %s must not contain a fragment", label)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("entra obo: %s must not contain a query string", label)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("entra obo: %s must use https (http allowed only for loopback)", label)
	default:
		return fmt.Errorf("entra obo: %s scheme must be https (got %q)", label, u.Scheme)
	}
}

// isLoopbackHost reports whether host is localhost or a loopback IP literal.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
