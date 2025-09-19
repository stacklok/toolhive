package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewSourceHandlerFactory(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	factory := NewSourceHandlerFactory(fakeClient)
	assert.NotNil(t, factory)
	assert.IsType(t, &DefaultSourceHandlerFactory{}, factory)
}

func TestDefaultSourceHandlerFactory_CreateHandler(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	factory := NewSourceHandlerFactory(fakeClient)

	tests := []struct {
		name          string
		sourceType    string
		expectError   bool
		expectedType  interface{}
		errorContains string
	}{
		{
			name:         "configmap source type",
			sourceType:   mcpv1alpha1.RegistrySourceTypeConfigMap,
			expectError:  false,
			expectedType: &ConfigMapSourceHandler{},
		},
		{
			name:         "git source type",
			sourceType:   mcpv1alpha1.RegistrySourceTypeGit,
			expectError:  false,
			expectedType: &GitSourceHandler{},
		},
		{
			name:          "unsupported source type",
			sourceType:    "unsupported",
			expectError:   true,
			errorContains: "unsupported source type",
		},
		{
			name:          "empty source type",
			sourceType:    "",
			expectError:   true,
			errorContains: "unsupported source type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, err := factory.CreateHandler(tt.sourceType)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, handler)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, handler)
				assert.IsType(t, tt.expectedType, handler)
			}
		})
	}
}
