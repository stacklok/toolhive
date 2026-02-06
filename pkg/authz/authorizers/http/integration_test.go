// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// TestClaimMapperIntegration tests the end-to-end flow with different claim mappers.
func TestClaimMapperIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		claimMapping      string
		jwtClaims         map[string]any
		expectedPrincipal map[string]any
	}{
		{
			name:         "MPE mapper with standard OIDC claims",
			claimMapping: "mpe",
			jwtClaims: map[string]any{
				"sub":    "user@example.com",
				"roles":  []any{"developer"},
				"groups": []any{"engineering"},
			},
			expectedPrincipal: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []any{"developer"},
				"mgroups":      []any{"engineering"},
				"mannotations": map[string]any{},
			},
		},
		{
			name:         "MPE mapper with MPE-native claims",
			claimMapping: "mpe",
			jwtClaims: map[string]any{
				"sub":        "user@example.com",
				"mroles":     []any{"admin"},
				"mgroups":    []any{"security"},
				"mclearance": "TOP_SECRET",
			},
			expectedPrincipal: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []any{"admin"},
				"mgroups":      []any{"security"},
				"mclearance":   "TOP_SECRET",
				"mannotations": map[string]any{},
			},
		},
		{
			name:         "Standard mapper with OIDC claims",
			claimMapping: "standard",
			jwtClaims: map[string]any{
				"sub":    "user@example.com",
				"roles":  []any{"developer"},
				"groups": []any{"engineering"},
			},
			expectedPrincipal: map[string]any{
				"sub":    "user@example.com",
				"roles":  []any{"developer"},
				"groups": []any{"engineering"},
			},
		},
		{
			name:         "Standard mapper ignores MPE-specific claims",
			claimMapping: "standard",
			jwtClaims: map[string]any{
				"sub":        "user@example.com",
				"mroles":     []any{"admin"},
				"mgroups":    []any{"security"},
				"mclearance": "SECRET",
			},
			expectedPrincipal: map[string]any{
				"sub": "user@example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test PDP server that captures the PORC and returns allow
			var capturedPORC PORC
			pdpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/decision" {
					t.Errorf("unexpected request path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// Capture the PORC
				if err := json.NewDecoder(r.Body).Decode(&capturedPORC); err != nil {
					t.Errorf("failed to decode PORC: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				// Return allow decision
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"allow": true})
			}))
			defer pdpServer.Close()

			// Create authorizer configuration with the specified claim mapper
			config := ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: pdpServer.URL,
				},
				ClaimMapping: tt.claimMapping,
			}

			// Create the authorizer
			authorizer, err := NewAuthorizer(config, "test-server")
			if err != nil {
				t.Fatalf("failed to create authorizer: %v", err)
			}
			defer func() {
				if err := authorizer.Close(); err != nil {
					t.Errorf("failed to close authorizer: %v", err)
				}
			}()

			// Create a context with identity
			identity := &auth.Identity{
				Claims: tt.jwtClaims,
			}
			ctx := auth.WithIdentity(context.Background(), identity)

			// Call the authorizer
			allowed, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"weather",
				map[string]any{"location": "New York"},
			)

			if err != nil {
				t.Fatalf("authorization failed: %v", err)
			}

			if !allowed {
				t.Errorf("expected authorization to be allowed, but was denied")
			}

			// Verify the principal in the captured PORC matches expectations
			principal, ok := capturedPORC["principal"].(map[string]any)
			if !ok {
				t.Fatalf("PORC principal is not a map: %T", capturedPORC["principal"])
			}

			// Compare principal fields
			for k, expectedVal := range tt.expectedPrincipal {
				actualVal, exists := principal[k]
				if !exists {
					t.Errorf("expected principal field %q not found in PORC", k)
					continue
				}

				// Use JSON marshaling for comparison to handle slice/map types
				expectedJSON, _ := json.Marshal(expectedVal)
				actualJSON, _ := json.Marshal(actualVal)
				if string(expectedJSON) != string(actualJSON) {
					t.Errorf("principal[%q] = %v, want %v", k, actualVal, expectedVal)
				}
			}

			// Verify operation and resource are present
			if capturedPORC["operation"] != "mcp:tool:call" {
				t.Errorf("operation = %v, want mcp:tool:call", capturedPORC["operation"])
			}
			if capturedPORC["resource"] != "mrn:mcp:test-server:tool:weather" {
				t.Errorf("resource = %v, want mrn:mcp:test-server:tool:weather", capturedPORC["resource"])
			}
		})
	}
}
