// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package retriever contains logic for fetching or building MCP servers.
package retriever

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive-core/httperr"
	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// VerifyImageWarn prints a warning when image validation fails.
	VerifyImageWarn = "warn"
	// VerifyImageEnabled treats validation failure as a fatal error.
	VerifyImageEnabled = "enabled"
	// VerifyImageDisabled turns off validation.
	VerifyImageDisabled = "disabled"
)

var (
	// ErrBadProtocolScheme is returned when the provided serverOrImage is not a valid protocol scheme.
	ErrBadProtocolScheme = httperr.WithCode(
		errors.New("invalid protocol scheme provided for MCP server"),
		http.StatusBadRequest,
	)
	// ErrImageNotFound is returned when the specified image is not found in the registry.
	ErrImageNotFound = httperr.WithCode(
		errors.New("image not found in registry, please check the image name or tag"),
		http.StatusNotFound,
	)
	// ErrInvalidRunConfig is returned when the run configuration built by RunConfigBuilder is invalid
	ErrInvalidRunConfig = httperr.WithCode(
		errors.New("invalid run configuration provided"),
		http.StatusBadRequest,
	)
)

// Retriever is a function that retrieves the MCP server definition from the registry.
type Retriever func(
	context.Context, string, string, string, string, *templates.RuntimeConfig,
) (string, types.ServerMetadata, error)

// GetMCPServer retrieves the MCP server definition from the registry.
func GetMCPServer(
	ctx context.Context,
	serverOrImage string,
	rawCACertPath string,
	verificationType string,
	groupName string,
	runtimeOverride *templates.RuntimeConfig,
) (string, types.ServerMetadata, error) {
	var imageMetadata *types.ImageMetadata
	var imageToUse string

	imageManager := images.NewImageManager(ctx)
	// Check if the serverOrImage is a protocol scheme, e.g., uvx://, npx://, or go://
	if runner.IsImageProtocolScheme(serverOrImage) {
		slog.Debug("Attempting to retrieve MCP server from protocol scheme",
			"server_or_image", serverOrImage)
		var err error
		imageToUse, imageMetadata, err = handleProtocolScheme(ctx, serverOrImage, rawCACertPath, imageManager, runtimeOverride)
		if err != nil {
			return "", nil, err
		}
	} else {
		slog.Debug("No protocol scheme detected, attempting to retrieve image or registry server",
			"server_or_image", serverOrImage)

		// If group name is provided, look up server in the group first
		if groupName != "" {
			var err error
			var server types.ServerMetadata
			imageToUse, imageMetadata, server, err = handleGroupLookup(ctx, serverOrImage, groupName)
			if err != nil {
				return "", nil, err
			}
			// Handle remote servers early return
			if server != nil && server.IsRemote() {
				return serverOrImage, server, nil
			}
		} else {
			var err error
			var server types.ServerMetadata
			imageToUse, imageMetadata, server, err = handleRegistryLookup(ctx, serverOrImage)
			if err != nil {
				return "", nil, err
			}
			// Handle remote servers early return
			if server != nil && server.IsRemote() {
				return serverOrImage, server, nil
			}
		}
	}

	// Verify the image against the expected provenance info (if applicable)
	if err := verifyImage(imageToUse, imageMetadata, verificationType); err != nil {
		return "", nil, err
	}

	// Pull the image if necessary
	if err := pullImage(ctx, imageToUse, imageManager); err != nil {
		// Check if the error is due to context cancellation/timeout
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", nil, fmt.Errorf("image pull timed out - the image may be too large or the connection too slow")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", nil, fmt.Errorf("image pull was canceled")
		}
		return "", nil, fmt.Errorf("failed to retrieve or pull image: %w", err)
	}

	return imageToUse, imageMetadata, nil
}

// handleProtocolScheme handles the protocol scheme case
func handleProtocolScheme(
	ctx context.Context,
	serverOrImage string,
	rawCACertPath string,
	imageManager images.ImageManager,
	runtimeOverride *templates.RuntimeConfig,
) (string, *types.ImageMetadata, error) {
	var imageMetadata *types.ImageMetadata
	var imageToUse string

	slog.Debug("Detected protocol scheme", "server", serverOrImage)
	// Process the protocol scheme and build the image
	caCertPath := resolveCACertPath(rawCACertPath)
	generatedImage, err := runner.HandleProtocolScheme(ctx, imageManager, serverOrImage, caCertPath, runtimeOverride)
	if err != nil {
		return "", nil, errors.Join(ErrBadProtocolScheme, err)
	}
	// Update the image in the runConfig with the generated image
	slog.Debug("Using built image", "image", generatedImage, "original", serverOrImage)
	imageToUse = generatedImage

	return imageToUse, imageMetadata, nil
}

