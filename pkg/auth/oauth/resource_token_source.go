// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"golang.org/x/oauth2"
)

// resourceTokenSource wraps a NonCachingRefresher with an internal token cache
// and adds the resource parameter to token refresh requests per RFC 8707.
// Token() returns the cached token when still valid and delegates to the
// inner NonCachingRefresher only when a refresh is needed.
type resourceTokenSource struct {
	ncr   *NonCachingRefresher
	token *oauth2.Token
}

// NewResourceTokenSource creates a token source that includes the resource parameter
// in all token requests, including refresh requests.
// The resource parameter must be non-empty (caller should check before calling).
func NewResourceTokenSource(config *oauth2.Config, token *oauth2.Token, resource string) oauth2.TokenSource {
	return &resourceTokenSource{
		ncr:   NewNonCachingRefresher(config, token.RefreshToken, resource),
		token: token,
	}
}

// Token returns a valid token, refreshing it if necessary.
// When refreshing, it delegates to NonCachingRefresher which adds the resource
// parameter per RFC 8707.
func (r *resourceTokenSource) Token() (*oauth2.Token, error) {
	if r.token.Valid() {
		return r.token, nil
	}

	newToken, err := r.ncr.Token()
	if err != nil {
		return nil, err
	}

	r.token = newToken
	return newToken, nil
}
