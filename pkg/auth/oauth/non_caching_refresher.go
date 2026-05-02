// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// NonCachingRefresher is an oauth2.TokenSource that always performs a network
// refresh when Token() is called — it holds no internal token cache.
//
// This is the correct innermost source for a preemptive-refresh chain:
// the outer oauth2.ReuseTokenSource provides caching; the inner source must
// always refresh when asked so that one network round-trip occurs per
// preemptive window instead of looping indefinitely.
//
// It handles both standard OAuth 2.0 refresh (resource == "") and RFC 8707
// resource-indicator refresh (resource != "") in a single type.
//
// Token() is safe for concurrent use. mu serializes access to refreshToken,
// which is updated in place when the IdP rotates it.
//
// When the IdP omits a new refresh token the previous token is preserved so
// the session survives providers that do not rotate on every refresh.
type NonCachingRefresher struct {
	mu           sync.Mutex
	cfg          *oauth2.Config
	resource     string // RFC 8707 resource indicator; empty for standard OAuth 2.0
	refreshToken string
	httpClient   *http.Client
}

// NewNonCachingRefresher creates a NonCachingRefresher that refreshes using
// cfg and the given refresh token. resource is the RFC 8707 resource indicator;
// pass "" for standard OAuth 2.0 refresh.
func NewNonCachingRefresher(cfg *oauth2.Config, refreshToken, resource string) *NonCachingRefresher {
	return &NonCachingRefresher{
		cfg:          cfg,
		resource:     resource,
		refreshToken: refreshToken,
		httpClient:   oauthproto.NewHTTPClient(),
	}
}

// Token always performs a token-endpoint refresh. It updates the stored refresh
// token when the IdP rotates it so callers (e.g. PersistingTokenSource) can
// detect the change and persist it.
func (n *NonCachingRefresher) Token() (*oauth2.Token, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.refreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, n.httpClient)

	var (
		tok *oauth2.Token
		err error
	)
	if n.resource != "" {
		tok, err = n.refreshWithResource(ctx)
	} else {
		tok, err = n.refreshStandard(ctx)
	}
	if err != nil {
		return nil, err
	}

	if tok.RefreshToken == "" {
		tok.RefreshToken = n.refreshToken
	} else {
		n.refreshToken = tok.RefreshToken
	}

	slog.Debug("Token refreshed", "resource", n.resource)
	return tok, nil
}

// refreshStandard uses the library's native tokenRefresher path, which sends
// grant_type=refresh_token without the empty code= parameter that Exchange
// always appends. A fresh TokenSource is constructed on each call so there is
// no internal cache. Scopes are not sent explicitly; servers must preserve them
// per RFC 6749 §6 (MUST).
func (n *NonCachingRefresher) refreshStandard(ctx context.Context) (*oauth2.Token, error) {
	// Passing an always-expired token forces the ReuseTokenSource returned by
	// cfg.TokenSource to call the inner tokenRefresher immediately.
	ts := n.cfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: n.refreshToken,
		Expiry:       time.Unix(1, 0),
	})
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("token refresh failed: %w", err)
	}
	return tok, nil
}

// refreshWithResource uses cfg.Exchange with overridden grant_type and
// refresh_token parameters to send the RFC 8707 resource= indicator.
// golang.org/x/oauth2 has no native support for resource indicators, so the
// Exchange workaround is unavoidable here. The side-effect empty code=
// parameter from Exchange is acceptable: resource-indicator IdPs are required
// to dispatch on grant_type first and typically tolerate extra parameters.
// Scopes are sent explicitly because some resource-indicator IdPs do not
// preserve them when omitted despite the RFC 6749 §6 MUST requirement.
func (n *NonCachingRefresher) refreshWithResource(ctx context.Context) (*oauth2.Token, error) {
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("grant_type", "refresh_token"),
		oauth2.SetAuthURLParam("refresh_token", n.refreshToken),
		oauth2.SetAuthURLParam("resource", n.resource),
	}
	if len(n.cfg.Scopes) > 0 {
		opts = append(opts, oauth2.SetAuthURLParam("scope", strings.Join(n.cfg.Scopes, " ")))
	}
	tok, err := n.cfg.Exchange(ctx, "", opts...)
	if err != nil {
		return nil, fmt.Errorf("token refresh failed: %w", err)
	}
	return tok, nil
}
