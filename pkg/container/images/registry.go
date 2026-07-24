// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	mobyclient "github.com/moby/moby/client"
)

// RegistryImageManager implements the ImageManager interface using go-containerregistry
// for direct registry operations without requiring a Docker daemon.
// However, for building images from Dockerfiles, it still uses the Docker client.
type RegistryImageManager struct {
	keychain authn.Keychain
	platform *v1.Platform
	// dockerClient is used for building images from Dockerfiles and, because the
	// moby/moby client already satisfies the go-containerregistry daemon.Client
	// interface, for daemon.Image/daemon.Write operations as well.
	dockerClient *mobyclient.Client
}

// NewRegistryImageManager creates a new RegistryImageManager instance
func NewRegistryImageManager(dockerClient *mobyclient.Client) *RegistryImageManager {
	return &RegistryImageManager{
		keychain:     NewCompositeKeychain(), // Use composite keychain (env vars + default)
		platform:     getDefaultPlatform(),   // Use a default platform based on host architecture
		dockerClient: dockerClient,
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

// PullImage pulls an image from a registry and saves it to the local daemon.
//
// For digest-pinned references (tag@digest or digest-only), the Docker daemon's
// native ImagePull is used. The daemon registers the OCI manifest digest so that
// subsequent container creates using a tag@digest reference can resolve the image.
// go-containerregistry's daemon.Write stores images under a tag only, without
// registering the manifest digest; Docker's tag@digest cross-check at
// container-create time then fails with "No such image".
//
// For plain tag references, go-containerregistry is used for cross-platform
// support and custom keychain auth.
func (r *RegistryImageManager) PullImage(ctx context.Context, imageName string) error {
	//nolint:gosec // G706: image name from user/config input
	slog.Info("pulling image", "image", imageName)

	ref, err := name.ParseReference(imageName)
	if err != nil {
		return fmt.Errorf("failed to parse image reference %q: %w", imageName, err)
	}

	if _, ok := ref.(name.Digest); ok {
		return r.pullDigestViaDockerDaemon(ctx, imageName, ref)
	}

	tag, ok := ref.(name.Tag)
	if !ok {
		return fmt.Errorf("unsupported image reference type %T", ref)
	}

	remoteOpts := []remote.Option{
		remote.WithAuthFromKeychain(r.keychain),
		remote.WithContext(ctx),
	}
	if r.platform != nil {
		remoteOpts = append(remoteOpts, remote.WithPlatform(*r.platform))
	}

	img, err := remote.Image(ref, remoteOpts...)
	if err != nil {
		return fmt.Errorf("failed to pull image from registry: %w", err)
	}

	response, err := daemon.Write(tag, img, daemon.WithClient(r.dockerClient))
	if err != nil {
		return fmt.Errorf("failed to write image to daemon: %w", err)
	}

	if _, err := fmt.Fprintf(os.Stdout, "Successfully pulled %s\n", imageName); err != nil {
		slog.Debug("failed to write success message", "error", err)
	}
	//nolint:gosec // G706: image name and response from registry pull
	slog.Debug("pull complete", "image", imageName, "response", response)

	return nil
}

// pullDigestViaDockerDaemon pulls a digest-pinned image (tag@digest or
// digest-only form) using the Docker daemon client so the OCI manifest digest
// is stored and Docker can resolve the reference at container-create time.
func (r *RegistryImageManager) pullDigestViaDockerDaemon(ctx context.Context, imageName string, ref name.Reference) error {
	// Resolve auth from the keychain and encode for the daemon API.
	var registryAuth string
	if auth, err := r.keychain.Resolve(ref.Context().Registry); err == nil && auth != authn.Anonymous {
		if authCfg, err := auth.Authorization(); err == nil {
			if authJSON, err := json.Marshal(authCfg); err == nil {
				registryAuth = base64.URLEncoding.EncodeToString(authJSON)
			}
		}
	}

	resp, err := r.dockerClient.ImagePull(ctx, imageName, mobyclient.ImagePullOptions{
		RegistryAuth: registryAuth,
	})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("failed waiting for image pull: %w", err)
	}

	if _, err := fmt.Fprintf(os.Stdout, "Successfully pulled %s\n", imageName); err != nil {
		slog.Debug("failed to write success message", "error", err)
	}

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

// buildDockerImage builds a Docker image using the Docker client API
func buildDockerImage(ctx context.Context, dockerClient *mobyclient.Client, contextDir, imageName string) error {
	//nolint:gosec // G706: image name and context dir from config
	slog.Debug("building image", "image", imageName, "context_dir", contextDir)

	// Create a tar archive of the context directory
	tarFile, err := os.CreateTemp("", "docker-build-context-*.tar")
	if err != nil {
		return fmt.Errorf("failed to create temporary tar file: %w", err)
	}
	defer func() {
		// #nosec G703 -- tarFile.Name() is from os.CreateTemp, not user input
		if err := os.Remove(tarFile.Name()); err != nil {
			// Non-fatal: temp file cleanup failure
			//nolint:gosec // G706: temp file path from os.CreateTemp
			slog.Debug("failed to remove temporary file", "path", tarFile.Name(), "error", err)
		}
	}()
	defer func() {
		if err := tarFile.Close(); err != nil {
			// Docker client closes the reader on success, so ignore "already closed" errors
			if !errors.Is(err, os.ErrClosed) {
				// Non-fatal: file cleanup failure
				slog.Debug("failed to close tar file", "error", err)
			}
		}
	}()

	// Create a tar archive of the context directory
	if err := createTarFromDir(contextDir, tarFile); err != nil {
		return fmt.Errorf("failed to create tar archive: %w", err)
	}

	// Reset the file pointer to the beginning of the file
	if _, err := tarFile.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset tar file pointer: %w", err)
	}

	// Build the image
	buildOptions := mobyclient.ImageBuildOptions{
		Tags:       []string{imageName},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	response, err := dockerClient.ImageBuild(ctx, tarFile, buildOptions)
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			// Non-fatal: response body cleanup failure
			slog.Debug("failed to close response body", "error", err)
		}
	}()

	// Parse and log the build output
	if err := parseBuildOutput(response.Body, os.Stdout); err != nil {
		return fmt.Errorf("failed to process build output: %w", err)
	}

	return nil
}

