package app

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestAddFormatFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
	}{
		{
			name:        "adds format flag with default",
			description: "Output format (json or text)",
		},
		{
			name:        "adds format flag with custom description",
			description: "Custom format description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			var format string

			AddFormatFlag(cmd, &format, tt.description)

			// Verify flag exists
			flag := cmd.Flags().Lookup("format")
			if flag == nil {
				t.Fatal("format flag was not added")
			}

			// Verify default value
			if flag.DefValue != FormatText {
				t.Errorf("expected default value %q, got %q", FormatText, flag.DefValue)
			}

			// Verify description
			if flag.Usage != tt.description {
				t.Errorf("expected description %q, got %q", tt.description, flag.Usage)
			}
		})
	}
}

func TestAddGroupFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		withShorthand bool
		wantShorthand string
	}{
		{
			name:          "adds group flag without shorthand",
			withShorthand: false,
			wantShorthand: "",
		},
		{
			name:          "adds group flag with shorthand",
			withShorthand: true,
			wantShorthand: "g",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			var group string

			AddGroupFlag(cmd, &group, tt.withShorthand)

			// Verify flag exists
			flag := cmd.Flags().Lookup("group")
			if flag == nil {
				t.Fatal("group flag was not added")
			}

			// Verify shorthand
			if flag.Shorthand != tt.wantShorthand {
				t.Errorf("expected shorthand %q, got %q", tt.wantShorthand, flag.Shorthand)
			}

			// Verify default value is empty
			if flag.DefValue != "" {
				t.Errorf("expected empty default value, got %q", flag.DefValue)
			}
		})
	}
}

func TestAddAllFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		withShorthand bool
		description   string
		wantShorthand string
	}{
		{
			name:          "adds all flag without shorthand",
			withShorthand: false,
			description:   "Show all items",
			wantShorthand: "",
		},
		{
			name:          "adds all flag with shorthand",
			withShorthand: true,
			description:   "Show all workloads",
			wantShorthand: "a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			var all bool

			AddAllFlag(cmd, &all, tt.withShorthand, tt.description)

			// Verify flag exists
			flag := cmd.Flags().Lookup("all")
			if flag == nil {
				t.Fatal("all flag was not added")
			}

			// Verify shorthand
			if flag.Shorthand != tt.wantShorthand {
				t.Errorf("expected shorthand %q, got %q", tt.wantShorthand, flag.Shorthand)
			}

			// Verify description
			if flag.Usage != tt.description {
				t.Errorf("expected description %q, got %q", tt.description, flag.Usage)
			}

			// Verify default value is false
			if flag.DefValue != "false" {
				t.Errorf("expected default value 'false', got %q", flag.DefValue)
			}
		})
	}
}

func TestValidateFormatFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		format         string
		allowedFormats []string
		wantErr        bool
	}{
		{
			name:           "valid format json with defaults",
			format:         "json",
			allowedFormats: nil,
			wantErr:        false,
		},
		{
			name:           "valid format text with defaults",
			format:         "text",
			allowedFormats: nil,
			wantErr:        false,
		},
		{
			name:           "invalid format with defaults",
			format:         "yaml",
			allowedFormats: nil,
			wantErr:        true,
		},
		{
			name:           "valid custom format",
			format:         "yaml",
			allowedFormats: []string{"json", "text", "yaml"},
			wantErr:        false,
		},
		{
			name:           "invalid custom format",
			format:         "xml",
			allowedFormats: []string{"json", "text", "yaml"},
			wantErr:        true,
		},
		{
			name:           "empty format is invalid",
			format:         "",
			allowedFormats: nil,
			wantErr:        true,
		},
		{
			name:           "format with mcpservers allowed",
			format:         "mcpservers",
			allowedFormats: []string{"json", "text", "mcpservers"},
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateFormatFlag(tt.format, tt.allowedFormats...)

			if tt.wantErr {
				if err == nil {
					t.Error("ValidateFormatFlag() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("ValidateFormatFlag() unexpected error: %v", err)
			}
		})
	}
}

func TestGetStringFlagOrEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		flagName string
		flagVal  string
		expected string
	}{
		{
			name:     "returns flag value when exists",
			flagName: "test-flag",
			flagVal:  "test-value",
			expected: "test-value",
		},
		{
			name:     "returns empty when flag does not exist",
			flagName: "nonexistent",
			flagVal:  "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}

			if tt.flagVal != "" {
				cmd.Flags().String(tt.flagName, tt.flagVal, "test flag")
			}

			result := GetStringFlagOrEmpty(cmd, tt.flagName)

			if result != tt.expected {
				t.Errorf("GetStringFlagOrEmpty() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestIsOIDCEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		jwksURL          string
		issuer           string
		introspectionURL string
		expectedEnabled  bool
	}{
		{
			name:            "enabled with jwks url",
			jwksURL:         "https://example.com/.well-known/jwks.json",
			expectedEnabled: true,
		},
		{
			name:            "enabled with issuer",
			issuer:          "https://accounts.google.com",
			expectedEnabled: true,
		},
		{
			name:             "enabled with introspection url",
			introspectionURL: "https://example.com/introspect",
			expectedEnabled:  true,
		},
		{
			name:            "disabled with no flags",
			expectedEnabled: false,
		},
		{
			name:            "enabled with multiple flags",
			jwksURL:         "https://example.com/.well-known/jwks.json",
			issuer:          "https://accounts.google.com",
			expectedEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}

			// Add OIDC flags
			AddOIDCFlags(cmd)

			// Set flag values
			if tt.jwksURL != "" {
				_ = cmd.Flags().Set("oidc-jwks-url", tt.jwksURL)
			}
			if tt.issuer != "" {
				_ = cmd.Flags().Set("oidc-issuer", tt.issuer)
			}
			if tt.introspectionURL != "" {
				_ = cmd.Flags().Set("oidc-introspection-url", tt.introspectionURL)
			}

			result := IsOIDCEnabled(cmd)

			if result != tt.expectedEnabled {
				t.Errorf("IsOIDCEnabled() = %v, want %v", result, tt.expectedEnabled)
			}
		})
	}
}
