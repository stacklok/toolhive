package kubernetes

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/logger"
)

// RunConfigMapReader defines the interface for reading RunConfig from ConfigMaps
// This interface allows for easy mocking in tests
//
//go:generate mockgen -destination=mocks/mock_configmap.go -package=mocks -source=configmap.go RunConfigMapReader
type RunConfigMapReader interface {
	// GetRunConfigMap retrieves the runconfig.json from a ConfigMap
	// configMapRef should be in the format "namespace/configmap-name"
	// Returns the runconfig.json content as a string
	GetRunConfigMap(ctx context.Context, configMapRef string) (string, error)
}

// ConfigMapReader implements RunConfigMapReader using real Kubernetes API
type ConfigMapReader struct {
	clientset kubernetes.Interface
}

// NewConfigMapReaderWithClient creates a new ConfigMapReader with the provided clientset
// This is useful for testing with a mock clientset
func NewConfigMapReaderWithClient(clientset kubernetes.Interface) *ConfigMapReader {
	return &ConfigMapReader{
		clientset: clientset,
	}
}

// NewConfigMapReader creates a new ConfigMapReader using in-cluster configuration
// This is the standard way to create a reader in production
// Note: This function is not unit tested as it requires a real Kubernetes cluster.
// The business logic is tested via NewConfigMapReaderWithClient with mock clients.
func NewConfigMapReader() (*ConfigMapReader, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return NewConfigMapReaderWithClient(clientset), nil
}

// GetRunConfigMap retrieves the runconfig.json from a ConfigMap
func (c *ConfigMapReader) GetRunConfigMap(ctx context.Context, configMapRef string) (string, error) {
	// Parse the ConfigMap reference
	namespace, name, err := parseConfigMapRef(configMapRef)
	if err != nil {
		return "", fmt.Errorf("invalid configmap reference: %w", err)
	}

	logger.Infof("Loading runconfig.json from ConfigMap '%s/%s'", namespace, name)

	// Get the ConfigMap
	configMap, err := c.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get ConfigMap '%s/%s': %w", namespace, name, err)
	}

	// Get the runconfig.json data
	data, ok := configMap.Data["runconfig.json"]
	if !ok {
		return "", fmt.Errorf("ConfigMap '%s/%s' does not contain 'runconfig.json' key", namespace, name)
	}

	logger.Infof("Successfully loaded %d bytes of runconfig.json from ConfigMap '%s/%s'",
		len(data), namespace, name)

	return data, nil
}

// parseConfigMapRef parses a ConfigMap reference in the format "namespace/configmap-name"
func parseConfigMapRef(ref string) (namespace, name string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected format 'namespace/configmap-name', got '%s'", ref)
	}

	namespace = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])

	if namespace == "" {
		return "", "", fmt.Errorf("namespace cannot be empty")
	}
	if name == "" {
		return "", "", fmt.Errorf("configmap name cannot be empty")
	}

	return namespace, name, nil
}
