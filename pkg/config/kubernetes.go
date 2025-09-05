package config

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
)

// KubernetesStore is a no-op config store implementation for Kubernetes environments
// where configuration is provided via ConfigMaps and environment variables
type KubernetesStore struct{}

// NewKubernetesStore creates a new KubernetesStore
func NewKubernetesStore() *KubernetesStore {
	logger.Debugf("Using Kubernetes no-op config store")
	return &KubernetesStore{}
}

// Load returns a default configuration with minimal settings for Kubernetes
func (*KubernetesStore) Load(_ context.Context) (*Config, error) {
	logger.Debugf("Kubernetes config store Load (returning default config)")
	return &Config{
		// Provide minimal default configuration for Kubernetes
		Secrets: Secrets{
			ProviderType:   "none", // Don't use secrets provider in Kubernetes
			SetupCompleted: true,   // Skip setup
		},
		Clients:                Clients{},             // Empty clients config
		RegistryUrl:            "",                    // Use default
		LocalRegistryPath:      "",                    // Not needed in Kubernetes
		AllowPrivateRegistryIp: false,                 // Default security setting
		CACertificatePath:      "",                    // Not needed in Kubernetes
		OTEL:                   OpenTelemetryConfig{}, // Empty OTEL config
		DefaultGroupMigration:  false,                 // Default setting
	}, nil
}

// Save is a no-op in Kubernetes (configuration is immutable via ConfigMaps)
func (*KubernetesStore) Save(_ context.Context, _ *Config) error {
	logger.Debugf("Kubernetes config store Save (no-op)")
	return nil
}

// Exists always returns true in Kubernetes (we always have default config)
func (*KubernetesStore) Exists(_ context.Context) (bool, error) {
	logger.Debugf("Kubernetes config store Exists (always true)")
	return true, nil
}

// Update is a no-op in Kubernetes (configuration is immutable via ConfigMaps)
func (*KubernetesStore) Update(_ context.Context, _ func(*Config)) error {
	logger.Debugf("Kubernetes config store Update (no-op)")
	return nil
}
