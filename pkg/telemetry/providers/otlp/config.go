// Package otlp provides OpenTelemetry Protocol (OTLP) provider implementations
package otlp

// Config holds OTLP-specific configuration
type Config struct {
	Endpoint     string
	Headers      map[string]string
	Insecure     bool
	SamplingRate float64
}
