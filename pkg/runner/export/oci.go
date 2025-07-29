// Package export provides functionality for exporting runtime configurations to various formats
package export

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	oras "oras.land/oras-go/v2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/oci"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// MediaTypeRunConfig is the media type for runtime configuration artifacts
	MediaTypeRunConfig = "application/vnd.toolhive.runconfig.v1+json"

	// ArtifactTypeRunConfig is the artifact type for runtime configurations
	ArtifactTypeRunConfig = "application/vnd.toolhive.runconfig"

	// AnnotationCreatedBy identifies the creator of the artifact
	AnnotationCreatedBy = "org.opencontainers.image.created.by"

	// AnnotationDescription provides a description of the artifact
	AnnotationDescription = "org.opencontainers.image.description"

	// AnnotationVersion provides version information
	AnnotationVersion = "org.opencontainers.image.version"
)

// OCIExporter handles exporting runtime configurations as OCI artifacts
type OCIExporter struct {
	ociClient *oci.Client
}

// NewOCIExporter creates a new OCI exporter
func NewOCIExporter(ociClient *oci.Client) *OCIExporter {
	return &OCIExporter{
		ociClient: ociClient,
	}
}

// PushRunConfig pushes a runtime configuration as an OCI artifact to a registry
func (e *OCIExporter) PushRunConfig(ctx context.Context, config *runner.RunConfig, ref string) error {
	logger.Infof("Pushing runtime configuration to %s", ref)

	// Create repository
	repo, err := e.ociClient.CreateRepository(ref)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	// Serialize the configuration to JSON
	configData, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal configuration: %w", err)
	}

	// Push the configuration as a blob
	configDescriptor, err := oras.PushBytes(ctx, repo, MediaTypeRunConfig, configData)
	if err != nil {
		return fmt.Errorf("failed to push configuration blob: %w", err)
	}

	// Create manifest with annotations
	annotations := map[string]string{
		AnnotationCreatedBy:   "toolhive",
		AnnotationDescription: fmt.Sprintf("Runtime configuration for %s", config.Name),
		AnnotationVersion:     "1.0",
		v1.AnnotationCreated:  oci.GetCurrentTimestamp(),
	}

	// Pack the manifest
	packOpts := oras.PackManifestOptions{
		Layers:              []v1.Descriptor{configDescriptor},
		ManifestAnnotations: annotations,
	}

	manifestDescriptor, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, ArtifactTypeRunConfig, packOpts)
	if err != nil {
		return fmt.Errorf("failed to pack manifest: %w", err)
	}

	// Tag the manifest
	tag := oci.ExtractTag(ref)
	if tag != "" {
		err = repo.Tag(ctx, manifestDescriptor, tag)
		if err != nil {
			return fmt.Errorf("failed to tag manifest: %w", err)
		}
	}

	logger.Infof("Successfully pushed runtime configuration with digest: %s", manifestDescriptor.Digest)
	return nil
}

// PullRunConfig pulls a runtime configuration from an OCI registry
func (e *OCIExporter) PullRunConfig(ctx context.Context, ref string) (*runner.RunConfig, error) {
	logger.Infof("Pulling runtime configuration from %s", ref)

	// Create repository
	repo, err := e.ociClient.CreateRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	// Resolve the reference to get the manifest descriptor
	tag := oci.ExtractTag(ref)
	if tag == "" {
		tag = "latest"
	}

	manifestDescriptor, err := repo.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
	}

	// Fetch the manifest
	manifestReader, err := repo.Fetch(ctx, manifestDescriptor)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer manifestReader.Close()

	// Parse the manifest to get layer descriptors
	manifestData, err := io.ReadAll(manifestReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest v1.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	// Find the configuration layer
	var configDescriptor *v1.Descriptor
	for _, layer := range manifest.Layers {
		if layer.MediaType == MediaTypeRunConfig {
			configDescriptor = &layer
			break
		}
	}

	if configDescriptor == nil {
		return nil, fmt.Errorf("no runtime configuration found in artifact")
	}

	// Fetch the configuration blob
	configReader, err := repo.Fetch(ctx, *configDescriptor)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch configuration blob: %w", err)
	}
	defer configReader.Close()

	// Parse the configuration
	configData, err := io.ReadAll(configReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read configuration data: %w", err)
	}

	var config runner.RunConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	logger.Infof("Successfully pulled runtime configuration for %s", config.Name)
	return &config, nil
}
