package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

type restartTestContext struct {
	mcpServer  *mcpv1alpha1.MCPServer
	client     client.Client
	reconciler *MCPServerReconciler
	t          *testing.T
}

func setupRestartTest(t *testing.T) *restartTestContext {
	t.Helper()
	name := "test-server"
	namespace := "default"
	mcpServer := createTestMCPServer(name, namespace)
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	return &restartTestContext{
		mcpServer: mcpServer,
		client:    fakeClient,
		reconciler: &MCPServerReconciler{
			Client: fakeClient,
			Scheme: testScheme,
		},
		t: t,
	}
}

func (tc *restartTestContext) createDeployment() {
	tc.t.Helper()
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.mcpServer.Name,
			Namespace: tc.mcpServer.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForMCPServer(tc.mcpServer.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(tc.mcpServer.Name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mcp",
							Image: "test-image:latest",
						},
					},
				},
			},
		},
	}
	err := tc.client.Create(context.TODO(), deployment)
	require.NoError(tc.t, err, "Failed to create test deployment")
}

func (tc *restartTestContext) createPods(count int) {
	tc.t.Helper()
	for i := 0; i < count; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-pod-%d", tc.mcpServer.Name, i),
				Namespace: tc.mcpServer.Namespace,
				Labels:    labelsForMCPServer(tc.mcpServer.Name),
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "mcp",
						Image: "test-image:latest",
					},
				},
			},
		}
		err := tc.client.Create(context.TODO(), pod)
		require.NoError(tc.t, err, "Failed to create test pod %d", i)
	}
}

func (tc *restartTestContext) setRestartAnnotation(timestamp string, strategy string) {
	tc.t.Helper()
	if tc.mcpServer.Annotations == nil {
		tc.mcpServer.Annotations = make(map[string]string)
	}
	tc.mcpServer.Annotations[RestartedAtAnnotationKey] = timestamp
	if strategy != "" {
		tc.mcpServer.Annotations[RestartStrategyAnnotationKey] = strategy
	}
}

func (tc *restartTestContext) setLastRestartRequest(timestamp time.Time) {
	tc.t.Helper()
	if tc.mcpServer.Annotations == nil {
		tc.mcpServer.Annotations = make(map[string]string)
	}
	tc.mcpServer.Annotations[LastProcessedRestartAnnotationKey] = timestamp.Format(time.RFC3339)
	// Update the MCPServer in the client as well
	err := tc.client.Update(context.TODO(), tc.mcpServer)
	require.NoError(tc.t, err, "Failed to update MCPServer with last restart request annotation")
}

func (tc *restartTestContext) handleRestartAnnotation() (bool, error) {
	tc.t.Helper()
	// First update the MCPServer in the client with the current annotations
	err := tc.client.Update(context.TODO(), tc.mcpServer)
	if err != nil {
		return false, err
	}

	// Now fetch the updated MCPServer for the actual test
	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.mcpServer.Name,
		Namespace: tc.mcpServer.Namespace,
	}, updatedMCPServer)
	if err != nil {
		return false, err
	}

	result, err := tc.reconciler.handleRestartAnnotation(context.TODO(), updatedMCPServer)

	// Update our test context with the modified MCPServer
	if err == nil {
		tc.mcpServer = updatedMCPServer
	}

	return result, err
}

func (tc *restartTestContext) assertDeploymentPodTemplateAnnotationUpdated() {
	tc.t.Helper()
	deployment := &appsv1.Deployment{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.mcpServer.Name,
		Namespace: tc.mcpServer.Namespace,
	}, deployment)
	require.NoError(tc.t, err)

	require.NotNil(tc.t, deployment.Spec.Template.Annotations)
	restartedAt, exists := deployment.Spec.Template.Annotations[RestartedAtAnnotationKey]
	assert.True(tc.t, exists, "Expected restart annotation to be present in deployment pod template")
	assert.NotEmpty(tc.t, restartedAt, "Expected restart annotation to have a value")

	// Validate timestamp format
	_, err = time.Parse(time.RFC3339, restartedAt)
	assert.NoError(tc.t, err, "Expected restart annotation to be a valid RFC3339 timestamp")
}

func (tc *restartTestContext) assertPodsDeleted(_ int) {
	tc.t.Helper()
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(tc.mcpServer.Namespace),
		client.MatchingLabels(labelsForMCPServer(tc.mcpServer.Name)),
	}

	err := tc.client.List(context.TODO(), podList, listOpts...)
	require.NoError(tc.t, err)

	// In a real cluster, pods would be deleted, but in our fake client they should be gone
	assert.Equal(tc.t, 0, len(podList.Items), "Expected all pods to be deleted for immediate restart")
}

