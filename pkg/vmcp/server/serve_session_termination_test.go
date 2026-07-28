// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	tcredis "github.com/stacklok/toolhive-core/redis"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
)

// sharedRedisSessionKeyPrefix is the Redis key prefix shared by the origin
// server's session-data storage and the out-of-band terminating Manager in
// TestRegression_OriginPod_RejectsRequestAfterCrossPodTermination, so both see
// the same underlying keys in the shared miniredis instance.
const sharedRedisSessionKeyPrefix = "test:vmcp:session:"

// TestRegression_OriginPod_RejectsRequestAfterCrossPodTermination pins the
// real U5 fix (toolhive-core v0.0.32, #5742): a session present in the origin
// pod's LOCAL bookkeeping but terminated in the SHARED (Redis) session store
// by a second pod must be rejected on the very next request to the origin
// pod, not served as if still live.
//
// This is distinct from the existing DELETE-path termination tests
// (TestRegression_TerminatedSessionRejected et al., session_lifecycle_regression_test.go):
// those terminate through the SAME server instance, so the SDK's local
// session bookkeeping (forgetSession) is dropped in lockstep with the storage
// delete. Here, termination happens on a SECOND Manager that only touches the
// shared store -- the origin pod's local go-sdk session and node-local cache
// entry are never told directly. The origin pod must instead detect the
// termination by re-validating the shared store on the next request (see
// mcpcompat/server/transports.go's "Local-session validation, issue #156,
// item U5" comment), and drop its local bookkeeping only then.
func TestRegression_OriginPod_RejectsRequestAfterCrossPodTermination(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)

	// Origin server ("pod A"): a Serve-built server backed by Redis-backed
	// session-data storage, so its vMCP session manager's Validate() answers
	// from the shared store rather than an in-process-only map.
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	testTool := vmcp.Tool{Name: "t"}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL: time.Minute,
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider:  "redis",
			Address:   mr.Addr(),
			KeyPrefix: sharedRedisSessionKeyPrefix,
		},
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	initResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "session should be registered locally on the origin pod")

	// Sanity: the origin pod must currently regard the session as live, or the
	// post-termination 404 assertion below would pass vacuously.
	liveResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	}, sessionID)
	require.Equal(t, http.StatusOK, liveResp.StatusCode, "session must be live before cross-pod termination")
	liveResp.Body.Close()

	// A second Manager ("pod B") over the SAME shared Redis store terminates
	// the session out-of-band. This does NOT go through the origin pod's
	// in-process forgetSession bookkeeping -- it only mutates the shared
	// store, exactly like a DELETE served by a different replica.
	terminatorFactory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	terminatorStorage, err := transportsession.NewRedisSessionDataStorage(
		context.Background(),
		tcredis.Config{Addr: mr.Addr()},
		sharedRedisSessionKeyPrefix,
		time.Hour,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminatorStorage.Close() })

	smB, cleanupB, err := sessionmanager.New(
		terminatorStorage,
		&sessionmanager.FactoryConfig{Base: terminatorFactory},
		vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanupB(context.Background()) })

	isNotAllowed, err := smB.Terminate(sessionID)
	require.NoError(t, err)
	require.False(t, isNotAllowed)

	// The origin pod must reject the NEXT request for the cross-pod-terminated
	// session with HTTP 404 -- the streamable transport's validate-on-every-
	// request path must observe the termination via the shared store and
	// refuse to serve it from local state.
	rejectResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": map[string]any{},
	}, sessionID)
	defer rejectResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, rejectResp.StatusCode,
		"a session terminated on another pod's shared store must be rejected as 404 "+
			"on the very next request to the origin pod")
}
