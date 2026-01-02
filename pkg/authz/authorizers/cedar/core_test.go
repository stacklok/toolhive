package cedar

import (
	"context"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/logger"
)

// TestNewCedarAuthorizer tests the creation of a new Cedar authorizer with different configurations.
func TestNewCedarAuthorizer(t *testing.T) {
	t.Parallel()

	// Initialize logger for tests
	logger.Initialize()
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
			identity := &auth.Identity{Subject: "test-user", Claims: tc.claims}
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
				identity := &auth.Identity{Subject: "", Claims: claims}
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
				identity := &auth.Identity{Subject: "", Claims: claims}
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
				identity := &auth.Identity{Subject: "user123", Claims: claims}
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
	})
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
