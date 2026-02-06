// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

func TestFactory_ValidateConfig(t *testing.T) {
	t.Parallel()

	factory := &Factory{}

	tests := []struct {
		name      string
		rawConfig string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid HTTP config",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1",
				"pdp": {
					"http": {
						"url": "http://localhost:9000"
					},
					"claim_mapping": "mpe"
				}
			}`,
			wantErr: false,
		},
		{
			name: "missing pdp field",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1"
			}`,
			wantErr: true,
			errMsg:  "pdp configuration is required",
		},
		{
			name: "HTTP config missing URL",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1",
				"pdp": {
					"http": {}
				}
			}`,
			wantErr: true,
			errMsg:  "http.url is required",
		},
		{
			name:      "invalid JSON",
			rawConfig: `{invalid`,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := factory.ValidateConfig(json.RawMessage(tt.rawConfig))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestFactory_CreateAuthorizer(t *testing.T) {
	t.Parallel()

	// Start a mock PDP server
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		_ = json.NewEncoder(w).Encode(DecisionResponse{Allow: true})
	}))
	t.Cleanup(func() { server.Close() })

	factory := &Factory{}

	tests := []struct {
		name      string
		rawConfig string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid HTTP config",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1",
				"pdp": {
					"http": {
						"url": "` + server.URL + `"
					},
					"claim_mapping": "mpe"
				}
			}`,
			wantErr: false,
		},
		{
			name: "missing pdp field",
			rawConfig: `{
				"version": "1.0",
				"type": "httpv1"
			}`,
			wantErr: true,
			errMsg:  "pdp configuration is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authz, err := factory.CreateAuthorizer(json.RawMessage(tt.rawConfig), "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateAuthorizer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("CreateAuthorizer() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
			if !tt.wantErr && authz == nil {
				t.Error("CreateAuthorizer() returned nil authorizer without error")
			}
		})
	}
}

func TestAuthorizer_AuthorizeWithJWTClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverAllow    bool
		feature        authorizers.MCPFeature
		operation      authorizers.MCPOperation
		resourceID     string
		arguments      map[string]interface{}
		claims         map[string]interface{}
		wantAuthorized bool
		wantErr        bool
	}{
		{
			name:        "authorized tool call",
			serverAllow: true,
			feature:     authorizers.MCPFeatureTool,
			operation:   authorizers.MCPOperationCall,
			resourceID:  "weather",
			arguments: map[string]interface{}{
				"location": "New York",
			},
			claims: map[string]interface{}{
				"sub":   "user@example.com",
				"roles": []string{"developer"},
			},
			wantAuthorized: true,
			wantErr:        false,
		},
		{
			name:        "denied tool call",
			serverAllow: false,
			feature:     authorizers.MCPFeatureTool,
			operation:   authorizers.MCPOperationCall,
			resourceID:  "admin-tool",
			arguments:   nil,
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			wantAuthorized: false,
			wantErr:        false,
		},
		{
			name:        "prompt get",
			serverAllow: true,
			feature:     authorizers.MCPFeaturePrompt,
			operation:   authorizers.MCPOperationGet,
			resourceID:  "greeting",
			arguments:   nil,
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			wantAuthorized: true,
			wantErr:        false,
		},
		{
			name:        "resource read",
			serverAllow: true,
			feature:     authorizers.MCPFeatureResource,
			operation:   authorizers.MCPOperationRead,
			resourceID:  "file://data.json",
			arguments:   nil,
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			wantAuthorized: true,
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Start a mock PDP server
			server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				// Verify the request
				var porc PORC
				if err := json.NewDecoder(r.Body).Decode(&porc); err != nil {
					t.Errorf("Failed to decode PORC: %v", err)
					w.WriteHeader(nethttp.StatusBadRequest)
					return
				}

				// Check that operation and resource are set
				if _, ok := porc["operation"]; !ok {
					t.Error("PORC missing operation")
				}
				if _, ok := porc["resource"]; !ok {
					t.Error("PORC missing resource")
				}

				w.WriteHeader(nethttp.StatusOK)
				_ = json.NewEncoder(w).Encode(DecisionResponse{Allow: tt.serverAllow})
			}))
			defer server.Close()

			// Create authorizer
			authz, err := NewAuthorizer(ConfigOptions{
				HTTP: &ConnectionConfig{
					URL: server.URL,
				},
				ClaimMapping: "mpe",
			}, "test")
			if err != nil {
				t.Fatalf("Failed to create authorizer: %v", err)
			}
			defer authz.Close()

			// Create context with identity
			identity := &auth.Identity{
				Claims: tt.claims,
			}
			ctx := auth.WithIdentity(context.Background(), identity)

			// Test authorization
			authorized, err := authz.AuthorizeWithJWTClaims(ctx, tt.feature, tt.operation, tt.resourceID, tt.arguments)
			if (err != nil) != tt.wantErr {
				t.Errorf("AuthorizeWithJWTClaims() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if authorized != tt.wantAuthorized {
				t.Errorf("AuthorizeWithJWTClaims() = %v, want %v", authorized, tt.wantAuthorized)
			}
		})
	}
}

func TestAuthorizer_AuthorizeWithJWTClaims_NoIdentity(t *testing.T) {
	t.Parallel()

	// Start a mock PDP server
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		_ = json.NewEncoder(w).Encode(DecisionResponse{Allow: true})
	}))
	defer server.Close()

	// Create authorizer
	authz, err := NewAuthorizer(ConfigOptions{
		HTTP: &ConnectionConfig{
			URL: server.URL,
		},
		ClaimMapping: "mpe",
	}, "test")
	if err != nil {
		t.Fatalf("Failed to create authorizer: %v", err)
	}
	defer authz.Close()

	// Create context without identity
	ctx := context.Background()

	// Test authorization - should fail due to missing identity
	_, err = authz.AuthorizeWithJWTClaims(ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "test", nil)
	if err == nil {
		t.Error("Expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "missing principal") {
		t.Errorf("Expected missing principal error, got: %v", err)
	}
}

func TestFactoryRegistration(t *testing.T) {
	t.Parallel()

	// Verify that the factory is registered
	factory := authorizers.GetFactory(ConfigType)
	if factory == nil {
		t.Errorf("Factory not registered for type %s", ConfigType)
	}

	// Verify it's the HTTP PDP factory
	if _, ok := factory.(*Factory); !ok {
		t.Errorf("Registered factory is not *Factory, got %T", factory)
	}
}
