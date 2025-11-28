package config

import (
	"testing"

	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestCRDToCliRoundtrip_HeaderInjection verifies that a BackendAuthStrategy with
// HeaderInjection config can be serialized to YAML and correctly deserialized
// by the CLI's yaml_loader.go code.
//
// This test simulates the flow:
// 1. Operator creates BackendAuthStrategy with HeaderInjection (like converters/header_injection.go does)
// 2. Config is serialized to YAML (for mounting as ConfigMap)
// 3. CLI parses YAML using rawBackendAuthStrategy + transformBackendAuthStrategy
// 4. All fields are correctly preserved
func TestCRDToCliRoundtrip_HeaderInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		operatorStrategy *authtypes.BackendAuthStrategy
		envVars          map[string]string
		wantType         string
		wantHeaderName   string
		wantHeaderValue  string
		wantErr          bool
		errContains      string
	}{
		{
			name: "header injection with literal value",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer secret-token-123",
				},
			},
			wantType:        authtypes.StrategyTypeHeaderInjection,
			wantHeaderName:  "Authorization",
			wantHeaderValue: "Bearer secret-token-123",
		},
		{
			name: "header injection with custom header name",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "api-key-value",
				},
			},
			wantType:        authtypes.StrategyTypeHeaderInjection,
			wantHeaderName:  "X-API-Key",
			wantHeaderValue: "api-key-value",
		},
		{
			name: "header injection with env var reference",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:     "Authorization",
					HeaderValueEnv: "MY_SECRET_TOKEN",
				},
			},
			envVars: map[string]string{
				"MY_SECRET_TOKEN": "resolved-secret-value",
			},
			wantType:        authtypes.StrategyTypeHeaderInjection,
			wantHeaderName:  "Authorization",
			wantHeaderValue: "resolved-secret-value",
		},
		{
			name: "header injection with missing env var fails",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:     "Authorization",
					HeaderValueEnv: "MISSING_VAR",
				},
			},
			wantErr:     true,
			errContains: "environment variable MISSING_VAR not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Step 1: Marshal the operator's BackendAuthStrategy to YAML
			yamlBytes, err := yaml.Marshal(tt.operatorStrategy)
			if err != nil {
				t.Fatalf("failed to marshal operator strategy to YAML: %v", err)
			}

			// Step 2: Unmarshal into CLI's raw struct (simulating YAML parsing)
			var rawStrategy rawBackendAuthStrategy
			if err := yaml.Unmarshal(yamlBytes, &rawStrategy); err != nil {
				t.Fatalf("failed to unmarshal YAML to raw strategy: %v", err)
			}

			// Step 3: Create mock env reader and YAMLLoader to use transform function
			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)

			// Set up expectations for env vars
			for key, value := range tt.envVars {
				mockEnv.EXPECT().Getenv(key).Return(value).AnyTimes()
			}
			// Return empty for any other env var lookups
			mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

			loader := NewYAMLLoader("", mockEnv)

			// Step 4: Transform raw struct to typed BackendAuthStrategy
			cliStrategy, err := loader.transformBackendAuthStrategy(&rawStrategy)

			// Check error expectations
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Step 5: Verify all fields are preserved
			if cliStrategy.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", cliStrategy.Type, tt.wantType)
			}

			if cliStrategy.HeaderInjection == nil {
				t.Fatalf("HeaderInjection config is nil")
			}

			if cliStrategy.HeaderInjection.HeaderName != tt.wantHeaderName {
				t.Errorf("HeaderName = %q, want %q",
					cliStrategy.HeaderInjection.HeaderName, tt.wantHeaderName)
			}

			if cliStrategy.HeaderInjection.HeaderValue != tt.wantHeaderValue {
				t.Errorf("HeaderValue = %q, want %q",
					cliStrategy.HeaderInjection.HeaderValue, tt.wantHeaderValue)
			}
		})
	}
}

