package runner

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/auth/discovery"
)

const (
	dynamicTestType = "dynamic"
)

// testServerSetup holds the mock servers for a test
type testServerSetup struct {
	MetadataServer *httptest.Server
	AuthServer     *httptest.Server
	InvalidServer  *httptest.Server
	Servers        map[string]*httptest.Server
}

// cleanup closes all servers
func (s *testServerSetup) cleanup() {
	if s.MetadataServer != nil {
		s.MetadataServer.Close()
	}
	if s.AuthServer != nil {
		s.AuthServer.Close()
	}
	if s.InvalidServer != nil {
		s.InvalidServer.Close()
	}
	for _, server := range s.Servers {
		if server != nil {
			server.Close()
		}
	}
}

// setupResourceMetadataTest creates linked mock servers for resource metadata testing
func setupResourceMetadataTest(t *testing.T, testType string) (*testServerSetup, *discovery.AuthInfo, string) {
	t.Helper()
	setup := &testServerSetup{
		Servers: make(map[string]*httptest.Server),
	}

	// Create auth server
	setup.AuthServer = createMockAuthServer(t, "")

	var authServers []string
	var scopes []string

	switch testType {
	case "multi-server":
		// Create invalid server for multi-server test
		setup.InvalidServer = createMock404Server(t)
		authServers = []string{setup.InvalidServer.URL, setup.AuthServer.URL}
	case "with-scopes":
		authServers = []string{setup.AuthServer.URL}
		scopes = []string{"resource", "scopes"}
	default:
		authServers = []string{setup.AuthServer.URL}
	}

	// Create metadata server with proper auth server URLs
	if len(scopes) > 0 {
		setup.MetadataServer = createMockResourceMetadataServerWithScopes(t, authServers, scopes)
	} else {
		setup.MetadataServer = createMockResourceMetadataServer(t, authServers)
	}

	// Create auth info with actual metadata URL
	authInfo := &discovery.AuthInfo{
		Type:             "OAuth",
		ResourceMetadata: setup.MetadataServer.URL + resourceMetadataPath,
	}

	// Return the expected issuer (auth server URL)
	return setup, authInfo, setup.AuthServer.URL
}

// processTestServers handles the server setup for a test case
func processTestServers(t *testing.T, tt *testCase) (*testServerSetup, *discovery.AuthInfo, string, string) {
	t.Helper()
	// Handle special dynamic test cases
	if tt.authInfo != nil && tt.authInfo.ResourceMetadata != "" {
		switch tt.authInfo.ResourceMetadata {
		case dynamicTestType:
			setup, authInfo, expectedIssuer := setupResourceMetadataTest(t, "single-server")
			if tt.expectedIssuer == dynamicTestType {
				tt.expectedIssuer = expectedIssuer
			}
			return setup, authInfo, tt.remoteURL, tt.expectedIssuer

		case "dynamic-multi":
			setup, authInfo, expectedIssuer := setupResourceMetadataTest(t, "multi-server")
			if tt.expectedIssuer == dynamicTestType {
				tt.expectedIssuer = expectedIssuer
			}
			return setup, authInfo, tt.remoteURL, tt.expectedIssuer

		case "dynamic-scopes":
			setup, authInfo, expectedIssuer := setupResourceMetadataTest(t, "with-scopes")
			if tt.expectedIssuer == dynamicTestType {
				tt.expectedIssuer = expectedIssuer
			}
			return setup, authInfo, tt.remoteURL, tt.expectedIssuer
		}
	}

	// Handle regular mock servers
	setup := &testServerSetup{
		Servers: make(map[string]*httptest.Server),
	}

	authInfo := tt.authInfo
	remoteURL := tt.remoteURL

	// Set up mock servers from test definition
	for host, server := range tt.mockServers {
		if host == "localhost" && server == nil {
			if containsAny(tt.name, "404", "all discovery methods fail") {
				server = createMock404Server(t)
			} else {
				server = createMockAuthServer(t, "")
			}
		}
		setup.Servers[host] = server
	}

	// Process URLs
	if len(setup.Servers) > 0 {
		remoteURL, tt.expectedIssuer = processURLsForServers(tt, authInfo, remoteURL, setup.Servers)
	}

	return setup, authInfo, remoteURL, tt.expectedIssuer
}

// processURLsForServers updates URLs to use mock server addresses
func processURLsForServers(tt *testCase, authInfo *discovery.AuthInfo, remoteURL string, servers map[string]*httptest.Server) (string, string) {
	expectedIssuer := tt.expectedIssuer

	// For resource metadata tests
	if authInfo != nil && authInfo.ResourceMetadata != "" && !containsAny(authInfo.ResourceMetadata, "dynamic") {
		for host, server := range servers {
			if containsAny(authInfo.ResourceMetadata, host) {
				authInfo.ResourceMetadata = replaceFirst(authInfo.ResourceMetadata, "https://"+host, server.URL)
				break
			}
		}
	}

	// For well-known discovery tests
	if remoteURL == "" && servers["localhost"] != nil {
		remoteURL = servers["localhost"].URL
		if expectedIssuer == "" {
			if containsAny(tt.name, "malformed resource metadata") {
				expectedIssuer = servers["localhost"].URL
			} else if containsAny(tt.name, "fallback", "all discovery") {
				expectedIssuer = servers["localhost"].URL
			}
		}
	} else {
		for host, server := range servers {
			if containsAny(remoteURL, host) {
				remoteURL = replaceFirst(remoteURL, "https://"+host, server.URL)
				break
			}
		}
	}

	return remoteURL, expectedIssuer
}

// Helper functions
func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func replaceFirst(s, old, replacement string) string {
	return strings.Replace(s, old, replacement, 1)
}

// testCase represents a single test case
type testCase struct {
	name               string
	config             *RemoteAuthConfig
	authInfo           *discovery.AuthInfo
	remoteURL          string
	mockServers        map[string]*httptest.Server
	expectedIssuer     string
	expectedScopes     []string
	expectedAuthServer bool
	expectError        bool
	errorContains      string
}
