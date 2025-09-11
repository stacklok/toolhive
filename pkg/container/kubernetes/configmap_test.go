package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestNewConfigMapReaderWithClient(t *testing.T) {
	tests := []struct {
		name      string
		clientset *fake.Clientset
	}{
		{
			name:      "creates reader with fake clientset",
			clientset: fake.NewSimpleClientset(),
		},
		{
			name:      "creates reader with nil clientset",
			clientset: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewConfigMapReaderWithClient(tt.clientset)

			if reader == nil {
				t.Error("expected non-nil reader")
			}

			if reader.clientset != tt.clientset {
				t.Error("clientset not set correctly")
			}
		})
	}
}

func TestNewConfigMapReader(t *testing.T) {
	// This test verifies that NewConfigMapReader fails gracefully
	// when not running in a Kubernetes cluster.
	// The success path cannot be unit tested as it requires a real cluster.
	reader, err := NewConfigMapReader()

	if err == nil {
		t.Error("expected error when running outside of cluster")
	}

	if reader != nil {
		t.Error("expected nil reader when error occurs")
	}

	if !strings.Contains(err.Error(), "failed to create in-cluster config") {
		t.Errorf("expected error about in-cluster config but got: %v", err)
	}
}

func TestConfigMapReader_GetRunConfigMap(t *testing.T) {
	tests := []struct {
		name          string
		configMapRef  string
		configMap     *corev1.ConfigMap
		simulateError bool
		errorMessage  string
		expectedData  string
		expectedError string
	}{
		{
			name:         "successful read with valid configmap",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"runconfig.json": `{"name":"test","version":"1.0"}`,
				},
			},
			expectedData: `{"name":"test","version":"1.0"}`,
		},
		{
			name:          "invalid configmap reference - missing slash",
			configMapRef:  "invalid-ref",
			expectedError: "invalid configmap reference",
		},
		{
			name:          "invalid configmap reference - empty string",
			configMapRef:  "",
			expectedError: "invalid configmap reference",
		},
		{
			name:          "invalid configmap reference - only slash",
			configMapRef:  "/",
			expectedError: "namespace cannot be empty",
		},
		{
			name:          "invalid configmap reference - empty namespace",
			configMapRef:  "/configmap",
			expectedError: "namespace cannot be empty",
		},
		{
			name:          "invalid configmap reference - empty name",
			configMapRef:  "namespace/",
			expectedError: "configmap name cannot be empty",
		},
		{
			name:          "invalid configmap reference - spaces only",
			configMapRef:  "  /  ",
			expectedError: "namespace cannot be empty",
		},
		{
			name:         "configmap reference with spaces (should trim)",
			configMapRef: " namespace / configmap ",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"runconfig.json": `{"trimmed":"true"}`,
				},
			},
			expectedData: `{"trimmed":"true"}`,
		},
		{
			name:         "configmap reference with multiple slashes",
			configMapRef: "namespace/configmap/extra",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap/extra",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"runconfig.json": `{"multi":"slash"}`,
				},
			},
			expectedData: `{"multi":"slash"}`,
		},
		{
			name:          "configmap not found",
			configMapRef:  "namespace/missing",
			expectedError: "failed to get ConfigMap",
		},
		{
			name:         "configmap missing runconfig.json key",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"other-key": "other-value",
				},
			},
			expectedError: "does not contain 'runconfig.json' key",
		},
		{
			name:         "configmap with empty data map",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{},
			},
			expectedError: "does not contain 'runconfig.json' key",
		},
		{
			name:         "configmap with nil data map",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
			},
			expectedError: "does not contain 'runconfig.json' key",
		},
		{
			name:         "configmap with binary data (not supported)",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				BinaryData: map[string][]byte{
					"runconfig.json": []byte(`{"binary":"data"}`),
				},
			},
			expectedError: "does not contain 'runconfig.json' key",
		},
		{
			name:         "configmap with empty runconfig.json value",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"runconfig.json": "",
				},
			},
			expectedData: "",
		},
		{
			name:         "configmap with large runconfig.json",
			configMapRef: "namespace/configmap",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "configmap",
					Namespace: "namespace",
				},
				Data: map[string]string{
					"runconfig.json": generateLargeJSON(),
				},
			},
			expectedData: generateLargeJSON(),
		},
		{
			name:          "kubernetes API error",
			configMapRef:  "namespace/configmap",
			simulateError: true,
			errorMessage:  "connection refused",
			expectedError: "failed to get ConfigMap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clientset
			var fakeClient *fake.Clientset
			if tt.configMap != nil {
				fakeClient = fake.NewSimpleClientset(tt.configMap)
			} else {
				fakeClient = fake.NewSimpleClientset()
			}

			// Simulate API error if needed
			if tt.simulateError {
				fakeClient.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("%s", tt.errorMessage)
				})
			}

			// Create reader
			reader := NewConfigMapReaderWithClient(fakeClient)

			// Call GetRunConfigMap
			data, err := reader.GetRunConfigMap(context.Background(), tt.configMapRef)

			// Check error
			if tt.expectedError != "" {
				if err == nil {
					t.Errorf("expected error containing %q but got none", tt.expectedError)
				} else if !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("expected error containing %q but got %q", tt.expectedError, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			// Check data
			if data != tt.expectedData {
				t.Errorf("expected data %q but got %q", tt.expectedData, data)
			}
		})
	}
}

