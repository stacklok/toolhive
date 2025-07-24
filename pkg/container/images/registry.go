package images

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/stacklok/toolhive/pkg/logger"
)

// RegistryImageManager implements the ImageManager interface using go-containerregistry
// for direct registry operations without requiring a Docker daemon.
// However, for building images from Dockerfiles, it still uses the Docker client.
type RegistryImageManager struct {
	keychain     authn.Keychain
	platform     *v1.Platform
	dockerClient *client.Client
}

// NewRegistryImageManager creates a new RegistryImageManager instance
func NewRegistryImageManager(dockerClient *client.Client) *RegistryImageManager {
	return &RegistryImageManager{
		keychain:     NewCompositeKeychain(), // Use composite keychain (env vars + default)
		platform:     getDefaultPlatform(),   // Use a default platform based on host architecture
		dockerClient: dockerClient,           // Used solely for building images from Dockerfiles
	}
}

// getDefaultPlatform returns the default platform for containers
// Uses host architecture
func getDefaultPlatform() *v1.Platform {
	return &v1.Platform{
		Architecture: runtime.GOARCH,
		OS:           "linux", // TODO: Should we support Windows too?
	}
}

// ImageExists checks if an image exists locally in the daemon or remotely in the registry
func (r *RegistryImageManager) ImageExists(_ context.Context, imageName string) (bool, error) {
	// Parse the image reference
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return false, fmt.Errorf("failed to parse image reference %q: %w", imageName, err)
	}

	// First check if image exists locally in daemon
	if _, err := daemon.Image(ref, daemon.WithClient(r.dockerClient)); err != nil {
		// Image does not exist locally
		return false, nil
	}
	// Image exists locally
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
	response, err := daemon.Write(tag, img, daemon.WithClient(r.dockerClient))
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
	return buildDockerImage(ctx, r.dockerClient, contextDir, imageName)
}

// WithKeychain sets the keychain for authentication
func (r *RegistryImageManager) WithKeychain(keychain authn.Keychain) *RegistryImageManager {
	r.keychain = keychain
	return r
}

// WithPlatform sets the platform for the RegistryImageManager
func (r *RegistryImageManager) WithPlatform(platform *v1.Platform) *RegistryImageManager {
	r.platform = platform
	return r
}
