package registryapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	sourcesmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources/mocks"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
	}{
		{
			name:        "successful manager creation",
			description: "Should create a new manager with all dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create mock dependencies
			mockSourceHandlerFactory := sourcesmocks.NewMockSourceHandlerFactory(ctrl)

			scheme := runtime.NewScheme()

			// Create manager
			manager := NewManager(nil, scheme, mockSourceHandlerFactory)

			// Verify manager is created
			assert.NotNil(t, manager)

			// Verify manager implements the interface
			var _ = manager
		})
	}
}

func TestManagerCheckAPIReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		deployment  *appsv1.Deployment
		expected    bool
		description string
	}{
		{
			name:        "nil deployment",
			deployment:  nil,
			expected:    false,
			description: "Should return false for nil deployment",
		},
		{
			name: "deployment with ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has ready replicas",
		},
		{
			name: "deployment with no ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
				},
			},
			expected:    false,
			description: "Should return false when deployment has no ready replicas",
		},
		{
			name: "deployment with partial ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      3,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has at least one ready replica",
		},
		{
			name: "deployment with failed condition",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentProgressing,
							Status:  corev1.ConditionFalse,
							Reason:  "ProgressDeadlineExceeded",
							Message: "ReplicaSet has timed out progressing",
						},
					},
				},
			},
			expected:    false,
			description: "Should return false when deployment is not progressing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &manager{}
			ctx := context.Background()

			result := manager.CheckAPIReadiness(ctx, tt.deployment)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}
