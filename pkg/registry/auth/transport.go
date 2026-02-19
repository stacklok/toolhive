// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"net/http"
)

// Transport wraps an http.RoundTripper to add OAuth authentication headers.
type Transport struct {
	Base   http.RoundTripper
	Source TokenSource
}

// RoundTrip executes a single HTTP transaction with authentication.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Source == nil {
		return t.base().RoundTrip(req)
	}

	// Get token from source
	token, err := t.Source.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	// If token is empty, pass through without auth
	if token == "" {
		return t.base().RoundTrip(req)
	}

	// Clone request and add authorization header
	clonedReq := req.Clone(req.Context())
	clonedReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	return t.base().RoundTrip(clonedReq)
}

// base returns the base RoundTripper, defaulting to http.DefaultTransport.
func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// WrapTransport wraps an http.RoundTripper with authentication support.
// If source is nil, returns the base transport unchanged.
func WrapTransport(base http.RoundTripper, source TokenSource) http.RoundTripper {
	if source == nil {
		return base
	}
	return &Transport{
		Base:   base,
		Source: source,
	}
}
