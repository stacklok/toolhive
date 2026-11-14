// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const testClientID = "client-under-test"

var (
	registeredClient = registration.MarkDCRIssued(&fosite.DefaultClient{ID: testClientID, Public: true})
	placeholder      = staticClientPlaceholder(&fosite.DefaultClient{ID: testClientID})
	// embedderClient has the connector-gateway shape: public, authorization_code.
	embedderClient = &fosite.DefaultClient{
		ID: testClientID, Public: true,
		GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"},
		RedirectURIs: []string{"http://127.0.0.1/cb"},
	}
)

// delegateShape returns the exact row shape every released binary wrote for a
// delegate client; mutate adjusts a copy into a near miss.
func delegateShape(mutate func(*fosite.DefaultClient)) *fosite.DefaultClient {
	c := &fosite.DefaultClient{
		ID: testClientID, Secret: []byte("hashed-secret"),
		GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
		Scopes:     []string{"openid"}, Audience: []string{"https://aud.example"},
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

type admissionSeed func(ctx context.Context, s Storage, c fosite.Client) error

func seedRegister(ctx context.Context, s Storage, c fosite.Client) error {
	return s.RegisterClient(ctx, c)
}

func seedReconcile(ctx context.Context, s Storage, c fosite.Client) error {
	return s.ReconcileConfiguredClient(ctx, c)
}

// rowWant is the expected class of a stored row and whether admission serves it
// when its ID is absent from, and present in, the configured list.
type rowWant struct {
	class          clientClass
	servedUnlisted bool
	servedListed   bool
}

// The admission decision for every class, listed and unlisted, with real stored
// rows read back through GetClient on both backends.
func TestClientAdmissionStorage_GetClient(t *testing.T) {
	t.Parallel()

	served := func(c clientClass) rowWant { return rowWant{c, true, true} }
	configured := rowWant{classConfigured, false, true}
	confidentialOIDCNoMethod := &fosite.DefaultOpenIDConnectClient{DefaultClient: delegateShape(nil)}

	tests := []struct {
		name   string
		client fosite.Client
		seed   admissionSeed
		want   rowWant
		// redis overrides want on the Redis backend where it differs.
		redis *rowWant
	}{
		{name: "placeholder", client: placeholder, seed: seedReconcile, want: rowWant{class: classPlaceholder}},
		{
			// Public, so only the stamp, not the legacy shape, makes it configured.
			name: "configured stamp", client: delegateShape(func(c *fosite.DefaultClient) { c.Public = true }),
			seed: seedReconcile, want: configured,
		},
		{name: "legacy delegate shape", client: delegateShape(nil), seed: seedRegister, want: configured},
		{name: "registered", client: registeredClient, seed: seedRegister, want: served(classRegistered)},
		{
			name: "connector-gateway shape (unmarked embedder client)", client: embedderClient,
			seed: seedRegister, want: served(classUnmarked),
		},
		{
			name: "near miss: public", client: delegateShape(func(c *fosite.DefaultClient) { c.Public = true }),
			seed: seedRegister, want: served(classUnmarked),
		},
		{
			name: "near miss: extra grant",
			client: delegateShape(func(c *fosite.DefaultClient) {
				c.GrantTypes = []string{oauthproto.GrantTypeTokenExchange, "refresh_token"}
			}),
			seed: seedRegister, want: served(classUnmarked),
		},
		{
			name: "near miss: has auth method",
			client: &fosite.DefaultOpenIDConnectClient{
				DefaultClient:           delegateShape(nil),
				TokenEndpointAuthMethod: "client_secret_basic",
			},
			seed: seedRegister, want: served(classUnmarked),
		},
		{
			// Redis stores no marker for an empty auth method, so the row reads
			// back as a bare DefaultClient of the legacy shape. Accepted difference.
			name: "OIDC client with no auth method", client: confidentialOIDCNoMethod,
			seed: seedRegister, want: served(classUnmarked), redis: &configured,
		},
	}
	for _, tt := range tests {
		for _, be := range configuredClientBackends() {
			want := tt.want
			if be.name == "redis" && tt.redis != nil {
				want = *tt.redis
			}
			for _, listed := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/listed=%t", tt.name, be.name, listed)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					ctx := context.Background()
					base := be.new(t)
					require.NoError(t, tt.seed(ctx, base, tt.client))
					stored, err := base.GetClient(ctx, testClientID)
					require.NoError(t, err)
					require.Equal(t, want.class, classifyClient(stored))

					var ids []string
					if listed {
						ids = []string{testClientID, testClientID} // repeats are accepted
					}
					admission, err := NewClientAdmissionStorage(base, ids)
					require.NoError(t, err)

					got, err := admission.GetClient(ctx, testClientID)
					if (listed && want.servedListed) || (!listed && want.servedUnlisted) {
						require.NoError(t, err)
						assert.Equal(t, testClientID, got.GetID())
						return
					}
					assert.Nil(t, got)
					assert.ErrorIs(t, err, ErrNotFound)
					assert.ErrorIs(t, err, fosite.ErrNotFound)
					_, err = base.GetClient(ctx, testClientID)
					require.NoError(t, err, "hiding a client must not delete its row")
				})
			}
		}
	}
}

