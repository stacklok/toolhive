// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExchangeGrantLive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name    string
		grant   ExchangeGrant
		wantErr error
	}{
		{
			name:  "live",
			grant: ExchangeGrant{ExpiresAt: future},
		},
		{
			name:    "expired",
			grant:   ExchangeGrant{ExpiresAt: past},
			wantErr: ErrExpired,
		},
		{
			name:    "revoked",
			grant:   ExchangeGrant{ExpiresAt: future, RevokedAt: past},
			wantErr: ErrRevoked,
		},
		{
			name:    "revoked and expired reports revoked",
			grant:   ExchangeGrant{ExpiresAt: past, RevokedAt: past},
			wantErr: ErrRevoked,
		},
		{
			name:    "zero expiry counts as expired",
			grant:   ExchangeGrant{},
			wantErr: ErrExpired,
		},
		{
			name:  "now equal to expiry is still live",
			grant: ExchangeGrant{ExpiresAt: now},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.grant.Live(now)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestNotFoundRFC6749Error(t *testing.T) {
	t.Parallel()

	err := notFoundRFC6749Error("item not found")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, err, fosite.ErrNotFound)

	var fositeErr *fosite.RFC6749Error
	if assert.True(t, errors.As(err, &fositeErr)) {
		assert.NotNil(t, fositeErr)
	}
}

func TestUpstreamTokens_IsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt time.Time
		checkTime time.Time
		want      bool
	}{
		{
			name:      "not expired - future expiration",
			expiresAt: now.Add(time.Hour),
			checkTime: now,
			want:      false,
		},
		{
			name:      "expired - past expiration",
			expiresAt: now.Add(-time.Hour),
			checkTime: now,
			want:      true,
		},
		{
			name:      "not expired - exact boundary (equal time)",
			expiresAt: now,
			checkTime: now,
			want:      false, // time.After returns false when times are equal
		},
		{
			name:      "expired - 1 nanosecond after expiration",
			expiresAt: now,
			checkTime: now.Add(time.Nanosecond),
			want:      true,
		},
		{
			name:      "not expired - 1 nanosecond before expiration",
			expiresAt: now,
			checkTime: now.Add(-time.Nanosecond),
			want:      false,
		},
		{
			name:      "zero expiration time treated as non-expiring",
			expiresAt: time.Time{},
			checkTime: now,
			want:      false,
		},
		{
			name:      "not expired - zero check time with future expiration",
			expiresAt: now,
			checkTime: time.Time{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := &UpstreamTokens{
				ExpiresAt: tt.expiresAt,
			}

			got := tokens.IsExpired(tt.checkTime)
			if got != tt.want {
				t.Errorf("IsExpired(%v) = %v, want %v (expiresAt=%v)",
					tt.checkTime, got, tt.want, tt.expiresAt)
			}
		})
	}
}

func TestUpstreamTokenRowIDResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		resolver              UpstreamTokenStorage
		delimiterRowsDistinct bool
	}{
		{name: "memory", resolver: NewMemoryStorage(), delimiterRowsDistinct: true},
		{name: "redis", resolver: &RedisStorage{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if memory, ok := tt.resolver.(*MemoryStorage); ok {
				t.Cleanup(func() { _ = memory.Close() })
			}

			first, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:one")
			if err != nil {
				t.Fatalf("resolve first row: %v", err)
			}
			again, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:one")
			if err != nil {
				t.Fatalf("resolve same row: %v", err)
			}
			otherSession, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session", "one:provider:one")
			if err != nil {
				t.Fatalf("resolve delimiter row: %v", err)
			}
			otherProvider, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:two")
			if err != nil {
				t.Fatalf("resolve different provider: %v", err)
			}

			if first == "" {
				t.Error("row ID must not be empty")
			}
			if first != again {
				t.Errorf("same row IDs differ: %q and %q", first, again)
			}
			if tt.delimiterRowsDistinct {
				if first == otherSession {
					t.Errorf("distinct delimiter rows must have distinct IDs: %q and %q", first, otherSession)
				}
			} else if first != otherSession {
				t.Errorf("equivalent Redis row keys must have equal IDs: %q and %q", first, otherSession)
			}
			if first == otherProvider || otherSession == otherProvider {
				t.Errorf("distinct rows must have distinct IDs: %q, %q, %q", first, otherSession, otherProvider)
			}
			if strings.Contains(string(first), "session") || strings.Contains(string(first), "provider") {
				t.Errorf("row ID must be opaque, got %q", first)
			}

			for _, input := range [][2]string{{"", "provider"}, {"session", ""}} {
				_, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), input[0], input[1])
				if !errors.Is(err, fosite.ErrInvalidRequest) {
					t.Errorf("resolve(%q, %q) error = %v, want invalid request", input[0], input[1], err)
				}
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
		return
	}

	if cfg.Type != TypeMemory {
		t.Errorf("DefaultConfig().Type = %q, want %q", cfg.Type, TypeMemory)
	}
}

