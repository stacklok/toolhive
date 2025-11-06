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
// boilerplate. Fields are registered once with validation, getters, setters,
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
// Step 2: Register the field using a helper constructor:
//
//	func init() {
//	    // For simple string fields:
//	    config.RegisterStringField("my-field",
//	        func(cfg *Config) *string { return &cfg.MyNewField },
//	        validateMyField) // Optional validator
//
//	    // For boolean fields:
//	    config.RegisterBoolField("my-bool-field",
//	        func(cfg *Config) *bool { return &cfg.MyBoolField },
//	        nil) // nil = no validation
//
//	    // For float fields:
//	    config.RegisterFloatField("my-float-field",
//	        func(cfg *Config) *float64 { return &cfg.MyFloatField },
//	        0.0, // zero value
//	        validateMyFloat)
//
//	    // For string slice fields (comma-separated):
//	    config.RegisterStringSliceField("my-list-field",
//	        func(cfg *Config) *[]string { return &cfg.MyListField },
//	        nil)
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
// # Advanced: Custom Field Registration
//
// For fields with complex logic, use RegisterConfigField directly:
//
//	config.RegisterConfigField(config.ConfigFieldSpec{
//	    Name: "my-complex-field",
//	    SetValidator: func(_ Provider, value string) error {
//	        // Custom validation logic
//	        return nil
//	    },
//	    Setter: func(cfg *Config, value string) {
//	        // Custom setter logic
//	    },
//	    Getter: func(cfg *Config) string {
//	        // Custom getter logic
//	        return ""
//	    },
//	    Unsetter: func(cfg *Config) {
//	        // Custom unsetter logic
//	    },
//	})
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
// Use these in your validator function for consistent error messages.
//
// # Built-in Fields
//
// The following fields are currently registered:
//   - ca-cert: Path to a CA certificate file for TLS validation
//   - registry-url: URL of the MCP server registry (HTTP/HTTPS)
//   - registry-file: Path to a local JSON file containing the registry
//   - otel-endpoint: OpenTelemetry OTLP endpoint
//   - otel-sampling-rate: Trace sampling rate (0.0-1.0)
//   - otel-env-vars: Environment variables for telemetry
//   - otel-metrics-enabled: Enable metrics export
//   - otel-tracing-enabled: Enable tracing export
//   - otel-insecure: Use insecure connection
//   - otel-enable-prometheus-metrics-path: Enable Prometheus endpoint
package config