// TestCRDToCliRoundtrip_TokenExchange verifies that a BackendAuthStrategy with
// TokenExchange config can be serialized to YAML and correctly deserialized
// by the CLI's yaml_loader.go code.
func TestCRDToCliRoundtrip_TokenExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		operatorStrategy *authtypes.BackendAuthStrategy
		envVars          map[string]string
		wantType         string
		wantTokenURL     string
		wantClientID     string
		wantAudience     string
		wantScopes       []string
		wantSubjectType  string
		wantErr          bool
		errContains      string
	}{
		{
			name: "token exchange with all fields",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/oauth/token",
					ClientID:         "my-client-id",
					ClientSecretEnv:  "TOKEN_EXCHANGE_SECRET",
					Audience:         "https://api.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
				},
			},
			envVars: map[string]string{
				"TOKEN_EXCHANGE_SECRET": "client-secret-value",
			},
			wantType:        authtypes.StrategyTypeTokenExchange,
			wantTokenURL:    "https://auth.example.com/oauth/token",
			wantClientID:    "my-client-id",
			wantAudience:    "https://api.example.com",
			wantScopes:      []string{"read", "write"},
			wantSubjectType: "urn:ietf:params:oauth:token-type:access_token",
		},
		{
			name: "token exchange with minimal fields",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
			wantType:     authtypes.StrategyTypeTokenExchange,
			wantTokenURL: "https://auth.example.com/token",
		},
		{
			name: "token exchange with client secret directly set",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:     "https://auth.example.com/token",
					ClientID:     "direct-client",
					ClientSecret: "direct-secret-value",
					Audience:     "https://backend.example.com",
				},
			},
			wantType:     authtypes.StrategyTypeTokenExchange,
			wantTokenURL: "https://auth.example.com/token",
			wantClientID: "direct-client",
			wantAudience: "https://backend.example.com",
		},
		{
			name: "token exchange with missing env var fails",
			operatorStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientSecretEnv: "MISSING_SECRET_VAR",
				},
			},
			wantErr:     true,
			errContains: "environment variable MISSING_SECRET_VAR not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Step 1: Marshal the operator's BackendAuthStrategy to YAML
			yamlBytes, err := yaml.Marshal(tt.operatorStrategy)
			if err != nil {
				t.Fatalf("failed to marshal operator strategy to YAML: %v", err)
			}

			// Step 2: Unmarshal into CLI's raw struct
			var rawStrategy rawBackendAuthStrategy
			if err := yaml.Unmarshal(yamlBytes, &rawStrategy); err != nil {
				t.Fatalf("failed to unmarshal YAML to raw strategy: %v", err)
			}

			// Step 3: Create mock env reader
			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)

			for key, value := range tt.envVars {
				mockEnv.EXPECT().Getenv(key).Return(value).AnyTimes()
			}
			mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

			loader := NewYAMLLoader("", mockEnv)

			// Step 4: Transform
			cliStrategy, err := loader.transformBackendAuthStrategy(&rawStrategy)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Step 5: Verify fields
			if cliStrategy.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", cliStrategy.Type, tt.wantType)
			}

			if cliStrategy.TokenExchange == nil {
				t.Fatalf("TokenExchange config is nil")
			}

			if cliStrategy.TokenExchange.TokenURL != tt.wantTokenURL {
				t.Errorf("TokenURL = %q, want %q",
					cliStrategy.TokenExchange.TokenURL, tt.wantTokenURL)
			}

			if cliStrategy.TokenExchange.ClientID != tt.wantClientID {
				t.Errorf("ClientID = %q, want %q",
					cliStrategy.TokenExchange.ClientID, tt.wantClientID)
			}

			if cliStrategy.TokenExchange.Audience != tt.wantAudience {
				t.Errorf("Audience = %q, want %q",
					cliStrategy.TokenExchange.Audience, tt.wantAudience)
			}

			if !stringSliceEqual(cliStrategy.TokenExchange.Scopes, tt.wantScopes) {
				t.Errorf("Scopes = %v, want %v",
					cliStrategy.TokenExchange.Scopes, tt.wantScopes)
			}

			if cliStrategy.TokenExchange.SubjectTokenType != tt.wantSubjectType {
				t.Errorf("SubjectTokenType = %q, want %q",
					cliStrategy.TokenExchange.SubjectTokenType, tt.wantSubjectType)
			}
		})
	}
}

