package docker

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/permissions"
)

func TestEgressProxyConfiguration(t *testing.T) {
	// Create a client
	client := &Client{
		egressProxyPort: 3128,
	}

	// Test getEgressProxyEnvironmentVariables
	proxyEnv := getEgressProxyEnvironmentVariables(3128)
	if proxyEnv["HTTP_PROXY"] == "" {
		t.Error("HTTP_PROXY environment variable not set")
	}
	if proxyEnv["HTTPS_PROXY"] == "" {
		t.Error("HTTPS_PROXY environment variable not set")
	}

	// Test shouldUseEgressProxy
	profile := &permissions.Profile{
		Network: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				InsecureAllowAll: true,
			},
		},
	}
	if client.shouldUseEgressProxy(profile) {
		t.Error("shouldUseEgressProxy should return false for InsecureAllowAll")
	}

	profile.Network.Outbound.InsecureAllowAll = false
	profile.Network.Outbound.AllowHost = []string{"example.com"}
	if !client.shouldUseEgressProxy(profile) {
		t.Error("shouldUseEgressProxy should return true for controlled access")
	}

	// Test getNetworkName
	networkName := getNetworkName("test-container")
	if networkName != "thv-test-container" {
		t.Errorf("Expected network name 'thv-test-container', got '%s'", networkName)
	}
}
