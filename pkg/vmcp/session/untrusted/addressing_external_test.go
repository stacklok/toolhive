// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted/mocks"
)

// userQuotaKeyForTest builds the quota key the admission layer reads
// (<prefix>untrusted:userquota:<sha256(userKey)[:40]>).
func userQuotaKeyForTest(prefix, userKey string) string {
	sum := sha256.Sum256([]byte(userKey))
	return prefix + "untrusted:userquota:" + hex.EncodeToString(sum[:])[:40]
}

func untrustedBackend(id, uid, baseURL string) *vmcp.Backend {
	return &vmcp.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       baseURL,
		TransportType: "streamable-http",
		Metadata: map[string]string{
			untrusted.MetadataKeyUntrusted:    "true",
			untrusted.MetadataKeyMCPServerUID: uid,
		},
	}
}

func trustedBackend(id, baseURL string) *vmcp.Backend {
	return &vmcp.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       baseURL,
		TransportType: "streamable-http",
		Metadata:      map[string]string{},
	}
}

func authSession() untrusted.SessionRef {
	return untrusted.SessionRef{SessionID: "sess-1", Issuer: "https://iss", Subject: "sub", Namespace: "toolhive"}
}

func readyPod(name, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "toolhive", UID: "pod-uid-1"},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func TestBackendPortAndSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		baseURL    string
		wantPort   int32
		wantSuffix string
		wantErr    string
	}{
		{name: "streamable-http path", baseURL: "http://svc:8080/mcp", wantPort: 8080, wantSuffix: "/mcp"},
		{name: "sse path+fragment", baseURL: "http://svc:8080/sse#github-mcp", wantPort: 8080, wantSuffix: "/sse#github-mcp"},
		{name: "root path", baseURL: "http://svc:9090", wantPort: 9090, wantSuffix: ""},
		{name: "https scheme", baseURL: "https://svc:8443/mcp", wantPort: 8443, wantSuffix: "/mcp"},
		{name: "missing port", baseURL: "http://svc/mcp", wantErr: "no explicit port"},
		{name: "bad port", baseURL: "http://svc:notaport/mcp", wantErr: "invalid port"},
		{name: "no scheme", baseURL: "svc:8080/mcp", wantErr: "unsupported scheme"},
		{name: "user info rejected", baseURL: "http://user@svc:8080/mcp", wantErr: "user info"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			port, suffix, err := untrusted.BackendPortAndSuffix(tc.baseURL)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantPort, port)
			assert.Equal(t, tc.wantSuffix, suffix)
		})
	}
}

func TestResolveTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("trusted backends pass through unmodified (same pointer)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		trusted := trustedBackend("trusted-1", "http://svc:8080/mcp")
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{trusted}, nil)
		require.Len(t, out, 1)
		assert.Same(t, trusted, out[0], "trusted backend must not be copied or rewritten")
	})

	t.Run("untrusted backend BaseURL rewritten to pod IP with port+suffix preserved", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		pod := readyPod("untrusted-uid-abc", "10.1.2.3")
		lc.EXPECT().EnsurePod(gomock.Any(), gomock.Any()).Return(pod, nil)
		lc.EXPECT().WaitReady(gomock.Any(), pod, gomock.Any()).Return(nil)
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		b := untrustedBackend("github-mcp", "uid-abc", "http://svc:8080/sse#github-mcp")
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{b}, nil)
		require.Len(t, out, 1)
		assert.Equal(t, "http://10.1.2.3:8080/sse#github-mcp", out[0].BaseURL)
		assert.Equal(t, pod.Name, out[0].Metadata[untrusted.MetadataKeyUntrustedPodName])
		assert.Equal(t, "pod-uid-1", out[0].Metadata[untrusted.MetadataKeyUntrustedPodUID])

		// Registry backend untouched.
		assert.Equal(t, "http://svc:8080/sse#github-mcp", b.BaseURL)
		_, hasPodKey := b.Metadata[untrusted.MetadataKeyUntrustedPodName]
		assert.False(t, hasPodKey, "registry backend metadata must not be mutated")
	})

	t.Run("EnsurePod failure soft-fails the backend only", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		lc.EXPECT().EnsurePod(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom"))
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		trusted := trustedBackend("trusted-1", "http://svc:8080/mcp")
		untrustedB := untrustedBackend("github-mcp", "uid-abc", "http://svc:8080/mcp")
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{trusted, untrustedB}, nil)
		require.Len(t, out, 1)
		assert.Equal(t, "trusted-1", out[0].ID)
	})

	t.Run("WaitReady failure soft-fails the backend", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		pod := readyPod("p", "10.0.0.1")
		lc.EXPECT().EnsurePod(gomock.Any(), gomock.Any()).Return(pod, nil)
		lc.EXPECT().WaitReady(gomock.Any(), pod, gomock.Any()).Return(errors.New("timeout"))
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{untrustedBackend("b", "uid", "http://svc:8080/mcp")}, nil)
		assert.Empty(t, out)
	})

	t.Run("anonymous session soft-fails untrusted backends without touching lifecycle", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl) // no EXPECT: any call fails the test
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		anon := untrusted.SessionRef{SessionID: "sess-1", Namespace: "toolhive"}
		out := resolver.ResolveTargets(ctx, anon, []*vmcp.Backend{
			untrustedBackend("github-mcp", "uid-abc", "http://svc:8080/mcp"),
			trustedBackend("trusted-1", "http://svc:8080/mcp"),
		}, nil)
		require.Len(t, out, 1)
		assert.Equal(t, "trusted-1", out[0].ID)
	})

	t.Run("missing MCPServer UID soft-fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)

		b := untrustedBackend("github-mcp", "uid-abc", "http://svc:8080/mcp")
		delete(b.Metadata, untrusted.MetadataKeyMCPServerUID)
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{b}, nil)
		assert.Empty(t, out)
	})

	t.Run("admission denial soft-fails with quota reason", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl) // no pod ops expected

		mr := miniredis.RunT(t)
		rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		adm, err := untrusted.NewAdmission(rc, "thv:vmcp:session:", "toolhive", untrusted.AdmissionConfig{
			PerUserPodQuota:   1,
			PerUserCreateRate: 100,
			PerMCPServerCap:   100,
			GlobalCapFactor:   0.8,
			CacheCapacity:     100,
		})
		require.NoError(t, err)
		// Exhaust the user's quota (key shape: <prefix>untrusted:userquota:<userHash>).
		require.NoError(t, rc.Set(ctx, userQuotaKeyForTest("thv:vmcp:session:", authSession().UserKey()), 1, time.Minute).Err())

		resolver := untrusted.NewPodAddressResolver(lc, adm, time.Second)
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{untrustedBackend("b", "uid", "http://svc:8080/mcp")}, nil)
		assert.Empty(t, out)
	})

	t.Run("nil backends are skipped", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		lc := mocks.NewMockPodLifecycle(ctrl)
		resolver := untrusted.NewPodAddressResolver(lc, nil, time.Second)
		out := resolver.ResolveTargets(ctx, authSession(), []*vmcp.Backend{nil}, nil)
		assert.Empty(t, out)
	})
}
