package kubernetes

import (
	"fmt"
	"os"

	"k8s.io/client-go/tools/clientcmd"
)

// GetCurrentNamespace attempts to determine the current Kubernetes namespace
// using multiple methods, falling back to "default" if none succeed.
func GetCurrentNamespace() string {
	// Method 1: Try to read from the service account namespace file
	ns, err := getNamespaceFromServiceAccount()
	if err == nil {
		return ns
	}

	// Method 2: Try to get the namespace from environment variables
	ns, err = getNamespaceFromEnv()
	if err == nil {
		return ns
	}

	// Method 3: Try to get the namespace from the current kubectl context
	ns, err = getNamespaceFromKubeConfig()
	if err == nil {
		return ns
	}

	// Method 4: Fall back to default
	return defaultNamespace
}

// getNamespaceFromServiceAccount attempts to read the namespace from the service account token file
func getNamespaceFromServiceAccount() (string, error) {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("failed to read namespace file: %w", err)
	}
	return string(data), nil
}

// getNamespaceFromEnv attempts to get the namespace from environment variables
func getNamespaceFromEnv() (string, error) {
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		return "", fmt.Errorf("POD_NAMESPACE environment variable not set")
	}
	return ns, nil
}

// getNamespaceFromKubeConfig attempts to get the namespace from the current kubectl context
func getNamespaceFromKubeConfig() (string, error) {
	// Use the same loading rules as the main client
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Get the raw config to access the current context
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return "", fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Get the current context
	currentContext := rawConfig.CurrentContext
	if currentContext == "" {
		return "", fmt.Errorf("no current context set in kubeconfig")
	}

	// Get the context details
	contextConfig, exists := rawConfig.Contexts[currentContext]
	if !exists {
		return "", fmt.Errorf("current context %q not found in kubeconfig", currentContext)
	}

	// Return the namespace from the context, or empty string if not set
	if contextConfig.Namespace == "" {
		return "", fmt.Errorf("no namespace set in current context %q", currentContext)
	}

	return contextConfig.Namespace, nil
}
