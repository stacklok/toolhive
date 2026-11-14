// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestCreateRunConfigFromMCPServerIgnoresPermissionProfile(t *testing.T) {
	t.Parallel()

	profilePath := filepath.Join(t.TempDir(), "must-not-be-read.json")
	withProfile := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.PermissionProfile = &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeBuiltin,
				Name: profilePath,
			}
		}),
	)
	withoutProfile := withProfile.DeepCopy()
	withoutProfile.Spec.PermissionProfile = nil

	reconciler := newTestMCPServerReconciler(nil, nil, kubernetes.PlatformKubernetes)
	want, err := reconciler.createRunConfigFromMCPServer(withoutProfile)
	require.NoError(t, err)

	got, err := reconciler.createRunConfigFromMCPServer(withProfile)
	require.NoError(t, err, "permission profile names must not be opened as files")
	assert.Equal(t, want, got, "permissionProfile must have no effect on the RunConfig")
}

func TestDeploymentForMCPServerIgnoresConfigMapPermissionProfile(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := newTestMCPServerReconciler(k8sClient, scheme, kubernetes.PlatformKubernetes)

	withProfile := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.PermissionProfile = &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeConfigMap,
				Name: "permission-profile-config",
				Key:  "profile.json",
			}
		}),
	)
	withoutProfile := withProfile.DeepCopy()
	withoutProfile.Spec.PermissionProfile = nil

	want, err := reconciler.deploymentForMCPServer(t.Context(), withoutProfile, "test-checksum")
	require.NoError(t, err)

	got, err := reconciler.deploymentForMCPServer(t.Context(), withProfile, "test-checksum")
	require.NoError(t, err)
	assert.Equal(t, want, got, "permissionProfile must have no effect on the Deployment")

	for _, volume := range got.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, "permission-profile", volume.Name)
	}
	for _, container := range got.Spec.Template.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			assert.NotEqual(t, "permission-profile", mount.Name)
		}
	}
}

func TestEmitPermissionProfileUnsupportedEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		permissionProfile  *mcpv1beta1.PermissionProfileRef
		generation         int64
		observedGeneration int64
		withRecorder       bool
		wantEvent          bool
	}{
		{
			name: "first reconciliation emits",
			permissionProfile: &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeBuiltin,
				Name: "network",
			},
			generation:   1,
			withRecorder: true,
			wantEvent:    true,
		},
		{
			name: "spec change emits",
			permissionProfile: &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeBuiltin,
				Name: "network",
			},
			generation:         2,
			observedGeneration: 1,
			withRecorder:       true,
			wantEvent:          true,
		},
		{
			name: "already observed generation is suppressed",
			permissionProfile: &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeBuiltin,
				Name: "network",
			},
			generation:         2,
			observedGeneration: 2,
			withRecorder:       true,
			wantEvent:          false,
		},
		{
			name:              "permission profile is absent",
			permissionProfile: nil,
			generation:        1,
			withRecorder:      true,
			wantEvent:         false,
		},
		{
			name: "nil recorder is safe",
			permissionProfile: &mcpv1beta1.PermissionProfileRef{
				Type: mcpv1beta1.PermissionProfileTypeBuiltin,
				Name: "network",
			},
			generation:   1,
			withRecorder: false,
			wantEvent:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var recorder *events.FakeRecorder
			reconciler := &MCPServerReconciler{}
			if tt.withRecorder {
				recorder = events.NewFakeRecorder(1)
				reconciler.Recorder = recorder
			}
			mcpServer := v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
					m.Spec.PermissionProfile = tt.permissionProfile
					m.Generation = tt.generation
					m.Status.ObservedGeneration = tt.observedGeneration
				}),
			)

			reconciler.emitPermissionProfileUnsupportedEvent(mcpServer)
			if recorder == nil {
				return
			}
			recordedEvents := drainEvents(recorder)
			if !tt.wantEvent {
				assert.Empty(t, recordedEvents)
				return
			}

			require.Len(t, recordedEvents, 1)
			assert.Contains(t, recordedEvents[0], "Warning")
			assert.Contains(t, recordedEvents[0], permissionProfileUnsupportedEventReason)
			assert.Contains(t, recordedEvents[0], "unsupported")
			assert.Contains(t, recordedEvents[0], "has no effect")
		})
	}
}
