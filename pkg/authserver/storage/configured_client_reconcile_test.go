// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/rsa"
	"log/slog"
	"math/big"
	"sync"
	"testing"

	"github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const reconcileTestClientID = "client-1"

// configuredClientBackend builds a fresh bare backend (no SPIFFE decorator),
// so seeding a row never goes through the decorator's own reserved-ID guard.
type configuredClientBackend struct {
	name string
	new  func(t *testing.T) Storage
}

func configuredClientBackends() []configuredClientBackend {
	return []configuredClientBackend{
		{name: "memory", new: func(t *testing.T) Storage {
			t.Helper()
			s := NewMemoryStorage()
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
		{name: "redis", new: func(t *testing.T) Storage {
			t.Helper()
			s, _ := newTestRedisStorage(t)
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	}
}

func testDelegateClient(t *testing.T, id, secret string, scopes, audience []string) fosite.Client {
	t.Helper()
	client, err := registration.NewStaticDelegateClient(registration.Config{
		ID: id, Secret: secret, GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
		Scopes: scopes, Audience: audience,
	})
	require.NoError(t, err)
	return client
}

// testPlaceholder builds the inert placeholder the SPIFFE decorator durably
// claims for a static SPIFFE client, with the given association identity.
func testPlaceholder(t *testing.T, id, identity string, scopes, resources []string) fosite.Client {
	t.Helper()
	spiffe, err := registration.NewSPIFFEClient(id, scopes, []string{"https://api.example.com"}, resources)
	require.NoError(t, err)
	return staticClientPlaceholder(testIdentityClient{SPIFFEClient: spiffe, identityFingerprint: identity})
}

func TestReconcileConfiguredClient_SharedBehaviour(t *testing.T) {
	t.Parallel()

	id := reconcileTestClientID
	audience := []string{"https://mcp.example"}
	delegate := func(t *testing.T, secret string, scopes ...string) fosite.Client {
		t.Helper()
		return testDelegateClient(t, id, secret, scopes, audience)
	}
	placeholder := func(t *testing.T, identity string, scopes, resources []string) fosite.Client {
		t.Helper()
		return testPlaceholder(t, id, identity, scopes, resources)
	}

	tests := []struct {
		name string
		// dcrSeed registers a DCR-issued client at id before the reconciles.
		dcrSeed bool
		// clients are reconciled in order; all but the last must succeed.
		clients      func(t *testing.T) []fosite.Client
		wantRefused  bool
		wantErr      string
		wantScopes   []string
		wantAudience []string
		wantGrants   []string
		wantSecret   string
		wantOldGone  string
		wantPublic   bool
		wantReserved bool
		wantDCR      bool
		// wantResources and wantIdentity are asserted for reserved rows only.
		wantResources []string
		wantIdentity  string
	}{
		{
			name: "delegate with changed scopes and audience is replaced",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					delegate(t, "s", "a"),
					testDelegateClient(t, id, "s", []string{"a", "b"}, []string{"https://other.example"}),
				}
			},
			wantScopes:   []string{"a", "b"},
			wantAudience: []string{"https://other.example"},
		},
		{
			name: "delegate secret rotation with a policy change authenticates with the new secret",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{delegate(t, "old-secret", "a"), delegate(t, "new-secret", "a", "b")}
			},
			wantScopes:  []string{"a", "b"},
			wantSecret:  "new-secret",
			wantOldGone: "old-secret",
		},
		{
			name: "delegate with a changed grant type is replaced",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					delegate(t, "s", "a"),
					&fosite.DefaultClient{
						ID: id, GrantTypes: []string{"client_credentials"}, Scopes: []string{"a"}, Audience: audience,
					},
				}
			},
			wantScopes: []string{"a"},
			wantGrants: []string{"client_credentials"},
		},
		{
			name: "confidential to public flip is refused and the stored row is unchanged",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					delegate(t, "s", "a"),
					&fosite.DefaultClient{
						ID: id, Public: true, GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
						Scopes: []string{"a", "b"}, Audience: audience,
					},
				}
			},
			wantRefused: true,
			wantErr:     "public and confidential clients cannot replace each other",
			wantScopes:  []string{"a"},
			wantGrants:  []string{oauthproto.GrantTypeTokenExchange},
		},
		{
			name: "confidential delegate over a public configured client is refused and the stored row is unchanged",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					&fosite.DefaultClient{
						ID: id, Public: true, GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
						Scopes: []string{"a"}, Audience: audience,
					},
					delegate(t, "s", "a", "b"),
				}
			},
			wantRefused: true,
			wantErr:     "public and confidential clients cannot replace each other",
			wantPublic:  true,
			wantScopes:  []string{"a"},
			wantGrants:  []string{oauthproto.GrantTypeTokenExchange},
		},
		{
			name: "last writer wins across differing policies",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{delegate(t, "s", "a"), delegate(t, "s", "a", "b"), delegate(t, "s", "a")}
			},
			wantScopes: []string{"a"},
		},
		{
			name: "placeholder with changed scopes is refused and the stored row is unchanged",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					placeholder(t, "id-a", []string{"openid"}, nil),
					placeholder(t, "id-a", []string{"openid", "profile"}, nil),
				}
			},
			wantRefused:  true,
			wantErr:      "SPIFFE-reserved clients must match exactly",
			wantScopes:   []string{"openid"},
			wantReserved: true,
			wantIdentity: "id-a",
		},
		{
			name: "placeholder with changed resources is refused and the stored row is unchanged",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					placeholder(t, "id-a", []string{"openid"}, []string{"https://res-a.example"}),
					placeholder(t, "id-a", []string{"openid"}, []string{"https://res-b.example"}),
				}
			},
			wantRefused:   true,
			wantErr:       "SPIFFE-reserved clients must match exactly",
			wantScopes:    []string{"openid"},
			wantReserved:  true,
			wantIdentity:  "id-a",
			wantResources: []string{"https://res-a.example"},
		},
		{
			name: "placeholder with a different identity is refused",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					placeholder(t, "id-a", []string{"openid"}, nil),
					placeholder(t, "id-b", []string{"openid"}, nil),
				}
			},
			wantRefused:  true,
			wantErr:      "SPIFFE-reserved clients must match exactly",
			wantScopes:   []string{"openid"},
			wantReserved: true,
			wantIdentity: "id-a",
		},
		{
			name: "unchanged placeholder is idempotent",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{
					placeholder(t, "id-a", []string{"openid"}, nil),
					placeholder(t, "id-a", []string{"openid"}, nil),
				}
			},
			wantScopes:   []string{"openid"},
			wantReserved: true,
			wantIdentity: "id-a",
		},
		{
			name: "delegate over a placeholder with an empty identity is refused",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{placeholder(t, "", []string{"a"}, nil), delegate(t, "s", "a")}
			},
			wantRefused:  true,
			wantErr:      "stored row is a SPIFFE-reserved placeholder",
			wantScopes:   []string{"a"},
			wantReserved: true,
		},
		{
			name: "placeholder with an empty identity over a delegate is refused",
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{delegate(t, "s", "a"), placeholder(t, "", []string{"a"}, nil)}
			},
			wantRefused: true,
			wantErr:     "incoming client is SPIFFE-reserved",
			wantScopes:  []string{"a"},
			wantGrants:  []string{oauthproto.GrantTypeTokenExchange},
		},
		{
			name:    "DCR-issued row is refused",
			dcrSeed: true,
			clients: func(t *testing.T) []fosite.Client {
				t.Helper()
				return []fosite.Client{delegate(t, "s", "a")}
			},
			wantRefused: true,
			wantErr:     "DCR-issued",
			wantPublic:  true,
			wantDCR:     true,
		},
	}

	for _, backend := range configuredClientBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					ctx := t.Context()
					s := backend.new(t)
					if tt.dcrSeed {
						require.NoError(t, s.RegisterClient(ctx, dcrClient(t, id)))
					}

					clients := tt.clients(t)
					for _, c := range clients[:len(clients)-1] {
						require.NoError(t, s.ReconcileConfiguredClient(ctx, c))
					}
					err := s.ReconcileConfiguredClient(ctx, clients[len(clients)-1])
					if tt.wantRefused {
						require.ErrorIs(t, err, ErrAlreadyExists)
						assert.Contains(t, err.Error(), tt.wantErr)
					} else {
						require.NoError(t, err)
					}

					got, err := s.GetClient(ctx, id)
					require.NoError(t, err)
					assert.Equal(t, tt.wantReserved, isReservedPlaceholder(got), "reserved kind of the stored row")
					assert.Equal(t, tt.wantPublic, got.IsPublic())
					assert.Equal(t, tt.wantDCR, registration.DCRIssued(got))
					if tt.wantScopes != nil {
						assert.ElementsMatch(t, tt.wantScopes, []string(got.GetScopes()))
					}
					if tt.wantAudience != nil {
						assert.ElementsMatch(t, tt.wantAudience, []string(got.GetAudience()))
					}
					if tt.wantGrants != nil {
						assert.ElementsMatch(t, tt.wantGrants, []string(got.GetGrantTypes()))
					}
					if tt.wantSecret != "" {
						require.NoError(t, registration.SHA256Hasher.Compare(ctx, got.GetHashedSecret(), []byte(tt.wantSecret)))
					}
					if tt.wantOldGone != "" {
						require.Error(t, registration.SHA256Hasher.Compare(ctx, got.GetHashedSecret(), []byte(tt.wantOldGone)))
					}
					if tt.wantReserved {
						rc, ok := got.(resourceScopedClient)
						require.True(t, ok)
						assert.ElementsMatch(t, tt.wantResources, rc.Resources())
						ic, ok := got.(spiffeIdentityClient)
						require.True(t, ok)
						assert.Equal(t, tt.wantIdentity, ic.IdentityFingerprint())
					}
				})
			}
		})
	}
}