func (tc *restartTestContext) assertLastRestartRequestUpdated(expectedTime time.Time) {
	tc.t.Helper()

	// Get the last processed restart annotation
	lastProcessedRestart := tc.mcpServer.Annotations[LastProcessedRestartAnnotationKey]
	assert.NotEmpty(tc.t, lastProcessedRestart, "Expected last processed restart annotation to be set")

	// Parse the annotation value
	lastProcessedTime, err := time.Parse(time.RFC3339, lastProcessedRestart)
	assert.NoError(tc.t, err, "Expected last processed restart annotation to be valid RFC3339")

	// Parse the expected time as RFC3339 to match the precision used in the annotation
	expectedTimeRFC3339, err := time.Parse(time.RFC3339, expectedTime.Format(time.RFC3339))
	assert.NoError(tc.t, err)

	assert.True(tc.t, lastProcessedTime.Equal(expectedTimeRFC3339),
		"Expected last processed restart to be updated to %v, got %v",
		expectedTimeRFC3339, lastProcessedTime)
}

func TestHandleRestartAnnotation_NoAnnotation(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.False(t, triggered, "Expected no restart to be triggered when annotation is not present")
}

func TestHandleRestartAnnotation_InvalidTimestamp(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)
	tc.setRestartAnnotation("invalid-timestamp", "")

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.False(t, triggered, "Expected no restart to be triggered when timestamp is invalid")
}

func TestHandleRestartAnnotation_AlreadyProcessed(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), "")
	tc.setLastRestartRequest(requestTime.Add(time.Minute)) // Last restart is newer

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.False(t, triggered, "Expected no restart when request has already been processed")
}

func TestHandleRestartAnnotation_RollingRestart_Success(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment
	tc.createDeployment()

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), RestartStrategyRolling)

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.True(t, triggered, "Expected restart to be triggered")
	tc.assertDeploymentPodTemplateAnnotationUpdated()
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_RollingRestart_DefaultStrategy(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment
	tc.createDeployment()

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), "") // No strategy specified

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.True(t, triggered, "Expected restart to be triggered with default rolling strategy")
	tc.assertDeploymentPodTemplateAnnotationUpdated()
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_RollingRestart_DeploymentNotFound(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), RestartStrategyRolling)

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err, "Expected no error when deployment is not found")
	assert.True(t, triggered, "Expected restart to be triggered even when deployment not found")
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_ImmediateRestart_Success(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create pods
	podCount := 3
	tc.createPods(podCount)

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), RestartStrategyImmediate)

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.True(t, triggered, "Expected restart to be triggered")
	tc.assertPodsDeleted(podCount)
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_ImmediateRestart_NoPods(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), RestartStrategyImmediate)

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err, "Expected no error when no pods exist")
	assert.True(t, triggered, "Expected restart to be triggered even when no pods exist")
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_UnknownStrategy(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment
	tc.createDeployment()

	requestTime := time.Now()
	tc.setRestartAnnotation(requestTime.Format(time.RFC3339), "unknown-strategy")

	triggered, err := tc.handleRestartAnnotation()

	require.NoError(t, err)
	assert.True(t, triggered, "Expected restart to be triggered with fallback to rolling strategy")
	tc.assertDeploymentPodTemplateAnnotationUpdated()
	tc.assertLastRestartRequestUpdated(requestTime)
}

func TestHandleRestartAnnotation_MultipleSequentialRequests(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment
	tc.createDeployment()

	// First request
	firstRequest := time.Now()
	tc.setRestartAnnotation(firstRequest.Format(time.RFC3339), RestartStrategyRolling)

	triggered, err := tc.handleRestartAnnotation()
	require.NoError(t, err)
	assert.True(t, triggered, "Expected first restart to be triggered")
	tc.assertLastRestartRequestUpdated(firstRequest)

	// Second request with later timestamp
	secondRequest := firstRequest.Add(time.Minute)
	tc.setRestartAnnotation(secondRequest.Format(time.RFC3339), RestartStrategyRolling)

	triggered, err = tc.handleRestartAnnotation()
	require.NoError(t, err)
	assert.True(t, triggered, "Expected second restart to be triggered")
	tc.assertLastRestartRequestUpdated(secondRequest)

	// Third request with same timestamp as second (should not trigger)
	triggered, err = tc.handleRestartAnnotation()
	require.NoError(t, err)
	assert.False(t, triggered, "Expected third restart with same timestamp to not be triggered")
}

