package k8s

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

const (
	// defaultNamespace is the default Kubernetes namespace
	defaultNamespace = "default"
	// defaultServiceAccountPath is the default path to the service account namespace file
	defaultServiceAccountPath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	// defaultPodNamespaceEnv is the default environment variable for POD_NAMESPACE
	defaultPodNamespaceEnv = "POD_NAMESPACE"
)

// GetCurrentNamespace attempts to determine the current Kubernetes namespace
// using multiple methods, falling back to "default" if none succeed.
func GetCurrentNamespace() string {
	// Method 1: Try to read from the service account namespace file
	if ns, err := getNamespaceFromServiceAccountPath(defaultServiceAccountPath); err == nil {
		return ns
	}

	// Method 2: Try to get the namespace from environment variables
	if ns, err := getNamespaceFromEnvVar(defaultPodNamespaceEnv); err == nil {
		return ns
	}

	// Method 3: Try to get the namespace from the current kubectl context
	if ns, err := getNamespaceFromKubeConfig(); err == nil {
		return ns
	}

	// Method 4: Fall back to default
	return defaultNamespace
}

// getNamespaceFromServiceAccountPath attempts to read the namespace from a service account token file
// This is a thin I/O wrapper - the logic is in parseNamespaceFromFile
func getNamespaceFromServiceAccountPath(path string) (string, error) {
	//nolint:gosec // G304: Reading from configurable path is intentional for testing
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read namespace file: %w", err)
	}
	return parseNamespaceFromFile(data)
}

// parseNamespaceFromFile parses namespace from file data
// This is pure logic, fully testable without I/O
func parseNamespaceFromFile(data []byte) (string, error) {
	// Kubernetes writes the namespace file without trailing newlines, but we trim
	// them for robustness in case the file was manually edited or created incorrectly.
	// We only trim newlines (not all whitespace) to be conservative.
	ns := strings.TrimRight(string(data), "\n\r")
	if ns == "" {
		return "", fmt.Errorf("namespace file is empty")
	}
	return ns, nil
}

// getNamespaceFromEnvVar attempts to get the namespace from a specific environment variable
// This is a thin I/O wrapper - the logic is in validateNamespaceValue
func getNamespaceFromEnvVar(envVar string) (string, error) {
	return validateNamespaceValue(os.Getenv(envVar), envVar)
}

// validateNamespaceValue validates a namespace value from an environment variable
// This is pure logic, fully testable without environment access
func validateNamespaceValue(ns, source string) (string, error) {
	if ns == "" {
		return "", fmt.Errorf("%s environment variable not set", source)
	}
	return ns, nil
}

// getNamespaceFromKubeConfig attempts to get the namespace from the current kubectl context
func getNamespaceFromKubeConfig() (string, error) {
	kubeConfig := loadKubeconfigRaw()
	return extractNamespaceFromKubeconfig(kubeConfig)
}

// loadKubeconfigRaw loads the raw kubeconfig
// This is a thin I/O wrapper
func loadKubeconfigRaw() clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
}

// extractNamespaceFromKubeconfig extracts namespace from kubeconfig
// This is pure logic, testable with mock configs
func extractNamespaceFromKubeconfig(kubeConfig clientcmd.ClientConfig) (string, error) {
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return "", fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	currentContext := rawConfig.CurrentContext
	if currentContext == "" {
		return "", fmt.Errorf("no current context set in kubeconfig")
	}

	contextConfig, exists := rawConfig.Contexts[currentContext]
	if !exists {
		return "", fmt.Errorf("current context %q not found in kubeconfig", currentContext)
	}

	ns := strings.TrimSpace(contextConfig.Namespace)
	if ns == "" {
		return "", fmt.Errorf("no namespace set in current context %q", currentContext)
	}

	return ns, nil
}