func TestNewClientAdmissionStorage_Errors(t *testing.T) {
	t.Parallel()

	_, err := NewClientAdmissionStorage(nil, []string{"a"})
	require.Error(t, err)
	base := configuredClientBackends()[0].new(t)
	_, err = NewClientAdmissionStorage(base, []string{"a", ""})
	require.Error(t, err)
}

func TestClientStoreBelowCIMD(t *testing.T) {
	t.Parallel()

	base := configuredClientBackends()[0].new(t)
	admission, err := NewClientAdmissionStorage(base, nil)
	require.NoError(t, err)
	assert.Same(t, admission, ClientStoreBelowCIMD(NewCIMDShapeGuardStorage(admission)))
	assert.Same(t, base, ClientStoreBelowCIMD(NewCIMDShapeGuardStorage(base)), "no admission layer: innermost store")
}

func TestClientAdmissionStorage_ConsumeAssertionJWT(t *testing.T) {
	t.Parallel()

	admission, err := NewClientAdmissionStorage(configuredClientBackends()[0].new(t), nil)
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	require.NoError(t, admission.ConsumeAssertionJWT(t.Context(), "purpose", "issuer", "jti-1", exp))
	assert.ErrorIs(t, admission.ConsumeAssertionJWT(t.Context(), "purpose", "issuer", "jti-1", exp), fosite.ErrJTIKnown)
	require.NoError(t, admission.ConsumeAssertionJWT(t.Context(), "purpose", "issuer", "jti-2", exp))
}

// classifyClient must give the same class before storage and after a round trip
// through each backend, or admission would judge a different client than the
// one written. Rows the GetClient table already covers are not repeated here.
func TestClassifyClient(t *testing.T) {
	t.Parallel()

	delegate, err := registration.NewStaticDelegateClient(registration.Config{
		ID: testClientID, Secret: "a-delegate-secret-that-is-long-enough",
		GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
		Scopes:     []string{"openid"}, Audience: []string{"https://aud.example"},
	})
	require.NoError(t, err)
	plain, err := registration.NewConfidentialPlain(registration.Config{ID: testClientID, Secret: "a-plain-secret"})
	require.NoError(t, err)

	tests := []struct {
		name   string
		client fosite.Client
		seed   admissionSeed
		want   clientClass
	}{
		{"NewConfidentialPlain", plain, seedRegister, classRegistered},
		{"static delegate", delegate, seedReconcile, classConfigured},
	}
	for _, tt := range tests {
		for _, be := range configuredClientBackends() {
			t.Run(tt.name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, tt.want, classifyClient(tt.client), "before storage")
				s := be.new(t)
				require.NoError(t, tt.seed(context.Background(), s, tt.client))
				got, err := s.GetClient(context.Background(), testClientID)
				require.NoError(t, err)
				assert.Equal(t, tt.want, classifyClient(got), "after round trip")
			})
		}
	}
}

// Not parallel: it swaps the process-wide slog default to observe the log line.
func TestClientAdmissionStorage_WarnsOncePerDeniedClient(t *testing.T) { //nolint:paralleltest // swaps slog default
	logs := captureSlog(t)

	base := configuredClientBackends()[0].new(t)
	ids := []string{"denied-a", "denied-b"}
	for _, id := range ids {
		row := delegateShape(nil)
		row.ID = id
		require.NoError(t, base.RegisterClient(context.Background(), row))
	}
	admission, err := NewClientAdmissionStorage(base, nil)
	require.NoError(t, err)
	for range 3 {
		for _, id := range ids {
			_, _ = admission.GetClient(context.Background(), id)
		}
	}
	for _, id := range ids {
		assert.Len(t, logs.warnsFor(id), 1, "one WARN per denied ID: %s", id)
	}
}

func TestIsLegacyDelegateShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client fosite.Client
		want   bool
	}{
		{"delegate shape", delegateShape(nil), true},
		{"no grants", delegateShape(func(c *fosite.DefaultClient) { c.GrantTypes = nil }), false},
		{"DCR-marked", registration.MarkDCRIssued(delegateShape(nil)), false},
		{"placeholder", placeholder, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLegacyDelegateShape(tt.client))
		})
	}
}
