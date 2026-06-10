// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func newTestRegistry(t *testing.T) vmcpauth.OutgoingAuthRegistry {
	t.Helper()
	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	return reg
}

func TestMergeForwardedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base *vmcp.HeaderForwardConfig
		// forwarded is the per-request captured header map, as returned by
		// headerforward.ForwardedHeadersFromContext.
		forwarded map[string]string
		// wantSameAsBase means we expect the returned pointer to equal the base
		// pointer (i.e., no merge was needed — original passed through unchanged).
		wantSameAsBase bool
		wantHeaders    map[string]string
	}{
		{
			name:           "nil forwarded returns base unchanged",
			base:           &vmcp.HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Static": "static"}},
			forwarded:      nil,
			wantSameAsBase: true,
		},
		{
			name:           "nil forwarded nil base returns nil",
			base:           nil,
			forwarded:      nil,
			wantSameAsBase: true,
		},
		{
			// Both nil and empty forwarded maps satisfy len==0 and return base unchanged.
			name:           "empty forwarded returns base unchanged",
			base:           &vmcp.HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Static": "static"}},
			forwarded:      map[string]string{},
			wantSameAsBase: true,
		},
		{
			name:        "forwarded header added to nil base",
			base:        nil,
			forwarded:   map[string]string{"X-Litellm-Api-Key": "sk-1"},
			wantHeaders: map[string]string{"X-Litellm-Api-Key": "sk-1"},
		},
		{
			name: "forwarded header added to base with other header",
			base: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Static": "static-value"},
			},
			forwarded: map[string]string{"X-Litellm-Api-Key": "sk-1"},
			wantHeaders: map[string]string{
				"X-Static":          "static-value",
				"X-Litellm-Api-Key": "sk-1",
			},
		},
		{
			// Covers both a canonical restricted header (Host) and a lowercase one
			// (x-forwarded-for) to verify case-insensitive matching in a single case.
			name: "restricted headers dropped (canonical and lowercase)",
			base: nil,
			forwarded: map[string]string{
				"Host":              "evil.example.com",
				"x-forwarded-for":   "1.2.3.4",
				"X-Litellm-Api-Key": "sk-2",
			},
			wantHeaders: map[string]string{"X-Litellm-Api-Key": "sk-2"},
		},
		{
			name: "static config wins over forwarded header with same name",
			base: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Litellm-Api-Key": "static-wins"},
			},
			forwarded:   map[string]string{"X-Litellm-Api-Key": "forwarded-loses"},
			wantHeaders: map[string]string{"X-Litellm-Api-Key": "static-wins"},
		},
		{
			name: "base AddHeadersFromSecret preserved unchanged",
			base: &vmcp.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{"X-Secret-Header": "my-secret-id"},
			},
			forwarded:   map[string]string{"X-Litellm-Api-Key": "sk-4"},
			wantHeaders: map[string]string{"X-Litellm-Api-Key": "sk-4"},
		},
		{
			name: "base not mutated when forwarded headers added",
			base: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Static": "original"},
			},
			forwarded: map[string]string{"X-New": "new-value"},
			// base should not gain X-New
			wantHeaders: map[string]string{
				"X-Static": "original",
				"X-New":    "new-value",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Snapshot the original base plaintext headers to verify they are not mutated.
			var origBasePlaintext map[string]string
			if tc.base != nil {
				origBasePlaintext = maps.Clone(tc.base.AddPlaintextHeaders)
			}

			got := mergeForwardedHeaders(tc.base, tc.forwarded)

			if tc.wantSameAsBase {
				assert.Same(t, tc.base, got, "expected the original base pointer to be returned unchanged")
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tc.wantHeaders, got.AddPlaintextHeaders,
				"merged AddPlaintextHeaders mismatch (checks both presence and absence of keys)")

			// Verify base was not mutated.
			if tc.base != nil {
				assert.Equal(t, origBasePlaintext, tc.base.AddPlaintextHeaders,
					"base.AddPlaintextHeaders must not be mutated")
			}
		})
	}
}

func TestCreateMCPClient_UnsupportedTransport(t *testing.T) {
	t.Parallel()

	unsupportedTypes := []string{"stdio", "grpc", "", "ws"}
	for _, transport := range unsupportedTypes {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()

			target := &vmcp.BackendTarget{
				WorkloadID:    "test-backend",
				WorkloadName:  "test-backend",
				BaseURL:       "http://localhost:9999",
				TransportType: transport,
			}

			_, err := createMCPClient(context.Background(), target, nil, newTestRegistry(t), "", secrets.NewEnvironmentProvider())
			require.Error(t, err)
			assert.ErrorIs(t, err, vmcp.ErrUnsupportedTransport,
				"transport %q should return ErrUnsupportedTransport", transport)
		})
	}
}
