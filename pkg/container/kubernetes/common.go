package kubernetes

import (
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Platform represents the Kubernetes platform type
type Platform int

const (
	// PlatformKubernetes represents standard Kubernetes
	PlatformKubernetes Platform = iota
	// PlatformOpenShift represents OpenShift
	PlatformOpenShift
)

// String returns the string representation of the Platform
func (p Platform) String() string {
	switch p {
	case PlatformKubernetes:
		return "Kubernetes"
	case PlatformOpenShift:
		return "OpenShift"
	default:
		return "Unknown"
	}
}

// PlatformDetector defines the interface for detecting the Kubernetes platform type
type PlatformDetector interface {
	DetectPlatform(config *rest.Config) (Platform, error)
}

// DefaultPlatformDetector implements PlatformDetector using the existing OpenShift detection logic
type DefaultPlatformDetector struct {
	once     sync.Once
	platform Platform
	err      error
}

// extra kinds
const (
	// defaultRetries is the number of times a resource discovery is retried
	defaultRetries = 10

	// defaultRetryInterval is the maximum interval between retries for resource discovery (used as cap in exponential backoff)
	defaultRetryInterval = 3 * time.Second
)

// DetectPlatform implements the PlatformDetector interface
func (d *DefaultPlatformDetector) DetectPlatform(config *rest.Config) (Platform, error) {
	d.once.Do(func() {
		// Check if we are running on OpenShift via environment variable override
		value, ok := os.LookupEnv("OPERATOR_OPENSHIFT")
		if ok {
			logger.Infof("OpenShift set by env var 'OPERATOR_OPENSHIFT': " + value)
			if strings.ToLower(value) == "true" {
				d.platform = PlatformOpenShift
			} else {
				d.platform = PlatformKubernetes
			}
			return
		}

		// Check for OpenShift by attempting to discover the Route resource
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
		if err != nil {
			d.err = err
			return
		}

		var isOpenShiftResourcePresent bool
		err = wait.ExponentialBackoff(wait.Backoff{
			Duration: time.Second,          // Initial delay
			Factor:   2.0,                  // Backoff factor
			Jitter:   0.1,                  // Add some randomness
			Steps:    defaultRetries,       // Maximum number of retries
			Cap:      defaultRetryInterval, // Maximum delay between retries
		}, func() (bool, error) {
			isOpenShiftResourcePresent, err = discovery.IsResourceEnabled(discoveryClient,
				schema.GroupVersionResource{
					Group:    "route.openshift.io",
					Version:  "v1",
					Resource: "routes",
				})

			if err != nil {
				// Return false to continue retrying, don't return the error yet
				return false, nil
			}

			// Success - stop retrying
			return true, nil
		})

		if err != nil {
			d.err = err
			return
		}

		if isOpenShiftResourcePresent {
			logger.Infof("OpenShift detected by route resource check.")
			d.platform = PlatformOpenShift
		} else {
			d.platform = PlatformKubernetes
		}
	})

	return d.platform, d.err
}

// NewDefaultPlatformDetector creates a new DefaultPlatformDetector
func NewDefaultPlatformDetector() PlatformDetector {
	return &DefaultPlatformDetector{}
}
