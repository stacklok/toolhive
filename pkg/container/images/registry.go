package images

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/stacklok/toolhive/pkg/logger"
)

// RegistryImageManager implements the ImageManager interface using go-containerregistry
// for direct registry operations without requiring a Docker daemon.
type RegistryImageManager struct {
	keychain authn.Keychain
}

// NewRegistryImageManager creates a new RegistryImageManager instance
func NewRegistryImageManager() *RegistryImageManager {
	return &RegistryImageManager{
		keychain: NewCompositeKeychain(), // Use composite keychain (env vars + default)
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
func (*RegistryImageManager) BuildImage(_ context.Context, contextDir, imageName string) error {
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

// WithKeychain sets the keychain for authentication
func (r *RegistryImageManager) WithKeychain(keychain authn.Keychain) *RegistryImageManager {
	r.keychain = keychain
	return r
}
