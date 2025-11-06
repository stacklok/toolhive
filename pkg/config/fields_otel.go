package config

import (
	"fmt"
	"strconv"
	"strings"
)

// init registers all OTEL config fields
func init() {
	registerOTELEndpoint()
	registerOTELSamplingRate()
	registerOTELEnvVars()
	registerOTELMetricsEnabled()
	registerOTELTracingEnabled()
	registerOTELInsecure()
	registerOTELEnablePrometheusMetricsPath()
}

// registerOTELEndpoint registers the OTEL endpoint config field
func registerOTELEndpoint() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-endpoint",
		SetValidator: func(_ Provider, value string) error {
			// The endpoint should not start with http:// or https://
			if value != "" && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
				return fmt.Errorf("endpoint URL should not start with http:// or https://")
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			cfg.OTEL.Endpoint = value
		},
		Getter: func(cfg *Config) string {
			return cfg.OTEL.Endpoint
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.Endpoint = ""
		},
		IsSet: func(cfg *Config) bool {
			return cfg.OTEL.Endpoint != ""
		},
		DisplayName: "OTEL Endpoint",
		HelpText:    "OpenTelemetry OTLP endpoint URL for tracing and metrics",
	})
}

// registerOTELSamplingRate registers the OTEL sampling rate config field
func registerOTELSamplingRate() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-sampling-rate",
		SetValidator: func(_ Provider, value string) error {
			rate, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid sampling rate format: %w", err)
			}
			if rate < 0.0 || rate > 1.0 {
				return fmt.Errorf("sampling rate must be between 0.0 and 1.0")
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			rate, _ := strconv.ParseFloat(value, 64) // Already validated
			cfg.OTEL.SamplingRate = rate
		},
		Getter: func(cfg *Config) string {
			if cfg.OTEL.SamplingRate == 0.0 {
				return ""
			}
			return strconv.FormatFloat(cfg.OTEL.SamplingRate, 'f', -1, 64)
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.SamplingRate = 0.0
		},
		IsSet: func(cfg *Config) bool {
			return cfg.OTEL.SamplingRate != 0.0
		},
		DisplayName: "OTEL Sampling Rate",
		HelpText:    "OpenTelemetry trace sampling rate (between 0.0 and 1.0)",
	})
}

// registerOTELEnvVars registers the OTEL environment variables config field
func registerOTELEnvVars() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-env-vars",
		SetValidator: func(_ Provider, value string) error {
			// No validation needed - any comma-separated string is valid
			return nil
		},
		Setter: func(cfg *Config, value string) {
			vars := strings.Split(value, ",")
			// Trim whitespace from each variable name
			for i, varName := range vars {
				vars[i] = strings.TrimSpace(varName)
			}
			cfg.OTEL.EnvVars = vars
		},
		Getter: func(cfg *Config) string {
			if len(cfg.OTEL.EnvVars) == 0 {
				return ""
			}
			return strings.Join(cfg.OTEL.EnvVars, ",")
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.EnvVars = nil
		},
		IsSet: func(cfg *Config) bool {
			return len(cfg.OTEL.EnvVars) > 0
		},
		DisplayName: "OTEL Environment Variables",
		HelpText:    "Comma-separated list of environment variable names to include in telemetry",
	})
}

// registerOTELMetricsEnabled registers the OTEL metrics enabled config field
func registerOTELMetricsEnabled() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-metrics-enabled",
		SetValidator: func(_ Provider, value string) error {
			_, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value: %w (expected: true, false, 1, 0)", err)
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			enabled, _ := strconv.ParseBool(value) // Already validated
			cfg.OTEL.MetricsEnabled = enabled
		},
		Getter: func(cfg *Config) string {
			return strconv.FormatBool(cfg.OTEL.MetricsEnabled)
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.MetricsEnabled = false
		},
		IsSet: func(cfg *Config) bool {
			// Consider it set if it's explicitly set to true
			return cfg.OTEL.MetricsEnabled
		},
		DisplayName: "OTEL Metrics Enabled",
		HelpText:    "Enable OpenTelemetry metrics export",
	})
}

// registerOTELTracingEnabled registers the OTEL tracing enabled config field
func registerOTELTracingEnabled() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-tracing-enabled",
		SetValidator: func(_ Provider, value string) error {
			_, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value: %w (expected: true, false, 1, 0)", err)
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			enabled, _ := strconv.ParseBool(value) // Already validated
			cfg.OTEL.TracingEnabled = enabled
		},
		Getter: func(cfg *Config) string {
			return strconv.FormatBool(cfg.OTEL.TracingEnabled)
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.TracingEnabled = false
		},
		IsSet: func(cfg *Config) bool {
			// Consider it set if it's explicitly set to true
			return cfg.OTEL.TracingEnabled
		},
		DisplayName: "OTEL Tracing Enabled",
		HelpText:    "Enable OpenTelemetry tracing export",
	})
}

// registerOTELInsecure registers the OTEL insecure config field
func registerOTELInsecure() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-insecure",
		SetValidator: func(_ Provider, value string) error {
			_, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value: %w (expected: true, false, 1, 0)", err)
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			insecure, _ := strconv.ParseBool(value) // Already validated
			cfg.OTEL.Insecure = insecure
		},
		Getter: func(cfg *Config) string {
			return strconv.FormatBool(cfg.OTEL.Insecure)
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.Insecure = false
		},
		IsSet: func(cfg *Config) bool {
			// Consider it set if it's explicitly set to true
			return cfg.OTEL.Insecure
		},
		DisplayName: "OTEL Insecure",
		HelpText:    "Use insecure connection to OpenTelemetry endpoint",
	})
}

// registerOTELEnablePrometheusMetricsPath registers the OTEL enable Prometheus metrics path config field
func registerOTELEnablePrometheusMetricsPath() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "otel-enable-prometheus-metrics-path",
		SetValidator: func(_ Provider, value string) error {
			_, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value: %w (expected: true, false, 1, 0)", err)
			}
			return nil
		},
		Setter: func(cfg *Config, value string) {
			enabled, _ := strconv.ParseBool(value) // Already validated
			cfg.OTEL.EnablePrometheusMetricsPath = enabled
		},
		Getter: func(cfg *Config) string {
			return strconv.FormatBool(cfg.OTEL.EnablePrometheusMetricsPath)
		},
		Unsetter: func(cfg *Config) {
			cfg.OTEL.EnablePrometheusMetricsPath = false
		},
		IsSet: func(cfg *Config) bool {
			// Consider it set if it's explicitly set to true
			return cfg.OTEL.EnablePrometheusMetricsPath
		},
		DisplayName: "OTEL Enable Prometheus Metrics Path",
		HelpText:    "Enable Prometheus metrics endpoint path",
	})
}
