// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"context"
	"testing"

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
		name         string
		policies     []string
		entitiesJSON string
		expectError  bool
		errorType    error
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
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a Cedar authorizer
			authorizer, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     tc.policies,
				EntitiesJSON: tc.entitiesJSON,
			})

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
			})
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
	})
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
	})
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
	})
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
	})
	require.NoError(t, err)

	// Cast to concrete type to access entity methods
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Get entity factory
	factory := cedarAuthorizer.GetEntityFactory()
	require.NotNil(t, factory)

	// Create a test entity using the factory
	uid, entity, _ := factory.CreatePrincipalEntity("Client", "testuser", map[string]interface{}{
		"name": "Test User",
	}, nil)

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
	})
	require.NoError(t, err)

	// Cast to concrete type
	cedarAuthorizer, ok := authorizer.(*Authorizer)
	require.True(t, ok)

	// Create a UID that doesn't exist
	factory := cedarAuthorizer.GetEntityFactory()
	uid, _, _ := factory.CreatePrincipalEntity("Client", "nonexistent", nil, nil)

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
	})
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
	})
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
	})
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
			name: "upstream_token_opaque_not_parseable",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: "thv-user",
					Claims:  map[string]any{"sub": "thv-user"},
				},
				UpstreamTokens: map[string]string{
					providerName: "opaque-token-cannot-be-parsed",
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
		Policies:     []string{policy},
		EntitiesJSON: `[]`,
	})
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

// TestAuthorizeWithJWTClaims_DoesNotMutateIdentity verifies that
// AuthorizeWithJWTClaims does not mutate the Identity stored in context.
// The Identity contract (see auth.Identity) requires that the struct MUST NOT
// be modified after it is placed in the request context to avoid concurrent
// write races with other middleware reading the same pointer.
func TestAuthorizeWithJWTClaims_DoesNotMutateIdentity(t *testing.T) {
	t.Parallel()

	policy := `permit(principal, action, resource);`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{policy},
		EntitiesJSON: `[]`,
	})
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
	})
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
			// GroupClaimName must survive the serialise→deserialise round-trip
			// that InjectUpstreamProvider performs internally. A refactor that
			// reconstructed ConfigOptions from scratch (populating only known
			// fields) would silently drop GroupClaimName without this test.
			name: "group_claim_name_preserved_after_inject",
			setup: func(t *testing.T) *authorizers.Config {
				t.Helper()
				cfg, err := authorizers.NewConfig(Config{
					Version: "1.0",
					Type:    ConfigType,
					Options: &ConfigOptions{
						Policies:       []string{`permit(principal, action, resource);`},
						EntitiesJSON:   "[]",
						GroupClaimName: "https://example.com/groups",
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
