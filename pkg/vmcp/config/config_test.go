package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOutgoingAuthConfig_ResolveForBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *OutgoingAuthConfig
		backendID   string
		wantType    string
		wantNil     bool
		description string
	}{
		{
			name:        "nil config returns nil",
			config:      nil,
			backendID:   "backend1",
			wantNil:     true,
			description: "When config is nil, should return nil",
		},
		{
			name: "backend-specific config takes precedence",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "X-API-Key",
							HeaderValue: "secret-token",
						},
					},
				},
			},
			backendID:   "backend1",
			wantType:    "header_injection",
			description: "Backend-specific config should override default",
		},
		{
			name: "falls back to default when backend not configured",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "Authorization",
							HeaderValue: "Bearer token123",
						},
					},
				},
			},
			backendID:   "backend2",
			wantType:    "unauthenticated",
			description: "Should use default when specific backend not configured",
		},
		{
			name: "returns nil when no default and backend not configured",
			config: &OutgoingAuthConfig{
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "X-Token",
							HeaderValue: "value123",
						},
					},
				},
			},
			backendID:   "backend2",
			wantNil:     true,
			description: "Should return nil when no default and backend not in map",
		},
		{
			name: "handles nil backend strategy in map",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": nil,
				},
			},
			backendID:   "backend1",
			wantType:    "unauthenticated",
			description: "Should fall back to default when backend strategy is nil",
		},
		{
			name: "returns nil when only default is nil",
			config: &OutgoingAuthConfig{
				Default:  nil,
				Backends: map[string]*authtypes.BackendAuthStrategy{},
			},
			backendID:   "backend1",
			wantNil:     true,
			description: "Should return nil when default is nil and backend not found",
		},
		{
			name: "handles header injection with env variable",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:     "Authorization",
						HeaderValueEnv: "API_KEY_ENV",
					},
				},
			},
			backendID:   "backend1",
			wantType:    "header_injection",
			description: "Should handle header injection with env variable",
		},
		{
			name: "handles token exchange strategy",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "token_exchange",
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL: "https://example.com/token",
						ClientID: "test-client",
						Audience: "api",
					},
				},
			},
			backendID:   "backend1",
			wantType:    "token_exchange",
			description: "Should handle token exchange strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.config.ResolveForBackend(tt.backendID)

			if tt.wantNil {
				assert.Nil(t, got, "Expected nil: %s", tt.description)
			} else {
				assert.NotNil(t, got, "Expected non-nil strategy: %s", tt.description)
				assert.Equal(t, tt.wantType, got.Type, "Type mismatch: %s", tt.description)
			}
		})
	}
}

// TestConfigFieldTagsAreCamelCase verifies that all exported fields in Config and its nested structs
// have yaml tags and that the tag names use camelCase (not snake_case).
func TestConfigFieldTagsAreCamelCase(t *testing.T) {
	t.Parallel()

	var cfg Config
	visited := make(map[reflect.Type]bool)
	err := checkStructTags(reflect.TypeOf(cfg), "", visited)

	require.NoError(t, err)
}

// checkStructTags recursively checks all struct fields for yaml tags and camelCase naming.
// Returns the first error encountered, or nil if all fields are valid.
func checkStructTags(t reflect.Type, path string, visited map[reflect.Type]bool) error {
	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Only process struct types
	if t.Kind() != reflect.Struct {
		return nil
	}

	// Avoid infinite recursion for circular references
	if visited[t] {
		return nil
	}
	visited[t] = true

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		fieldPath := field.Name
		if path != "" {
			fieldPath = path + "." + field.Name
		}

		// Check for yaml tag
		yamlTag := field.Tag.Get("yaml")
		if yamlTag == "" {
			return fmt.Errorf("field %s is missing yaml tag", fieldPath)
		}

		// Extract the field name from the tag (before any comma for omitempty, etc.)
		tagName := strings.Split(yamlTag, ",")[0]

		// Skip "-" tags (fields that should be ignored)
		if tagName != "-" && tagName != "" {
			// Check if the tag name uses snake_case (contains underscore)
			if strings.Contains(tagName, "_") {
				return fmt.Errorf("field %s has snake_case yaml tag '%s', should be camelCase", fieldPath, tagName)
			}

			// Check if the tag name starts with uppercase (should be lowercase for camelCase)
			if len(tagName) > 0 && unicode.IsUpper(rune(tagName[0])) {
				return fmt.Errorf("field %s has yaml tag '%s' starting with uppercase, should be camelCase", fieldPath, tagName)
			}
		}

		// Check for json tag consistency with yaml tag
		jsonTag := field.Tag.Get("json")
		if jsonTag != "" {
			jsonName := strings.Split(jsonTag, ",")[0]
			yamlName := strings.Split(yamlTag, ",")[0]
			if jsonName != yamlName && jsonName != "-" && yamlName != "-" {
				return fmt.Errorf("field %s has mismatched json ('%s') and yaml ('%s') tag names", fieldPath, jsonName, yamlName)
			}
		}

		// Recursively check nested types
		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		switch fieldType.Kind() { //nolint:exhaustive // Only checking struct, slice, and map types
		case reflect.Struct:
			// Skip time.Duration and other standard library types
			if fieldType.PkgPath() != "" && !strings.HasPrefix(fieldType.PkgPath(), "github.com/stacklok/toolhive") {
				continue
			}
			if err := checkStructTags(fieldType, fieldPath, visited); err != nil {
				return err
			}

		case reflect.Slice:
			elemType := fieldType.Elem()
			if elemType.Kind() == reflect.Ptr {
				elemType = elemType.Elem()
			}
			if elemType.Kind() == reflect.Struct {
				if elemType.PkgPath() != "" && strings.HasPrefix(elemType.PkgPath(), "github.com/stacklok/toolhive") {
					if err := checkStructTags(elemType, fieldPath+"[]", visited); err != nil {
						return err
					}
				}
			}

		case reflect.Map:
			elemType := fieldType.Elem()
			if elemType.Kind() == reflect.Ptr {
				elemType = elemType.Elem()
			}
			if elemType.Kind() == reflect.Struct {
				if elemType.PkgPath() != "" && strings.HasPrefix(elemType.PkgPath(), "github.com/stacklok/toolhive") {
					if err := checkStructTags(elemType, fieldPath+"[key]", visited); err != nil {
						return err
					}
				}
			}
		default:
			// Skip other types
			continue
		}

	}

	return nil
}
