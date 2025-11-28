package registryexport

import (
	"context"
	"testing"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stacklok/toolhive/pkg/registry/registry"
)

func TestGetConfigMapName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		namespace string
		want      string
	}{
		{"default", "default-registry-export"},
		{"mcp-servers", "mcp-servers-registry-export"},
	}

	for _, tt := range tests {
		t.Run(tt.namespace, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, GetConfigMapName(tt.namespace))
		})
	}
}

func TestConfigMapManager_UpsertConfigMap(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name      string
		namespace string
		existing  *corev1.ConfigMap
		servers   []upstreamv0.ServerJSON
		wantErr   bool
	}{
		{
			name:      "create new",
			namespace: "test-ns",
			servers:   []upstreamv0.ServerJSON{{Name: "test/server", Description: "Test", Version: "1.0.0"}},
		},
		{
			name:      "update existing",
			namespace: "test-ns",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ns-registry-export", Namespace: "test-ns"},
				Data:       map[string]string{ConfigMapKey: "{}"},
			},
			servers: []upstreamv0.ServerJSON{{Name: "test/server", Description: "Updated", Version: "1.0.0"}},
		},
		{
			name:      "empty servers",
			namespace: "test-ns",
			servers:   []upstreamv0.ServerJSON{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var objs []runtime.Object
			if tt.existing != nil {
				objs = append(objs, tt.existing)
			}
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			mgr := NewConfigMapManager(client)

			reg := &registry.UpstreamRegistry{
				Version: "1.0.0",
				Meta:    registry.UpstreamMeta{LastUpdated: "2024-01-01T00:00:00Z"},
				Data:    registry.UpstreamData{Servers: tt.servers},
			}

			err := mgr.UpsertConfigMap(context.Background(), tt.namespace, reg)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify ConfigMap was created/updated
			cm, err := mgr.GetConfigMap(context.Background(), tt.namespace)
			require.NoError(t, err)
			assert.Equal(t, GetConfigMapName(tt.namespace), cm.Name)
			assert.Equal(t, LabelRegistryExportValue, cm.Labels[LabelRegistryExport])
			assert.NotEmpty(t, cm.Annotations[ContentChecksumAnnotation])
			assert.Contains(t, cm.Data, ConfigMapKey)
		})
	}
}

func TestConfigMapManager_DeleteConfigMap(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name      string
		namespace string
		existing  *corev1.ConfigMap
		wantErr   bool
	}{
		{
			name:      "delete existing",
			namespace: "test-ns",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ns-registry-export", Namespace: "test-ns"},
			},
		},
		{
			name:      "delete non-existing",
			namespace: "test-ns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var objs []runtime.Object
			if tt.existing != nil {
				objs = append(objs, tt.existing)
			}
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			mgr := NewConfigMapManager(client)

			err := mgr.DeleteConfigMap(context.Background(), tt.namespace)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify ConfigMap is gone
			_, err = mgr.GetConfigMap(context.Background(), tt.namespace)
			assert.Error(t, err, "ConfigMap should not exist after deletion")
		})
	}
}

func TestConfigMapManager_checksumHasChanged(t *testing.T) {
	t.Parallel()
	mgr := &ConfigMapManager{}

	tests := []struct {
		name    string
		current *corev1.ConfigMap
		desired *corev1.ConfigMap
		want    bool
	}{
		{
			name: "same checksum",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ContentChecksumAnnotation: "abc123"}},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ContentChecksumAnnotation: "abc123"}},
			},
			want: false,
		},
		{
			name: "different checksum",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ContentChecksumAnnotation: "abc123"}},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ContentChecksumAnnotation: "def456"}},
			},
			want: true,
		},
		{
			name: "missing current checksum",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
				Data:       map[string]string{"key": "value1"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ContentChecksumAnnotation: "def456"}},
				Data:       map[string]string{"key": "value2"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, mgr.checksumHasChanged(tt.current, tt.desired))
		})
	}
}
