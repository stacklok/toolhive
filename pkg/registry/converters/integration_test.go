package converters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	upstream "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stretchr/testify/require"
)

// ToolHiveRegistry represents the structure of registry.json
type ToolHiveRegistry struct {
	Servers       map[string]json.RawMessage `json:"servers"`
	RemoteServers map[string]json.RawMessage `json:"remote_servers"`
}

// parseServerEntry parses a server entry as either ImageMetadata or RemoteServerMetadata
func parseServerEntry(data json.RawMessage) (imageMetadata *registry.ImageMetadata, remoteMetadata *registry.RemoteServerMetadata, err error) {
	// Try to detect type by checking for "image" vs "url" field
	var typeCheck map[string]interface{}
	if err := json.Unmarshal(data, &typeCheck); err != nil {
		return nil, nil, err
	}

	if _, hasImage := typeCheck["image"]; hasImage {
		// It's an ImageMetadata
		var img registry.ImageMetadata
		if err := json.Unmarshal(data, &img); err != nil {
			return nil, nil, err
		}
		return &img, nil, nil
	} else if _, hasURL := typeCheck["url"]; hasURL {
		// It's a RemoteServerMetadata
		var remote registry.RemoteServerMetadata
		if err := json.Unmarshal(data, &remote); err != nil {
			return nil, nil, err
		}
		return nil, &remote, nil
	}

	return nil, nil, fmt.Errorf("unable to determine server type")
}

// OfficialRegistry represents the structure of official-registry.json
type OfficialRegistry struct {
	Data struct {
		Servers []upstream.ServerJSON `json:"servers"`
	} `json:"data"`
}

// TestRoundTrip_RealRegistryData tests that we can convert the official registry back to toolhive format
// and that it matches the original registry.json
// Note: This is an integration test that reads from build/ directory, so we don't run it in parallel
//
//nolint:paralleltest // Integration test reads from shared build/ directory
func TestRoundTrip_RealRegistryData(t *testing.T) {
	// Skip if running in CI or if files don't exist
	officialPath := filepath.Join("..", "..", "..", "build", "official-registry.json")
	toolhivePath := filepath.Join("..", "..", "..", "build", "registry.json")

	if _, err := os.Stat(officialPath); os.IsNotExist(err) {
		t.Skip("Skipping integration test: official-registry.json not found")
	}
	if _, err := os.Stat(toolhivePath); os.IsNotExist(err) {
		t.Skip("Skipping integration test: registry.json not found")
	}

	// Read official registry
	officialData, err := os.ReadFile(officialPath)
	require.NoError(t, err, "Failed to read official-registry.json")

	var officialRegistry OfficialRegistry
	err = json.Unmarshal(officialData, &officialRegistry)
	require.NoError(t, err, "Failed to parse official-registry.json")

	// Read toolhive registry
	toolhiveData, err := os.ReadFile(toolhivePath)
	require.NoError(t, err, "Failed to read registry.json")

	var toolhiveRegistry ToolHiveRegistry
	err = json.Unmarshal(toolhiveData, &toolhiveRegistry)
	require.NoError(t, err, "Failed to parse registry.json")

	t.Logf("Loaded %d servers from official registry", len(officialRegistry.Data.Servers))
	t.Logf("Loaded %d image servers and %d remote servers from toolhive registry",
		len(toolhiveRegistry.Servers), len(toolhiveRegistry.RemoteServers))

	// Track statistics
	stats := struct {
		total            int
		imageServers     int
		remoteServers    int
		conversionErrors int
		mismatches       []string
	}{}

	// For each server in official registry, convert back and compare
	for _, serverJSON := range officialRegistry.Data.Servers {
		stats.total++

		// Extract simple name from reverse DNS format
		simpleName := ExtractServerName(serverJSON.Name)

		t.Run(simpleName, func(t *testing.T) {
			// Find corresponding entry in toolhive registry (check both servers and remote_servers)
			var originalData json.RawMessage
			var exists bool

			// Try servers first
			originalData, exists = toolhiveRegistry.Servers[simpleName]
			if !exists {
				// Try remote_servers
				originalData, exists = toolhiveRegistry.RemoteServers[simpleName]
				if !exists {
					t.Logf("⚠️  Server '%s' not found in toolhive registry (checked both servers and remote_servers)", simpleName)
					return
				}
			}

			// Parse the original entry
			originalImage, originalRemote, err := parseServerEntry(originalData)
			if err != nil {
				t.Errorf("❌ Failed to parse original entry: %v", err)
				return
			}

			// Determine if it's an image or remote server from official registry
			isImage := len(serverJSON.Packages) > 0
			isRemote := len(serverJSON.Remotes) > 0

			if isImage {
				stats.imageServers++
				testImageServerRoundTrip(t, simpleName, &serverJSON, originalImage, &stats)
			} else if isRemote {
				stats.remoteServers++
				testRemoteServerRoundTrip(t, simpleName, &serverJSON, originalRemote, &stats)
			} else {
				t.Errorf("❌ Server '%s' has neither packages nor remotes", simpleName)
			}
		})
	}

	// Print summary
	separator := strings.Repeat("=", 80)
	t.Logf("\n%s", separator)
	t.Logf("INTEGRATION TEST SUMMARY")
	t.Logf("%s", separator)
	t.Logf("Total servers: %d", stats.total)
	t.Logf("  - Image servers: %d", stats.imageServers)
	t.Logf("  - Remote servers: %d", stats.remoteServers)
	t.Logf("Conversion errors: %d", stats.conversionErrors)
	t.Logf("Field mismatches: %d", len(stats.mismatches))

	if len(stats.mismatches) > 0 {
		t.Logf("\nMismatched fields:")
		for _, mismatch := range stats.mismatches {
			t.Logf("  - %s", mismatch)
		}
	}

	if stats.conversionErrors == 0 && len(stats.mismatches) == 0 {
		t.Logf("\n✅ All servers converted successfully with no mismatches!")
	}
	t.Logf("%s", separator)
}