// TestCRDToCliRoundtrip_FullOutgoingAuthConfig verifies that a complete OutgoingAuthConfig
// with both Default and per-backend strategies can be serialized and deserialized correctly.
func TestCRDToCliRoundtrip_FullOutgoingAuthConfig(t *testing.T) {
	t.Parallel()

	// Simulate operator creating OutgoingAuthConfig
	operatorConfig := &OutgoingAuthConfig{
		Source: "inline",
		Default: &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeHeaderInjection,
			HeaderInjection: &authtypes.HeaderInjectionConfig{
				HeaderName:  "Authorization",
				HeaderValue: "Bearer default-token",
			},
		},
		Backends: map[string]*authtypes.BackendAuthStrategy{
			"github": {
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer github-token",
				},
			},
			"internal-api": {
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.internal.com/token",
					ClientID:         "internal-client",
					ClientSecretEnv:  "INTERNAL_SECRET",
					Audience:         "https://api.internal.com",
					Scopes:           []string{"api.read", "api.write"},
					SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
				},
			},
			"public-api": {
				Type: authtypes.StrategyTypeUnauthenticated,
			},
		},
	}

	// Step 1: Marshal to YAML
	yamlBytes, err := yaml.Marshal(operatorConfig)
	if err != nil {
		t.Fatalf("failed to marshal config to YAML: %v", err)
	}

	// Step 2: Unmarshal into raw struct
	var rawConfig rawOutgoingAuth
	if err := yaml.Unmarshal(yamlBytes, &rawConfig); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	// Step 3: Create mock env reader
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("INTERNAL_SECRET").Return("internal-secret-value").AnyTimes()
	mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

	loader := NewYAMLLoader("", mockEnv)

	// Step 4: Transform
	cliConfig, err := loader.transformOutgoingAuth(&rawConfig)
	if err != nil {
		t.Fatalf("failed to transform config: %v", err)
	}

	// Step 5: Verify structure
	if cliConfig.Source != "inline" {
		t.Errorf("Source = %q, want %q", cliConfig.Source, "inline")
	}

	// Verify default strategy
	if cliConfig.Default == nil {
		t.Fatal("Default strategy is nil")
	}
	if cliConfig.Default.Type != authtypes.StrategyTypeHeaderInjection {
		t.Errorf("Default.Type = %q, want %q",
			cliConfig.Default.Type, authtypes.StrategyTypeHeaderInjection)
	}
	if cliConfig.Default.HeaderInjection == nil {
		t.Fatal("Default.HeaderInjection is nil")
	}
	if cliConfig.Default.HeaderInjection.HeaderValue != "Bearer default-token" {
		t.Errorf("Default header value = %q, want %q",
			cliConfig.Default.HeaderInjection.HeaderValue, "Bearer default-token")
	}

	// Verify github backend
	github, ok := cliConfig.Backends["github"]
	if !ok {
		t.Fatal("github backend not found")
	}
	if github.Type != authtypes.StrategyTypeHeaderInjection {
		t.Errorf("github.Type = %q, want %q", github.Type, authtypes.StrategyTypeHeaderInjection)
	}
	if github.HeaderInjection == nil || github.HeaderInjection.HeaderValue != "Bearer github-token" {
		t.Errorf("github header value = %v, want %q",
			github.HeaderInjection, "Bearer github-token")
	}

	// Verify internal-api backend (token exchange)
	internalAPI, ok := cliConfig.Backends["internal-api"]
	if !ok {
		t.Fatal("internal-api backend not found")
	}
	if internalAPI.Type != authtypes.StrategyTypeTokenExchange {
		t.Errorf("internal-api.Type = %q, want %q",
			internalAPI.Type, authtypes.StrategyTypeTokenExchange)
	}
	if internalAPI.TokenExchange == nil {
		t.Fatal("internal-api.TokenExchange is nil")
	}
	if internalAPI.TokenExchange.TokenURL != "https://auth.internal.com/token" {
		t.Errorf("internal-api.TokenURL = %q, want %q",
			internalAPI.TokenExchange.TokenURL, "https://auth.internal.com/token")
	}
	if internalAPI.TokenExchange.ClientID != "internal-client" {
		t.Errorf("internal-api.ClientID = %q, want %q",
			internalAPI.TokenExchange.ClientID, "internal-client")
	}
	if internalAPI.TokenExchange.Audience != "https://api.internal.com" {
		t.Errorf("internal-api.Audience = %q, want %q",
			internalAPI.TokenExchange.Audience, "https://api.internal.com")
	}
	if !stringSliceEqual(internalAPI.TokenExchange.Scopes, []string{"api.read", "api.write"}) {
		t.Errorf("internal-api.Scopes = %v, want %v",
			internalAPI.TokenExchange.Scopes, []string{"api.read", "api.write"})
	}

	// Verify public-api backend (unauthenticated)
	publicAPI, ok := cliConfig.Backends["public-api"]
	if !ok {
		t.Fatal("public-api backend not found")
	}
	if publicAPI.Type != authtypes.StrategyTypeUnauthenticated {
		t.Errorf("public-api.Type = %q, want %q",
			publicAPI.Type, authtypes.StrategyTypeUnauthenticated)
	}
}