// createTarFromDir creates a tar archive from a directory
func createTarFromDir(srcDir string, writer io.Writer) error {
	// Create a new tar writer
	tw := tar.NewWriter(writer)
	defer func() {
		if err := tw.Close(); err != nil {
			// Non-fatal: tar writer cleanup failure
			slog.Debug("failed to close tar writer", "error", err)
		}
	}()

	// Walk through the directory and add files to the tar archive
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get the relative path
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Skip the root directory
		if relPath == "." {
			return nil
		}

		// Create a tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header: %w", err)
		}

		// Set the name to the relative path
		header.Name = relPath

		// Write the header
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		// If it's a regular file, write the contents
		if !info.IsDir() {
			// #nosec G304 - This is safe because we're only opening files within the specified context directory
			file, err := os.Open(path) //nolint:gosec // G122 - path from filepath.Walk within validated source directory
			if err != nil {
				return fmt.Errorf("failed to open file: %w", err)
			}
			defer func() {
				if err := file.Close(); err != nil {
					// Non-fatal: file cleanup failure
					slog.Debug("failed to close file", "error", err)
				}
			}()

			if _, err := io.Copy(tw, file); err != nil {
				return fmt.Errorf("failed to copy file contents: %w", err)
			}
		}

		return nil
	})
}

// parseBuildOutput parses the Docker image build output and formats it in a more readable way
func parseBuildOutput(reader io.Reader, writer io.Writer) error {
	decoder := json.NewDecoder(reader)
	for {
		var buildOutput struct {
			Stream string `json:"stream,omitempty"`
			Error  string `json:"error,omitempty"`
		}

		if err := decoder.Decode(&buildOutput); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode build output: %w", err)
		}

		// Check for errors
		if buildOutput.Error != "" {
			return fmt.Errorf("build error: %s", buildOutput.Error)
		}

		// Print the stream output
		if buildOutput.Stream != "" {
			if _, err := fmt.Fprint(writer, buildOutput.Stream); err != nil {
				slog.Debug("failed to write build output", "error", err)
			}
		}
	}

	return nil
}
