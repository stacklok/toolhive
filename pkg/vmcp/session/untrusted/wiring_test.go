// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// newWiringTestScheme builds the scheme the lifecycle's K8s client needs
// (core pods + the MCPServer CRD is unnecessary here — the mock lifecycle
// never touches the cluster, so an empty core scheme is enough).
func newWiringTestK8sClient(t *testing.T) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme)
}

// TestNewStack_WiresTunables pins the Wave-5 tunables → WiringConfig →
// resolver flow: the images and sidecar-resource multipliers the composition
// root resolves from THV_UNTRUSTED_* env must reach the EnsurePodRequest the
// resolver issues for every provisioned pod (they are consumed by
// applyEgressBrokerSidecar at clone time).
func TestNewStack_WiresTunables(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	k8sClient := newWiringTestK8sClient(t).Build()
	images := &SidecarImages{EnvoyProxy: "mirror.local/envoy:v1", EgressBroker: "mirror.local/broker:v2"}
	sidecarRes := &SidecarResourceOverride{CPUMultiplier: 2.0, MemoryMultiplier: 1.5}
	tokenStore := &TokenStoreConfig{
		RedisAddr:   "redis.auth:6379",
		KeyPrefix:   "thv:auth:{toolhive:my-vmcp}:",
		KEKSecret:   "my-vmcp-kek",
		KEKActiveID: "kek-1",
		KEKIDs:      []string{"kek-1"},
	}

	stack, err := NewStack(WiringConfig{
		K8sClient:        k8sClient,
		RedisClient:      redisClient,
		KeyPrefix:        "thv:vmcp:session:",
		Namespace:        "toolhive",
		VMCPUId:          "vmcp-1",
		Admission:        AdmissionConfig{CacheCapacity: 100},
		TokenStore:       tokenStore,
		Images:           images,
		SidecarResources: sidecarRes,
	})
	require.NoError(t, err)
	require.NotNil(t, stack)

	// The resolver must carry the tunables into the EnsurePodRequest. Swap in
	// a capturing lifecycle to observe the request without a cluster.
	resolver, ok := stack.Resolver.(*podAddressResolver)
	require.True(t, ok, "NewStack must return the production podAddressResolver")

	var captured EnsurePodRequest
	resolver.lifecycle = captureLifecycle{pod: &corev1.Pod{}, capture: &captured}

	backend := &vmcp.Backend{
		ID: "github-mcp", Name: "github-mcp", BaseURL: "http://svc:8080/mcp",
		TransportType: "streamable-http",
		Metadata: map[string]string{
			MetadataKeyUntrusted:    "true",
			MetadataKeyMCPServerUID: "uid-abc",
		},
	}
	sess := SessionRef{SessionID: "sess-1", Issuer: "https://iss", Subject: "sub", Namespace: "toolhive"}
	out := resolver.ResolveTargets(context.Background(), sess, []*vmcp.Backend{backend}, nil)
	require.Len(t, out, 1, "the untrusted backend must resolve through the capturing lifecycle")

	assert.Same(t, tokenStore, captured.TokenStore, "token-store coordinates must flow to the clone request")
	assert.Same(t, images, captured.Images, "image overrides must flow to the clone request")
	assert.Same(t, sidecarRes, captured.SidecarResources, "resource multipliers must flow to the clone request")
}

// TestNewStack_RejectsInvalidTunables pins fail-loud wiring: a non-positive
// sidecar multiplier or an invalid TokenStore is a startup error, not a
// silently-defaulted clone.
func TestNewStack_RejectsInvalidTunables(t *testing.T) {
	t.Parallel()

	base := func() WiringConfig {
		mr := miniredis.RunT(t)
		redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = redisClient.Close() })
		return WiringConfig{
			K8sClient:   newWiringTestK8sClient(t).Build(),
			RedisClient: redisClient,
			KeyPrefix:   "thv:vmcp:session:",
			Namespace:   "toolhive",
			VMCPUId:     "vmcp-1",
			Admission:   AdmissionConfig{CacheCapacity: 100},
		}
	}

	t.Run("zero CPU multiplier", func(t *testing.T) {
		t.Parallel()
		cfg := base()
		cfg.SidecarResources = &SidecarResourceOverride{CPUMultiplier: 0, MemoryMultiplier: 1}
		_, err := NewStack(cfg)
		require.Error(t, err)
	})

	t.Run("negative memory multiplier", func(t *testing.T) {
		t.Parallel()
		cfg := base()
		cfg.SidecarResources = &SidecarResourceOverride{CPUMultiplier: 1, MemoryMultiplier: -2}
		_, err := NewStack(cfg)
		require.Error(t, err)
	})

	t.Run("partial KEK coordinates", func(t *testing.T) {
		t.Parallel()
		cfg := base()
		cfg.TokenStore = &TokenStoreConfig{
			RedisAddr: "redis.auth:6379", KeyPrefix: "thv:auth:{a:b}:", KEKSecret: "s",
		}
		_, err := NewStack(cfg)
		require.Error(t, err)
	})

	t.Run("nil images and nil resources wire the pinned defaults downstream", func(t *testing.T) {
		t.Parallel()
		cfg := base()
		stack, err := NewStack(cfg)
		require.NoError(t, err)
		resolver, ok := stack.Resolver.(*podAddressResolver)
		require.True(t, ok)
		assert.Nil(t, resolver.images, "nil images fall back to the pinned defaults at clone time")
		assert.Nil(t, resolver.sidecarRes, "nil resources fall back to the defaults at clone time")
	})
}

// captureLifecycle is a PodLifecycle stub that records the EnsurePodRequest
// and reports an immediately-ready pod.
type captureLifecycle struct {
	pod     *corev1.Pod
	capture *EnsurePodRequest
}

func (c captureLifecycle) EnsurePod(_ context.Context, req EnsurePodRequest) (*corev1.Pod, error) {
	*c.capture = req
	return c.pod, nil
}

func (captureLifecycle) WaitReady(_ context.Context, _ *corev1.Pod, _ time.Duration) error {
	return nil
}

func (captureLifecycle) DeletePod(_ context.Context, _ string) error { return nil }

var _ PodLifecycle = captureLifecycle{}
