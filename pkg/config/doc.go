// Package config provides configuration management for ToolHive, including a
// generic framework for easily adding new configuration fields.
//
// # Architecture
//
// The package uses a Provider pattern to abstract configuration storage:
//   - DefaultProvider: Uses XDG config directories (~/.config/toolhive/config.yaml)
//   - PathProvider: Uses a specific file path (useful for testing)
//   - KubernetesProvider: No-op implementation for Kubernetes environments
//
// # Generic Config Field Framework
//
// The framework allows you to define config fields declaratively with minimal
// boilerplate. Each field is registered once with validation, getters, setters,
// and unseters.
//
// # Adding a New Config Field
//
// Step 1: Add your field to the Config struct:
//
//	type Config struct {
//	    // ... existing fields ...
//	    MyNewField string `yaml:"my_new_field,omitempty"`
//	}
//
// Step 2: Register the field (typically in fields_builtin.go):
//
//	func init() {
//	    registerMyNewField()
//	}
//
//	func registerMyNewField() {
//	    RegisterConfigField(ConfigFieldSpec{
//	        Name: "my-field",
//	        SetValidator: func(_ Provider, value string) error {
//	            // Optional: validate the value before setting
//	            if value == "" {
//	                return fmt.Errorf("value cannot be empty")
//	            }
//	            return nil
//	        },
//	        Setter: func(cfg *Config, value string) {
//	            cfg.MyNewField = value
//	        },
//	        Getter: func(cfg *Config) string {
//	            return cfg.MyNewField
//	        },
//	        Unsetter: func(cfg *Config) {
//	            cfg.MyNewField = ""
//	        },
//	        DisplayName: "My New Field",
//	        HelpText:    "Description of what this field does",
//	    })
//	}
//
// Step 3: Use the field through the generic framework:
//
//	provider := config.NewDefaultProvider()
//
//	// Set a value
//	err := config.SetConfigField(provider, "my-field", "some-value")
//
//	// Get a value
//	value, isSet, err := config.GetConfigField(provider, "my-field")
//
//	// Unset a value
//	err := config.UnsetConfigField(provider, "my-field")
//
// # Validation Helpers
//
// The package provides common validation functions:
//   - validateFilePath: Validates file exists and returns cleaned path
//   - validateFileExists: Checks if file exists
//   - validateJSONFile: Validates file is JSON format
//   - validateURLScheme: Validates URL scheme (http/https)
//   - makeAbsolutePath: Converts relative to absolute path
//
// Use these in your SetValidator function for consistent error messages.
//
// # Built-in Fields
//
// The following fields are currently registered:
//   - ca-cert: Path to a CA certificate file for TLS validation
//   - registry-url: URL of the MCP server registry (HTTP/HTTPS)
//   - registry-file: Path to a local JSON file containing the registry
package config