// capturedLogs records slog output for assertions.
type capturedLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*capturedLogs) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedLogs) WithAttrs([]slog.Attr) slog.Handler     { return c }
func (c *capturedLogs) WithGroup(string) slog.Handler          { return c }

func (c *capturedLogs) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}

// warnsFor returns the attributes of every WARN record keyed on clientID.
func (c *capturedLogs) warnsFor(clientID string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, r := range c.records {
		attrs := map[string]any{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		if r.Level == slog.LevelWarn && attrs["client_id"] == clientID {
			out = append(out, attrs)
		}
	}
	return out
}

// captureSlog swaps slog's global default for a recorder until the test ends.
// Tests using it must not call t.Parallel, since the default is process-wide.
func captureSlog(t *testing.T) *capturedLogs {
	t.Helper()
	prev := slog.Default()
	logs := &capturedLogs{}
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// TestReconcileConfiguredClient_PolicyChangeWarning deliberately does not run
// in parallel: it swaps the global slog default.
//
//nolint:paralleltest // captures the process-global slog default
func TestReconcileConfiguredClient_PolicyChangeWarning(t *testing.T) {
	id := reconcileTestClientID

	tests := []struct {
		name                     string
		oldSecret, newSecret     string
		oldScopes, newScopes     []string
		oldAudience, newAudience []string
		wantWarns                int
	}{
		{
			name:      "scope change logs once with old and new policy",
			oldSecret: "old-secret", newSecret: "new-secret",
			oldScopes: []string{"a"}, newScopes: []string{"a", "b"},
			oldAudience: []string{"https://old.example"}, newAudience: []string{"https://old.example"},
			wantWarns: 1,
		},
		{
			name:      "audience change logs once with old and new audience",
			oldSecret: "old-secret", newSecret: "old-secret",
			oldScopes: []string{"a"}, newScopes: []string{"a"},
			oldAudience: []string{"https://old.example"}, newAudience: []string{"https://new.example"},
			wantWarns: 1,
		},
		{
			name:      "unchanged restart logs nothing",
			oldSecret: "old-secret", newSecret: "old-secret",
			oldScopes: []string{"a"}, newScopes: []string{"a"},
			oldAudience: []string{"https://old.example"}, newAudience: []string{"https://old.example"},
		},
		{
			name:      "secret rotation alone logs nothing",
			oldSecret: "old-secret", newSecret: "new-secret",
			oldScopes: []string{"a"}, newScopes: []string{"a"},
			oldAudience: []string{"https://old.example"}, newAudience: []string{"https://old.example"},
		},
	}

	for _, backend := range configuredClientBackends() {
		for _, tt := range tests {
			t.Run(backend.name+"/"+tt.name, func(t *testing.T) {
				s := backend.new(t)
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(),
					testDelegateClient(t, id, tt.oldSecret, tt.oldScopes, tt.oldAudience)))

				logs := captureSlog(t)
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(),
					testDelegateClient(t, id, tt.newSecret, tt.newScopes, tt.newAudience)))

				warns := logs.warnsFor(id)
				require.Len(t, warns, tt.wantWarns)
				if tt.wantWarns == 0 {
					return
				}
				assert.Equal(t, map[string]any{
					"client_id":                      id,
					"old_scopes":                     tt.oldScopes,
					"new_scopes":                     tt.newScopes,
					"old_audiences":                  tt.oldAudience,
					"new_audiences":                  tt.newAudience,
					"old_grant_types":                []string{oauthproto.GrantTypeTokenExchange},
					"new_grant_types":                []string{oauthproto.GrantTypeTokenExchange},
					"old_redirect_uris":              []string(nil),
					"new_redirect_uris":              []string(nil),
					"old_token_endpoint_auth_method": "",
					"new_token_endpoint_auth_method": "",
				}, warns[0])
			})
		}
	}
}

