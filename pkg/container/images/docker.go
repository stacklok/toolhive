// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/stacklok/toolhive/pkg/logger"
)

// DockerImageManager implements the ImageManager interface for Docker,
// or compatible runtimes such as Podman.
type DockerImageManager struct {
	client *client.Client
}

// NewDockerImageManager creates a new DockerImageManager instance
// This is intended for the Docker runtime implementation.
func NewDockerImageManager(dockerClient *client.Client) *DockerImageManager {
	return &DockerImageManager{
		client: dockerClient,
	}
}

// ImageExists checks if an image exists locally
func (d *DockerImageManager) ImageExists(ctx context.Context, imageName string) (bool, error) {
	// List images with the specified name
	filterArgs := filters.NewArgs()
	filterArgs.Add("reference", imageName)

	images, err := d.client.ImageList(ctx, dockerimage.ListOptions{
		Filters: filterArgs,
	})
	if err != nil {
		return false, fmt.Errorf("failed to list images: %w", err)
	}

	return len(images) > 0, nil
}

// BuildImage builds a Docker image from a Dockerfile in the specified context directory
func (d *DockerImageManager) BuildImage(ctx context.Context, contextDir, imageName string) error {
	return buildDockerImage(ctx, d.client, contextDir, imageName)
}

// PullImage pulls an image from a registry
func (d *DockerImageManager) PullImage(ctx context.Context, imageName string) error {
	logger.Infof("Pulling image: %s", imageName)

	// Pull the image
	reader, err := d.client.ImagePull(ctx, imageName, dockerimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			// Non-fatal: reader cleanup failure
			logger.Debugf("Failed to close image reader: %v", err)
		}
	}()

	// Parse and filter the pull output
	if err := parsePullOutput(reader, os.Stdout); err != nil {
		return fmt.Errorf("failed to process pull output: %w", err)
	}

	return nil
}

// buildDockerImage builds a Docker image using the Docker client API
func buildDockerImage(ctx context.Context, dockerClient *client.Client, contextDir, imageName string) error {
	logger.Infof("Building image %s from context directory %s", imageName, contextDir)

	// Create a tar archive of the context directory
	tarFile, err := os.CreateTemp("", "docker-build-context-*.tar")
	if err != nil {
		return fmt.Errorf("failed to create temporary tar file: %w", err)
	}
	defer func() {
		if err := os.Remove(tarFile.Name()); err != nil {
			// Non-fatal: temp file cleanup failure
			logger.Debugf("Failed to remove temporary file %s: %v", tarFile.Name(), err)
		}
	}()
	defer func() {
		if err := tarFile.Close(); err != nil {
			// Non-fatal: file cleanup failure
			logger.Warnf("Failed to close tar file: %v", err)
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
	buildOptions := build.ImageBuildOptions{
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
			logger.Debugf("Failed to close response body: %v", err)
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
			logger.Warnf("Failed to close tar writer: %v", err)
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
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open file: %w", err)
			}
			defer func() {
				if err := file.Close(); err != nil {
					// Non-fatal: file cleanup failure
					logger.Warnf("Failed to close file: %v", err)
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
				logger.Debugf("Failed to write build output: %v", err)
			}
		}
	}

	return nil
}

// parsePullOutput parses the Docker image pull output and formats it in a more readable way
func parsePullOutput(reader io.Reader, writer io.Writer) error {
	decoder := json.NewDecoder(reader)
	for {
		var pullStatus struct {
			Status         string          `json:"status"`
			ID             string          `json:"id,omitempty"`
			ProgressDetail json.RawMessage `json:"progressDetail,omitempty"`
			Progress       string          `json:"progress,omitempty"`
		}

		if err := decoder.Decode(&pullStatus); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode pull output: %w", err)
		}

		// Format the output based on the type of message
		if pullStatus.Progress != "" {
			// This is a progress update
			if _, err := fmt.Fprintf(writer, "%s: %s %s\n", pullStatus.Status, pullStatus.ID, pullStatus.Progress); err != nil {
				logger.Debugf("Failed to write pull output: %v", err)
			}
		} else if pullStatus.ID != "" {
			// This is a layer-specific status update
			if _, err := fmt.Fprintf(writer, "%s: %s\n", pullStatus.Status, pullStatus.ID); err != nil {
				logger.Debugf("Failed to write pull output: %v", err)
			}
		} else {
			// This is a general status update
			if _, err := fmt.Fprintf(writer, "%s\n", pullStatus.Status); err != nil {
				logger.Debugf("Failed to write pull output: %v", err)
			}
		}
	}

	return nil
}