// TestClientFingerprintFieldsAreJustified is a drift guard over
// clientFingerprint's field set: every field must carry a one-sentence
// justification here, so a field added to clientFingerprint without a
// matching entry fails loudly instead of silently compiling green. Unlike a
// bare []string of names, an unjustified addition can't be satisfied by
// just appending the name — the reviewer has to say why the field belongs in
// equal(). Only reserved SPIFFE placeholders compare in full with equal();
// other configured clients are decided by configuredClientConflict.
//
// Listing a field here is not enough on its own: a field added to the struct
// and to this map, but never wired into equal(), previously still passed
// this test (only a memory-backend collision test happened to catch it).
// The second half of this test closes that gap by constructing, for every
// justified field, two fingerprints that differ ONLY in that field and
// asserting equal() reports them as different -- proving each field actually
// participates in the comparison, not just that it exists on the struct.
func TestClientFingerprintFieldsAreJustified(t *testing.T) {
	t.Parallel()

	justifications := map[string]string{
		"scopes":                  "placeholders must not roll to a different OAuth scope set without a manual delete",
		"audience":                "placeholders must not roll to a different RFC 8707/8693 audience allowlist",
		"grantTypes":              "placeholders must not roll to a different grant-type set",
		"responseTypes":           "placeholders must not roll to a different response-type set",
		"redirectURIs":            "placeholders carry no redirect URIs; one appearing is a different client",
		"tokenEndpointAuthMethod": "placeholders never carry an auth method; one appearing is a different client",
		"resources":               "placeholders must not roll to a different RFC 8707 resource allowlist (#6473)",
		"identityFingerprint": "a different SPIFFE association identity (trust domain, principal, methods) " +
			"behind the same placeholder ID is a different association",
		"public": "public vs. confidential is a different client class",
		"reserved": "a SPIFFE-reserved placeholder and an ordinary configured client at the same ID " +
			"are different kinds of client, whatever their other fields say",
	}

	var gotFields []string
	for _, f := range reflect.VisibleFields(reflect.TypeOf(clientFingerprint{})) {
		gotFields = append(gotFields, f.Name)
	}

	var wantFields []string
	for name, justification := range justifications {
		assert.NotEmptyf(t, justification, "field %q must carry a non-empty justification", name)
		wantFields = append(wantFields, name)
	}

	assert.ElementsMatchf(t, wantFields, gotFields,
		"clientFingerprint's field set changed (got %v); add or remove a justification entry in this test "+
			"and confirm the new field is actually wired into fingerprintOfClient and storedClient.fingerprint",
		gotFields)

	base := clientFingerprint{
		scopes:              []string{"scope-a"},
		audience:            []string{"audience-a"},
		grantTypes:          []string{"grant-a"},
		responseTypes:       []string{"response-a"},
		redirectURIs:        []string{"https://a.example/cb"},
		resources:           []string{"resource-a"},
		identityFingerprint: "identity-a",
		public:              false,
		reserved:            false,
	}

	for name := range justifications {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			other := base
			switch name {
			case "scopes":
				other.scopes = []string{"scope-b"}
			case "audience":
				other.audience = []string{"audience-b"}
			case "grantTypes":
				other.grantTypes = []string{"grant-b"}
			case "responseTypes":
				other.responseTypes = []string{"response-b"}
			case "redirectURIs":
				other.redirectURIs = []string{"https://b.example/cb"}
			case "tokenEndpointAuthMethod":
				other.tokenEndpointAuthMethod = "private_key_jwt"
			case "resources":
				other.resources = []string{"resource-b"}
			case "identityFingerprint":
				other.identityFingerprint = "identity-b"
			case "public":
				other.public = !base.public
			case "reserved":
				other.reserved = !base.reserved
			default:
				t.Fatalf("field %q has a justification but no wiring check in this test -- add one", name)
			}

			assert.False(t, base.equal(other),
				"clientFingerprint.equal() must report a mismatch when only %q differs", name)
		})
	}
}

