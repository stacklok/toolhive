package sources

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// DefaultSourceHandlerFactory is the default implementation of SourceHandlerFactory
type DefaultSourceHandlerFactory struct {
	client client.Client
}

// NewSourceHandlerFactory creates a new source handler factory
func NewSourceHandlerFactory(k8sClient client.Client) SourceHandlerFactory {
	return &DefaultSourceHandlerFactory{
		client: k8sClient,
	}
}

// CreateHandler creates a source handler for the given source type
func (f *DefaultSourceHandlerFactory) CreateHandler(sourceType string) (SourceHandler, error) {
	switch sourceType {
	case mcpv1alpha1.RegistrySourceTypeConfigMap:
		return NewConfigMapSourceHandler(f.client), nil
	case mcpv1alpha1.RegistrySourceTypeGit:
		return NewGitSourceHandler(), nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}