// TestCRDToCliRoundtrip_Unauthenticated verifies the unauthenticated strategy roundtrip.
func TestCRDToCliRoundtrip_Unauthenticated(t *testing.T) {
	t.Parallel()

	operatorStrategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeUnauthenticated,
	}

	// Step 1: Marshal to YAML
	yamlBytes, err := yaml.Marshal(operatorStrategy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Step 2: Unmarshal to raw struct
	var rawStrategy rawBackendAuthStrategy
	if err := yaml.Unmarshal(yamlBytes, &rawStrategy); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Step 3: Transform
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

	loader := NewYAMLLoader("", mockEnv)
	cliStrategy, err := loader.transformBackendAuthStrategy(&rawStrategy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step 4: Verify
	if cliStrategy.Type != authtypes.StrategyTypeUnauthenticated {
		t.Errorf("Type = %q, want %q", cliStrategy.Type, authtypes.StrategyTypeUnauthenticated)
	}

	// Unauthenticated should have no HeaderInjection or TokenExchange config
	if cliStrategy.HeaderInjection != nil {
		t.Errorf("HeaderInjection should be nil for unauthenticated, got %+v",
			cliStrategy.HeaderInjection)
	}
	if cliStrategy.TokenExchange != nil {
		t.Errorf("TokenExchange should be nil for unauthenticated, got %+v",
			cliStrategy.TokenExchange)
	}
}

// TestYAMLFieldNaming verifies that YAML field names match between operator and CLI.
// This is a sanity check to ensure struct tags are consistent.
func TestYAMLFieldNaming(t *testing.T) {
	t.Parallel()

	// Create a strategy with all fields populated
	operatorStrategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeHeaderInjection,
		HeaderInjection: &authtypes.HeaderInjectionConfig{
			HeaderName:     "X-Custom-Header",
			HeaderValue:    "custom-value",
			HeaderValueEnv: "CUSTOM_ENV",
		},
	}

	// Marshal to YAML
	yamlBytes, err := yaml.Marshal(operatorStrategy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	yamlStr := string(yamlBytes)

	// Verify expected field names are present in YAML
	expectedFields := []string{
		"type:",
		"header_injection:",
		"header_name:",
		"header_value:",
		"header_value_env:",
	}

	for _, field := range expectedFields {
		if !containsString(yamlStr, field) {
			t.Errorf("YAML missing expected field %q in:\n%s", field, yamlStr)
		}
	}

	// Verify JSON tags produce same field names when using yaml.v3 with json tags
	// (yaml.v3 can use json tags as fallback)
	tokenStrategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL:         "https://example.com/token",
			ClientID:         "client-123",
			ClientSecretEnv:  "SECRET_ENV",
			Audience:         "https://api.example.com",
			Scopes:           []string{"read", "write"},
			SubjectTokenType: "access_token",
		},
	}

	tokenYamlBytes, err := yaml.Marshal(tokenStrategy)
	if err != nil {
		t.Fatalf("failed to marshal token strategy: %v", err)
	}

	tokenYamlStr := string(tokenYamlBytes)

	expectedTokenFields := []string{
		"token_exchange:",
		"token_url:",
		"client_id:",
		"client_secret_env:",
		"audience:",
		"scopes:",
		"subject_token_type:",
	}

	for _, field := range expectedTokenFields {
		if !containsString(tokenYamlStr, field) {
			t.Errorf("YAML missing expected field %q in:\n%s", field, tokenYamlStr)
		}
	}
}

// containsString checks if s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// stringSliceEqual compares two string slices for equality.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
