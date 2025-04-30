package server

import (
	"context"

	"github.com/StacklokLabs/toolhive/pkg/api"
)

// Version is the implementation of the api.VersionAPI interface.
type Version struct {
	// debug indicates whether debug mode is enabled
	debug bool
}

// NewVersion creates a new VersionAPI with the provided debug flag.
func NewVersion(debug bool) api.VersionAPI {
	return &Version{
		debug: debug,
	}
}

// Get returns version information.
func (*Version) Get(_ context.Context, _ *api.VersionOptions) (string, error) {
	// Implementation would go here
	return "", nil
}
