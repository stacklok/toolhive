package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/certs"
)

// setCACert validates and sets the CA certificate path using the provided provider.
// It performs the following validations:
//   - Verifies the file exists and is readable
//   - Reads the certificate content
//   - Validates the certificate format using pkg/certs.ValidateCACertificate
//   - Cleans the file path
//
// The function returns an error if any validation fails or if updating the configuration fails.
func setCACert(provider Provider, certPath string) error {
	// Clean the path
	certPath = filepath.Clean(certPath)

	// Validate that the file exists and is readable
	if _, err := os.Stat(certPath); err != nil {
		return fmt.Errorf("CA certificate file not found or not accessible: %w", err)
	}

	// Read and validate the certificate
	certContent, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate file: %w", err)
	}

	// Validate the certificate format
	if err := certs.ValidateCACertificate(certContent); err != nil {
		return fmt.Errorf("invalid CA certificate: %w", err)
	}

	// Update the configuration
	err = provider.UpdateConfig(func(c *Config) {
		c.CACertificatePath = certPath
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// getCACert returns the currently configured CA certificate path and its accessibility status.
// It returns three values:
//   - certPath: the configured certificate path (empty string if not set)
//   - exists: true if a CA certificate is configured in the config
//   - accessible: true if the certificate file exists and is accessible on the filesystem
//
// Note: exists can be true while accessible is false if the file was deleted after configuration.
func getCACert(provider Provider) (certPath string, exists bool, accessible bool) {
	cfg := provider.GetConfig()

	if cfg.CACertificatePath == "" {
		return "", false, false
	}

	certPath = cfg.CACertificatePath
	exists = true

	// Check if the file is still accessible
	if _, err := os.Stat(certPath); err != nil {
		accessible = false
	} else {
		accessible = true
	}

	return certPath, exists, accessible
}

// unsetCACert removes the CA certificate configuration from the config file.
// If no CA certificate is currently configured, this function is a no-op and returns nil.
// Returns an error if updating the configuration fails.
func unsetCACert(provider Provider) error {
	cfg := provider.GetConfig()

	if cfg.CACertificatePath == "" {
		// Already unset, no-op
		return nil
	}

	// Update the configuration
	err := provider.UpdateConfig(func(c *Config) {
		c.CACertificatePath = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}
