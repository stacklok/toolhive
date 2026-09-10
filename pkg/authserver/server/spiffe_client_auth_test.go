// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// stubResolver is a SPIFFEClientResolver that is never called by these tests;
// it exists only to make "resolver configured" distinguishable from "resolver
// nil" for the client-authentication strategy under test.
func stubResolver(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
	panic("not called")
}

func TestSPIFFEClientAuthenticationStrategy(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	defaultClient := &fosite.DefaultClient{ID: "default-client"}
	defaultErr := errors.New("default strategy error")

	tests := []struct {
		name            string
		ctx             context.Context
		form            url.Values
		resolver        SPIFFEClientResolver
		wantErr         string
		wantDefaultCall bool
	}{
		{
			name:     "SPIFFE JWT assertion takes precedence over X.509 identity",
			ctx:      spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
				"client_assertion":      {"sensitive-assertion"},
			},
			wantErr: "SPIFFE JWT client authentication is not implemented",
		},
		{
			name:     "SPIFFE X.509 identity does not fall through",
			ctx:      spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			},
			wantErr: "SPIFFE X.509 client authentication is not implemented",
		},
		{
			name:     "SPIFFE JWT assertion is detected when not the first value",
			ctx:      context.Background(),
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {
					"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
					spiffeauth.SPIFFEJWTAssertionType,
				},
			},
			wantErr: "SPIFFE JWT client authentication is not implemented",
		},
		{
			name:     "RFC 7523 assertion delegates to default strategy",
			ctx:      context.Background(),
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			},
			wantDefaultCall: true,
		},
		{
			name:            "requests without SPIFFE credentials delegate to default strategy",
			ctx:             context.Background(),
			resolver:        stubResolver,
			form:            url.Values{"client_id": {"client"}},
			wantDefaultCall: true,
		},
		{
			name: "nil resolver delegates to default strategy even with a SPIFFE identity",
			ctx:  spiffeauth.ContextWithSPIFFEID(context.Background(), spiffeID),
			form: url.Values{
				"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
			},
			resolver:        nil,
			wantDefaultCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defaultCalled := false
			strategy := newSPIFFEClientAuthenticationStrategy(func(_ context.Context, _ *http.Request, _ url.Values) (fosite.Client, error) {
				defaultCalled = true
				return defaultClient, defaultErr
			}, tt.resolver)

			req := httptest.NewRequest("POST", "/oauth/token", nil).WithContext(tt.ctx)
			client, err := strategy(tt.ctx, req, tt.form)

			assert.Equal(t, tt.wantDefaultCall, defaultCalled)
			if tt.wantErr != "" {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfcErr)
				assert.Equal(t, tt.wantErr, rfcErr.HintField)
				if assertion := tt.form.Get("client_assertion"); assertion != "" {
					assert.NotContains(t, rfcErr.HintField, assertion)
				}
				assert.Nil(t, client)
				return
			}

			assert.ErrorIs(t, err, defaultErr)
			assert.Same(t, defaultClient, client)
		})
	}
}
