// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// makeUnsignedJWT creates a JWT with the given claims using the "none" algorithm.
// This is only used in tests; the production code parses without verification.
func makeUnsignedJWT(claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		panic("makeUnsignedJWT: " + err.Error())
	}
	return signed
}

// TestNewCedarAuthorizer tests the creation of a new Cedar authorizer with different configurations.
func TestNewCedarAuthorizer(t *testing.T) {
	t.Parallel()

	// Test cases
	testCases := []struct {
		name              string
		policies          []string
		entitiesJSON      string
		roleClaimName     string
		serverName        string
		expectError       bool
		errorType         error
		wantRoleClaimName string
		wantServerName    string
	}{
		{
			name:         "Valid policy and empty entities",
			policies:     []string{`permit(principal, action, resource);`},
			entitiesJSON: `[]`,
			expectError:  false,
		},
		{
			name:         "Multiple valid policies",
			policies:     []string{`permit(principal, action, resource);`, `forbid(principal, action, resource);`},
			entitiesJSON: `[]`,
			expectError:  false,
		},
		{
			name:         "Invalid policy",
			policies:     []string{`invalid policy syntax`},
			entitiesJSON: `[]`,
			expectError:  true,
		},
		{
			name:         "No policies",
			policies:     []string{},
			entitiesJSON: `[]`,
			expectError:  true,
			errorType:    ErrNoPolicies,
		},
		{
			name:         "Invalid entities JSON",
			policies:     []string{`permit(principal, action, resource);`},
			entitiesJSON: `invalid json`,
			expectError:  true,
		},
		{
			name:         "Valid policy and valid entities",
			policies:     []string{`permit(principal, action, resource);`},
			entitiesJSON: `[{"uid": {"type": "User", "id": "alice"}, "attrs": {}, "parents": []}]`,
			expectError:  false,
		},
		{
			name:              "Stores configured role claim",
			policies:          []string{`permit(principal, action, resource);`},
			entitiesJSON:      `[]`,
			roleClaimName:     "roles",
			expectError:       false,
			wantRoleClaimName: "roles",
		},
		{
			name:              "Stores URI-style role claim",
			policies:          []string{`permit(principal, action, resource);`},
			entitiesJSON:      `[]`,
			roleClaimName:     "https://example.com/roles",
			expectError:       false,
			wantRoleClaimName: "https://example.com/roles",
		},
		{
			name:           "Stores server name",
			policies:       []string{`permit(principal, action, resource);`},
			entitiesJSON:   `[]`,
			serverName:     "my-mcp-server",
			expectError:    false,
			wantServerName: "my-mcp-server",
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a Cedar authorizer
			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:      tc.policies,
				EntitiesJSON:  tc.entitiesJSON,
				RoleClaimName: tc.roleClaimName,
			}, tc.serverName)

			// Check error expectations
			if tc.expectError {
				assert.Error(t, err, "Expected an error but got none")
				if tc.errorType != nil {
					assert.ErrorIs(t, err, tc.errorType, "Expected error type %v but got %v", tc.errorType, err)
				}
				assert.Nil(t, authorizer, "Expected nil authorizer when error occurs")
			} else {
				assert.NoError(t, err, "Unexpected error: %v", err)
				require.NotNil(t, authorizer, "Cedar authorizer is nil")

				cedarAuthz, ok := authorizer.(*Authorizer)
				require.True(t, ok)
				assert.Equal(t, tc.wantRoleClaimName, cedarAuthz.roleClaimName)
				assert.Equal(t, tc.wantServerName, cedarAuthz.serverName)
			}
		})
	}
}

// TestAuthorizeWithJWTClaims tests the AuthorizeWithJWTClaims function with different roles in claims.
func TestAuthorizeWithJWTClaims(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name             string
		policy           string
		claims           jwt.MapClaims
		feature          authorizers.MCPFeature
		operation        authorizers.MCPOperation
		resourceID       string
		arguments        map[string]interface{}
		expectAuthorized bool
	}{
		{
			name: "User with correct name can call weather tool",
			policy: `
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"weather"
			)
			when {
				context.claim_name == "John Doe"
			};
			`,
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "John Doe",
				"roles": []string{"user", "reader"},
			},
			feature:          authorizers.MCPFeatureTool,
			operation:        authorizers.MCPOperationCall,
			resourceID:       "weather",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User with incorrect name cannot call weather tool",
			policy: `
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"weather"
			)
			when {
				context.claim_name == "John Doe"
			};
			`,
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "Jane Smith",
				"roles": []string{"user", "reader"},
			},
			feature:          authorizers.MCPFeatureTool,
			operation:        authorizers.MCPOperationCall,
			resourceID:       "weather",
			arguments:        nil,
			expectAuthorized: false,
		},
		{
			name: "Admin user can call any tool",
			policy: `
			permit(
				principal,
				action == Action::"call_tool",
				resource
			)
			when {
				context.claim_role == "admin"
			};
			`,
			claims: map[string]interface{}{
				"sub":  "admin123",
				"name": "Admin User",
				"role": "admin",
			},
			feature:          authorizers.MCPFeatureTool,
			operation:        authorizers.MCPOperationCall,
			resourceID:       "any_tool",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User with specific argument value can call tool",
			policy: `
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"calculator"
			)
			when {
				context.arg_operation == "add" && context.arg_value1 == 5
			};
			`,
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			feature:    authorizers.MCPFeatureTool,
			operation:  authorizers.MCPOperationCall,
			resourceID: "calculator",
			arguments: map[string]interface{}{
				"operation": "add",
				"value1":    5,
				"value2":    10,
			},
			expectAuthorized: true,
		},
		{
			name: "User with specific role in array can access resource",
			policy: `
			permit(
				principal,
				action == Action::"read_resource",
				resource == Resource::"sensitive_data"
			)
			when {
				context.claim_groups.contains("editor")
			};
			`,
			claims: jwt.MapClaims{
				"sub":    "user123",
				"name":   "John Doe",
				"groups": []string{"reader", "editor", "viewer"},
			},
			feature:          authorizers.MCPFeatureResource,
			operation:        authorizers.MCPOperationRead,
			resourceID:       "sensitive_data",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "Resource entity exposes name attribute for Cedar schema",
			policy: `
			permit(
				principal,
				action == Action::"read_resource",
				resource
			)
			when {
				resource.name == "sensitive_data"
			};
			`,
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			feature:          authorizers.MCPFeatureResource,
			operation:        authorizers.MCPOperationRead,
			resourceID:       "sensitive_data",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "Resource entity retains uri attribute for backward compat",
			policy: `
			permit(
				principal,
				action == Action::"read_resource",
				resource
			)
			when {
				resource.uri == "sensitive_data"
			};
			`,
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			feature:          authorizers.MCPFeatureResource,
			operation:        authorizers.MCPOperationRead,
			resourceID:       "sensitive_data",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "Resource name and uri attributes carry the same value",
			policy: `
			permit(
				principal,
				action == Action::"read_resource",
				resource
			)
			when {
				resource.name == resource.uri
			};
			`,
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			feature:          authorizers.MCPFeatureResource,
			operation:        authorizers.MCPOperationRead,
			resourceID:       "sensitive_data",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User can get prompt",
			policy: `
			permit(
				principal,
				action == Action::"get_prompt",
				resource == Prompt::"greeting"
			);
			`,
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
				"role": "user",
			},
			feature:          authorizers.MCPFeaturePrompt,
			operation:        authorizers.MCPOperationGet,
			resourceID:       "greeting",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User can list tools",
			policy: `
			permit(
				principal,
				action == Action::"list_tools",
				resource == FeatureType::"tool"
			);
			`,
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
				"role": "user",
			},
			feature:          authorizers.MCPFeatureTool,
			operation:        authorizers.MCPOperationList,
			resourceID:       "",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User can list prompts",
			policy: `
			permit(
				principal,
				action == Action::"list_prompts",
				resource == FeatureType::"prompt"
			);
			`,
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
				"role": "user",
			},
			feature:          authorizers.MCPFeaturePrompt,
			operation:        authorizers.MCPOperationList,
			resourceID:       "",
			arguments:        nil,
			expectAuthorized: true,
		},
		{
			name: "User can list resources",
			policy: `
			permit(
				principal,
				action == Action::"list_resources",
				resource == FeatureType::"resource"
			);
			`,
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
				"role": "user",
			},
			feature:          authorizers.MCPFeatureResource,
			operation:        authorizers.MCPOperationList,
			resourceID:       "",
			arguments:        nil,
			expectAuthorized: true,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a context
			ctx := context.Background()

			// Create a Cedar authorizer
			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     []string{tc.policy},
				EntitiesJSON: `[]`,
			}, "")
			require.NoError(t, err, "Failed to create Cedar authorizer")

			// Create a context with JWT claims
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "test-user", Claims: tc.claims}}
			claimsCtx := auth.WithIdentity(ctx, identity)

			// Test authorization
			authorized, err := authorizer.AuthorizeWithJWTClaims(claimsCtx, tc.feature, tc.operation, tc.resourceID, tc.arguments)
			assert.NoError(t, err, "Authorization error")
			assert.Equal(t, tc.expectAuthorized, authorized, "Authorization result does not match expectation")
		})
	}
}

