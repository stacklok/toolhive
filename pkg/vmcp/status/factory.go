package status

import (
	"os"

	"github.com/stacklok/toolhive/pkg/logger"
)

// NewReporter creates Reporter based on runtime environment
func NewReporter(name, namespace string) (Reporter, error) {
	if isKubernetesRuntime() {
		logger.Infof("Detected Kubernetes environment, using K8sReporter")
		return NewK8sReporter(name, namespace)
	}

	logger.Infof("Detected CLI environment, using LogReporter")
	return NewLogReporter(name), nil
}

// isKubernatesRuntime detects if were running inside the Kubernetes cluster
func isKubernetesRuntime() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