func testImageServerRoundTrip(t *testing.T, name string, serverJSON *upstream.ServerJSON, original *registry.ImageMetadata, stats *struct {
	total            int
	imageServers     int
	remoteServers    int
	conversionErrors int
	mismatches       []string
}) {
	t.Helper()
	if original == nil {
		t.Errorf("❌ Original ImageMetadata is nil for '%s'", name)
		return
	}

	// Convert ServerJSON back to ImageMetadata
	converted, err := ServerJSONToImageMetadata(serverJSON)
	if err != nil {
		t.Errorf("❌ Conversion failed: %v", err)
		stats.conversionErrors++
		return
	}

	// Compare fields
	compareImageMetadata(t, name, original, converted, stats)
}

func testRemoteServerRoundTrip(t *testing.T, name string, serverJSON *upstream.ServerJSON, original *registry.RemoteServerMetadata, stats *struct {
	total            int
	imageServers     int
	remoteServers    int
	conversionErrors int
	mismatches       []string
}) {
	t.Helper()
	if original == nil {
		t.Errorf("❌ Original RemoteServerMetadata is nil for '%s'", name)
		return
	}

	// Convert ServerJSON back to RemoteServerMetadata
	converted, err := ServerJSONToRemoteServerMetadata(serverJSON)
	if err != nil {
		t.Errorf("❌ Conversion failed: %v", err)
		stats.conversionErrors++
		return
	}

	// Compare fields
	compareRemoteServerMetadata(t, name, original, converted, stats)
}

func compareImageMetadata(t *testing.T, name string, original, converted *registry.ImageMetadata, stats *struct {
	total            int
	imageServers     int
	remoteServers    int
	conversionErrors int
	mismatches       []string
}) {
	t.Helper()
	// Compare basic fields
	if original.Image != converted.Image {
		recordMismatch(t, stats, name, "Image", original.Image, converted.Image)
	}
	if original.Description != converted.Description {
		recordMismatch(t, stats, name, "Description", original.Description, converted.Description)
	}
	if original.Transport != converted.Transport {
		recordMismatch(t, stats, name, "Transport", original.Transport, converted.Transport)
	}
	if original.RepositoryURL != converted.RepositoryURL {
		recordMismatch(t, stats, name, "RepositoryURL", original.RepositoryURL, converted.RepositoryURL)
	}
	if original.Status != converted.Status {
		recordMismatch(t, stats, name, "Status", original.Status, converted.Status)
	}
	if original.Tier != converted.Tier {
		recordMismatch(t, stats, name, "Tier", original.Tier, converted.Tier)
	}
	if original.TargetPort != converted.TargetPort {
		recordMismatch(t, stats, name, "TargetPort", original.TargetPort, converted.TargetPort)
	}

	// Compare slices
	if !stringSlicesEqual(original.Tools, converted.Tools) {
		recordMismatch(t, stats, name, "Tools", original.Tools, converted.Tools)
	}
	if !stringSlicesEqual(original.Tags, converted.Tags) {
		recordMismatch(t, stats, name, "Tags", original.Tags, converted.Tags)
	}

	// Compare EnvVars
	if len(original.EnvVars) != len(converted.EnvVars) {
		recordMismatch(t, stats, name, "EnvVars.length", len(original.EnvVars), len(converted.EnvVars))
	} else {
		for i := range original.EnvVars {
			if !envVarsEqual(original.EnvVars[i], converted.EnvVars[i]) {
				recordMismatch(t, stats, name, fmt.Sprintf("EnvVars[%d]", i), original.EnvVars[i], converted.EnvVars[i])
			}
		}
	}

	// Compare Metadata
	if !metadataEqual(original.Metadata, converted.Metadata) {
		recordMismatch(t, stats, name, "Metadata", original.Metadata, converted.Metadata)
	}

	// Note: Permissions, Provenance, Args are in extensions and may not round-trip perfectly
	// We'll log these separately if they differ
	if original.Permissions != nil && converted.Permissions == nil {
		t.Logf("⚠️  '%s': Permissions not preserved in round-trip", name)
	}
	if original.Provenance != nil && converted.Provenance == nil {
		t.Logf("⚠️  '%s': Provenance not preserved in round-trip", name)
	}
	if len(original.Args) > 0 && len(converted.Args) == 0 {
		t.Logf("⚠️  '%s': Args not preserved in round-trip", name)
	}
}