func TestConfiguredClientConflict(t *testing.T) {
	t.Parallel()

	delegate := clientFingerprint{
		scopes:        []string{"a"},
		audience:      []string{"aud-a"},
		grantTypes:    []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		responseTypes: []string{},
	}
	placeholder := clientFingerprint{
		scopes:              []string{"a"},
		audience:            []string{"aud-a"},
		resources:           []string{"res-a"},
		identityFingerprint: "id-a",
		reserved:            true,
	}
	with := func(f clientFingerprint, mutate func(*clientFingerprint)) clientFingerprint {
		mutate(&f)
		return f
	}

	tests := []struct {
		name     string
		existing clientFingerprint
		incoming clientFingerprint
		wantErr  string
	}{
		{
			name:     "placeholder with empty identity existing, delegate incoming, is refused",
			existing: with(placeholder, func(f *clientFingerprint) { f.identityFingerprint = "" }),
			incoming: delegate,
			wantErr:  "stored row is a SPIFFE-reserved placeholder",
		},
		{
			name:     "delegate existing, placeholder with empty identity incoming, is refused",
			existing: delegate,
			incoming: with(placeholder, func(f *clientFingerprint) { f.identityFingerprint = "" }),
			wantErr:  "incoming client is SPIFFE-reserved",
		},
		{
			name:     "identical placeholders are idempotent",
			existing: placeholder,
			incoming: placeholder,
		},
		{
			name:     "placeholder scope change is refused",
			existing: placeholder,
			incoming: with(placeholder, func(f *clientFingerprint) { f.scopes = []string{"a", "b"} }),
			wantErr:  "SPIFFE-reserved clients must match exactly",
		},
		{
			name:     "placeholder identity change is refused",
			existing: placeholder,
			incoming: with(placeholder, func(f *clientFingerprint) { f.identityFingerprint = "id-b" }),
			wantErr:  "SPIFFE-reserved clients must match exactly",
		},
		{
			name:     "identical delegates are idempotent",
			existing: delegate,
			incoming: delegate,
		},
		{
			name:     "delegate scope change replaces",
			existing: delegate,
			incoming: with(delegate, func(f *clientFingerprint) { f.scopes = []string{"a", "b"} }),
		},
		{
			name:     "delegates differing only in resources and identity still replace",
			existing: delegate,
			incoming: with(delegate, func(f *clientFingerprint) {
				f.resources = []string{"res-x"}
				f.identityFingerprint = "id-x"
			}),
		},
		{
			name:     "confidential to public flip is refused",
			existing: delegate,
			incoming: with(delegate, func(f *clientFingerprint) { f.public = true }),
			wantErr:  "public and confidential clients cannot replace each other",
		},
		{
			name:     "public to confidential flip is refused",
			existing: with(delegate, func(f *clientFingerprint) { f.public = true }),
			incoming: delegate,
			wantErr:  "public and confidential clients cannot replace each other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := configuredClientConflict("client-1", tt.existing, tt.incoming)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAlreadyExists)
			assert.Contains(t, err.Error(), "different configured client")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "client-1")
		})
	}
}

