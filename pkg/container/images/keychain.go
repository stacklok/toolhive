package images

import (
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
)

// envKeychain implements a keychain that reads credentials from environment variables
type envKeychain struct{}

// Resolve implements the authn.Keychain interface
func (*envKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	registry := target.RegistryStr()

	// Try registry-specific environment variables first
	// Format: REGISTRY_<NORMALIZED_REGISTRY_NAME>_USERNAME/PASSWORD
	normalizedRegistry := strings.ToUpper(strings.ReplaceAll(registry, ".", "_"))
	normalizedRegistry = strings.ReplaceAll(normalizedRegistry, "-", "_")

	username := os.Getenv("REGISTRY_" + normalizedRegistry + "_USERNAME")
	password := os.Getenv("REGISTRY_" + normalizedRegistry + "_PASSWORD")

	// If registry-specific vars not found, try generic ones
	if username == "" || password == "" {
		username = os.Getenv("DOCKER_USERNAME")
		password = os.Getenv("DOCKER_PASSWORD")
	}

	// If still not found, try REGISTRY_USERNAME/PASSWORD
	if username == "" || password == "" {
		username = os.Getenv("REGISTRY_USERNAME")
		password = os.Getenv("REGISTRY_PASSWORD")
	}

	if username != "" && password != "" {
		return &authn.Basic{
			Username: username,
			Password: password,
		}, nil
	}

	return authn.Anonymous, nil
}

// compositeKeychain combines multiple keychains and tries them in order
type compositeKeychain struct {
	keychains []authn.Keychain
}

// Resolve implements the authn.Keychain interface
func (c *compositeKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	for _, keychain := range c.keychains {
		auth, err := keychain.Resolve(target)
		if err != nil {
			continue
		}

		// Check if we got actual credentials (not anonymous)
		if auth != authn.Anonymous {
			return auth, nil
		}
	}

	// If no keychain provided credentials, return anonymous
	return authn.Anonymous, nil
}

// NewCompositeKeychain creates a keychain that tries environment variables first,
// then falls back to the default keychain
func NewCompositeKeychain() authn.Keychain {
	return &compositeKeychain{
		keychains: []authn.Keychain{
			&envKeychain{},        // Try environment variables first
			authn.DefaultKeychain, // Then try default keychain (Docker config, etc.)
		},
	}
}
