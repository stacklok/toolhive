package operator_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// Common test data builders for registry sync tests

// CreateOriginalTestServers creates the standard original test server data
func CreateOriginalTestServers() []RegistryServer {
	return []RegistryServer{
		{
			Name:        "test-server-1",
			Description: "Test server 1",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"test_tool_1"},
			Image:       "docker.io/test/server1:latest",
			Tags:        []string{"testing", "original"},
		},
	}
}

// CreateUpdatedTestServers creates the standard updated test server data
func CreateUpdatedTestServers() []RegistryServer {
	return []RegistryServer{
		{
			Name:        "test-server-1",
			Description: "Test server 1 updated",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"test_tool_1", "test_tool_2"},
			Image:       "docker.io/test/server1:v1.1",
			Tags:        []string{"testing", "updated"},
		},
		{
			Name:        "test-server-2",
			Description: "Test server 2",
			Tier:        "Official",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"test_tool_3"},
			Image:       "docker.io/test/server2:latest",
			Tags:        []string{"testing", "new"},
		},
	}
}

// CreateComplexTestServers creates complex test server data with multiple server types
func CreateComplexTestServers() []RegistryServer {
	return []RegistryServer{
		{
			Name:        "database-server",
			Description: "PostgreSQL database connector",
			Tier:        "Official",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"execute_query", "list_tables", "backup_db"},
			Image:       "docker.io/postgres/mcp-server:v1.2.0",
			Tags:        []string{"database", "postgresql", "production"},
		},
		{
			Name:        "file-manager",
			Description: "File system operations",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"read_file", "write_file", "list_dir"},
			Image:       "docker.io/mcp/filesystem:latest",
			Tags:        []string{"filesystem", "files", "utility"},
		},
	}
}

// UpdateConfigMapWithServers updates a ConfigMap with new server data
func UpdateConfigMapWithServers(configMap *corev1.ConfigMap, servers []RegistryServer) error {
	updatedRegistryData := ToolHiveRegistryData{
		Version:     "1.0.1",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     make(map[string]RegistryServer),
	}
	for _, server := range servers {
		updatedRegistryData.Servers[server.Name] = server
	}
	jsonData, err := json.MarshalIndent(updatedRegistryData, "", "  ")
	if err != nil {
		return err
	}
	configMap.Data["registry.json"] = string(jsonData)
	return nil
}

// CreateBasicMCPRegistrySpec creates a basic MCPRegistry spec for testing
func CreateBasicMCPRegistrySpec(displayName, configMapName string,
	syncPolicy *mcpv1alpha1.SyncPolicy) mcpv1alpha1.MCPRegistrySpec {
	spec := mcpv1alpha1.MCPRegistrySpec{
		DisplayName: displayName,
		Source: mcpv1alpha1.MCPRegistrySource{
			Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
			Format: mcpv1alpha1.RegistryFormatToolHive,
			ConfigMap: &mcpv1alpha1.ConfigMapSource{
				Name: configMapName,
				Key:  "registry.json",
			},
		},
	}
	if syncPolicy != nil {
		spec.SyncPolicy = syncPolicy
	}
	return spec
}

// CreateMCPRegistryWithSyncPolicy creates an MCPRegistry with automatic sync policy
func CreateMCPRegistryWithSyncPolicy(name, namespace, displayName, configMapName, interval string) *mcpv1alpha1.MCPRegistry {
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: CreateBasicMCPRegistrySpec(displayName, configMapName, &mcpv1alpha1.SyncPolicy{
			Interval: interval,
		}),
	}
}

// CreateMCPRegistryManualOnly creates an MCPRegistry without automatic sync policy (manual only)
func CreateMCPRegistryManualOnly(name, namespace, displayName, configMapName string) *mcpv1alpha1.MCPRegistry {
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: CreateBasicMCPRegistrySpec(displayName, configMapName, nil),
	}
}

// CreateMCPRegistryWithGitSource creates an MCPRegistry with Git source and automatic sync policy
func CreateMCPRegistryWithGitSource(
	name, namespace, displayName, repository,
	branch, path, interval string) *mcpv1alpha1.MCPRegistry {
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			DisplayName: displayName,
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   mcpv1alpha1.RegistrySourceTypeGit,
				Format: mcpv1alpha1.RegistryFormatToolHive,
				Git: &mcpv1alpha1.GitSource{
					Repository: repository,
					Branch:     branch,
					Path:       path,
				},
			},
			SyncPolicy: &mcpv1alpha1.SyncPolicy{
				Interval: interval,
			},
		},
	}
}

// AddManualSyncTrigger adds a manual sync trigger annotation to an MCPRegistry
func AddManualSyncTrigger(mcpRegistry *mcpv1alpha1.MCPRegistry, triggerValue string, syncTriggerAnnotation string) {
	if mcpRegistry.Annotations == nil {
		mcpRegistry.Annotations = make(map[string]string)
	}
	mcpRegistry.Annotations[syncTriggerAnnotation] = triggerValue
}

// UniqueNames is a struct that contains unique names for test resources
type UniqueNames struct {
	RegistryName  string
	ConfigMapName string
	Timestamp     int64
}

// NewUniqueNames creates a new set of unique names for test resources
func NewUniqueNames(prefix string) *UniqueNames {
	timestamp := time.Now().Unix()
	return &UniqueNames{
		RegistryName:  fmt.Sprintf("%s-registry-%d", prefix, timestamp),
		ConfigMapName: fmt.Sprintf("%s-data-%d", prefix, timestamp),
		Timestamp:     timestamp,
	}
}

// GenerateTriggerValue generates a unique trigger value for manual sync
func (u *UniqueNames) GenerateTriggerValue(operation string) string {
	return fmt.Sprintf("%s-%d", operation, u.Timestamp)
}

// verifyServerContent is a helper function to verify that stored registry server content
// matches the expected servers array. It performs comprehensive field-by-field comparison.
func verifyServerContent(storedRegistry ToolHiveRegistryData, expectedServers []RegistryServer) {
	gomega.Expect(storedRegistry.Servers).To(gomega.HaveLen(len(expectedServers)))

	for _, expectedServer := range expectedServers {
		serverName := expectedServer.Name
		gomega.Expect(storedRegistry.Servers).To(gomega.HaveKey(serverName))

		actualServer := storedRegistry.Servers[serverName]
		gomega.Expect(actualServer.Name).To(gomega.Equal(expectedServer.Name))
		gomega.Expect(actualServer.Description).To(gomega.Equal(expectedServer.Description))
		gomega.Expect(actualServer.Tier).To(gomega.Equal(expectedServer.Tier))
		gomega.Expect(actualServer.Status).To(gomega.Equal(expectedServer.Status))
		gomega.Expect(actualServer.Transport).To(gomega.Equal(expectedServer.Transport))
		gomega.Expect(actualServer.Image).To(gomega.Equal(expectedServer.Image))
		gomega.Expect(actualServer.Tools).To(gomega.Equal(expectedServer.Tools))
		gomega.Expect(actualServer.Tags).To(gomega.Equal(expectedServer.Tags))
	}
}
