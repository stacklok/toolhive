package images

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/stacklok/toolhive/pkg/logger"
)

// RegistryImageManager implements the ImageManager interface using go-containerregistry
// for direct registry operations without requiring a Docker daemon.
type RegistryImageManager struct {
	keychain authn.Keychain
	platform *v1.Platform
}

// NewRegistryImageManager creates a new RegistryImageManager instance
func NewRegistryImageManager() *RegistryImageManager {
	return &RegistryImageManager{
		keychain: newCompositeKeychain(), // Use composite keychain (env vars + default)
		platform: nil,                   // Use default platform
	}
}

// ImageExists checks if an image exists locally in the daemon or remotely in the registry
func (r *RegistryImageManager) ImageExists(ctx context.Context, imageName string) (bool, error) {
	// Parse the image reference
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return false, fmt.Errorf("failed to parse image reference %q: %w", imageName, err)
	}

	// First check if image exists locally in daemon
	if _, err := daemon.Image(ref); err == nil {
		return true, nil
	}

	// If not found locally, check if it exists in the remote registry
	remoteOpts := []remote.Option{
		remote.WithAuthFromKeychain(r.keychain),
		remote.WithContext(ctx),
	}

	if r.platform != nil {
		remoteOpts = append(remoteOpts, remote.WithPlatform(*r.platform))
	}

	// Use HEAD request to check if image exists without downloading
	_, err = remote.Head(ref, remoteOpts...)
	if err != nil {
		// If we get an error, the image likely doesn't exist
		return false, nil
	}

	return true, nil
}

// PullImage pulls an image from a registry and saves it to the local daemon
func (r *RegistryImageManager) PullImage(ctx context.Context, imageName string) error {
	logger.Infof("Pulling image: %s", imageName)

	// Parse the image reference
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return fmt.Errorf("failed to parse image reference %q: %w", imageName, err)
	}

	// Configure remote options
	remoteOpts := []remote.Option{
		remote.WithAuthFromKeychain(r.keychain),
		remote.WithContext(ctx),
	}

	if r.platform != nil {
		remoteOpts = append(remoteOpts, remote.WithPlatform(*r.platform))
	}

	// Pull the image from the registry
	img, err := remote.Image(ref, remoteOpts...)
	if err != nil {
		return fmt.Errorf("failed to pull image from registry: %w", err)
	}

	// Convert reference to tag for daemon.Write
	tag, ok := ref.(name.Tag)
	if !ok {
		// If it's not a tag, try to convert to tag
		tag, err = name.NewTag(ref.String())
		if err != nil {
			return fmt.Errorf("failed to convert reference to tag: %w", err)
		}
	}

	// Save the image to the local daemon
	response, err := daemon.Write(tag, img)
	if err != nil {
		return fmt.Errorf("failed to write image to daemon: %w", err)
	}

	// Display success message
	fmt.Fprintf(os.Stdout, "Successfully pulled %s\n", imageName)
	logger.Infof("Pull complete for image: %s, response: %s", imageName, response)

	return nil
}

// BuildImage builds a Docker image from a Dockerfile in the specified context directory
func (r *RegistryImageManager) BuildImage(ctx context.Context, contextDir, imageName string) error {
	logger.Infof("Building image %s from context directory %s", imageName, contextDir)

	// Parse the image reference
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return fmt.Errorf("failed to parse image reference %q: %w", imageName, err)
	}

	// Create a tar archive of the context directory (reusing existing logic)
	tarFile, err := os.CreateTemp("", "registry-build-context-*.tar")
	if err != nil {
		return fmt.Errorf("failed to create temporary tar file: %w", err)
	}
	defer os.Remove(tarFile.Name())
	defer tarFile.Close()

	// Create a tar archive of the context directory
	if err := createTarFromDir(contextDir, tarFile); err != nil {
		return fmt.Errorf("failed to create tar archive: %w", err)
	}

	// Reset the file pointer to the beginning of the file
	if _, err := tarFile.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset tar file pointer: %w", err)
	}

	// Build the image from the tarball
	img, err := tarball.ImageFromPath(tarFile.Name(), nil)
	if err != nil {
		return fmt.Errorf("failed to build image from tarball: %w", err)
	}

	// Convert reference to tag for daemon.Write
	tag, ok := ref.(name.Tag)
	if !ok {
		// If it's not a tag, try to convert to tag
		tag, err = name.NewTag(ref.String())
		if err != nil {
			return fmt.Errorf("failed to convert reference to tag: %w", err)
		}
	}

	// Save the image to the local daemon
	response, err := daemon.Write(tag, img)
	if err != nil {
		return fmt.Errorf("failed to write built image to daemon: %w", err)
	}

	// Display success message
	fmt.Fprintf(os.Stdout, "Successfully built %s\n", imageName)
	logger.Infof("Build complete for image: %s, response: %s", imageName, response)

	return nil
}

// WithPlatform sets the platform for the RegistryImageManager
func (r *RegistryImageManager) WithPlatform(platform *v1.Platform) *RegistryImageManager {
	r.platform = platform
	return r
}

// WithKeychain sets the keychain for authentication
func (r *RegistryImageManager) WithKeychain(keychain authn.Keychain) *RegistryImageManager {
	r.keychain = keychain
	return r
}

// envKeychain implements a keychain that reads credentials from environment variables
type envKeychain struct{}

// Resolve implements the authn.Keychain interface
func (e *envKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
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

// newCompositeKeychain creates a keychain that tries environment variables first,
// then falls back to the default keychain
func newCompositeKeychain() authn.Keychain {
	return &compositeKeychain{
		keychains: []authn.Keychain{
			&envKeychain{},           // Try environment variables first
			authn.DefaultKeychain,    // Then try default keychain (Docker config, etc.)
		},
	}
}