// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/stacklok/toolhive/pkg/updates"
)

const testNamespace = "toolhive-test"

// enableUpdateChecks clears the variables that make updates.ShouldSkipUpdateChecks
// return true, so these tests behave the same locally and in CI.
func enableUpdateChecks(t *testing.T) {
	t.Helper()
	t.Setenv(updates.EnvVarSkipUpdateCheck, "")
	for _, name := range []string{
		"GITHUB_ACTIONS", "CI", "GITLAB_CI", "CIRCLECI", "TRAVIS", "BUILDKITE", "DRONE", "CONTINUOUS_INTEGRATION",
	} {
		t.Setenv(name, "")
	}
	require.False(t, updates.ShouldSkipUpdateChecks(), "a CI variable is still set; update this list")
}

func telemetryConfigMap(t *testing.T, instanceID string) *corev1.ConfigMap {
	t.Helper()
	data, err := json.Marshal(telemetryData{InstanceID: instanceID})
	require.NoError(t, err)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: testNamespace},
		Data:       map[string]string{instanceIDKey: string(data)},
	}
}

// noDelay retries three times without sleeping.
var noDelay = wait.Backoff{Steps: 3}

func storedInstanceID(t *testing.T, c client.Reader) string {
	t.Helper()
	_, id, err := readInstanceID(context.Background(), c, types.NamespacedName{Name: configMapName, Namespace: testNamespace})
	require.NoError(t, err)
	return id
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

//nolint:paralleltest // Uses t.Setenv.
func TestService_EnsureInstanceID(t *testing.T) {
	enableUpdateChecks(t)
	ctx := context.Background()
	cmResource := schema.GroupResource{Resource: "configmaps"}

	t.Run("creates the ConfigMap when missing", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)

		_, err = uuid.Parse(id)
		require.NoError(t, err, "instance ID must be a UUID")
		assert.Equal(t, id, storedInstanceID(t, c))
	})

	t.Run("returns the existing ID", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(telemetryConfigMap(t, "existing-id")).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)
		assert.Equal(t, "existing-id", id)
	})

	t.Run("reads the winner's ID when another replica creates it first", func(t *testing.T) {
		winner := telemetryConfigMap(t, "winner-id")
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, inner client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				// Another replica creates the ConfigMap between our read and create.
				require.NoError(t, inner.Create(ctx, winner.DeepCopy()))
				return apierrors.NewAlreadyExists(cmResource, configMapName)
			},
		}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)
		assert.Equal(t, "winner-id", id)
	})

	t.Run("returns create errors", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return apierrors.NewForbidden(cmResource, configMapName, errors.New("denied"))
			},
		}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.ErrorContains(t, err, "failed to create telemetry ConfigMap")
		assert.NotErrorIs(t, err, ErrInstanceIDUnavailable)
		assert.Empty(t, id)
	})

	t.Run("returns a forbidden read at once", func(t *testing.T) {
		gets := 0
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				gets++
				return apierrors.NewForbidden(cmResource, configMapName, errors.New("denied"))
			},
		}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.ErrorContains(t, err, "failed to get telemetry ConfigMap")
		assert.NotErrorIs(t, err, ErrInstanceIDUnavailable, "a forbidden read is not transient")
		assert.Empty(t, id)
		assert.Equal(t, 1, gets, "a forbidden read is not retried")
	})

	t.Run("reports a persistent transient error as unavailable", func(t *testing.T) {
		gets := 0
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				gets++
				return apierrors.NewServiceUnavailable("apiserver restarting")
			},
		}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.ErrorIs(t, err, ErrInstanceIDUnavailable)
		assert.ErrorContains(t, err, "apiserver restarting")
		assert.Empty(t, id)
		assert.Equal(t, noDelay.Steps, gets)
	})

	t.Run("retries a transient read error", func(t *testing.T) {
		gets := 0
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(telemetryConfigMap(t, "existing-id")).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(
					ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
				) error {
					gets++
					if gets == 1 {
						return apierrors.NewServiceUnavailable("apiserver restarting")
					}
					return inner.Get(ctx, key, obj, opts...)
				},
			}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)
		assert.Equal(t, "existing-id", id)
		assert.Equal(t, 2, gets)
	})

	t.Run("stores an ID in a ConfigMap that holds none", func(t *testing.T) {
		corrupt := telemetryConfigMap(t, "")
		corrupt.Data[instanceIDKey] = "not json"
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(corrupt).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)

		_, err = uuid.Parse(id)
		require.NoError(t, err, "instance ID must be a UUID")
		assert.Equal(t, id, storedInstanceID(t, c))
	})

	t.Run("reads the winner's ID when another replica repairs it first", func(t *testing.T) {
		corrupt := telemetryConfigMap(t, "")
		corrupt.Data[instanceIDKey] = "not json"
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(corrupt).WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, inner client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
				// Another replica stores its ID between our read and update.
				winner := telemetryConfigMap(t, "winner-id")
				current := &corev1.ConfigMap{}
				require.NoError(t, inner.Get(ctx, client.ObjectKeyFromObject(winner), current))
				winner.ResourceVersion = current.ResourceVersion
				require.NoError(t, inner.Update(ctx, winner))
				return apierrors.NewConflict(cmResource, configMapName, errors.New("modified"))
			},
		}).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)
		assert.Equal(t, "winner-id", id)
	})

	t.Run("skip update check returns no ID and creates nothing", func(t *testing.T) {
		t.Setenv(updates.EnvVarSkipUpdateCheck, "true")
		c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

		id, err := NewService(c, testNamespace).EnsureInstanceID(ctx, c, noDelay)
		require.NoError(t, err)
		assert.Empty(t, id)

		err = c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: testNamespace}, &corev1.ConfigMap{})
		assert.True(t, apierrors.IsNotFound(err), "ConfigMap must not be created, got %v", err)
	})
}