func testPinnedJWKS(kid string) *jose.JSONWebKeySet {
	key := &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 2047), E: 65537}
	return &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key, KeyID: kid, Use: "sig", Algorithm: "RS256"}}}
}

// TestReconcileConfiguredClient_ClientMetadataChangeWarning deliberately does
// not run in parallel: it swaps the global slog default.
//
//nolint:paralleltest // captures the process-global slog default
func TestReconcileConfiguredClient_ClientMetadataChangeWarning(t *testing.T) {
	id := reconcileTestClientID
	base := func(mutate func(*registration.Config)) registration.Config {
		cfg := registration.Config{
			ID: id, Secret: "secret", Scopes: []string{"a"}, Audience: []string{"https://api.example"},
			TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		}
		mutate(&cfg)
		return cfg
	}
	keyed := func(kid string) func(*registration.Config) {
		return func(c *registration.Config) {
			c.Secret = ""
			c.TokenEndpointAuthMethod = oauthproto.TokenEndpointAuthMethodPrivateKeyJWT
			c.JSONWebKeys = testPinnedJWKS(kid)
		}
	}
	noop := func(*registration.Config) {}
	withRedirects := func(uris ...string) func(*registration.Config) {
		return func(c *registration.Config) {
			c.GrantTypes = []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeTokenExchange}
			c.RedirectURIs = uris
		}
	}

	tests := []struct {
		name      string
		old, new  func(*registration.Config)
		wantWarns int
		wantKeys  []string
		wantAttrs map[string]any
	}{
		{
			name: "grant type change warns",
			old:  noop,
			new: func(c *registration.Config) {
				c.GrantTypes = []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer}
			},
			wantWarns: 1,
			wantKeys:  []string{"old_grant_types", "new_grant_types"},
		},
		{
			name:      "auth method change warns",
			old:       noop,
			new:       keyed("k1"),
			wantWarns: 1,
			wantKeys:  []string{"old_token_endpoint_auth_method", "new_token_endpoint_auth_method"},
		},
		{
			name:      "redirect URI change warns",
			old:       withRedirects("https://app.example/cb"),
			new:       withRedirects("https://app.example/cb2"),
			wantWarns: 1,
			wantAttrs: map[string]any{
				"old_redirect_uris": []string{"https://app.example/cb"},
				"new_redirect_uris": []string{"https://app.example/cb2"},
			},
		},
		{
			name: "redirect URI reorder is silent",
			old:  withRedirects("https://app.example/a", "https://app.example/b"),
			new:  withRedirects("https://app.example/b", "https://app.example/a"),
		},
		{
			name: "unchanged redirect URIs are silent",
			old:  withRedirects("https://app.example/cb"),
			new:  withRedirects("https://app.example/cb"),
		},
		{name: "jwks rotation is silent", old: keyed("k1"), new: keyed("k2")},
		{name: "unchanged client is silent", old: keyed("k1"), new: keyed("k1")},
	}

	for _, backend := range configuredClientBackends() {
		for _, tt := range tests {
			t.Run(backend.name+"/"+tt.name, func(t *testing.T) {
				s := backend.new(t)
				oldClient, err := registration.NewStaticDelegateClient(base(tt.old))
				require.NoError(t, err)
				newClient, err := registration.NewStaticDelegateClient(base(tt.new))
				require.NoError(t, err)
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(), oldClient))

				logs := captureSlog(t)
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(), newClient))

				warns := logs.warnsFor(id)
				require.Len(t, warns, tt.wantWarns)
				for _, key := range tt.wantKeys {
					assert.Contains(t, warns[0], key)
				}
				for key, want := range tt.wantAttrs {
					assert.Equal(t, want, warns[0][key], key)
				}

				stored, err := s.GetClient(t.Context(), id)
				require.NoError(t, err)
				assert.Equal(t, fingerprintOfClient(newClient), fingerprintOfClient(stored))
			})
		}
	}
}