func TestClientFingerprintPolicyChanged(t *testing.T) {
	t.Parallel()

	base := clientFingerprint{scopes: []string{"a", "b"}, audience: []string{"x"}}

	tests := []struct {
		name  string
		other clientFingerprint
		want  bool
	}{
		{name: "identical is unchanged", other: base, want: false},
		{
			name:  "reordered sets are unchanged",
			other: clientFingerprint{scopes: []string{"b", "a"}, audience: []string{"x"}},
			want:  false,
		},
		{name: "scope change is a policy change", other: clientFingerprint{scopes: []string{"a"}, audience: []string{"x"}}, want: true},
		{
			name:  "audience change is a policy change",
			other: clientFingerprint{scopes: []string{"a", "b"}, audience: []string{"y"}},
			want:  true,
		},
		{
			name: "grant type change is a policy change",
			other: clientFingerprint{
				scopes: []string{"a", "b"}, audience: []string{"x"}, grantTypes: []string{"g"},
			},
			want: true,
		},
		{
			name: "auth method change is a policy change",
			other: clientFingerprint{
				scopes: []string{"a", "b"}, audience: []string{"x"}, tokenEndpointAuthMethod: "private_key_jwt",
			},
			want: true,
		},
		{
			name: "redirect URI change is a policy change",
			other: clientFingerprint{
				scopes: []string{"a", "b"}, audience: []string{"x"}, redirectURIs: []string{"https://a.example/cb"},
			},
			want: true,
		},
		{
			name: "response type change alone is not a policy change",
			other: clientFingerprint{
				scopes: []string{"a", "b"}, audience: []string{"x"}, responseTypes: []string{"code"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, base.policyChanged(tt.other))
		})
	}
}

func TestClientFingerprintPolicyChanged_RedirectURIs(t *testing.T) {
	t.Parallel()

	base := clientFingerprint{redirectURIs: []string{"https://a.example/cb", "https://b.example/cb"}}

	assert.False(t, base.policyChanged(clientFingerprint{
		redirectURIs: []string{"https://b.example/cb", "https://a.example/cb", "https://a.example/cb"},
	}), "reordering or duplicating redirect URIs is not a change")
	assert.True(t, base.policyChanged(clientFingerprint{redirectURIs: []string{"https://a.example/cb"}}))
	assert.True(t, base.policyChanged(clientFingerprint{}))
}

// resourceIdentityClient is a non-placeholder client that implements both
// resourceScopedClient and spiffeIdentityClient, so only the reserved gate in
// fingerprintOfClient keeps its values out of the fingerprint.
type resourceIdentityClient struct {
	*fosite.DefaultClient
	resources []string
	identity  string
}

func (c resourceIdentityClient) Resources() []string         { return c.resources }
func (c resourceIdentityClient) IdentityFingerprint() string { return c.identity }

func TestClientFingerprintReservedWiring(t *testing.T) {
	t.Parallel()

	placeholder := inertPlaceholderClient{
		DefaultClient:       &fosite.DefaultClient{ID: "p", Scopes: []string{"a"}},
		resources:           []string{"res-a"},
		identityFingerprint: "id-a",
	}
	nonPlaceholder := resourceIdentityClient{
		DefaultClient: &fosite.DefaultClient{ID: "d", Scopes: []string{"a"}},
		resources:     []string{"res-a"},
		identity:      "id-a",
	}

	t.Run("placeholder reads resources and identity", func(t *testing.T) {
		t.Parallel()
		got := fingerprintOfClient(placeholder)
		assert.True(t, got.reserved)
		assert.Equal(t, []string{"res-a"}, got.resources)
		assert.Equal(t, "id-a", got.identityFingerprint)
	})

	t.Run("non-placeholder ignores resources and identity", func(t *testing.T) {
		t.Parallel()
		got := fingerprintOfClient(nonPlaceholder)
		assert.False(t, got.reserved)
		assert.Empty(t, got.resources)
		assert.Empty(t, got.identityFingerprint)
	})

	t.Run("stored reserved row sets reserved", func(t *testing.T) {
		t.Parallel()
		assert.True(t, storedClient{Reserved: true}.fingerprint().reserved)
	})

	t.Run("legacy stored row without reserved key is not reserved", func(t *testing.T) {
		t.Parallel()
		var legacy storedClient
		require.NoError(t, json.Unmarshal([]byte(`{"id":"legacy","scopes":["a"]}`), &legacy))
		assert.False(t, legacy.fingerprint().reserved)
	})
}
