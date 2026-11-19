// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
)

const (
	testTenant       = "contoso.onmicrosoft.com"
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret-VALUE"
	testUserToken    = "test.user.assertion-SECRET-VALUE"
	testAudience     = "api://backend/.default"
	testAccessToken  = "ISSUED-BACKEND-ACCESS-TOKEN"

	wireGrantType          = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	wireRequestedTokenUse  = "on_behalf_of"
	wireTokenEndpointPath  = "/contoso.onmicrosoft.com/oauth2/v2.0/token"
	successResponseExpires = 3599
)

// newTestExchanger returns an Exchanger pointed at authority (an httptest URL).
func newTestExchanger(t *testing.T, authority string) *entra.Exchanger {
	t.Helper()
	ex, err := entra.New(entra.Config{
		TenantID:     testTenant,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		Authority:    authority,
	})
	require.NoError(t, err)
	return ex
}

// captured records what the stub token endpoint observed. It is delivered over a
// buffered channel so the assertions read it with a happens-before guarantee
// (race-detector clean) rather than racing the server goroutine.
type captured struct {
	method      string
	path        string
	contentType string
	form        url.Values
}

func TestExchanger_ExchangeToken_Success(t *testing.T) {
	t.Parallel()

	ch := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ch <- captured{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			form:        r.PostForm,
		}
		w.Header().Set("Content-Type", "application/json")
		// Build the body from the constant so the wire value and the expiry
		// assertion below share a single source of truth.
		_, _ = io.WriteString(w, fmt.Sprintf(
			`{"token_type":"Bearer","scope":%q,"expires_in":%d,"ext_expires_in":%d,"access_token":%q}`,
			testAudience, successResponseExpires, successResponseExpires, testAccessToken))
	}))
	t.Cleanup(srv.Close)

	ex := newTestExchanger(t, srv.URL)

	start := time.Now()
	tok, err := ex.ExchangeToken(t.Context(), testUserToken, testAudience)
	require.NoError(t, err)

	assert.Equal(t, testAccessToken, tok.AccessToken)
	assert.WithinDuration(t, start.Add(successResponseExpires*time.Second), tok.Expiry, 5*time.Second,
		"expiry must be derived from expires_in")

	got := <-ch
	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, wireTokenEndpointPath, got.path)
	assert.Contains(t, got.contentType, "application/x-www-form-urlencoded")

	// The exact Entra OBO request shape.
	assert.Equal(t, wireGrantType, got.form.Get("grant_type"))
	assert.Equal(t, testClientID, got.form.Get("client_id"))
	assert.Equal(t, testClientSecret, got.form.Get("client_secret"))
	assert.Equal(t, testUserToken, got.form.Get("assertion"))
	assert.Equal(t, testAudience, got.form.Get("scope"), "audience argument is sent as the scope parameter")
	assert.Equal(t, wireRequestedTokenUse, got.form.Get("requested_token_use"))
}

func TestExchanger_Exchange_ReturnsAccessTokenOnly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token_type":"Bearer","expires_in":3599,"access_token":"`+testAccessToken+`"}`)
	}))
	t.Cleanup(srv.Close)

	ex := newTestExchanger(t, srv.URL)
	token, err := ex.Exchange(t.Context(), testUserToken, testAudience)
	require.NoError(t, err)
	assert.Equal(t, testAccessToken, token)
}

func TestExchanger_Exchange_PropagatesError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"AADSTS50013"}`)
	}))
	t.Cleanup(srv.Close)

	ex := newTestExchanger(t, srv.URL)
	token, err := ex.Exchange(t.Context(), testUserToken, testAudience)
	require.Error(t, err)
	assert.Empty(t, token)
	assert.ErrorIs(t, err, entra.ErrInvalidGrant)
}

func TestExchanger_ExchangeToken_NoExpiryWhenExpiresInAbsent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token_type":"Bearer","access_token":"`+testAccessToken+`"}`)
	}))
	t.Cleanup(srv.Close)

	ex := newTestExchanger(t, srv.URL)
	tok, err := ex.ExchangeToken(t.Context(), testUserToken, testAudience)
	require.NoError(t, err)
	assert.Equal(t, testAccessToken, tok.AccessToken)
	assert.True(t, tok.Expiry.IsZero(), "missing expires_in yields a zero (non-cacheable) expiry")
}

