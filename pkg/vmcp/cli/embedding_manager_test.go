// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/cli/mocks"
)

func TestContainerNameForModel(t *testing.T) {
	t.Parallel()

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, containerNameForModel("BAAI/bge-small-en-v1.5"),
			containerNameForModel("BAAI/bge-small-en-v1.5"))
	})

	t.Run("different models produce different names", func(t *testing.T) {
		t.Parallel()
		assert.NotEqual(t,
			containerNameForModel("BAAI/bge-small-en-v1.5"),
			containerNameForModel("sentence-transformers/all-MiniLM-L6-v2"))
	})

	t.Run("has expected prefix", func(t *testing.T) {
		t.Parallel()
		name := containerNameForModel("BAAI/bge-small-en-v1.5")
		assert.True(t, strings.HasPrefix(name, "thv-embedding-"),
			"expected prefix thv-embedding- in %q", name)
	})

	t.Run("no slashes in name", func(t *testing.T) {
		t.Parallel()
		name := containerNameForModel("BAAI/bge-small-en-v1.5")
		assert.NotContains(t, name, "/")
	})

	t.Run("hash is 8 hex chars", func(t *testing.T) {
		t.Parallel()
		name := containerNameForModel("BAAI/bge-small-en-v1.5")
		hash := strings.TrimPrefix(name, "thv-embedding-")
		require.Len(t, hash, 8)
		for _, c := range hash {
			assert.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
				"expected hex char, got %c", c)
		}
	})
}

func TestNewEmbeddingServiceManager_NilFactory(t *testing.T) {
	t.Parallel()

	_, err := NewEmbeddingServiceManager(nil, EmbeddingServiceManagerConfig{Model: "BAAI/bge-small-en-v1.5"})
	assert.ErrorContains(t, err, "factory")
}

func TestNewEmbeddingServiceManager_EmptyModel(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	_, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{Model: ""})
	assert.ErrorContains(t, err, "model")
}

func TestNewEmbeddingServiceManager_WhitespaceModel(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	_, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{Model: "   "})
	assert.ErrorContains(t, err, "model")
}

func TestNewEmbeddingServiceManager_WhitespaceModelTrimmed(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{Model: "  BAAI/bge-small-en-v1.5  "})
	require.NoError(t, err)
	assert.Equal(t, "BAAI/bge-small-en-v1.5", mgr.cfg.Model)
}

func TestNewEmbeddingServiceManager_DefaultImage(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	assert.Equal(t, defaultTEIImage, mgr.cfg.Image)
	assert.Equal(t, containerNameForModel("BAAI/bge-small-en-v1.5"), mgr.containerName)
}

func TestStart_ReuseExistingContainer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(healthServer.Close)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)

	// Redirect health checks to the test server; pin the port so the returned
	// URL matches what GetWorkloadInfo reports via the label.
	u, err := url.Parse(healthServer.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	mgr.healthURLFor = func(_ int) string { return healthServer.URL + healthPath }
	mgr.urlFor = func(_ int) string { return fmt.Sprintf("http://localhost:%d", port) }

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), mgr.containerName).Return(true, nil)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), mgr.containerName).Return(runtime.ContainerInfo{
		Labels: map[string]string{"toolhive-port": strconv.Itoa(port)},
	}, nil)

	gotURL, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("http://localhost:%d", port), gotURL)
	assert.False(t, mgr.started, "started must be false when reusing an existing container")
}

func TestStart_FactoryError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)

	mockFactory.EXPECT().Create(gomock.Any()).Return(nil, fmt.Errorf("daemon unavailable"))

	_, err = mgr.Start(context.Background())
	assert.ErrorContains(t, err, "daemon unavailable")
}

// pinPortAndHealth configures mgr to use the port of server for port allocation
// and redirects health checks to server. Call t.Cleanup(server.Close) separately.
func pinPortAndHealth(t *testing.T, mgr *EmbeddingServiceManager, server *httptest.Server) {
	t.Helper()
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	mgr.portFinder = func() int { return port }
	mgr.healthURLFor = func(_ int) string { return server.URL + healthPath }
}