// handleGroupLookup handles the group lookup case
func handleGroupLookup(
	_ context.Context,
	serverOrImage string,
	groupName string,
) (string, *types.ImageMetadata, types.ServerMetadata, error) {
	var imageMetadata *types.ImageMetadata
	var imageToUse string

	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get registry provider: %w", err)
	}

	reg, err := provider.GetRegistry()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get registry: %w", err)
	}

	group, exists := reg.GetGroupByName(groupName)
	if !exists {
		return "", nil, nil, fmt.Errorf("group '%s' not found in registry", groupName)
	}

	// First check if the server exists and whether it's remote
	var server types.ServerMetadata
	var serverFound bool
	if containerServer, exists := group.Servers[serverOrImage]; exists {
		server = containerServer
		serverFound = true
	} else if remoteServer, exists := group.RemoteServers[serverOrImage]; exists {
		server = remoteServer
		serverFound = true
	}

	if serverFound {
		// Server found, check if it's remote
		if server.IsRemote() {
			return serverOrImage, nil, server, nil
		}
		// It's a container server, get the ImageMetadata
		if imgMetadata, ok := server.(*types.ImageMetadata); ok {
			imageMetadata = imgMetadata
			slog.Debug("Found imageMetadata in group", "server", serverOrImage, "metadata", imageMetadata)
			imageToUse = imageMetadata.Image
		} else {
			// This shouldn't happen since we just found it, but handle it anyway
			slog.Debug("ImageMetadata not found in group: could not cast", "server", serverOrImage)
			imageToUse = serverOrImage
		}
	} else {
		// Server not found in group - fail explicitly
		return "", nil, nil, fmt.Errorf("server '%s' not found in group '%s'", serverOrImage, groupName)
	}

	return imageToUse, imageMetadata, nil, nil
}

// handleRegistryLookup handles the standard registry lookup case
func handleRegistryLookup(
	_ context.Context,
	serverOrImage string,
) (string, *types.ImageMetadata, types.ServerMetadata, error) {
	var imageMetadata *types.ImageMetadata
	var imageToUse string

	// Try to find the server in the registry
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get registry provider: %w", err)
	}

	// First check if the server exists and whether it's remote
	server, err := provider.GetServer(serverOrImage)
	if err == nil {
		// Server found, check if it's remote
		if server.IsRemote() {
			return serverOrImage, nil, server, nil
		}
		// It's a container server, get the ImageMetadata
		imageMetadata, err = provider.GetImageServer(serverOrImage)
		if err != nil {
			// This shouldn't happen since we just found it, but handle it anyway
			slog.Debug("ImageMetadata not found in registry", "server", serverOrImage, "error", err)
			imageToUse = serverOrImage
		} else {
			slog.Debug("Found imageMetadata in registry", "server", serverOrImage, "metadata", imageMetadata)
			imageToUse = imageMetadata.Image
		}
	} else {
		// Server not found in registry, treat as a direct image reference
		slog.Debug("Server not found in registry", "server", serverOrImage, "error", err)
		imageToUse = serverOrImage
	}

	return imageToUse, imageMetadata, nil, nil
}

