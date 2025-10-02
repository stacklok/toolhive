package token

import (
	"context"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Introspector defines the interface for token introspection providers
type Introspector interface {
	// Name returns the provider name
	Name() string

	// CanHandle returns true if this provider can handle the given introspection URL
	CanHandle(introspectURL string) bool

	// IntrospectToken introspects an opaque token and returns JWT claims
	IntrospectToken(ctx context.Context, token string) (jwt.MapClaims, error)
}

// IntrospectorRegistry maintains a list of available token introspection providers
type IntrospectorRegistry struct {
	introspectors []Introspector
}

// NewIntrospectorRegistry creates a new provider registry
func NewIntrospectorRegistry() *IntrospectorRegistry {
	return &IntrospectorRegistry{
		introspectors: []Introspector{},
	}
}

// GetIntrospector returns the appropriate provider for the given introspection URL
func (r *IntrospectorRegistry) GetIntrospector(introspectURL string) Introspector {
	for _, introspector := range r.introspectors {
		if introspector.CanHandle(introspectURL) {
			logger.Debugf("Selected introspector: %s (url: %s)", introspector.Name(), introspectURL)
			return introspector
		}
	}
	// Return nil if no provider found - the caller should handle this
	logger.Debugf("No introspector found for URL: %s", introspectURL)
	return nil
}

// AddIntrospector adds a new provider to the registry
func (r *IntrospectorRegistry) AddIntrospector(introspector Introspector) {
	r.introspectors = append(r.introspectors, introspector)
}
