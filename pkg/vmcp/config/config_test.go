package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// TestCheckStructTags verifies that checkStructTags correctly detects various tag issues.
// checkStructTags is complex and some errors could result in false negatives (e.g. checkStructTags returns no error due to an implementation bug).
func TestCheckStructTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		testType    reflect.Type
		errContains string
	}{
		{
			name: "valid struct passes",
			testType: reflect.TypeOf(struct {
				Name string `json:"name" yaml:"name"`
			}{}),
		},
		{
			name: "missing yaml tag detected",
			testType: reflect.TypeOf(struct {
				Name string `json:"name"`
			}{}),
			errContains: "is missing yaml tag",
		},
		{
			name: "missing json tag detected",
			testType: reflect.TypeOf(struct {
				Name string `yaml:"name"`
			}{}),
			errContains: "is missing json tag",
		},
		{
			name: "snake_case yaml tag detected",
			testType: reflect.TypeOf(struct {
				UserName string `json:"user_name" yaml:"user_name"`
			}{}),
			errContains: "has snake_case yaml tag",
		},
		{
			name: "uppercase yaml tag detected",
			testType: reflect.TypeOf(struct {
				Name string `json:"Name" yaml:"Name"`
			}{}),
			errContains: "starting with uppercase",
		},
		{
			name: "mismatched json and yaml tags detected",
			testType: reflect.TypeOf(struct {
				Name string `json:"name" yaml:"userName"`
			}{}),
			errContains: "has mismatched json",
		},
		{
			name: "nested struct with missing tag detected",
			testType: reflect.TypeOf(struct {
				Outer struct {
					Inner string `json:"inner"`
				} `json:"outer" yaml:"outer"`
			}{}),
			errContains: "Outer.Inner is missing yaml tag",
		},
		{
			name: "pointer to struct with missing tag detected",
			testType: reflect.TypeOf(struct {
				Ptr *struct {
					Field string `json:"field"`
				} `json:"ptr" yaml:"ptr"`
			}{}),
			errContains: "Ptr.Field is missing yaml tag",
		},
		{
			name: "slice of structs with missing tag detected",
			testType: reflect.TypeOf(struct {
				Items []struct {
					Value string `json:"value"`
				} `json:"items" yaml:"items"`
			}{}),
			errContains: "Items.Value is missing yaml tag",
		},
		{
			name: "map value struct with missing tag detected",
			testType: reflect.TypeOf(struct {
				Data map[string]struct {
					Key string `json:"key"`
				} `json:"data" yaml:"data"`
			}{}),
			errContains: "Data.Key is missing yaml tag",
		},
		{
			name: "unexported fields are skipped",
			testType: reflect.TypeOf(struct {
				Name       string `json:"name" yaml:"name"`
				unexported string //nolint:unused // intentionally unexported for test
			}{}),
		},
		{
			name: "dash tag is allowed",
			testType: reflect.TypeOf(struct {
				Ignored string `json:"-" yaml:"-"`
			}{}),
		},
		{
			name: "omitempty is handled correctly",
			testType: reflect.TypeOf(struct {
				Optional string `json:"optional,omitempty" yaml:"optional,omitempty"`
			}{}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			visited := make(map[reflect.Type]bool)
			err := checkStructTags(tt.testType, "", visited)
			if tt.errContains == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// checkStructTags recursively checks all struct fields for yaml tags and camelCase naming.
// Returns the first error encountered, or nil if all fields are valid.
func checkStructTags(t reflect.Type, path string, visited map[reflect.Type]bool) error {
	// Skip over maps, slices, and pointers to get to the underlying struct type.
	t = func() reflect.Type {
		for {
			switch t.Kind() { //nolint:exhaustive // Only checking slice, map, and ptr types
			case reflect.Slice, reflect.Map, reflect.Ptr:
				t = t.Elem()
			default:
				return t
			}
		}
	}()

	// Only process struct types
	if t.Kind() != reflect.Struct {
		return nil
	}

	// Skip types in other libraries.
	if t.PkgPath() != "" && !strings.HasPrefix(t.PkgPath(), "github.com/stacklok/toolhive") {
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
		if jsonTag == "" {
			return fmt.Errorf("field %s is missing json tag", fieldPath)
		}

		jsonName := strings.Split(jsonTag, ",")[0]
		yamlName := strings.Split(yamlTag, ",")[0]
		if jsonName != yamlName && jsonName != "-" && yamlName != "-" {
			return fmt.Errorf("field %s has mismatched json ('%s') and yaml ('%s') tag names", fieldPath, jsonName, yamlName)
		}

		if err := checkStructTags(field.Type, fieldPath, visited); err != nil {
			return err
		}
	}

	return nil
}

// TestConfigTypesDocumentedInCRDAPI verifies that all struct types referenced by Config
// are documented in the CRD API documentation.
//
// This test ensures that when new types are added to the config package,
// they are also included in the generated API documentation.
//
// If this test fails, you need to:
// 1. Add the +gendoc marker to the struct that needs to be documented
// 2. Ensure the package has a doc.go with +groupName marker
// 3. Run 'task operator-manifests' from the repo root to regenerate docs
func TestConfigTypesDocumentedInCRDAPI(t *testing.T) {
	t.Parallel()

	// Find the repo root by looking for go.mod
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to get caller info")

	repoRoot := filepath.Dir(filename)
	for !fileExists(filepath.Join(repoRoot, "go.mod")) && repoRoot != "/" {
		repoRoot = filepath.Dir(repoRoot)
	}
	require.NotEqual(t, "/", repoRoot, "could not find repo root")

	// Read the CRD API documentation
	crdAPIPath := filepath.Join(repoRoot, "docs", "operator", "crd-api.md")
	content, err := os.ReadFile(crdAPIPath)
	require.NoError(t, err, "failed to read crd-api.md")
	crdAPIContent := string(content)

	// Collect all struct types referenced by Config
	var cfg Config
	visited := make(map[reflect.Type]bool)
	types := collectStructTypes(reflect.TypeOf(cfg), visited)

	// Check that each type has a definition in the CRD API docs
	var missingTypes []string
	for _, typeName := range types {
		// The heading format is: #### pkg.subpkg.TypeName
		// The anchor format is: #pkgsubpkgtypename (dots removed, lowercase)
		// We search for the heading pattern
		heading := fmt.Sprintf("#### %s", typeName)
		if !strings.Contains(crdAPIContent, heading) {
			missingTypes = append(missingTypes, typeName)
		}
	}

	if len(missingTypes) > 0 {
		t.Errorf("The following types from pkg/vmcp/config are not documented in crd-api.md:\n"+
			"  %s\n\n"+
			"To fix this:\n"+
			"1. Add '// +gendoc' marker above the struct definition\n"+
			"2. Ensure the package has a doc.go with '// +groupName=toolhive.stacklok.dev'\n"+
			"3. Run 'task operator-manifests' from the repo root to regenerate docs",
			strings.Join(missingTypes, "\n  "))
	}
}

// collectStructTypes recursively collects all struct type names referenced by a type.
// Returns a list of type names in the format "pkg.TypeName" for types in the toolhive codebase.
func collectStructTypes(t reflect.Type, visited map[reflect.Type]bool) []string {
	var types []string

	// Unwrap pointers, slices, maps
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		if t.Kind() == reflect.Map {
			// Also check map key/value types
			types = append(types, collectStructTypes(t.Key(), visited)...)
			t = t.Elem()
		} else {
			t = t.Elem()
		}
	}

	if t.Kind() != reflect.Struct {
		return types
	}

	// Skip external packages
	pkgPath := t.PkgPath()
	if pkgPath == "" || !strings.HasPrefix(pkgPath, "github.com/stacklok/toolhive") {
		return types
	}

	// Skip pkg/json.Data types - they are generic container types that don't need documentation
	if strings.HasSuffix(pkgPath, "/pkg/json") && strings.HasPrefix(t.Name(), "Data[") {
		return types
	}

	// Avoid infinite recursion
	if visited[t] {
		return types
	}
	visited[t] = true

	// Extract package prefix (last two path segments)
	parts := strings.Split(pkgPath, "/")
	var prefix string
	if len(parts) >= 2 {
		prefix = parts[len(parts)-2] + "." + parts[len(parts)-1]
	} else {
		prefix = parts[len(parts)-1]
	}

	types = append(types, prefix+"."+t.Name())

	// Recurse into fields
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.IsExported() {
			types = append(types, collectStructTypes(field.Type, visited)...)
		}
	}

	return types
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
