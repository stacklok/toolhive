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

// Validators for OTEL fields

func validateOTELEndpoint(_ Provider, value string) error {
	// The endpoint should not start with http:// or https://
	if value != "" && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
		return fmt.Errorf("endpoint URL should not start with http:// or https://")
	}
	return nil
}

func validateOTELSamplingRate(_ Provider, value string) error {
	rate, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("invalid sampling rate format: %w", err)
	}
	if rate < 0.0 || rate > 1.0 {
		return fmt.Errorf("sampling rate must be between 0.0 and 1.0")
	}
	return nil
}

func validateBool(_ Provider, value string) error {
	_, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid boolean value: %w (expected: true, false, 1, 0)", err)
	}
	return nil
}

// Field registrations using helper constructors

func registerOTELEndpoint() {
	RegisterStringField("otel-endpoint",
		func(cfg *Config) *string { return &cfg.OTEL.Endpoint },
		validateOTELEndpoint)
}

func registerOTELSamplingRate() {
	RegisterFloatField("otel-sampling-rate",
		func(cfg *Config) *float64 { return &cfg.OTEL.SamplingRate },
		0.0,
		validateOTELSamplingRate)
}

func registerOTELEnvVars() {
	RegisterStringSliceField("otel-env-vars",
		func(cfg *Config) *[]string { return &cfg.OTEL.EnvVars },
		nil) // No validation needed
}

func registerOTELMetricsEnabled() {
	RegisterBoolField("otel-metrics-enabled",
		func(cfg *Config) *bool { return &cfg.OTEL.MetricsEnabled },
		validateBool)
}

func registerOTELTracingEnabled() {
	RegisterBoolField("otel-tracing-enabled",
		func(cfg *Config) *bool { return &cfg.OTEL.TracingEnabled },
		validateBool)
}

func registerOTELInsecure() {
	RegisterBoolField("otel-insecure",
		func(cfg *Config) *bool { return &cfg.OTEL.Insecure },
		validateBool)
}

func registerOTELEnablePrometheusMetricsPath() {
	RegisterBoolField("otel-enable-prometheus-metrics-path",
		func(cfg *Config) *bool { return &cfg.OTEL.EnablePrometheusMetricsPath },
		validateBool)
}