func TestExchanger_ExchangeToken_Failures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		body         string
		wantKind     entra.ErrorKind
		wantSentinel error // nil means: matches none of the sentinels
		wantClaims   string
	}{
		{
			name:         "interaction required via OAuth error and claims challenge",
			status:       http.StatusBadRequest,
			body:         `{"error":"interaction_required","error_description":"AADSTS50079: proof-up required","claims":"{\"access_token\":{\"acrs\":{\"essential\":true}}}"}`,
			wantKind:     entra.KindInteractionRequired,
			wantSentinel: entra.ErrInteractionRequired,
			wantClaims:   `{"access_token":{"acrs":{"essential":true}}}`,
		},
		{
			name:         "interaction required via AADSTS code without OAuth error",
			status:       http.StatusBadRequest,
			body:         `{"error":"invalid_grant","error_description":"AADSTS50076: MFA required","error_codes":[50076]}`,
			wantKind:     entra.KindInteractionRequired,
			wantSentinel: entra.ErrInteractionRequired,
		},
		{
			name:         "invalid grant",
			status:       http.StatusBadRequest,
			body:         `{"error":"invalid_grant","error_description":"AADSTS50013: assertion is expired"}`,
			wantKind:     entra.KindInvalidGrant,
			wantSentinel: entra.ErrInvalidGrant,
		},
		{
			name:         "invalid client",
			status:       http.StatusUnauthorized,
			body:         `{"error":"invalid_client","error_description":"AADSTS7000215: invalid client secret"}`,
			wantKind:     entra.KindInvalidClient,
			wantSentinel: entra.ErrInvalidClient,
		},
		{
			name:         "unauthorized client maps to invalid client",
			status:       http.StatusBadRequest,
			body:         `{"error":"unauthorized_client","error_description":"AADSTS700016: app not found"}`,
			wantKind:     entra.KindInvalidClient,
			wantSentinel: entra.ErrInvalidClient,
		},
		{
			name:         "transient 5xx with JSON body",
			status:       http.StatusServiceUnavailable,
			body:         `{"error":"temporarily_unavailable","error_description":"AADSTS90033: transient error"}`,
			wantKind:     entra.KindTransient,
			wantSentinel: entra.ErrTransient,
		},
		{
			name:         "transient 5xx with non-JSON body",
			status:       http.StatusBadGateway,
			body:         `<html><body>502 Bad Gateway</body></html>`,
			wantKind:     entra.KindTransient,
			wantSentinel: entra.ErrTransient,
		},
		{
			name:         "unrecognised 4xx error is permanent unknown",
			status:       http.StatusBadRequest,
			body:         `{"error":"some_unmodelled_error","error_description":"AADSTS99999: brand new"}`,
			wantKind:     entra.KindUnknown,
			wantSentinel: nil,
		},
		{
			name:         "2xx body missing access_token",
			status:       http.StatusOK,
			body:         `{"token_type":"Bearer","expires_in":3599}`,
			wantKind:     entra.KindUnknown,
			wantSentinel: nil,
		},
		{
			name:         "2xx body missing token_type",
			status:       http.StatusOK,
			body:         `{"access_token":"x","expires_in":3599}`,
			wantKind:     entra.KindUnknown,
			wantSentinel: nil,
		},
		{
			name:         "2xx body with non-Bearer token_type",
			status:       http.StatusOK,
			body:         `{"access_token":"x","token_type":"mac","expires_in":3599}`,
			wantKind:     entra.KindUnknown,
			wantSentinel: nil,
		},
		{
			name:         "malformed 2xx JSON body",
			status:       http.StatusOK,
			body:         `{not valid json`,
			wantKind:     entra.KindUnknown,
			wantSentinel: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(srv.Close)

			ex := newTestExchanger(t, srv.URL)
			tok, err := ex.ExchangeToken(t.Context(), testUserToken, testAudience)

			require.Error(t, err)
			assert.Empty(t, tok.AccessToken)

			var exErr *entra.ExchangeError
			require.ErrorAs(t, err, &exErr)
			assert.Equal(t, tt.wantKind, exErr.Kind)
			assert.Equal(t, tt.status, exErr.HTTPStatus)
			assert.Equal(t, tt.wantClaims, exErr.Claims)

			if tt.wantSentinel != nil {
				assert.ErrorIs(t, err, tt.wantSentinel)
			} else {
				// KindUnknown must not masquerade as any classified failure.
				for _, s := range []error{
					entra.ErrInteractionRequired, entra.ErrInvalidGrant,
					entra.ErrInvalidClient, entra.ErrTransient,
				} {
					assert.NotErrorIs(t, err, s)
				}
			}

			// No secret material may appear in the error string.
			assert.NotContains(t, err.Error(), testUserToken)
			assert.NotContains(t, err.Error(), testClientSecret)
			assert.NotContains(t, err.Error(), testAccessToken)
		})
	}
}

