// Package oci provides general OCI registry operations and utilities
package oci

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/stacklok/toolhive/pkg/container/images"
)

// Client provides general OCI registry operations
type Client struct {
	imageManager images.ImageManager
}

// NewClient creates a new OCI client
func NewClient(imageManager images.ImageManager) *Client {
	return &Client{
		imageManager: imageManager,
	}
}

// CreateRepository creates a repository client with authentication for the given reference
func (*Client) CreateRepository(ref string) (*remote.Repository, error) {
	// Parse the reference using go-containerregistry for proper validation
	parsedRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid reference format: %s", ref)
	}

	// Extract the repository path (without tag/digest)
	repoPath := parsedRef.Context().String()

	repo, err := remote.NewRepository(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	// Set up authentication using the existing keychain infrastructure
	keychain := images.NewCompositeKeychain()

	repo.Client = &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.NewCache(),
		Credential: func(_ context.Context, registry string) (auth.Credential, error) {
			// Try to get credentials from the keychain
			target := &registryTarget{registry: registry}
			authenticator, err := keychain.Resolve(target)
			if err != nil {
				return auth.EmptyCredential, nil
			}

			// Convert to ORAS credential format
			authConfig, err := authenticator.Authorization()
			if err != nil {
				return auth.EmptyCredential, nil
			}

			if authConfig != nil && authConfig.Username != "" {
				return auth.Credential{
					Username: authConfig.Username,
					Password: authConfig.Password,
				}, nil
			}

			return auth.EmptyCredential, nil
		},
	}

	return repo, nil
}

// registryTarget implements authn.Resource for keychain resolution
type registryTarget struct {
	registry string
}

func (r *registryTarget) String() string {
	return r.registry
}

func (r *registryTarget) RegistryStr() string {
	return r.registry
}