// pullImage pulls an image from a remote registry if it has the "latest" tag
// or if it doesn't exist locally. If the image is a local image, it will not be pulled.
// If the image has the latest tag, it will be pulled to ensure we have the most recent version.
// however, if there is a failure in pulling the "latest" tag, it will check if the image exists locally
// as it is possible that the image was locally built.
func pullImage(ctx context.Context, image string, imageManager images.ImageManager) error {
	// Check if the image has the "latest" tag
	isLatestTag := hasLatestTag(image)

	if isLatestTag {
		// For "latest" tag, try to pull first
		slog.Debug("Image has 'latest' tag, pulling to ensure we have the most recent version...", "image", image)
		err := imageManager.PullImage(ctx, image)
		if err != nil {
			// Check if the error is due to context cancellation/timeout
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("image pull timed out for %s - the image may be too large or the connection too slow", image)
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				return fmt.Errorf("image pull was canceled for %s", image)
			}

			// Pull failed, check if it exists locally
			slog.Debug("Pull failed, checking if image exists locally", "image", image)
			imageExists, checkErr := imageManager.ImageExists(ctx, image)
			if checkErr != nil {
				return fmt.Errorf("failed to check if image exists: %w", checkErr)
			}

			if imageExists {
				slog.Debug("Using existing local image", "image", image)
			} else {
				return fmt.Errorf("%w: %s", ErrImageNotFound, image)
			}
		} else {
			slog.Debug("Successfully pulled image", "image", image)
		}
	} else {
		// For non-latest tags, check locally first
		slog.Debug("Checking if image exists locally", "image", image)
		imageExists, err := imageManager.ImageExists(ctx, image)
		slog.Debug("ImageExists locally", "exists", imageExists)
		if err != nil {
			return fmt.Errorf("failed to check if image exists locally: %w", err)
		}

		if imageExists {
			slog.Debug("Using existing local image", "image", image)
		} else {
			// Image doesn't exist locally, try to pull
			slog.Info("Image not found locally, pulling...", "image", image)
			if err := imageManager.PullImage(ctx, image); err != nil {
				// Check if the error is due to context cancellation/timeout
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return fmt.Errorf("image pull timed out for %s - the image may be too large or the connection too slow", image)
				}
				if errors.Is(ctx.Err(), context.Canceled) {
					return fmt.Errorf("image pull was canceled for %s", image)
				}
				// TODO: need more fine grained error handling here.
				return fmt.Errorf("%w: %s", ErrImageNotFound, image)
			}
			slog.Debug("Successfully pulled image", "image", image)
		}
	}

	return nil
}

// resolveCACertPath determines the CA certificate path to use, prioritizing command-line flag over configuration
func resolveCACertPath(flagValue string) string {
	// If command-line flag is provided, use it (highest priority)
	if flagValue != "" {
		return flagValue
	}

	// Otherwise, check configuration
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()
	if cfg.CACertificatePath != "" {
		slog.Debug("Using configured CA certificate", "path", cfg.CACertificatePath)
		return cfg.CACertificatePath
	}

	// No CA certificate configured
	return ""
}

// verifyImage verifies the image using the specified verification setting (warn, enabled, or disabled)
func verifyImage(image string, server *types.ImageMetadata, verifySetting string) error {
	switch verifySetting {
	case VerifyImageDisabled:
		slog.Warn("Image verification is disabled")
	case VerifyImageWarn, VerifyImageEnabled:
		// Guard against missing provenance info before calling the verifier.
		if server == nil || server.Provenance == nil {
			if verifySetting == VerifyImageWarn {
				slog.Warn("MCP server has no provenance information set, skipping image verification", "image", image)
				return nil
			}
			return verifier.ErrProvenanceServerInformationNotSet
		}

		// Create a new verifier
		v, err := verifier.New(server.Provenance, images.NewCompositeKeychain())
		if err != nil {
			return err
		}

		// Verify the image passing the provenance info
		if err = v.VerifyServer(image, server.Provenance); err != nil {
			if (errors.Is(err, verifier.ErrImageNotSigned) || errors.Is(err, verifier.ErrProvenanceMismatch)) &&
				verifySetting == VerifyImageWarn {
				slog.Warn("MCP server failed image verification", "image", image, "reason", err)
				return nil
			}
			return fmt.Errorf("image verification failed: %w", err)
		}
		slog.Debug("MCP server is verified successfully", "image", image)
	default:
		return fmt.Errorf("invalid value for --image-verification: %s", verifySetting)
	}
	return nil
}

// hasLatestTag checks if the given image reference has the "latest" tag or no tag (which defaults to "latest")
func hasLatestTag(imageRef string) bool {
	ref, err := nameref.ParseReference(imageRef)
	if err != nil {
		// If we can't parse the reference, assume it's not "latest"
		slog.Warn("failed to parse image reference", "error", err)
		return false
	}

	// Check if the reference is a tag
	if taggedRef, ok := ref.(nameref.Tag); ok {
		// Check if the tag is "latest"
		return taggedRef.TagStr() == "latest"
	}

	// If the reference is not a tag (e.g., it's a digest), it's not "latest"
	// If no tag was specified, it defaults to "latest"
	_, isDigest := ref.(nameref.Digest)
	return !isDigest
}
