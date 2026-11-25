// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stacklok/toolhive/pkg/updates"
)

// mockVersionClient is a mock implementation of the VersionClient interface
type mockVersionClient struct {
	version string
	err     error
}

func (m *mockVersionClient) GetLatestVersion(_ string, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.version, nil
}

func (*mockVersionClient) GetComponent() string {
	return "operator"
}

func TestService_CheckForUpdates(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name              string
		existingConfigMap *corev1.ConfigMap
		mockVersion       string
		mockError         error
		expectedError     bool
		expectedCallToAPI bool
	}{
		{
			name:              "first time check creates new data",
			existingConfigMap: nil,
			mockVersion:       "v1.2.3",
			expectedCallToAPI: true,
		},
		{
			name: "recent check skips API call",
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: configMapNamespace,
				},
				Data: map[string]string{
					instanceIDKey: `{"instance_id":"test-id","last_update_check":"` + time.Now().Format(time.RFC3339) + `","latest_version":"v1.2.2"}`,
				},
			},
			expectedCallToAPI: false,
		},
		{
			name: "old check triggers API call",
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: configMapNamespace,
				},
				Data: map[string]string{
					instanceIDKey: `{"instance_id":"test-id","last_update_check":"` + time.Now().Add(-5*time.Hour).Format(time.RFC3339) + `","latest_version":"v1.2.2"}`,
				},
			},
			mockVersion:       "v1.2.3",
			expectedCallToAPI: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create fake client
			objects := []client.Object{}
			if tt.existingConfigMap != nil {
				objects = append(objects, tt.existingConfigMap)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create telemetry service with mock version client
			service := &Service{
				client: fakeClient,
				versionClient: &mockVersionClient{
					version: tt.mockVersion,
					err:     tt.mockError,
				},
				namespace: configMapNamespace,
			}

			// Run the check
			err := service.CheckForUpdates(context.Background())

			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// Check if we're running in CI - if so, update checks should be skipped
			isCI := updates.ShouldSkipUpdateChecks()

			// Verify ConfigMap was created/updated if API call was expected AND not in CI
			if tt.expectedCallToAPI && !isCI {
				cm := &corev1.ConfigMap{}
				err = fakeClient.Get(context.Background(), types.NamespacedName{
					Name:      configMapName,
					Namespace: configMapNamespace,
				}, cm)
				require.NoError(t, err)
				assert.Contains(t, cm.Data, instanceIDKey)
			} else if isCI {
				// In CI, verify that no ConfigMap was created since update check was skipped
				cm := &corev1.ConfigMap{}
				err = fakeClient.Get(context.Background(), types.NamespacedName{
					Name:      configMapName,
					Namespace: configMapNamespace,
				}, cm)
				if tt.existingConfigMap == nil {
					// Should not find the ConfigMap since no update check happened
					assert.True(t, err != nil, "Expected no ConfigMap to be created in CI environment")
				}
			}
		})
	}
}

func TestService_GetTelemetryData(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name               string
		startupID          string
		existingConfigMap  *corev1.ConfigMap
		expectedInstanceID string
		expectNewID        bool
	}{
		{
			name:              "no configmap creates new data",
			existingConfigMap: nil,
			expectNewID:       true,
		},
		{
			name:               "no configmap reuses the ID resolved at startup",
			startupID:          "startup-id",
			expectedInstanceID: "startup-id",
		},
		{
			name: "existing valid data",
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: configMapNamespace,
				},
				Data: map[string]string{
					instanceIDKey: `{"instance_id":"existing-id","last_update_check":"2023-01-01T00:00:00Z","latest_version":"v1.0.0"}`,
				},
			},
			expectedInstanceID: "existing-id",
		},
		{
			name: "corrupted data creates new data",
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: configMapNamespace,
				},
				Data: map[string]string{
					instanceIDKey: "invalid json",
				},
			},
			expectNewID: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create fake client
			objects := []client.Object{}
			if tt.existingConfigMap != nil {
				objects = append(objects, tt.existingConfigMap)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			service := &Service{
				client:     fakeClient,
				namespace:  configMapNamespace,
				instanceID: tt.startupID,
			}

			data, err := service.getTelemetryData(context.Background())
			require.NoError(t, err)

			if tt.expectNewID {
				assert.NotEmpty(t, data.InstanceID)
				// Verify it's a valid UUID
				_, err := uuid.Parse(data.InstanceID)
				assert.NoError(t, err)
			} else {
				assert.Equal(t, tt.expectedInstanceID, data.InstanceID)
			}
		})
	}
}

func TestService_SaveTelemetryData_KeepsStoredID(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(telemetryConfigMap(t, "stored-id")).Build()
	service := NewService(c, testNamespace)

	err := service.saveTelemetryData(context.Background(), &telemetryData{InstanceID: "other-id", LatestVersion: "v9.9.9"})
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: configMapName, Namespace: testNamespace}, cm))
	stored := &telemetryData{}
	require.NoError(t, json.Unmarshal([]byte(cm.Data[instanceIDKey]), stored))
	assert.Equal(t, "stored-id", stored.InstanceID, "a stored ID is never replaced")
	assert.Equal(t, "v9.9.9", stored.LatestVersion, "the rest of the data is saved")
}