func TestHandleRestartAnnotation_DifferentStrategies(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		strategy string
	}{
		{"rolling strategy", RestartStrategyRolling},
		{"immediate strategy", RestartStrategyImmediate},
		{"empty strategy defaults to rolling", ""},
		{"unknown strategy defaults to rolling", "custom-strategy"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testCtx := setupRestartTest(t)

			// Create deployment and pods for both strategies
			testCtx.createDeployment()
			testCtx.createPods(2)

			requestTime := time.Now()
			testCtx.setRestartAnnotation(requestTime.Format(time.RFC3339), tc.strategy)

			triggered, err := testCtx.handleRestartAnnotation()

			require.NoError(t, err)
			assert.True(t, triggered, "Expected restart to be triggered for strategy: %s", tc.strategy)
			testCtx.assertLastRestartRequestUpdated(requestTime)

			// For immediate strategy, verify pods are deleted
			if tc.strategy == RestartStrategyImmediate {
				testCtx.assertPodsDeleted(2)
			} else {
				// For rolling strategy (including defaults), verify deployment is updated
				testCtx.assertDeploymentPodTemplateAnnotationUpdated()
			}
		})
	}
}

func TestPerformRollingRestart_Success(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment without pod template annotations
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.mcpServer.Name,
			Namespace: tc.mcpServer.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(tc.mcpServer.Name),
				},
			},
		},
	}
	err := tc.client.Create(context.TODO(), deployment)
	require.NoError(t, err)

	err = tc.reconciler.performRollingRestart(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	tc.assertDeploymentPodTemplateAnnotationUpdated()
}

func TestPerformRollingRestart_ExistingAnnotations(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	// Create deployment with existing pod template annotations
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.mcpServer.Name,
			Namespace: tc.mcpServer.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(tc.mcpServer.Name),
					Annotations: map[string]string{
						"existing-annotation": "existing-value",
					},
				},
			},
		},
	}
	err := tc.client.Create(context.TODO(), deployment)
	require.NoError(t, err)

	err = tc.reconciler.performRollingRestart(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	// Verify both existing and new annotations are present
	updatedDeployment := &appsv1.Deployment{}
	err = tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.mcpServer.Name,
		Namespace: tc.mcpServer.Namespace,
	}, updatedDeployment)
	require.NoError(t, err)

	assert.Equal(t, "existing-value", updatedDeployment.Spec.Template.Annotations["existing-annotation"])
	assert.Contains(t, updatedDeployment.Spec.Template.Annotations, RestartedAtAnnotationKey)
}

func TestPerformImmediateRestart_Success(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	podCount := 3
	tc.createPods(podCount)

	err := tc.reconciler.performImmediateRestart(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	tc.assertPodsDeleted(podCount)
}

func TestPerformImmediateRestart_NoPods(t *testing.T) {
	t.Parallel()
	tc := setupRestartTest(t)

	err := tc.reconciler.performImmediateRestart(context.TODO(), tc.mcpServer)
	require.NoError(t, err, "Expected no error when no pods exist")
}

func TestPerformRestart_ValidStrategies(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		strategy string
	}{
		{"rolling", RestartStrategyRolling},
		{"immediate", RestartStrategyImmediate},
		{"unknown defaults to rolling", "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testCtx := setupRestartTest(t)

			// Create both deployment and pods to handle either strategy
			testCtx.createDeployment()
			testCtx.createPods(2)

			err := testCtx.reconciler.performRestart(context.TODO(), testCtx.mcpServer, tc.strategy)
			require.NoError(t, err, "Expected no error for strategy: %s", tc.strategy)
		})
	}
}

// Test error handling in handleRestartAnnotation
func TestHandleRestartAnnotation_ErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("PerformRestart_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)

		// Set a restart annotation with immediate strategy but don't create pods
		// This will cause an error when trying to list pods for immediate restart
		testCtx.setRestartAnnotation("2023-01-01T12:00:00Z", "immediate")

		// Mock a client that returns an error on List operations
		// Create a mock client that fails on List
		mockClient := &mockFailingClient{
			Client:     testCtx.client,
			failOnList: true,
		}
		testCtx.reconciler.Client = mockClient

		shouldRestart, err := testCtx.reconciler.handleRestartAnnotation(context.TODO(), testCtx.mcpServer)
		assert.False(t, shouldRestart)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to perform restart")
	})

	t.Run("UpdateMCPServer_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)
		testCtx.createDeployment()
		testCtx.setRestartAnnotation("2023-01-01T12:00:00Z", "rolling")

		// Mock a client that fails only on MCPServer Update operations
		mockClient := &mockFailingClient{
			Client:                testCtx.client,
			failOnMCPServerUpdate: true,
		}
		testCtx.reconciler.Client = mockClient

		shouldRestart, err := testCtx.reconciler.handleRestartAnnotation(context.TODO(), testCtx.mcpServer)
		assert.False(t, shouldRestart)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to update MCPServer with last processed restart annotation")
	})
}

