package registryexport

import (
	"testing"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestGenerator_GenerateServerEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		annotations map[string]string
		transport   string
		wantName    string
		wantURL     string
		wantErr     bool
		wantNil     bool
	}{
		{
			name:        "valid annotations",
			annotations: map[string]string{AnnotationRegistryURL: "https://mcp.example.com/github", AnnotationRegistryDescription: "GitHub MCP server"},
			transport:   "sse",
			wantName:    "dev.stacklok.toolhive/test-ns.test-server",
			wantURL:     "https://mcp.example.com/github",
		},
		{
			name:        "missing URL annotation",
			annotations: map[string]string{AnnotationRegistryDescription: "Test"},
			wantNil:     true,
		},
		{
			name:        "missing description annotation",
			annotations: map[string]string{AnnotationRegistryURL: "https://example.com"},
			wantErr:     true,
		},
		{
			name:        "custom name override",
			annotations: map[string]string{AnnotationRegistryURL: "https://example.com", AnnotationRegistryDescription: "Test", AnnotationRegistryName: "custom/name"},
			transport:   "streamable-http",
			wantName:    "custom/name",
			wantURL:     "https://example.com",
		},
		{
			name:        "nil annotations",
			annotations: nil,
			wantNil:     true,
		},
		{
			name:        "default transport",
			annotations: map[string]string{AnnotationRegistryURL: "https://example.com", AnnotationRegistryDescription: "Test"},
			transport:   "",
			wantName:    "dev.stacklok.toolhive/test-ns.test-server",
			wantURL:     "https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewGenerator()
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "test-ns", Annotations: tt.annotations},
			}

			entry, err := g.GenerateServerEntry(ExportableResource{Object: server, Transport: tt.transport})

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, entry)
				return
			}

			require.NotNil(t, entry)
			assert.Equal(t, tt.wantName, entry.Name)
			assert.Equal(t, model.CurrentSchemaURL, entry.Schema)
			assert.Equal(t, DefaultVersion, entry.Version)
			require.Len(t, entry.Remotes, 1)
			assert.Equal(t, tt.wantURL, entry.Remotes[0].URL)
		})
	}
}

func TestGenerator_BuildUpstreamRegistry(t *testing.T) {
	t.Parallel()
	g := NewGenerator()
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "ns",
			Annotations: map[string]string{AnnotationRegistryURL: "https://test.com", AnnotationRegistryDescription: "Test"},
		},
	}

	entry, err := g.GenerateServerEntry(ExportableResource{Object: server, Transport: "sse"})
	require.NoError(t, err)
	require.NotNil(t, entry)

	registry := g.BuildUpstreamRegistry([]upstreamv0.ServerJSON{*entry})

	assert.Equal(t, "1.0.0", registry.Version)
	assert.NotEmpty(t, registry.Meta.LastUpdated)
	require.Len(t, registry.Data.Servers, 1)
	assert.Equal(t, entry.Name, registry.Data.Servers[0].Name)
}

func TestHasRegistryExportAnnotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{"with URL annotation", map[string]string{AnnotationRegistryURL: "https://test.com"}, true},
		{"without URL annotation", map[string]string{"other": "value"}, false},
		{"nil annotations", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			assert.Equal(t, tt.want, HasRegistryExportAnnotation(server))
		})
	}
}
