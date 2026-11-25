// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package useragent sets the vMCP User-Agent on requests vMCP sends to its
// backends, so backend proxies can attribute those requests to vMCP instead of
// seeing Go's default Go-http-client agent.
package useragent

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/versions"
)

// component is the component name vMCP reports in its User-Agent. It must keep
// the "vmcp" token: pkg/usagemetrics attributes a sessionless call to vmcp when
// its User-Agent contains both "toolhive" and "vmcp".
const component = "vmcp"

// Value returns the User-Agent vMCP sends to backends.
func Value() string {
	return versions.GetComponentUserAgent(component)
}

// RoundTripper sets the vMCP User-Agent on requests that carry none.
//
// A User-Agent already on the request is kept. Place this stage inside
// (closer to the wire than) the header-forward stage so a User-Agent an
// operator forwards through passthroughHeaders or headerForward reaches the
// backend unchanged.
type RoundTripper struct {
	base  http.RoundTripper
	value string
}

// NewRoundTripper wraps base with a stage that sets the vMCP User-Agent.
func NewRoundTripper(base http.RoundTripper) *RoundTripper {
	return &RoundTripper{base: base, value: Value()}
}

// RoundTrip implements http.RoundTripper. It clones the request before setting
// the header, per the RoundTripper contract.
func (t *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") != "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set("User-Agent", t.value)
	return t.base.RoundTrip(clone)
}

// CloseIdleConnections forwards to the wrapped RoundTripper so it reaches the
// concrete *http.Transport at the bottom of the chain.
func (t *RoundTripper) CloseIdleConnections() {
	networking.ForwardCloseIdle(t.base)
}

var _ networking.IdleConnectionCloser = (*RoundTripper)(nil)
