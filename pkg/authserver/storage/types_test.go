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
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
)

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
// the identity comparison.
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
		"scopes":        "a different OAuth scope set is a different logical client authorization",
		"audience":      "a different RFC 8707/8693 audience allowlist is a different logical client authorization",
		"grantTypes":    "a different grant-type set is a different logical client capability",
		"responseTypes": "a different response-type set is a different logical client capability",
		"resources":     "a different RFC 8707 resource allowlist is a different logical client authorization",
		"identityFingerprint": "a different SPIFFE association identity (trust domain, principal, methods) " +
			"behind the same client ID is a different logical client authorization",
		"public": "public vs. confidential is a different logical client class",
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
		resources:           []string{"resource-a"},
		identityFingerprint: "identity-a",
		public:              false,
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
			case "resources":
				other.resources = []string{"resource-b"}
			case "identityFingerprint":
				other.identityFingerprint = "identity-b"
			case "public":
				other.public = !base.public
			default:
				t.Fatalf("field %q has a justification but no wiring check in this test -- add one", name)
			}

			assert.False(t, base.equal(other),
				"clientFingerprint.equal() must report a mismatch when only %q differs", name)
		})
	}
}