// TestAuthorizeWithJWTClaimsErrors tests error cases for AuthorizeWithJWTClaims.
func TestAuthorizeWithJWTClaimsErrors(t *testing.T) {
	t.Parallel()
	// Create a context
	ctx := context.Background()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Test cases
	testCases := []struct {
		name        string
		setupCtx    func(context.Context) context.Context
		feature     authorizers.MCPFeature
		operation   authorizers.MCPOperation
		resourceID  string
		arguments   map[string]interface{}
		expectError bool
		errorType   error
	}{
		{
			name: "Missing claims in context",
			setupCtx: func(ctx context.Context) context.Context {
				// Don't add claims to the context
				return ctx
			},
			feature:     authorizers.MCPFeatureTool,
			operation:   authorizers.MCPOperationCall,
			resourceID:  "weather",
			arguments:   nil,
			expectError: true,
			errorType:   ErrMissingPrincipal,
		},
		{
			name: "Missing sub claim",
			setupCtx: func(ctx context.Context) context.Context {
				// Add claims without sub
				claims := jwt.MapClaims{
					"name": "John Doe",
					"role": "user",
				}
				identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "", Claims: claims}}
				return auth.WithIdentity(ctx, identity)
			},
			feature:     authorizers.MCPFeatureTool,
			operation:   authorizers.MCPOperationCall,
			resourceID:  "weather",
			arguments:   nil,
			expectError: true,
			errorType:   ErrMissingPrincipal,
		},
		{
			name: "Empty sub claim",
			setupCtx: func(ctx context.Context) context.Context {
				// Add claims with empty sub
				claims := jwt.MapClaims{
					"sub":  "",
					"name": "John Doe",
					"role": "user",
				}
				identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "", Claims: claims}}
				return auth.WithIdentity(ctx, identity)
			},
			feature:     authorizers.MCPFeatureTool,
			operation:   authorizers.MCPOperationCall,
			resourceID:  "weather",
			arguments:   nil,
			expectError: true,
			errorType:   ErrMissingPrincipal,
		},
		{
			name: "Unsupported feature/operation combination",
			setupCtx: func(ctx context.Context) context.Context {
				// Add valid claims
				claims := jwt.MapClaims{
					"sub":  "user123",
					"name": "John Doe",
					"role": "user",
				}
				identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user123", Claims: claims}}
				return auth.WithIdentity(ctx, identity)
			},
			feature:     "invalid_feature",
			operation:   "invalid_operation",
			resourceID:  "resource",
			arguments:   nil,
			expectError: true,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Setup context
			testCtx := tc.setupCtx(ctx)

			// Test authorization
			_, err := authorizer.AuthorizeWithJWTClaims(testCtx, tc.feature, tc.operation, tc.resourceID, tc.arguments)
			assert.Error(t, err, "Expected an error")
			if tc.errorType != nil {
				assert.ErrorIs(t, err, tc.errorType, "Expected error type %v but got %v", tc.errorType, err)
			}
		})
	}
}

// TestExtractConfig tests the ExtractConfig function
func TestExtractConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		config      *authorizers.Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Nil config",
			config:      nil,
			expectError: true,
			errorMsg:    "config is nil",
		},
		{
			name: "Empty raw config",
			config: &authorizers.Config{
				Version: "1.0",
				Type:    ConfigType,
			},
			expectError: true,
			errorMsg:    "config has no raw data",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config, err := ExtractConfig(tc.config)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				assert.Nil(t, config)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)
		})
	}
}

// TestExtractConfigValid tests ExtractConfig with a valid config
func TestExtractConfigValid(t *testing.T) {
	t.Parallel()

	// Create a valid Cedar config
	cedarConfig := Config{
		Version: "1.0",
		Type:    ConfigType,
		Options: &ConfigOptions{
			Policies:     []string{`permit(principal, action, resource);`},
			EntitiesJSON: "[]",
		},
	}

	// Create an authorizers.Config from it
	authzConfig, err := authorizers.NewConfig(cedarConfig)
	require.NoError(t, err)

	// Extract the Cedar config
	extracted, err := ExtractConfig(authzConfig)
	require.NoError(t, err)
	require.NotNil(t, extracted)
	require.NotNil(t, extracted.Options)
	assert.Equal(t, cedarConfig.Version, extracted.Version)
	assert.Equal(t, cedarConfig.Type, extracted.Type)
	assert.Equal(t, cedarConfig.Options.Policies, extracted.Options.Policies)
}

// TestExtractConfigMissingCedarField tests ExtractConfig with missing cedar field
func TestExtractConfigMissingCedarField(t *testing.T) {
	t.Parallel()

	// Create a config without the cedar field
	authzConfig, err := authorizers.NewConfig(map[string]interface{}{
		"version": "1.0",
		"type":    ConfigType,
		// No "cedar" field
	})
	require.NoError(t, err)

	// Extract should fail
	_, err = ExtractConfig(authzConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cedar config is nil")
}

// TestFactoryValidateConfig tests the Factory.ValidateConfig method
func TestFactoryValidateConfig(t *testing.T) {
	t.Parallel()

	factory := &Factory{}

	testCases := []struct {
		name        string
		rawConfig   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Invalid JSON",
			rawConfig:   `{"invalid`,
			expectError: true,
			errorMsg:    "failed to parse configuration",
		},
		{
			name:        "Missing cedar field",
			rawConfig:   `{"version":"1.0","type":"cedarv1"}`,
			expectError: true,
			errorMsg:    "cedar configuration is required",
		},
		{
			name:        "Empty policies",
			rawConfig:   `{"version":"1.0","type":"cedarv1","cedar":{"policies":[]}}`,
			expectError: true,
			errorMsg:    "at least one policy is required",
		},
		{
			name:        "Valid config",
			rawConfig:   `{"version":"1.0","type":"cedarv1","cedar":{"policies":["permit(principal, action, resource);"]}}`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := factory.ValidateConfig([]byte(tc.rawConfig))

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}

// TestFactoryCreateAuthorizer tests the Factory.CreateAuthorizer method
func TestFactoryCreateAuthorizer(t *testing.T) {
	t.Parallel()

	factory := &Factory{}

	testCases := []struct {
		name        string
		rawConfig   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Invalid JSON",
			rawConfig:   `{"invalid`,
			expectError: true,
			errorMsg:    "failed to parse configuration",
		},
		{
			name:        "Missing cedar field",
			rawConfig:   `{"version":"1.0","type":"cedarv1"}`,
			expectError: true,
			errorMsg:    "cedar configuration is required",
		},
		{
			name:        "Valid config",
			rawConfig:   `{"version":"1.0","type":"cedarv1","cedar":{"policies":["permit(principal, action, resource);"]}}`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer, err := factory.CreateAuthorizer([]byte(tc.rawConfig), "testServer")

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				assert.Nil(t, authorizer)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, authorizer)

			cedarAuthz, ok := authorizer.(*Authorizer)
			require.True(t, ok)
			assert.Equal(t, "testServer", cedarAuthz.serverName)
		})
	}
}