func TestExchanger_ExchangeToken_CapsResponseBody(t *testing.T) {
	t.Parallel()

	// A >1 MiB body whose valid-JSON prefix is followed by megabytes of padding.
	// The 1 MiB LimitReader truncates it mid-stream, so the decode fails and the
	// result is classified as a malformed (unknown) response rather than hanging
	// or exhausting memory.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+testAccessToken+`","expires_in":3599,"pad":"`)
		pad := make([]byte, 2<<20) // 2 MiB, past the 1 MiB cap
		for i := range pad {
			pad[i] = 'A'
		}
		_, _ = w.Write(pad)
		_, _ = io.WriteString(w, `"}`)
	}))
	t.Cleanup(srv.Close)

	ex := newTestExchanger(t, srv.URL)
	_, err := ex.ExchangeToken(t.Context(), testUserToken, testAudience)

	require.Error(t, err)
	var exErr *entra.ExchangeError
	require.ErrorAs(t, err, &exErr)
	assert.Equal(t, entra.KindUnknown, exErr.Kind, "a truncated body is a malformed response")
	assert.NotContains(t, err.Error(), testAccessToken)
}

func TestExchanger_ExchangeToken_TransientOnTransportFailure(t *testing.T) {
	t.Parallel()

	// A server that is closed immediately forces a dial failure.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	authority := srv.URL
	srv.Close()

	ex := newTestExchanger(t, authority)
	_, err := ex.ExchangeToken(t.Context(), testUserToken, testAudience)

	require.Error(t, err)
	assert.ErrorIs(t, err, entra.ErrTransient)
	assert.NotContains(t, err.Error(), testUserToken)
	assert.NotContains(t, err.Error(), testClientSecret)
}

func TestExchanger_ExchangeToken_ContextCancelledPropagates(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+testAccessToken+`","expires_in":3599}`)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancelled before the call begins

	ex := newTestExchanger(t, srv.URL)
	_, err := ex.ExchangeToken(ctx, testUserToken, testAudience)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "caller cancellation surfaces, not a transient classification")
	assert.NotErrorIs(t, err, entra.ErrTransient)
}

func TestNew_EnforcesRedirectGuardOnInjectedClient(t *testing.T) {
	t.Parallel()

	var redirectFollowed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/leaked", func(w http.ResponseWriter, _ *http.Request) {
		// Reached only if the 307 was followed, replaying the secret-bearing body.
		redirectFollowed.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"leaked","token_type":"Bearer","expires_in":3599}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/leaked", http.StatusTemporaryRedirect) // 307 preserves the POST body
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Inject a client that, left alone, would follow redirects (nil CheckRedirect).
	ex, err := entra.New(entra.Config{
		TenantID:     testTenant,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		Authority:    srv.URL,
		HTTPClient:   &http.Client{},
	})
	require.NoError(t, err)

	_, err = ex.ExchangeToken(t.Context(), testUserToken, testAudience)
	require.Error(t, err) // the 307 itself is not a usable token response
	assert.False(t, redirectFollowed.Load(),
		"New must enforce the no-redirect guard even on a caller-injected client")
}

func TestExchanger_ExchangeToken_RejectsEmptyArguments(t *testing.T) {
	t.Parallel()

	// No server is contacted; these fail before any request is built.
	ex := newTestExchanger(t, "https://login.microsoftonline.com")

	_, err := ex.ExchangeToken(t.Context(), "", testAudience)
	require.Error(t, err)

	_, err = ex.ExchangeToken(t.Context(), testUserToken, "")
	require.Error(t, err)
}