func TestConfigMapReader_GetRunConfigMap_ContextCancellation(t *testing.T) {
	// Create a configmap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "namespace",
		},
		Data: map[string]string{
			"runconfig.json": `{"test":"data"}`,
		},
	}

	// Create fake clientset
	fakeClient := fake.NewSimpleClientset(configMap)

	// Create reader
	reader := NewConfigMapReaderWithClient(fakeClient)

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Try to get configmap with cancelled context
	// The fake client doesn't actually respect context cancellation,
	// but this test ensures the context is properly passed through
	data, err := reader.GetRunConfigMap(ctx, "namespace/configmap")

	// The operation might succeed (fake client) or fail (if context handling is added)
	// We're mainly testing that the function accepts and passes the context
	if err == nil && data != `{"test":"data"}` {
		t.Errorf("expected data %q but got %q", `{"test":"data"}`, data)
	}
}

func TestConfigMapReader_InterfaceCompliance(t *testing.T) {
	// Verify that ConfigMapReader implements RunConfigMapReader interface
	fakeClient := fake.NewSimpleClientset()
	reader := NewConfigMapReaderWithClient(fakeClient)

	// This will fail to compile if ConfigMapReader doesn't implement RunConfigMapReader
	var _ RunConfigMapReader = reader

	// Also test that we can use it through the interface
	var interfaceReader RunConfigMapReader = reader

	// Call method through interface
	_, err := interfaceReader.GetRunConfigMap(context.Background(), "namespace/configmap")

	// Should fail (configmap doesn't exist) but that's expected
	if err == nil {
		t.Error("expected error for non-existent configmap")
	}
}

func TestConfigMapReader_MultipleCallsWithSameClient(t *testing.T) {
	// Test that a single reader can be used for multiple calls
	configMap1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "config1",
			Namespace: "ns1",
		},
		Data: map[string]string{
			"runconfig.json": `{"id":"1"}`,
		},
	}

	configMap2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "config2",
			Namespace: "ns2",
		},
		Data: map[string]string{
			"runconfig.json": `{"id":"2"}`,
		},
	}

	fakeClient := fake.NewSimpleClientset(configMap1, configMap2)
	reader := NewConfigMapReaderWithClient(fakeClient)

	// First call
	data1, err1 := reader.GetRunConfigMap(context.Background(), "ns1/config1")
	if err1 != nil {
		t.Errorf("unexpected error on first call: %v", err1)
	}
	if data1 != `{"id":"1"}` {
		t.Errorf("expected data %q but got %q", `{"id":"1"}`, data1)
	}

	// Second call with same reader
	data2, err2 := reader.GetRunConfigMap(context.Background(), "ns2/config2")
	if err2 != nil {
		t.Errorf("unexpected error on second call: %v", err2)
	}
	if data2 != `{"id":"2"}` {
		t.Errorf("expected data %q but got %q", `{"id":"2"}`, data2)
	}

	// Third call - non-existent configmap
	_, err3 := reader.GetRunConfigMap(context.Background(), "ns3/config3")
	if err3 == nil {
		t.Error("expected error for non-existent configmap")
	}
}

// Helper function to generate large JSON for testing
func generateLargeJSON() string {
	var sb strings.Builder
	sb.WriteString(`{`)
	sb.WriteString(`"name":"large-config",`)
	sb.WriteString(`"description":"This is a large configuration for testing purposes with lots of data to ensure the function handles large payloads correctly",`)
	sb.WriteString(`"items":[`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf(`{"id":%d,"value":"item-%d"}`, i, i))
	}
	sb.WriteString(`],`)
	sb.WriteString(`"metadata":{`)
	sb.WriteString(`"version":"1.0.0",`)
	sb.WriteString(`"created":"2024-01-01T00:00:00Z",`)
	sb.WriteString(`"author":"test"`)
	sb.WriteString(`}}`)
	return sb.String()
}