//nolint:paralleltest,tparallel // Subtests cannot be parallelized as they modify shared authorizer state
func TestUpdatePolicies(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type to access UpdatePolicies
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	testCases := []struct {
		name        string
		policies    []string
		expectError bool
		errorType   error
	}{
		{
			name:        "Empty policies",
			policies:    []string{},
			expectError: true,
			errorType:   ErrNoPolicies,
		},
		{
			name:        "Invalid policy",
			policies:    []string{`invalid policy syntax`},
			expectError: true,
		},
		{
			name:        "Valid policy",
			policies:    []string{`forbid(principal, action, resource);`},
			expectError: false,
		},
		{
			name:        "Multiple valid policies",
			policies:    []string{`permit(principal, action, resource);`, `forbid(principal == Client::"evil", action, resource);`},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := cedarAuthorizer.UpdatePolicies(tc.policies)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorType != nil {
					assert.ErrorIs(t, err, tc.errorType)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}

//nolint:paralleltest,tparallel // Subtests cannot be parallelized as they modify shared authorizer state
func TestUpdateEntities(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type to access UpdateEntities
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	testCases := []struct {
		name         string
		entitiesJSON string
		expectError  bool
	}{
		{
			name:         "Invalid JSON",
			entitiesJSON: `invalid`,
			expectError:  true,
		},
		{
			name:         "Empty array",
			entitiesJSON: `[]`,
			expectError:  false,
		},
		{
			name:         "Valid entities",
			entitiesJSON: `[{"uid": {"type": "User", "id": "alice"}, "attrs": {}, "parents": []}]`,
			expectError:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := cedarAuthorizer.UpdateEntities(tc.entitiesJSON)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
		})
	}
}

// TestEntityOperations tests AddEntity, RemoveEntity, and GetEntity methods
func TestEntityOperations(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type to access entity methods
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Get entity factory
	factory := cedarAuthorizer.GetEntityFactory()
	require.NotNil(t, factory)

	// Create a test entity using the factory
	uid, entity := factory.CreatePrincipalEntity("Client", "testuser", map[string]interface{}{
		"name": "Test User",
	})

	// Add entity
	cedarAuthorizer.AddEntity(entity)

	// Get entity
	retrieved, found := cedarAuthorizer.GetEntity(uid)
	assert.True(t, found)
	assert.Equal(t, uid, retrieved.UID)

	// Remove entity
	cedarAuthorizer.RemoveEntity(uid)

	// Verify entity is removed
	_, found = cedarAuthorizer.GetEntity(uid)
	assert.False(t, found)
}

// TestGetEntityNotFound tests GetEntity for a non-existent entity
func TestGetEntityNotFound(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Create a UID that doesn't exist
	factory := cedarAuthorizer.GetEntityFactory()
	uid, _ := factory.CreatePrincipalEntity("Client", "nonexistent", nil)

	// Try to get it
	_, found := cedarAuthorizer.GetEntity(uid)
	assert.False(t, found)
}

// TestIsAuthorizedErrors tests error cases for IsAuthorized
func TestIsAuthorizedErrors(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	testCases := []struct {
		name        string
		principal   string
		action      string
		resource    string
		expectError bool
		errorType   error
	}{
		{
			name:        "Empty principal",
			principal:   "",
			action:      "Action::test",
			resource:    "Resource::test",
			expectError: true,
			errorType:   ErrMissingPrincipal,
		},
		{
			name:        "Empty action",
			principal:   "Client::test",
			action:      "",
			resource:    "Resource::test",
			expectError: true,
			errorType:   ErrMissingAction,
		},
		{
			name:        "Empty resource",
			principal:   "Client::test",
			action:      "Action::test",
			resource:    "",
			expectError: true,
			errorType:   ErrMissingResource,
		},
		{
			name:        "Invalid principal format",
			principal:   "invalid",
			action:      "Action::test",
			resource:    "Resource::test",
			expectError: true,
		},
		{
			name:        "Invalid action format",
			principal:   "Client::test",
			action:      "invalid",
			resource:    "Resource::test",
			expectError: true,
		},
		{
			name:        "Invalid resource format",
			principal:   "Client::test",
			action:      "Action::test",
			resource:    "invalid",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := cedarAuthorizer.IsAuthorized(tc.principal, tc.action, tc.resource, nil)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorType != nil {
					assert.ErrorIs(t, err, tc.errorType)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}

// TestIsAuthorizedWithEntities tests IsAuthorized with custom entities
func TestIsAuthorizedWithEntities(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer with a policy that checks entity attributes
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource
			);
		`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	// Cast to concrete type
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Get factory and create entities
	factory := cedarAuthorizer.GetEntityFactory()
	entities, err := factory.CreateEntitiesForRequest(
		"Client::testuser",
		"Action::call_tool",
		"Tool::weather",
		map[string]interface{}{"name": "Test User"},
		map[string]interface{}{"name": "weather"},
		nil,
		"",
	)
	require.NoError(t, err)

	// Test authorization with custom entities
	authorized, err := cedarAuthorizer.IsAuthorized(
		"Client::testuser",
		"Action::call_tool",
		"Tool::weather",
		map[string]interface{}{},
		entities,
	)
	assert.NoError(t, err)
	assert.True(t, authorized)
}

// TestServerScopedPolicyWithMCPParent verifies end-to-end Cedar evaluation
// with a server-scoped policy. When the authorizer has a serverName, resource
// entities get an MCP parent and `resource in MCP::"<server>"` matches.
// When serverName is empty, the same policy denies because there is no parent.
func TestServerScopedPolicyWithMCPParent(t *testing.T) {
	t.Parallel()

	policy := `permit(
		principal,
		action == Action::"call_tool",
		resource in MCP::"test-server"
	);`

	// The MCP entity must be present in the entity store for Cedar's `in`
	// operator to traverse the parent chain. In production this comes from
	// entities_json managed by the enterprise controller.
	mcpEntity := `[{"uid":{"type":"MCP","id":"test-server"},"parents":[],"attrs":{}}]`

	tests := []struct {
		name       string
		serverName string
		wantAllow  bool
	}{
		{
			name:       "serverName_matches_policy_permits",
			serverName: "test-server",
			wantAllow:  true,
		},
		{
			name:       "empty_serverName_policy_denies",
			serverName: "",
			wantAllow:  false,
		},
		{
			name:       "wrong_serverName_policy_denies",
			serverName: "other-server",
			wantAllow:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     []string{policy},
				EntitiesJSON: mcpEntity,
			}, tt.serverName)
			require.NoError(t, err)

			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "testuser", Claims: map[string]interface{}{"sub": "testuser"}}}
			ctx := auth.WithIdentity(context.Background(), identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "weather", nil)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantAllow, authorized,
				"serverName=%q: expected allow=%v", tt.serverName, tt.wantAllow)
		})
	}
}

// TestParseUpstreamJWTClaims tests the parseUpstreamJWTClaims helper.
func TestParseUpstreamJWTClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		token       string
		wantErr     bool
		errContains string
		checkClaims func(t *testing.T, claims jwt.MapClaims)
	}{
		{
			name: "valid_jwt_with_groups_claim",
			token: makeUnsignedJWT(jwt.MapClaims{
				"sub":    "upstream-user",
				"groups": []interface{}{"eng", "platform"},
			}),
			wantErr: false,
			checkClaims: func(t *testing.T, claims jwt.MapClaims) {
				t.Helper()
				sub, err := claims.GetSubject()
				require.NoError(t, err)
				assert.Equal(t, "upstream-user", sub)
				_, ok := claims["groups"]
				assert.True(t, ok, "expected 'groups' claim to be present")
			},
		},
		{
			name: "valid_jwt_minimal_claims",
			token: makeUnsignedJWT(jwt.MapClaims{
				"sub": "user42",
				"iss": "https://idp.example.com",
			}),
			wantErr: false,
			checkClaims: func(t *testing.T, claims jwt.MapClaims) {
				t.Helper()
				sub, err := claims.GetSubject()
				require.NoError(t, err)
				assert.Equal(t, "user42", sub)
			},
		},
		{
			name:        "opaque_token_returns_error",
			token:       "opaque-token-not-a-jwt",
			wantErr:     true,
			errContains: "upstream token is not a parseable JWT",
		},
		{
			name:        "empty_string_returns_error",
			token:       "",
			wantErr:     true,
			errContains: "upstream token is not a parseable JWT",
		},
		{
			name:        "random_base64_not_jwt",
			token:       "aGVsbG8=.d29ybGQ=",
			wantErr:     true,
			errContains: "upstream token is not a parseable JWT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims, err := parseUpstreamJWTClaims(tt.token)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, claims)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, claims)
			if tt.checkClaims != nil {
				tt.checkClaims(t, claims)
			}
		})
	}
}

// TestAuthorizeWithJWTClaims_UpstreamProvider tests AuthorizeWithJWTClaims
// when primaryUpstreamProvider is set, exercising the upstream token path.
func TestAuthorizeWithJWTClaims_UpstreamProvider(t *testing.T) {
	t.Parallel()

	const providerName = "github"

	// Policy that allows a call only when the upstream claim_sub matches.
	policy := `
		permit(
			principal,
			action == Action::"call_tool",
			resource == Tool::"deploy"
		)
		when {
			context.claim_sub == "upstream-user"
		};
	`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:                []string{policy},
		EntitiesJSON:            `[]`,
		PrimaryUpstreamProvider: providerName,
	}, "")
	require.NoError(t, err)

	upstreamToken := makeUnsignedJWT(jwt.MapClaims{
		"sub": "upstream-user",
		"iss": "https://idp.example.com",
	})

	tests := []struct {
		name          string
		identity      *auth.Identity
		wantAuthorize bool
		wantErr       bool
		errContains   string
	}{
		{
			name: "upstream_token_present_and_authorized",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: upstreamToken,
				},
			},
			wantAuthorize: true,
		},
		{
			name: "upstream_token_present_but_wrong_sub",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"sub": "different-upstream-user",
					}),
				},
			},
			wantAuthorize: false,
		},
		{
			name: "upstream_token_missing_from_identity",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{},
			},
			wantErr:     true,
			errContains: "upstream token for provider",
		},
		{
			name: "upstream_token_opaque_falls_back_to_request_claims_denied",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: "opaque-token-cannot-be-parsed",
				},
			},
			// Opaque upstream tokens (Google's ya29.*, GitHub's gho_*, etc.)
			// trigger the fallback to identity.Claims. Here the request-token
			// sub does not match the policy, so authorization is correctly
			// denied based on policy evaluation rather than a parse-time error.
			wantAuthorize: false,
		},
		{
			name: "upstream_token_opaque_falls_back_to_request_claims_permitted",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "upstream-user",
					Claims:  map[string]any{"sub": "upstream-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: "opaque-token-cannot-be-parsed",
				},
			},
			// When the upstream token is not a JWT, Cedar evaluates against
			// the request-token claims. The embedded auth server already
			// mirrors the upstream OIDC sub/email/name into its issued token,
			// so a policy referencing claim_sub still matches the user.
			wantAuthorize: true,
		},
		{
			name: "upstream_token_jwt_shaped_but_malformed_still_errors",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					// Three-segment shape (looks like a JWT) but the segments are
					// not valid base64-encoded JSON — i.e. a tampered or
					// corrupted JWT. The fallback path MUST NOT trigger here:
					// silently degrading a tampered upstream JWT to fallback
					// claims would be a security regression.
					providerName: "not-base64.not-base64.not-base64",
				},
			},
			wantErr:     true,
			errContains: "failed to parse upstream token",
		},
		{
			name: "upstream_tokens_nil_map",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: nil,
			},
			wantErr:     true,
			errContains: "upstream token for provider",
		},
		{
			name: "upstream_token_has_no_sub_claim",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"iss": "https://idp.example.com",
						// intentionally no "sub"
					}),
				},
			},
			wantErr:     true,
			errContains: "missing principal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := auth.WithIdentity(context.Background(), tt.identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"deploy",
				nil,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthorize, authorized)
		})
	}
}

// TestAuthorizeWithJWTClaims_GroupMembership verifies that Cedar policies using
// "principal in THVGroup::..." are enforced when groups are present in the claims.
func TestAuthorizeWithJWTClaims_GroupMembership(t *testing.T) {
	t.Parallel()

	// Policy: only members of "engineering" may call the deploy tool.
	policy := `
		permit(
			principal in THVGroup::"engineering",
			action == Action::"call_tool",
			resource == Tool::"deploy"
		);
	`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:       []string{policy},
		EntitiesJSON:   `[]`,
		GroupClaimName: "groups",
	}, "")
	require.NoError(t, err)

	tests := []struct {
		name          string
		claims        jwt.MapClaims
		wantAuthorize bool
	}{
		{
			name: "member_of_engineering_is_authorized",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"engineering", "platform"},
			},
			wantAuthorize: true,
		},
		{
			name: "non_member_is_denied",
			claims: jwt.MapClaims{
				"sub":    "user2",
				"groups": []interface{}{"marketing"},
			},
			wantAuthorize: false,
		},
		{
			name: "no_groups_claim_is_denied",
			claims: jwt.MapClaims{
				"sub": "user3",
			},
			wantAuthorize: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: tt.claims["sub"].(string),
					Claims:  map[string]any(tt.claims),
				},
			}
			ctx := auth.WithIdentity(context.Background(), identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"deploy",
				nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthorize, authorized)
		})
	}
}

// TestAuthorizeWithJWTClaims_TransitiveHierarchyPreserved is a regression test
// for the merge-order hazard fixed in 40119c8e. When entities_json defines a
// THVGroup with a THVRole parent, the transitive policy "principal in THVRole"
// must still evaluate correctly after the request entity merge in IsAuthorized.
// Before the fix, CreateEntitiesForRequest inserted bare THVGroup entities that
// overwrote the static ones (which carry THVRole parents), severing the hierarchy.
func TestAuthorizeWithJWTClaims_TransitiveHierarchyPreserved(t *testing.T) {
	t.Parallel()

	// Policy: only members of THVRole::"developer" may call the deploy tool.
	// The user is in THVGroup::"engineering" which is a child of THVRole::"developer"
	// in entities_json — so this requires transitive "in" evaluation.
	policy := `
		permit(
			principal in THVRole::"developer",
			action == Action::"call_tool",
			resource == Tool::"deploy"
		);
	`

	// Static entities: THVGroup::"engineering" → THVRole::"developer".
	entitiesJSON := `[
		{
			"uid": {"type": "THVGroup", "id": "engineering"},
			"attrs": {},
			"parents": [{"type": "THVRole", "id": "developer"}]
		},
		{
			"uid": {"type": "THVRole", "id": "developer"},
			"attrs": {},
			"parents": []
		}
	]`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:       []string{policy},
		EntitiesJSON:   entitiesJSON,
		GroupClaimName: "groups",
	}, "")
	require.NoError(t, err)

	// User belongs to "engineering" via JWT groups claim.
	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user1",
			Claims: map[string]any{
				"sub":    "user1",
				"groups": []interface{}{"engineering"},
			},
		},
	}
	ctx := auth.WithIdentity(context.Background(), identity)

	authorized, err := authorizer.AuthorizeWithJWTClaims(
		ctx,
		authorizers.MCPFeatureTool,
		authorizers.MCPOperationCall,
		"deploy",
		nil,
	)
	require.NoError(t, err)
	assert.True(t, authorized,
		"transitive hierarchy THVGroup→THVRole from entities_json must survive entity merge")
}

// TestAuthorizeWithJWTClaims_DoesNotMutateIdentity verifies that
// AuthorizeWithJWTClaims does not mutate the Identity stored in context.
// The Identity contract (see auth.Identity) requires that the struct MUST NOT
// be modified after it is placed in the request context to avoid concurrent
// write races with other middleware reading the same pointer.
func TestAuthorizeWithJWTClaims_DoesNotMutateIdentity(t *testing.T) {
	t.Parallel()

	policy := `permit(principal, action, resource);`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:       []string{policy},
		EntitiesJSON:   `[]`,
		GroupClaimName: "groups",
	}, "")
	require.NoError(t, err)

	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user1",
			Claims: map[string]any{
				"sub":    "user1",
				"groups": []interface{}{"devs", "ops"},
			},
		},
	}
	// Record pre-call state.
	originalGroups := identity.Groups // nil before the call

	ctx := auth.WithIdentity(context.Background(), identity)

	_, err = authorizer.AuthorizeWithJWTClaims(
		ctx,
		authorizers.MCPFeatureTool,
		authorizers.MCPOperationCall,
		"any-tool",
		nil,
	)
	require.NoError(t, err)

	// Identity.Groups must NOT have been written by the authorizer.
	assert.Equal(t, originalGroups, identity.Groups,
		"authorizer must not mutate Identity after it is placed in context")
}

// TestAuthorizeWithJWTClaims_CustomGroupClaimName tests that GroupClaimName
// is respected when resolving group membership.
func TestAuthorizeWithJWTClaims_CustomGroupClaimName(t *testing.T) {
	t.Parallel()

	policy := `
		permit(
			principal in THVGroup::"platform",
			action == Action::"call_tool",
			resource
		);
	`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:       []string{policy},
		EntitiesJSON:   `[]`,
		GroupClaimName: "https://example.com/groups",
	}, "")
	require.NoError(t, err)

	// The custom claim holds "platform"; the well-known "groups" key holds other groups.
	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user1",
			Claims: map[string]any{
				"sub":                        "user1",
				"https://example.com/groups": []interface{}{"platform"},
				"groups":                     []interface{}{"other"},
			},
		},
	}
	ctx := auth.WithIdentity(context.Background(), identity)

	authorized, err := authorizer.AuthorizeWithJWTClaims(
		ctx,
		authorizers.MCPFeatureTool,
		authorizers.MCPOperationCall,
		"some-tool",
		nil,
	)
	require.NoError(t, err)
	assert.True(t, authorized, "expected authorization via custom group claim")
}

// TestAuthorizeWithJWTClaims_UpstreamProviderWithGroups verifies the end-to-end
// path where PrimaryUpstreamProvider is set AND the Cedar policy uses group-based
// authorization (principal in THVGroup::"..."). Groups must be extracted from the
// upstream token's claims, not from the ToolHive-issued token.
func TestAuthorizeWithJWTClaims_UpstreamProviderWithGroups(t *testing.T) {
	t.Parallel()

	const providerName = "github"

	// Policy: only members of "platform-eng" may call the deploy tool.
	policy := `
		permit(
			principal in THVGroup::"platform-eng",
			action == Action::"call_tool",
			resource == Tool::"deploy"
		);
	`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:                []string{policy},
		EntitiesJSON:            `[]`,
		PrimaryUpstreamProvider: providerName,
		GroupClaimName:          "groups",
	}, "")
	require.NoError(t, err)

	tests := []struct {
		name          string
		identity      *auth.Identity
		wantAuthorize bool
		wantErr       bool
		errContains   string
	}{
		{
			name: "upstream_groups_authorize",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"sub":    "upstream-user",
						"groups": []interface{}{"platform-eng", "devs"},
					}),
				},
			},
			wantAuthorize: true,
		},
		{
			name: "upstream_groups_deny_wrong_group",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"sub":    "upstream-user",
						"groups": []interface{}{"marketing"},
					}),
				},
			},
			wantAuthorize: false,
		},
		{
			name: "upstream_no_groups_deny",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"sub": "upstream-user",
					}),
				},
			},
			wantAuthorize: false,
		},
		{
			name: "toolhive_groups_ignored_when_upstream_configured",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					// ToolHive token has the right group, but it should be ignored.
					Claims: map[string]any{
						"sub":    "thv-user",
						"groups": []interface{}{"platform-eng"},
					},
				},
				UpstreamTokens: map[string]string{
					// Upstream token has no groups.
					providerName: makeUnsignedJWT(jwt.MapClaims{
						"sub": "upstream-user",
					}),
				},
			},
			wantAuthorize: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := auth.WithIdentity(context.Background(), tt.identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"deploy",
				nil,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthorize, authorized)
		})
	}
}

// TestInjectUpstreamProvider tests the InjectUpstreamProvider helper.
func TestInjectUpstreamProvider(t *testing.T) {
	t.Parallel()

	baseCedarConfig := Config{
		Version: "1.0",
		Type:    ConfigType,
		Options: &ConfigOptions{
			Policies:     []string{`permit(principal, action, resource);`},
			EntitiesJSON: "[]",
		},
	}

	tests := []struct {
		name         string
		setup        func(t *testing.T) *authorizers.Config
		providerName string
		wantErr      bool
		checkResult  func(t *testing.T, result *authorizers.Config)
	}{
		{
			name: "injects_provider_name",
			setup: func(t *testing.T) *authorizers.Config {
				t.Helper()
				cfg, err := authorizers.NewConfig(baseCedarConfig)
				require.NoError(t, err)
				return cfg
			},
			providerName: "github",
			wantErr:      false,
			checkResult: func(t *testing.T, result *authorizers.Config) {
				t.Helper()
				extracted, err := ExtractConfig(result)
				require.NoError(t, err)
				assert.Equal(t, "github", extracted.Options.PrimaryUpstreamProvider)
				// Other options should be preserved.
				assert.NotEmpty(t, extracted.Options.Policies)
			},
		},
		{
			name: "empty_provider_name_returns_src_unchanged",
			setup: func(t *testing.T) *authorizers.Config {
				t.Helper()
				cfg, err := authorizers.NewConfig(baseCedarConfig)
				require.NoError(t, err)
				return cfg
			},
			providerName: "",
			wantErr:      false,
			checkResult: func(t *testing.T, result *authorizers.Config) {
				t.Helper()
				extracted, err := ExtractConfig(result)
				require.NoError(t, err)
				assert.Empty(t, extracted.Options.PrimaryUpstreamProvider)
			},
		},
		{
			name: "nil_src_returns_nil",
			setup: func(t *testing.T) *authorizers.Config {
				t.Helper()
				return nil
			},
			providerName: "github",
			wantErr:      false,
			checkResult: func(t *testing.T, result *authorizers.Config) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		{
			// GroupClaimName and RoleClaimName must survive the
			// serialise→deserialise round-trip that InjectUpstreamProvider
			// performs internally. A refactor that reconstructed ConfigOptions
			// from scratch (populating only known fields) would silently drop
			// these claim name fields without this test.
			name: "claim_names_preserved_after_inject",
			setup: func(t *testing.T) *authorizers.Config {
				t.Helper()
				cfg, err := authorizers.NewConfig(Config{
					Version: "1.0",
					Type:    ConfigType,
					Options: &ConfigOptions{
						Policies:       []string{`permit(principal, action, resource);`},
						EntitiesJSON:   "[]",
						GroupClaimName: "https://example.com/groups",
						RoleClaimName:  "https://example.com/roles",
					},
				})
				require.NoError(t, err)
				return cfg
			},
			providerName: "my-provider",
			wantErr:      false,
			checkResult: func(t *testing.T, result *authorizers.Config) {
				t.Helper()
				extracted, err := ExtractConfig(result)
				require.NoError(t, err)
				assert.Equal(t, "https://example.com/groups", extracted.Options.GroupClaimName,
					"GroupClaimName must be unchanged after InjectUpstreamProvider")
				assert.Equal(t, "https://example.com/roles", extracted.Options.RoleClaimName,
					"RoleClaimName must be unchanged after InjectUpstreamProvider")
				assert.Equal(t, "my-provider", extracted.Options.PrimaryUpstreamProvider,
					"PrimaryUpstreamProvider must be set by InjectUpstreamProvider")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := tt.setup(t)
			result, err := InjectUpstreamProvider(src, tt.providerName)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

// TestInjectUpstreamProvider_NonCedarPassThrough verifies that a config whose
// authorizer type is not "cedarv1" is returned as the identical pointer.
// This is the key safety property that allows InjectUpstreamProvider to be
// called unconditionally without knowing the authorizer type in advance.
func TestInjectUpstreamProvider_NonCedarPassThrough(t *testing.T) {
	t.Parallel()

	src, err := authorizers.NewConfig(map[string]interface{}{
		"version": "1.0",
		"type":    "http", // deliberately not "cedarv1"
	})
	require.NoError(t, err)

	result, err := InjectUpstreamProvider(src, "github")
	require.NoError(t, err)
	assert.Same(t, src, result,
		"non-Cedar config must be returned as the same pointer — InjectUpstreamProvider must be a no-op for unknown types")
}

// TestResolveNestedClaim tests the resolveNestedClaim function.
func TestResolveNestedClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims jwt.MapClaims
		path   string
		want   interface{}
	}{
		{
			name:   "exact_top_level_match",
			claims: jwt.MapClaims{"groups": []interface{}{"eng", "platform"}},
			path:   "groups",
			want:   []interface{}{"eng", "platform"},
		},
		{
			name: "dot_notation_traversal",
			claims: jwt.MapClaims{
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"admin", "user"},
				},
			},
			path: "realm_access.roles",
			want: []interface{}{"admin", "user"},
		},
		{
			name: "auth0_url_claim_with_dots_matches_exact_first",
			claims: jwt.MapClaims{
				"https://myapp.example.com/roles": []interface{}{"editor"},
			},
			path: "https://myapp.example.com/roles",
			want: []interface{}{"editor"},
		},
		{
			name:   "missing_claim_returns_nil",
			claims: jwt.MapClaims{"sub": "user1"},
			path:   "nonexistent",
			want:   nil,
		},
		{
			name: "nested_traversal_hits_non_object",
			claims: jwt.MapClaims{
				"foo": "a-string-not-a-map",
			},
			path: "foo.bar",
			want: nil,
		},
		{
			name: "three_level_nesting",
			claims: jwt.MapClaims{
				"resource_access": map[string]interface{}{
					"my-app": map[string]interface{}{
						"roles": []interface{}{"viewer", "contributor"},
					},
				},
			},
			path: "resource_access.my-app.roles",
			want: []interface{}{"viewer", "contributor"},
		},
		{
			name:   "empty_path_returns_nil",
			claims: jwt.MapClaims{"groups": []interface{}{"eng"}},
			path:   "",
			want:   nil,
		},
		{
			name:   "empty_claims_returns_nil",
			claims: jwt.MapClaims{},
			path:   "groups",
			want:   nil,
		},
		{
			name: "partial_nested_path_missing_leaf",
			claims: jwt.MapClaims{
				"realm_access": map[string]interface{}{
					"other": "value",
				},
			},
			path: "realm_access.roles",
			want: nil,
		},
		{
			// Pathological path shapes. Each produces at least one empty
			// segment after Split, which the traversal loop treats as a
			// missing key. Pinned as tests so a future refactor that tries
			// to "normalize" paths by skipping empty segments cannot silently
			// change resolution behavior.
			name: "trailing_dot_returns_nil",
			claims: jwt.MapClaims{
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"admin"},
				},
			},
			path: "realm_access.",
			want: nil,
		},
		{
			name: "leading_dot_returns_nil",
			claims: jwt.MapClaims{
				"roles": []interface{}{"admin"},
			},
			path: ".roles",
			want: nil,
		},
		{
			name: "consecutive_dots_return_nil",
			claims: jwt.MapClaims{
				"a": map[string]interface{}{
					"b": []interface{}{"x"},
				},
			},
			path: "a..b",
			want: nil,
		},
		{
			name: "exact_match_wins_over_dot_traversal",
			claims: jwt.MapClaims{
				"realm_access.roles": []interface{}{"literal-match"},
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"nested-match"},
				},
			},
			path: "realm_access.roles",
			want: []interface{}{"literal-match"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveNestedClaim(tt.claims, tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestExtractGroups tests the extractGroups function.
func TestExtractGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		claims     jwt.MapClaims
		claimName  string
		wantGroups []string
	}{
		{
			name:       "flat_claim_string_slice",
			claims:     jwt.MapClaims{"groups": []string{"admin", "developers"}},
			claimName:  "groups",
			wantGroups: []string{"admin", "developers"},
		},
		{
			name:       "flat_claim_interface_slice",
			claims:     jwt.MapClaims{"groups": []interface{}{"reader", "writer"}},
			claimName:  "groups",
			wantGroups: []string{"reader", "writer"},
		},
		{
			name: "nested_keycloak_claim",
			claims: jwt.MapClaims{
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"admin", "user"},
				},
			},
			claimName:  "realm_access.roles",
			wantGroups: []string{"admin", "user"},
		},
		{
			name:       "empty_claim_name_returns_nil",
			claims:     jwt.MapClaims{"groups": []interface{}{"eng"}},
			claimName:  "",
			wantGroups: nil,
		},
		{
			name:       "missing_claim_returns_nil",
			claims:     jwt.MapClaims{"sub": "user1"},
			claimName:  "groups",
			wantGroups: nil,
		},
		{
			name:       "non_array_claim_returns_nil",
			claims:     jwt.MapClaims{"groups": "not-a-slice"},
			claimName:  "groups",
			wantGroups: nil,
		},
		{
			name:       "non_string_elements_skipped",
			claims:     jwt.MapClaims{"groups": []interface{}{"valid", 42, true, "also-valid"}},
			claimName:  "groups",
			wantGroups: []string{"valid", "also-valid"},
		},
		{
			name:       "empty_array_returns_empty",
			claims:     jwt.MapClaims{"groups": []interface{}{}},
			claimName:  "groups",
			wantGroups: []string{},
		},
		{
			name: "auth0_url_claim_name",
			claims: jwt.MapClaims{
				"https://example.com/groups": []interface{}{"platform"},
			},
			claimName:  "https://example.com/groups",
			wantGroups: []string{"platform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractGroups(tt.claims, tt.claimName)
			assert.Equal(t, tt.wantGroups, got)
		})
	}
}

// TestDedup tests the dedup function.
func TestDedup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "nil_returns_nil",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty_returns_empty",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "no_duplicates",
			input: []string{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "with_duplicates_preserves_order",
			input: []string{"a", "b", "a", "c", "b"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "all_duplicates",
			input: []string{"x", "x", "x"},
			want:  []string{"x"},
		},
		{
			name:  "single_element",
			input: []string{"only"},
			want:  []string{"only"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dedup(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestAuthorizeWithJWTClaims_DualClaim verifies that groups from both
// GroupClaimName and RoleClaimName are merged and deduplicated for Cedar
// evaluation. This is the core dual-claim extraction behavior from #4768.
func TestAuthorizeWithJWTClaims_DualClaim(t *testing.T) {
	t.Parallel()

	// Policy: only members of "platform" may call the deploy tool.
	policy := `
		permit(
			principal in THVGroup::"platform",
			action == Action::"call_tool",
			resource == Tool::"deploy"
		);
	`

	tests := []struct {
		name          string
		groupClaim    string
		roleClaim     string
		claims        jwt.MapClaims
		wantAuthorize bool
	}{
		{
			name:       "group_claim_only",
			groupClaim: "groups",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"platform", "devs"},
			},
			wantAuthorize: true,
		},
		{
			name:      "role_claim_only",
			roleClaim: "roles",
			claims: jwt.MapClaims{
				"sub":   "user1",
				"roles": []interface{}{"platform"},
			},
			wantAuthorize: true,
		},
		{
			name:       "both_claims_merged",
			groupClaim: "groups",
			roleClaim:  "roles",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
				"roles":  []interface{}{"platform"},
			},
			wantAuthorize: true,
		},
		{
			name:       "duplicates_across_claims_are_deduplicated",
			groupClaim: "groups",
			roleClaim:  "roles",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"platform", "devs"},
				"roles":  []interface{}{"platform", "ops"},
			},
			wantAuthorize: true,
		},
		{
			name:       "neither_claim_has_matching_group",
			groupClaim: "groups",
			roleClaim:  "roles",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"marketing"},
				"roles":  []interface{}{"sales"},
			},
			wantAuthorize: false,
		},
		{
			name:       "both_claims_empty_falls_back_to_well_known",
			groupClaim: "",
			roleClaim:  "",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"platform"},
			},
			wantAuthorize: true, // well-known "groups" claim is checked when GroupClaimName is empty
		},
		{
			name:       "custom_group_claim_absent_falls_back_to_well_known",
			groupClaim: "https://example.com/groups",
			roleClaim:  "",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"platform"},
			},
			wantAuthorize: true, // custom claim missing, well-known "groups" used as fallback
		},
		{
			// Pins the "present but empty" semantic: if the configured custom
			// claim exists as an empty array, the IdP has explicitly said
			// "no groups" — fallback to well-known names MUST NOT fire. Without
			// this test, a refactor of extractGroups that returns nil on empty
			// arrays would silently flip the semantic and allow well-known
			// claims like "roles" to grant access.
			name:       "custom_group_claim_present_but_empty_does_not_fall_back",
			groupClaim: "https://example.com/groups",
			roleClaim:  "",
			claims: jwt.MapClaims{
				"sub":                        "user1",
				"https://example.com/groups": []interface{}{}, // present, empty
				"roles":                      []interface{}{"platform"},
			},
			wantAuthorize: false, // explicit empty suppresses fallback; "roles" is NOT consulted
		},
		{
			name:       "nested_role_claim_with_dot_notation",
			groupClaim: "groups",
			roleClaim:  "realm_access.roles",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"platform"},
				},
			},
			wantAuthorize: true,
		},
		{
			name:       "same_claim_for_both_dedup",
			groupClaim: "groups",
			roleClaim:  "groups",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"platform", "devs"},
			},
			wantAuthorize: true,
		},
		{
			name:       "group_claim_missing_from_jwt_role_claim_matches",
			groupClaim: "groups",
			roleClaim:  "roles",
			claims: jwt.MapClaims{
				"sub":   "user1",
				"roles": []interface{}{"platform"},
			},
			wantAuthorize: true,
		},
		{
			name:       "non_array_group_claim_role_claim_still_works",
			groupClaim: "groups",
			roleClaim:  "roles",
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": "not-an-array",
				"roles":  []interface{}{"platform"},
			},
			wantAuthorize: true,
		},
		{
			name:       "both_claims_use_dot_notation",
			groupClaim: "custom.groups",
			roleClaim:  "custom.roles",
			claims: jwt.MapClaims{
				"sub": "user1",
				"custom": map[string]interface{}{
					"groups": []interface{}{"devs"},
					"roles":  []interface{}{"platform"},
				},
			},
			wantAuthorize: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:       []string{policy},
				EntitiesJSON:   `[]`,
				GroupClaimName: tt.groupClaim,
				RoleClaimName:  tt.roleClaim,
			}, "")
			require.NoError(t, err)

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: tt.claims["sub"].(string),
					Claims:  map[string]any(tt.claims),
				},
			}
			ctx := auth.WithIdentity(context.Background(), identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"deploy",
				nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthorize, authorized)
		})
	}
}

// TestAuthorizeWithJWTClaims_BackwardCompat verifies that when both GroupClaimName
// and RoleClaimName are empty (pre-dual-claim configuration), the well-known
// fallback claim names ("groups", "roles", "cognito:groups") are still checked.
// This prevents a behavior regression for existing configs that rely on implicit
// group extraction without setting GroupClaimName.
func TestAuthorizeWithJWTClaims_BackwardCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		claimKey   string
		claimValue []interface{}
		wantAuth   bool
	}{
		{
			name:       "well-known groups claim extracted",
			claimKey:   "groups",
			claimValue: []interface{}{"eng"},
			wantAuth:   true,
		},
		{
			name:       "well-known roles claim extracted",
			claimKey:   "roles",
			claimValue: []interface{}{"eng"},
			wantAuth:   true,
		},
		{
			name:       "well-known cognito:groups claim extracted",
			claimKey:   "cognito:groups",
			claimValue: []interface{}{"eng"},
			wantAuth:   true,
		},
		{
			name:       "no well-known claim present denies",
			claimKey:   "custom_groups",
			claimValue: []interface{}{"eng"},
			wantAuth:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Group-based policy: only permits if the principal is in THVGroup::"eng".
			// This will fail unless groups are actually extracted from claims.
			policy := `permit(principal in THVGroup::"eng", action, resource);`

			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     []string{policy},
				EntitiesJSON: `[]`,
				// Both claim names empty — backward compatible mode.
			}, "")
			require.NoError(t, err)

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "user1",
					Claims: map[string]any{
						"sub":       "user1",
						tt.claimKey: tt.claimValue,
					},
				},
			}
			ctx := auth.WithIdentity(context.Background(), identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"any-tool",
				nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuth, authorized)
		})
	}
}

// TestParseCedarEntityID tests the parseCedarEntityID helper function.
func TestParseCedarEntityID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantType string
		wantID   string
		wantErr  bool
	}{
		{
			name:     "valid_client",
			input:    "Client::user123",
			wantType: "Client",
			wantID:   "user123",
		},
		{
			name:     "valid_action",
			input:    "Action::call_tool",
			wantType: "Action",
			wantID:   "call_tool",
		},
		{
			name:     "valid_thvgroup",
			input:    "THVGroup::engineering",
			wantType: "THVGroup",
			wantID:   "engineering",
		},
		{
			name:     "id_contains_double_colon",
			input:    "A::B::C",
			wantType: "A",
			wantID:   "B::C",
		},
		{
			name:    "no_separator",
			input:   "nodoublecolon",
			wantErr: true,
		},
		{
			name:    "empty_string",
			input:   "",
			wantErr: true,
		},
		{
			name:     "empty_type",
			input:    "::id",
			wantType: "",
			wantID:   "id",
		},
		{
			name:     "empty_id",
			input:    "Type::",
			wantType: "Type",
			wantID:   "",
		},
		{
			name:    "single_colon_no_match",
			input:   "Type:ID",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotType, gotID, err := parseCedarEntityID(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, gotType)
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

// TestSanitizeURIForCedar tests the sanitizeURIForCedar helper function.
func TestSanitizeURIForCedar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty_string", input: "", want: ""},
		{name: "already_clean", input: "simple_resource", want: "simple_resource"},
		{name: "colon", input: "a:b", want: "a_b"},
		{name: "forward_slash", input: "a/b", want: "a_b"},
		{name: "backslash", input: `a\b`, want: "a_b"},
		{name: "question_mark", input: "a?b", want: "a_b"},
		{name: "ampersand", input: "a&b", want: "a_b"},
		{name: "equals", input: "a=b", want: "a_b"},
		{name: "hash", input: "a#b", want: "a_b"},
		{name: "space", input: "a b", want: "a_b"},
		{name: "dot", input: "a.b", want: "a_b"},
		{
			name:  "complex_uri",
			input: "https://api.example.com/v1/data?key=val&other=123#fragment",
			want:  "https___api_example_com_v1_data_key_val_other_123_fragment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeURIForCedar(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestExtractClientIDFromClaims tests the extractClientIDFromClaims helper.
func TestExtractClientIDFromClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims jwt.MapClaims
		wantID string
		wantOK bool
	}{
		{
			name:   "valid_sub",
			claims: jwt.MapClaims{"sub": "user123"},
			wantID: "user123",
			wantOK: true,
		},
		{
			name:   "empty_sub",
			claims: jwt.MapClaims{"sub": ""},
			wantID: "",
			wantOK: false,
		},
		{
			name:   "missing_sub",
			claims: jwt.MapClaims{"name": "John"},
			wantID: "",
			wantOK: false,
		},
		{
			name:   "empty_claims",
			claims: jwt.MapClaims{},
			wantID: "",
			wantOK: false,
		},
		{
			name:   "non_string_sub",
			claims: jwt.MapClaims{"sub": 42},
			wantID: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, ok := extractClientIDFromClaims(tt.claims)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

// TestPreprocessClaims tests the preprocessClaims helper.
func TestPreprocessClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims jwt.MapClaims
		want   map[string]interface{}
	}{
		{
			name:   "standard_claims_get_prefix",
			claims: jwt.MapClaims{"sub": "user1", "role": "admin"},
			want:   map[string]interface{}{"claim_sub": "user1", "claim_role": "admin"},
		},
		{
			name:   "empty_map",
			claims: jwt.MapClaims{},
			want:   map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := preprocessClaims(tt.claims)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPreprocessArguments tests the preprocessArguments helper.
func TestPreprocessArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "simple_types_get_prefix",
			args: map[string]interface{}{"name": "test", "count": 5, "flag": true},
			want: map[string]interface{}{"arg_name": "test", "arg_count": 5, "arg_flag": true},
		},
		{
			name: "complex_type_gets_present_marker",
			args: map[string]interface{}{"data": map[string]interface{}{"nested": true}},
			want: map[string]interface{}{"arg_data_present": true},
		},
		{
			name: "nil_input_returns_nil",
			args: nil,
			want: nil,
		},
		{
			name: "float_preserved",
			args: map[string]interface{}{"score": float64(9.5)},
			want: map[string]interface{}{"arg_score": float64(9.5)},
		},
		{
			name: "int64_preserved",
			args: map[string]interface{}{"id": int64(42)},
			want: map[string]interface{}{"arg_id": int64(42)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := preprocessArguments(tt.args)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMergeContexts tests the mergeContexts helper.
func TestMergeContexts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		maps []map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "non_overlapping_merge",
			maps: []map[string]interface{}{
				{"a": 1},
				{"b": 2},
			},
			want: map[string]interface{}{"a": 1, "b": 2},
		},
		{
			name: "overlapping_later_wins",
			maps: []map[string]interface{}{
				{"a": 1, "b": 2},
				{"b": 3, "c": 4},
			},
			want: map[string]interface{}{"a": 1, "b": 3, "c": 4},
		},
		{
			name: "nil_maps_skipped",
			maps: []map[string]interface{}{
				{"a": 1},
				nil,
				{"b": 2},
			},
			want: map[string]interface{}{"a": 1, "b": 2},
		},
		{
			name: "all_nil_returns_empty",
			maps: []map[string]interface{}{nil, nil},
			want: map[string]interface{}{},
		},
		{
			name: "single_map",
			maps: []map[string]interface{}{{"a": 1}},
			want: map[string]interface{}{"a": 1},
		},
		{
			name: "no_maps",
			maps: []map[string]interface{}{},
			want: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeContexts(tt.maps...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestIsAuthorized_EntityMergePriority verifies that when a request entity has
// the same UID as a global entity, the request entity wins. This documents the
// merge contract: request entities are applied after global entities in the merge.
func TestIsAuthorized_EntityMergePriority(t *testing.T) {
	t.Parallel()

	// Policy: permit only when resource.tier == "silver".
	policy := `
		permit(
			principal,
			action == Action::"call_tool",
			resource == Tool::"weather"
		)
		when {
			resource.tier == "silver"
		};
	`

	// Global entity has tier = "gold" — policy should deny with global entity alone.
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies: []string{policy},
		EntitiesJSON: `[
			{"uid": {"type": "Tool", "id": "weather"}, "attrs": {"tier": "gold"}, "parents": []},
			{"uid": {"type": "Client", "id": "user1"}, "attrs": {}, "parents": []},
			{"uid": {"type": "Action", "id": "call_tool"}, "attrs": {}, "parents": []}
		]`,
	}, "")
	require.NoError(t, err)

	cedarAuthz, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Verify global entity alone denies (tier = "gold" != "silver").
	denied, err := cedarAuthz.IsAuthorized(
		"Client::user1", "Action::call_tool", "Tool::weather", nil,
	)
	require.NoError(t, err)
	assert.False(t, denied, "global entity tier=gold should not match policy requiring tier=silver")

	// Request entity: same UID but tier = "silver".
	requestEntities := make(cedar.EntityMap)
	uid := cedar.NewEntityUID("Tool", cedar.String("weather"))
	requestEntities[uid] = cedar.Entity{
		UID: uid,
		Attributes: cedar.NewRecord(cedar.RecordMap{
			cedar.String("tier"): cedar.String("silver"),
		}),
		Parents: cedar.NewEntityUIDSet(),
		Tags:    cedar.NewRecord(cedar.RecordMap{}),
	}

	// Request entity should overwrite global entity → policy matches.
	allowed, err := cedarAuthz.IsAuthorized(
		"Client::user1", "Action::call_tool", "Tool::weather",
		nil, requestEntities,
	)
	require.NoError(t, err)
	assert.True(t, allowed, "request entity (tier=silver) must overwrite global entity (tier=gold)")
}

// TestConfigOptionsRoleClaimNameJSON verifies JSON marshal/unmarshal of the
// RoleClaimName field, including backward compatibility when the field is absent.
func TestConfigOptionsRoleClaimNameJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		jsonInput     string
		wantRole      string
		wantOmitOnMar bool // when true, marshal output must NOT contain "role_claim_name"
	}{
		{
			name:          "present",
			jsonInput:     `{"policies":["permit(principal,action,resource);"],"role_claim_name":"roles"}`,
			wantRole:      "roles",
			wantOmitOnMar: false,
		},
		{
			name:          "absent_gives_empty_string",
			jsonInput:     `{"policies":["permit(principal,action,resource);"]}`,
			wantRole:      "",
			wantOmitOnMar: true,
		},
		{
			name:          "uri_style_claim",
			jsonInput:     `{"policies":["permit(principal,action,resource);"],"role_claim_name":"https://example.com/roles"}`,
			wantRole:      "https://example.com/roles",
			wantOmitOnMar: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts ConfigOptions
			err := json.Unmarshal([]byte(tt.jsonInput), &opts)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRole, opts.RoleClaimName)

			marshalled, err := json.Marshal(opts)
			require.NoError(t, err)
			if tt.wantOmitOnMar {
				assert.NotContains(t, string(marshalled), "role_claim_name",
					"empty RoleClaimName must be omitted from JSON output")
			} else {
				assert.Contains(t, string(marshalled), "role_claim_name")
			}
		})
	}
}

// TestValidateGroupEntityType exercises the private validateGroupEntityType helper
// directly. Each case names an input, states whether it should succeed, and — for
// error cases — a substring that the error message must contain so operators can
// diagnose misconfiguration from a single log line.
//
// Only our package's contract is tested here:
//  1. Empty string short-circuits to nil.
//  2. Inputs containing "::" are rejected with our project-specific error.
//  3. Valid Cedar identifiers pass through (smoke test of the cedar-go delegation path).
//  4. Invalid Cedar identifiers surface the cedar-go rejection wrapped with our message.
//  5. __cedarFoo is accepted — the Cedar spec only reserves the bare "__cedar" token,
//     not the entire prefix namespace. This intentional behavioral difference vs older
//     hand-rolled validators would be the most surprising case for a future reader.
//
// Exhaustive grammar testing (hyphens, leading digits, whitespace, reserved words, …)
// belongs in cedar-go's own test suite, not here.
func TestValidateGroupEntityType(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		input       string
		wantErr     bool
		errContains string // substring the error message must contain (ignored when wantErr=false)
	}{
		{
			// Empty string triggers the short-circuit: our function returns nil immediately
			// without consulting cedar-go's parser.
			name:    "empty string accepted (short-circuit, means use default)",
			input:   "",
			wantErr: false,
		},
		{
			// Smoke test: a plain valid identifier must pass the cedar-go delegation path.
			name:    "valid Cedar identifier accepted",
			input:   "OrgRole",
			wantErr: false,
		},
		{
			// Our project rule: "::" always means a namespaced type which is never a
			// valid bare entity-type name. We reject before delegating to cedar-go.
			name:        "namespaced type rejected with project-specific message",
			input:       "Foo::Bar",
			wantErr:     true,
			errContains: "::",
		},
		{
			// Smoke test: an invalid Cedar identifier must produce an error containing
			// our wrapper text, proving the cedar-go rejection bubbles up correctly.
			// One representative case is sufficient; the grammar details are cedar-go's domain.
			name:        "invalid Cedar identifier rejected with wrapper message",
			input:       "Org-Role",
			wantErr:     true,
			errContains: "not a valid Cedar identifier",
		},
		{
			// The Cedar spec reserves the literal "__cedar" token. "__cedarFoo" (with a
			// suffix) is accepted because the reservation does NOT extend to the whole
			// prefix namespace. This is intentionally different from older hand-rolled
			// validators that rejected the entire "__cedar" prefix — keep this case so a
			// future refactor cannot silently regress to the stricter behavior.
			name:    "__cedarFoo accepted (Cedar spec only reserves bare __cedar)",
			input:   "__cedarFoo",
			wantErr: false,
		},
		{
			// Sanity check that cedar-go's reserved-word rejection surfaces through our
			// wrapper. One reserved word is enough to prove the path works.
			name:        "reserved word 'in' rejected",
			input:       "in",
			wantErr:     true,
			errContains: "not a valid Cedar identifier",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateGroupEntityType(tc.input)

			if tc.wantErr {
				require.Error(t, err, "expected an error for input %q", tc.input)
				assert.Contains(t, err.Error(), tc.errContains,
					"error for %q should mention %q", tc.input, tc.errContains)
			} else {
				require.NoError(t, err, "unexpected error for input %q", tc.input)
			}
		})
	}
}

// TestAuthorizeWithJWTClaims_CustomGroupEntityType proves that GroupEntityType
// actually flows through Cedar evaluation, not just through entity construction.
// Case A: GroupEntityType "OrgRole" with policy "principal in OrgRole::..." → Permit.
// Case B: same policy, default GroupEntityType "" (resolves to THVGroup) → Deny,
// because the parent UIDs are typed THVGroup::"engineering" which is not in OrgRole.
// The two cases are adjacent so the contrast is visible to reviewers.
func TestAuthorizeWithJWTClaims_CustomGroupEntityType(t *testing.T) {
	t.Parallel()

	// Policy references OrgRole — only a factory configured with GroupEntityType "OrgRole"
	// will synthesise parent UIDs that match this policy.
	policy := `
		permit(
			principal in OrgRole::"engineering",
			action == Action::"call_tool",
			resource == Tool::"deploy"
		);
	`

	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user1",
			Claims: map[string]any{
				"sub":    "user1",
				"groups": []interface{}{"engineering"},
			},
		},
	}

	tests := []struct {
		name            string
		groupEntityType string
		wantAuthorize   bool
	}{
		{
			// GroupEntityType "OrgRole" makes the factory emit OrgRole::"engineering"
			// as the principal's parent UID. Cedar's `in` resolves to true → Permit.
			name:            "custom_type_OrgRole_permits",
			groupEntityType: "OrgRole",
			wantAuthorize:   true,
		},
		{
			// Default GroupEntityType "" resolves to THVGroup. The factory emits
			// THVGroup::"engineering" instead of OrgRole::"engineering". Cedar's `in`
			// for OrgRole::"engineering" evaluates to false → Deny by default.
			name:            "default_type_THVGroup_denies",
			groupEntityType: "",
			wantAuthorize:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:        []string{policy},
				EntitiesJSON:    `[]`,
				GroupClaimName:  "groups",
				GroupEntityType: tt.groupEntityType,
			}, "")
			require.NoError(t, err)

			ctx := auth.WithIdentity(context.Background(), identity)

			authorized, err := authorizer.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"deploy",
				nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthorize, authorized,
				"GroupEntityType=%q: expected allow=%v", tt.groupEntityType, tt.wantAuthorize)
		})
	}
}

// TestNewCedarAuthorizerGroupEntityTypeValidation is a thin wiring proof
// that NewCedarAuthorizer actually invokes validateGroupEntityType. The
// exhaustive rejection coverage lives in TestValidateGroupEntityType — this
// test only confirms one valid input passes through and one invalid input
// produces the validator's error at the constructor boundary.
func TestNewCedarAuthorizerGroupEntityTypeValidation(t *testing.T) {
	t.Parallel()

	validPolicy := []string{`permit(principal, action, resource);`}

	testCases := []struct {
		name            string
		groupEntityType string
		wantErr         bool
		errContains     string
	}{
		{name: "empty string succeeds", groupEntityType: "", wantErr: false},
		{name: "namespaced type fails", groupEntityType: "Foo::Bar", wantErr: true, errContains: "::"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewCedarAuthorizer(ConfigOptions{
				Policies:        validPolicy,
				GroupEntityType: tc.groupEntityType,
			}, "")

			if tc.wantErr {
				require.Error(t, err, "expected construction error for GroupEntityType=%q", tc.groupEntityType)
				assert.Contains(t, err.Error(), tc.errContains,
					"validator error must bubble up unchanged to the constructor boundary")
			} else {
				require.NoError(t, err, "unexpected error for GroupEntityType=%q", tc.groupEntityType)
			}
		})
	}
}

// captureSlogWarn redirects slog's default logger to a bytes.Buffer for the
// duration of f, then restores the original default. Returns the captured
// output. This helper exists because slog.SetDefault is a process-global
// side effect — tests that use it must NOT run in parallel.
func captureSlogWarn(t *testing.T, f func()) string {
	t.Helper()

	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(orig) })

	f()

	return buf.String()
}

// TestStaleTHVGroupWarning verifies that NewCedarAuthorizer emits a WARN log
// when entities_json contains entities of type "THVGroup" while GroupEntityType
// is configured to a different value. The mismatch causes Cedar's `in` operator
// to evaluate to false for those entities — a silent deny that is hard to debug
// without this diagnostic.
//
// Subtests use slog.SetDefault (process-global), so they must NOT run in
// parallel with other tests. The parent is still parallel-safe because it does
// not touch global state itself.
//
//nolint:paralleltest,tparallel // Subtests redirect slog.Default, which is process-global state
func TestStaleTHVGroupWarning(t *testing.T) {
	t.Parallel()

	const thvGroupEntity = `[{"uid":{"type":"THVGroup","id":"engineering"},"attrs":{},"parents":[]}]`
	validPolicy := []string{`permit(principal, action, resource);`}

	tests := []struct {
		name            string
		groupEntityType string
		entitiesJSON    string
		wantWarn        bool
		wantContains    []string // when wantWarn=true, log must contain each of these
	}{
		{
			name:            "warns when stale THVGroup present and GroupEntityType differs",
			groupEntityType: "OrgRole",
			entitiesJSON:    thvGroupEntity,
			wantWarn:        true,
			wantContains:    []string{"GroupEntityType", "OrgRole", "THVGroup"},
		},
		{
			// Most common path: GroupEntityType is empty (uses THVGroup default), so no
			// conflict is possible. One negative is sufficient to prove the guard works.
			name:            "no warning when GroupEntityType is empty (uses THVGroup default)",
			groupEntityType: "",
			entitiesJSON:    thvGroupEntity,
			wantWarn:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot be parallel: subtests redirect slog.Default.
			output := captureSlogWarn(t, func() {
				_, err := NewCedarAuthorizer(ConfigOptions{
					Policies:        validPolicy,
					EntitiesJSON:    tt.entitiesJSON,
					GroupEntityType: tt.groupEntityType,
				}, "")
				require.NoError(t, err)
			})

			if tt.wantWarn {
				require.NotEmpty(t, output, "expected a warn log")
				for _, want := range tt.wantContains {
					assert.Contains(t, output, want,
						"warn log must mention %q", want)
				}
			} else {
				assert.Empty(t, output, "no warning expected")
			}
		})
	}
}
