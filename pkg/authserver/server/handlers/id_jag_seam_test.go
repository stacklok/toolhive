// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	seamForeignResource = "https://mcp.other-as.example.net/mcp"
	seamDPoPValue       = "seam-dpop-proof-value"
)

// seamProvider answers the token endpoint with a prepared request and response
// and counts the calls that follow. Error writing goes through the embedded
// real provider.
type seamProvider struct {
	fosite.OAuth2Provider
	request  *fosite.AccessRequest
	response fosite.AccessResponder

	responses int
	written   int
}

func (p *seamProvider) NewAccessRequest(context.Context, *http.Request, fosite.Session) (fosite.AccessRequester, error) {
	return p.request, nil
}

func (p *seamProvider) NewAccessResponse(context.Context, fosite.AccessRequester) (fosite.AccessResponder, error) {
	p.responses++
	return p.response, nil
}

func (p *seamProvider) WriteAccessResponse(_ context.Context, w http.ResponseWriter, _ fosite.AccessRequester, _ fosite.AccessResponder) {
	p.written++
	w.WriteHeader(http.StatusOK)
}

type seamCase struct {
	grantType     string
	requestedType string
	resource      string
	dpop          []string
	clientID      string
	granted       []string
	issuedType    string
}

func (c seamCase) request() *fosite.AccessRequest {
	form := url.Values{}
	if c.requestedType != "" {
		form.Set("requested_token_type", c.requestedType)
	}
	if c.resource != "" {
		form.Set("resource", c.resource)
	}
	req := fosite.NewAccessRequest(nil)
	req.GrantTypes = fosite.Arguments{c.grantType}
	req.Form = form
	if c.clientID != "" {
		req.Client = &fosite.DefaultClient{ID: c.clientID}
	}
	for _, aud := range c.granted {
		req.GrantAudience(aud)
	}
	return req
}

// run drives TokenHandler against a seamProvider built from c.
func (c seamCase) run(t *testing.T) (*seamProvider, *httptest.ResponseRecorder) {
	t.Helper()

	handler, _, _ := handlerTestSetup(t)
	response := fosite.NewAccessResponse()
	if c.issuedType != "" {
		response.SetExtra("issued_token_type", c.issuedType)
	}
	provider := &seamProvider{OAuth2Provider: handler.provider, request: c.request(), response: response}
	handler.provider = provider

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, value := range c.dpop {
		req.Header.Add("DPoP", value)
	}
	rec := httptest.NewRecorder()
	handler.TokenHandler(rec, req)
	return provider, rec
}

func TestTokenHandler_IDJAGRequests(t *testing.T) {
	t.Parallel()

	idJAG := seamCase{
		grantType: oauthproto.GrantTypeTokenExchange, requestedType: oauthproto.TokenTypeIDJAG,
		resource: seamForeignResource, issuedType: oauthproto.TokenTypeIDJAG,
	}
	plainExchange := seamCase{
		grantType: oauthproto.GrantTypeTokenExchange, resource: seamForeignResource,
		issuedType: oauthproto.TokenTypeAccessToken,
	}
	with := func(base seamCase, mutate func(*seamCase)) seamCase {
		mutate(&base)
		return base
	}

	tests := []struct {
		name          string
		in            seamCase
		wantStatus    int
		wantCode      string
		wantBody      string
		wantResponses int
		wantWritten   int
	}{
		{
			name:       "DPoP header on an ID-JAG request is refused with the exact reason",
			in:         with(idJAG, func(c *seamCase) { c.dpop = []string{seamDPoPValue} }),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"invalid_request","error_description":"` + server.ReasonDPoPNotSupported + `"}`,
		},
		{
			name:       "DPoP header on a plain token exchange is unaffected",
			in:         with(plainExchange, func(c *seamCase) { c.dpop = []string{seamDPoPValue}; c.resource = "" }),
			wantStatus: http.StatusOK, wantResponses: 1, wantWritten: 1,
		},
		{
			name:       "ID-JAG resource outside AllowedAudiences is not refused by the resource check",
			in:         idJAG,
			wantStatus: http.StatusOK, wantResponses: 1, wantWritten: 1,
		},
		{
			name:       "plain token exchange with the same resource is still refused",
			in:         plainExchange,
			wantStatus: http.StatusBadRequest, wantCode: "invalid_target",
		},
		{
			name:       "ID-JAG request answered with another token type fails closed",
			in:         with(idJAG, func(c *seamCase) { c.issuedType = oauthproto.TokenTypeAccessToken }),
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantResponses: 1,
		},
		{
			name:       "ID-JAG request answered without an issued_token_type fails closed",
			in:         with(idJAG, func(c *seamCase) { c.issuedType = "" }),
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantResponses: 1,
		},
		{
			name:       "ID-JAG request with a granted audience fails closed",
			in:         with(idJAG, func(c *seamCase) { c.granted = []string{seamForeignResource} }),
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantResponses: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, rec := tt.in.run(t)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantResponses, provider.responses, "NewAccessResponse calls")
			assert.Equal(t, tt.wantWritten, provider.written, "WriteAccessResponse calls")
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, rec.Body.String())
			}
			if tt.wantCode != "" {
				var body map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tt.wantCode, body["error"])
			}
		})
	}
}