func compareRemoteServerMetadata(t *testing.T, name string, original, converted *registry.RemoteServerMetadata, stats *struct {
	total            int
	imageServers     int
	remoteServers    int
	conversionErrors int
	mismatches       []string
}) {
	t.Helper()
	// Compare basic fields
	if original.URL != converted.URL {
		recordMismatch(t, stats, name, "URL", original.URL, converted.URL)
	}
	if original.Description != converted.Description {
		recordMismatch(t, stats, name, "Description", original.Description, converted.Description)
	}
	if original.Transport != converted.Transport {
		recordMismatch(t, stats, name, "Transport", original.Transport, converted.Transport)
	}
	if original.RepositoryURL != converted.RepositoryURL {
		recordMismatch(t, stats, name, "RepositoryURL", original.RepositoryURL, converted.RepositoryURL)
	}
	if original.Status != converted.Status {
		recordMismatch(t, stats, name, "Status", original.Status, converted.Status)
	}
	if original.Tier != converted.Tier {
		recordMismatch(t, stats, name, "Tier", original.Tier, converted.Tier)
	}

	// Compare slices
	if !stringSlicesEqual(original.Tools, converted.Tools) {
		recordMismatch(t, stats, name, "Tools", original.Tools, converted.Tools)
	}
	if !stringSlicesEqual(original.Tags, converted.Tags) {
		recordMismatch(t, stats, name, "Tags", original.Tags, converted.Tags)
	}

	// Compare Headers
	if len(original.Headers) != len(converted.Headers) {
		recordMismatch(t, stats, name, "Headers.length", len(original.Headers), len(converted.Headers))
	} else {
		for i := range original.Headers {
			if !headersEqual(original.Headers[i], converted.Headers[i]) {
				recordMismatch(t, stats, name, fmt.Sprintf("Headers[%d]", i), original.Headers[i], converted.Headers[i])
			}
		}
	}

	// Compare Metadata
	if !metadataEqual(original.Metadata, converted.Metadata) {
		recordMismatch(t, stats, name, "Metadata", original.Metadata, converted.Metadata)
	}

	// Note: OAuthConfig may not round-trip perfectly
	if original.OAuthConfig != nil && converted.OAuthConfig == nil {
		t.Logf("⚠️  '%s': OAuthConfig not preserved in round-trip", name)
	}
}

func recordMismatch(t *testing.T, stats *struct {
	total            int
	imageServers     int
	remoteServers    int
	conversionErrors int
	mismatches       []string
}, serverName, field string, original, converted interface{}) {
	t.Helper()
	msg := fmt.Sprintf("%s.%s: expected %v, got %v", serverName, field, original, converted)
	stats.mismatches = append(stats.mismatches, msg)
	t.Logf("⚠️  %s", msg)
}

// Helper comparison functions

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func envVarsEqual(a, b *registry.EnvVar) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.Required == b.Required &&
		a.Secret == b.Secret &&
		a.Default == b.Default
}

func headersEqual(a, b *registry.Header) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.Required == b.Required &&
		a.Secret == b.Secret
}

func metadataEqual(a, b *registry.Metadata) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Stars == b.Stars &&
		a.Pulls == b.Pulls &&
		a.LastUpdated == b.LastUpdated
}