// Test error handling in performRollingRestart
func TestPerformRollingRestart_ErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("GetDeployment_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)

		// Mock a client that fails on Get operations
		mockClient := &mockFailingClient{
			Client:    testCtx.client,
			failOnGet: true,
		}
		testCtx.reconciler.Client = mockClient

		err := testCtx.reconciler.performRollingRestart(context.TODO(), testCtx.mcpServer)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get deployment for rolling restart")
	})

	t.Run("UpdateDeployment_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)
		testCtx.createDeployment()

		// Mock a client that fails on Update operations
		mockClient := &mockFailingClient{
			Client:       testCtx.client,
			failOnUpdate: true,
		}
		testCtx.reconciler.Client = mockClient

		err := testCtx.reconciler.performRollingRestart(context.TODO(), testCtx.mcpServer)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to update deployment for rolling restart")
	})
}

// Test error handling in performImmediateRestart
func TestPerformImmediateRestart_ErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("ListPods_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)

		// Mock a client that fails on List operations
		mockClient := &mockFailingClient{
			Client:     testCtx.client,
			failOnList: true,
		}
		testCtx.reconciler.Client = mockClient

		err := testCtx.reconciler.performImmediateRestart(context.TODO(), testCtx.mcpServer)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list pods for immediate restart")
	})

	t.Run("DeletePod_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)
		testCtx.createPods(2)

		// Mock a client that fails on Delete operations
		mockClient := &mockFailingClient{
			Client:       testCtx.client,
			failOnDelete: true,
		}
		testCtx.reconciler.Client = mockClient

		err := testCtx.reconciler.performImmediateRestart(context.TODO(), testCtx.mcpServer)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to delete pod")
		assert.Contains(t, err.Error(), "for immediate restart")
	})
}

// Test main reconciler error handling
func TestReconcile_HandleRestartAnnotation_ErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("HandleRestartAnnotation_Error_Returns_Error", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)
		testCtx.setRestartAnnotation("2023-01-01T12:00:00Z", "immediate")

		// Mock a client that fails on List operations (will cause handleRestartAnnotation to fail)
		mockClient := &mockFailingClient{
			Client:     testCtx.client,
			failOnList: true,
		}
		testCtx.reconciler.Client = mockClient

		result, err := testCtx.reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      testCtx.mcpServer.Name,
				Namespace: testCtx.mcpServer.Namespace,
			},
		})

		assert.Error(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("HandleRestartAnnotation_Success_Returns_Requeue", func(t *testing.T) {
		t.Parallel()
		testCtx := setupRestartTest(t)
		testCtx.createDeployment()
		testCtx.setRestartAnnotation("2023-01-01T12:00:00Z", "rolling")

		result, err := testCtx.reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      testCtx.mcpServer.Name,
				Namespace: testCtx.mcpServer.Namespace,
			},
		})

		assert.NoError(t, err)
		//nolint:staticcheck // Requeue is what the controller actually returns
		assert.True(t, result.Requeue, "Expected requeue to be requested")
	})
}

// mockFailingClient is a test helper that wraps a real client and can be configured to fail on specific operations
type mockFailingClient struct {
	client.Client
	failOnGet             bool
	failOnList            bool
	failOnUpdate          bool
	failOnDelete          bool
	failOnMCPServerUpdate bool
}

func (m *mockFailingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if m.failOnGet {
		return fmt.Errorf("mock error: Get operation failed")
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

func (m *mockFailingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if m.failOnList {
		return fmt.Errorf("mock error: List operation failed")
	}
	return m.Client.List(ctx, list, opts...)
}

func (m *mockFailingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if m.failOnUpdate {
		return fmt.Errorf("mock error: Update operation failed")
	}
	if m.failOnMCPServerUpdate {
		// Check if the object being updated is an MCPServer
		if _, isMCPServer := obj.(*mcpv1alpha1.MCPServer); isMCPServer {
			return fmt.Errorf("mock error: MCPServer Update operation failed")
		}
	}
	return m.Client.Update(ctx, obj, opts...)
}

func (m *mockFailingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if m.failOnDelete {
		return fmt.Errorf("mock error: Delete operation failed")
	}
	return m.Client.Delete(ctx, obj, opts...)
}
