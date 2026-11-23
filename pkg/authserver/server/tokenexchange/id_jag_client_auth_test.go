// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const clientAuthRequiredDescription = "client_authentication_required: The ID-JAG grant requires client authentication."

func TestIDJAGHandler_RefusesUnauthenticatedClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client fosite.Client
	}{
		{name: "public client", client: &fosite.DefaultClient{ID: idJAGTestClientID, Public: true}},
		{name: "nil client"},
		{name: "placeholder client with an empty ID", client: &fosite.DefaultClient{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator := &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
			consumer := &recordingAssertionConsumer{}
			h := newIDJAGTestHandler(t, validator, consumer)
			req := newIDJAGRequest(t, newTestJWKS(t), "")
			req.Client = tt.client

			err := h.HandleTokenEndpointRequest(context.Background(), req)

			requireRefusalStatus(t, err, http.StatusUnauthorized, "invalid_client", clientAuthRequiredDescription)
			assert.Zero(t, validator.calls, "the assertion must not be validated")
			assert.Empty(t, consumer.key, "the assertion must not be consumed")
		})
	}
}

func TestIDJAGHandler_checkClientBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		client  fosite.Client
		wantErr string
	}{
		{name: "nil client fails closed", wantErr: "invalid_client"},
		{name: "placeholder client with an empty ID fails closed", client: &fosite.DefaultClient{}, wantErr: "invalid_client"},
		{name: "public client fails closed", client: &fosite.DefaultClient{ID: idJAGTestClientID, Public: true}, wantErr: "invalid_client"},
		{
			name:    "client other than the assertion's",
			client:  &fosite.DefaultClient{ID: "some-other-client"},
			wantErr: "invalid_grant",
		},
		{name: "matching client", client: &fosite.DefaultClient{ID: idJAGTestClientID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{}, &recordingAssertionConsumer{})
			req := newIDJAGRequest(t, newTestJWKS(t), "")
			req.Client = tt.client

			err := h.checkClientBinding(req, validIDJAGClaims())

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			var rfcErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, tt.wantErr, rfcErr.ErrorField)
			if tt.wantErr == "invalid_client" {
				assert.Equal(t, "client_authentication_required: The ID-JAG grant requires client authentication.",
					rfcErr.GetDescription())
			}
		})
	}
}

func TestIDJAGHandler_CanSkipClientAuth(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	idJAG := signAssertionWithType(t, tj, oauthproto.JWTTypeIDJAG)
	plain := signAssertionWithType(t, tj, nil)
	idJAGHandler := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{}, &recordingAssertionConsumer{})
	plainHandler := newTestJWTBearerHandler(t, testIssuanceDeps(&testJWTBearerAssertionValidator{}, nil))

	const assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	tests := []struct {
		name      string
		extra     url.Values
		basicUser string
		basicPass string
		noReqCtx  bool
		wantIDJAG bool
		wantPlain bool
	}{
		{name: "no credentials", wantIDJAG: true, wantPlain: true},
		{name: "client_assertion_type with a blank first value", extra: url.Values{
			"client_assertion_type": {"", assertionType},
		}},
		{name: "client_id with a blank first value", extra: url.Values{"client_id": {"", "c"}}},
		{name: "blank form credential values", extra: url.Values{
			"client_id": {""}, "client_secret": {""}, "client_assertion": {""}, "client_assertion_type": {""},
		}, wantIDJAG: true, wantPlain: true},
		{name: "client_id", extra: url.Values{"client_id": {"c"}}},
		{name: "client_secret", extra: url.Values{"client_secret": {"s"}}},
		{name: "client_assertion", extra: url.Values{"client_assertion": {"a"}}},
		{name: "client_assertion_type", extra: url.Values{"client_assertion_type": {assertionType}}},
		{name: "HTTP Basic", basicUser: "c", basicPass: "s"},
		{name: "HTTP Basic with empty username", basicPass: "s"},
		{name: "no HTTP request in context", noReqCtx: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if !tt.noReqCtx {
				ctx = contextWithHTTPRequest(t, tt.basicUser, tt.basicPass)
			}
			form := func(assertion string) url.Values {
				f := url.Values{"assertion": {assertion}}
				for k, v := range tt.extra {
					f[k] = v
				}
				return f
			}

			assert.Equal(t, tt.wantIDJAG, idJAGHandler.CanSkipClientAuth(ctx, newJWTBearerRequest(form(idJAG))))
			assert.Equal(t, tt.wantPlain, plainHandler.CanSkipClientAuth(ctx, newJWTBearerRequest(form(plain))))
			// The plain handler never skips for an ID-JAG, whatever else the
			// request carries.
			assert.False(t, plainHandler.CanSkipClientAuth(ctx, newJWTBearerRequest(form(idJAG))))
		})
	}
}
