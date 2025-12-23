package config

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestCRDToCliRoundtrip_HeaderInjection verifies that a BackendAuthStrategy with
// HeaderInjection config can be serialized to YAML and correctly deserialized.
//
// This test simulates the flow:
// 1. Operator creates BackendAuthStrategy with HeaderInjection
// 2. Config is serialized to YAML (for mounting as ConfigMap)
// 3. CLI parses YAML directly to BackendAuthStrategy
// 4. All fields are correctly preserved
func TestCRDToCliRoundtrip_HeaderInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		operatorStrategy *authtypes.BackendAuthStrategy
		wantType         string
		wantHeaderName   string
		wantHeaderValue  string
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
			wantType:       authtypes.StrategyTypeHeaderInjection,
			wantHeaderName: "Authorization",
			// HeaderValue stays empty, HeaderValueEnv is preserved
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

			// Step 2: Unmarshal directly into BackendAuthStrategy
			var cliStrategy authtypes.BackendAuthStrategy
			if err := yaml.Unmarshal(yamlBytes, &cliStrategy); err != nil {
				t.Fatalf("failed to unmarshal YAML to strategy: %v", err)
			}

			// Step 3: Verify all fields are preserved
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

			if tt.wantHeaderValue != "" && cliStrategy.HeaderInjection.HeaderValue != tt.wantHeaderValue {
				t.Errorf("HeaderValue = %q, want %q",
					cliStrategy.HeaderInjection.HeaderValue, tt.wantHeaderValue)
			}
		})
	}
}

// TestCRDToCliRoundtrip_TokenExchange verifies that a BackendAuthStrategy with
// TokenExchange config can be serialized to YAML and correctly deserialized.
func TestCRDToCliRoundtrip_TokenExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		operatorStrategy *authtypes.BackendAuthStrategy
		wantType         string
		wantTokenURL     string
		wantClientID     string
		wantAudience     string
		wantScopes       []string
		wantSubjectType  string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Step 1: Marshal the operator's BackendAuthStrategy to YAML
			yamlBytes, err := yaml.Marshal(tt.operatorStrategy)
			if err != nil {
				t.Fatalf("failed to marshal operator strategy to YAML: %v", err)
			}

			// Step 2: Unmarshal directly into BackendAuthStrategy
			var cliStrategy authtypes.BackendAuthStrategy
			if err := yaml.Unmarshal(yamlBytes, &cliStrategy); err != nil {
				t.Fatalf("failed to unmarshal YAML to strategy: %v", err)
			}

			// Step 3: Verify fields
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

	// Step 2: Unmarshal directly into OutgoingAuthConfig
	var cliConfig OutgoingAuthConfig
	if err := yaml.Unmarshal(yamlBytes, &cliConfig); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	// Step 3: Verify structure
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

	// Step 2: Unmarshal directly to BackendAuthStrategy
	var cliStrategy authtypes.BackendAuthStrategy
	if err := yaml.Unmarshal(yamlBytes, &cliStrategy); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Step 3: Verify
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

	// Verify token exchange field naming
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

// TestConfigRoundtrip tests full Config struct roundtrip.
func TestConfigRoundtrip(t *testing.T) {
	t.Parallel()

	originalConfig := &Config{
		Name:  "test-server",
		Group: "test-group",
		IncomingAuth: &IncomingAuthConfig{
			Type: "oidc",
			OIDC: &OIDCConfig{
				Issuer:   "https://issuer.example.com",
				ClientID: "client-123",
				Audience: "api://test",
			},
		},
		OutgoingAuth: &OutgoingAuthConfig{
			Source: "inline",
			Default: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUnauthenticated,
			},
		},
		Aggregation: &AggregationConfig{
			ConflictResolution: "prefix",
			ConflictResolutionConfig: &ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
			Tools: []*WorkloadToolConfig{
				{
					Workload: "github-mcp",
					Filter:   []string{"search_*"},
				},
			},
		},
		CompositeTools: []*CompositeToolConfig{
			{
				Name:        "test-tool",
				Description: "A test composite tool",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input": map[string]any{"type": "string"},
					},
				},
				Steps: []*WorkflowStepConfig{
					{
						ID:   "step1",
						Type: "tool",
						Tool: "github-mcp.search_repos",
					},
				},
			},
		},
	}

	// Marshal to YAML
	yamlBytes, err := yaml.Marshal(originalConfig)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}

	// Unmarshal with strict mode
	var parsedConfig Config
	decoder := yaml.NewDecoder(bytes.NewReader(yamlBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsedConfig); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	// Verify key fields
	if parsedConfig.Name != originalConfig.Name {
		t.Errorf("Name = %q, want %q", parsedConfig.Name, originalConfig.Name)
	}
	if parsedConfig.Group != originalConfig.Group {
		t.Errorf("Group = %q, want %q", parsedConfig.Group, originalConfig.Group)
	}
	if parsedConfig.IncomingAuth == nil {
		t.Fatal("IncomingAuth is nil")
	}
	if parsedConfig.IncomingAuth.Type != "oidc" {
		t.Errorf("IncomingAuth.Type = %q, want %q", parsedConfig.IncomingAuth.Type, "oidc")
	}
	if len(parsedConfig.CompositeTools) != 1 {
		t.Fatalf("CompositeTools length = %d, want 1", len(parsedConfig.CompositeTools))
	}
	if parsedConfig.CompositeTools[0].Name != "test-tool" {
		t.Errorf("CompositeTools[0].Name = %q, want %q", parsedConfig.CompositeTools[0].Name, "test-tool")
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