func TestStart_DeployNewContainer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(healthServer.Close)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	pinPortAndHealth(t, mgr, healthServer)

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), mgr.containerName).Return(false, nil)
	mockRT.EXPECT().DeployWorkload(
		gomock.Any(),
		defaultTEIImage,
		mgr.containerName,
		[]string{"--model-id", "BAAI/bge-small-en-v1.5"},
		gomock.Nil(),
		gomock.Any(),
		gomock.Any(),
		"streamable-http",
		gomock.Any(),
		false,
	).Return(0, nil)

	gotURL, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, mgr.started, "started must be true after deploying a new container")
	assert.Contains(t, gotURL, "http://127.0.0.1:")
}

// TestStart_DeployNewContainer_Kubernetes verifies that on a Kubernetes runtime
// the manager deploys without a localhost port binding or host bind-mount, and
// returns a Kubernetes service URL.
func TestStart_DeployNewContainer_Kubernetes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(healthServer.Close)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)

	// Simulate Kubernetes runtime without environment mutation.
	svcName := fmt.Sprintf("mcp-%s", mgr.containerName)
	mgr.isKubernetes = true
	mgr.portFinder = func() int { return 80 }
	mgr.urlFor = func(_ int) string {
		return fmt.Sprintf("http://%s:%s", svcName, teiContainerPort)
	}
	mgr.healthURLFor = func(_ int) string { return healthServer.URL + healthPath }

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), mgr.containerName).Return(false, nil)
	mockRT.EXPECT().DeployWorkload(
		gomock.Any(),
		defaultTEIImage,
		mgr.containerName,
		[]string{"--model-id", "BAAI/bge-small-en-v1.5"},
		gomock.Nil(), // no env vars
		gomock.Any(), // labels
		gomock.Nil(), // no permission profile on Kubernetes
		"streamable-http",
		gomock.Any(),
		false,
	).Return(0, nil)

	gotURL, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, mgr.started)
	assert.Equal(t, fmt.Sprintf("http://%s:%s", svcName, teiContainerPort), gotURL)
}

func TestStart_HealthPollTimeout(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(healthServer.Close)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	pinPortAndHealth(t, mgr, healthServer)

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), gomock.Any()).Return(false, nil)
	mockRT.EXPECT().DeployWorkload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(0, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	t.Cleanup(cancel)

	_, err = mgr.Start(ctx)
	assert.ErrorContains(t, err, "healthy")
}

func TestStart_DeployError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(healthServer.Close)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	pinPortAndHealth(t, mgr, healthServer)

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), gomock.Any()).Return(false, nil)
	mockRT.EXPECT().DeployWorkload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(0, fmt.Errorf("image pull failed"))

	_, err = mgr.Start(context.Background())
	assert.ErrorContains(t, err, "image pull failed")
	assert.False(t, mgr.started)
}

func TestStart_ZeroPort(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	mgr.portFinder = func() int { return 0 }

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), gomock.Any()).Return(false, nil)

	_, err = mgr.Start(context.Background())
	assert.ErrorContains(t, err, "port")
}

func TestStop_OwnsContainer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	mgr.started = true // simulate this instance having deployed the container

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().StopWorkload(gomock.Any(), mgr.containerName).Return(nil)

	err = mgr.Stop(context.Background())
	assert.NoError(t, err)
}

func TestStop_ReuseContainer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	// mgr.started == false (default) — reuse scenario; factory.Create and StopWorkload must NOT be called

	err = mgr.Stop(context.Background())
	assert.NoError(t, err)
}

func TestStop_RuntimeError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockFactory := mocks.NewMockContainerFactory(ctrl)
	mockRT := runtimemocks.NewMockRuntime(ctrl)

	mgr, err := NewEmbeddingServiceManager(mockFactory, EmbeddingServiceManagerConfig{
		Model: "BAAI/bge-small-en-v1.5",
	})
	require.NoError(t, err)
	mgr.started = true

	mockFactory.EXPECT().Create(gomock.Any()).Return(mockRT, nil)
	mockRT.EXPECT().StopWorkload(gomock.Any(), mgr.containerName).Return(fmt.Errorf("stop failed"))

	err = mgr.Stop(context.Background())
	assert.ErrorContains(t, err, "stop failed")
}
