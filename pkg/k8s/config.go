// Package k8s provides common Kubernetes utilities for creating clients,
// configs, and namespace detection that can be shared across packages.
package k8s

import (
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// configLoader defines the interface for loading Kubernetes configs
type configLoader interface {
	// InClusterConfig returns the in-cluster config
	InClusterConfig() (*rest.Config, error)
	// LoadFromRules loads config using clientcmd loading rules
	LoadFromRules(rules *clientcmd.ClientConfigLoadingRules) (*rest.Config, error)
}

// defaultConfigLoader implements configLoader using real Kubernetes client-go functions
type defaultConfigLoader struct{}

func (*defaultConfigLoader) InClusterConfig() (*rest.Config, error) {
	return rest.InClusterConfig()
}

func (*defaultConfigLoader) LoadFromRules(rules *clientcmd.ClientConfigLoadingRules) (*rest.Config, error) {
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, configOverrides)
	return kubeConfig.ClientConfig()
}

// GetConfig returns a Kubernetes REST config with the following fallback strategy:
//  1. In-cluster config (when running inside a Kubernetes pod)
//  2. Out-of-cluster config using standard kubeconfig loading rules:
//     a. KUBECONFIG environment variable (colon-separated paths)
//     b. ~/.kube/config file
//
// This order prioritizes in-cluster config for security and reliability when
// running as a pod, while supporting local development when running outside the cluster.
//
// The returned config uses secure defaults:
//   - TLS certificate verification is enabled
//   - In-cluster: Service account CA cert is used automatically
//   - Out-of-cluster: certificate-authority-data from kubeconfig is used
//   - Default QPS: 5 requests/second, Burst: 10 (suitable for most use cases)
//
// For high-volume operations (e.g., operators reconciling many resources),
// consider increasing QPS and Burst limits:
//
//	config, err := k8s.GetConfig()
//	if err != nil {
//	    return err
//	}
//	config.QPS = 50      // Increase from default 5
//	config.Burst = 100   // Increase from default 10
func GetConfig() (*rest.Config, error) {
	return getConfigWithLoader(&defaultConfigLoader{})
}

// getConfigWithLoader is the internal implementation that accepts a configLoader
func getConfigWithLoader(loader configLoader) (*rest.Config, error) {
	// Try in-cluster config first
	config, err := loader.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// If in-cluster config fails, try out-of-cluster config
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err = loader.LoadFromRules(loadingRules)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes config (tried both in-cluster and out-of-cluster): %w", err)
	}

	return config, nil
}

// getConfigFromKubeconfigFile loads config from a specific kubeconfig file path
// This is primarily useful for testing
func getConfigFromKubeconfigFile(kubeconfigPath string) (*rest.Config, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{
		ExplicitPath: kubeconfigPath,
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", kubeconfigPath, err)
	}
	return config, nil
}
