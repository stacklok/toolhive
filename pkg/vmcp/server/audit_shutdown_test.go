// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
)

func TestStopIncompleteDrainPreservesWorkflowAuditUntilRetry(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	backend := vmcpmocks.NewMockBackendClient(ctrl)
	agg := aggmocks.NewMockAggregator(ctrl)
	target := &vmcp.BackendTarget{WorkloadID: "backend"}
	agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{{Name: "echo", BackendID: "backend"}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"echo": target}},
	}, nil).AnyTimes()

	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	backend.EXPECT().CallTool(gomock.Any(), target, "echo", gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *vmcp.BackendTarget, string, map[string]any, map[string]any, map[string]string) (*vmcp.ToolCallResult, error) {
			close(entered)
			// Deliberately ignore request cancellation: Stop must not tear down
			// resources while an uncooperative backend call is still running.
			select {
			case <-release:
				return &vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil
			case <-time.After(30 * time.Second):
				return nil, errors.New("timeout waiting to release backend")
			}
		})

	auditPath := filepath.Join(t.TempDir(), "workflow-audit.log")
	registry := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: "backend"}})
	v, err := core.New(&core.Config{
		Aggregator: agg, Router: routermocks.NewMockRouter(ctrl), BackendClient: backend, BackendRegistry: registry,
		AuditConfig: &audit.Config{LogFile: auditPath, EventTypes: []string{
			audit.EventTypeWorkflowStarted, audit.EventTypeWorkflowCompleted,
		}},
		WorkflowDefs: map[string]*composer.WorkflowDefinition{
			"workflow": {Name: "workflow", Steps: []composer.WorkflowStep{{ID: "step", Type: composer.StepTypeTool, Tool: "echo"}}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, v.Close()) })

	cfg := testMinimalServeConfig()
	cfg.BackendRegistry = registry
	cfg.SessionManagerConfig = &sessionmanager.FactoryConfig{Base: sessionmocks.NewMockMultiSessionFactory(ctrl)}
	// Only the core owns the file-backed workflow auditor; HTTP request auditing
	// is independent of the resource whose shutdown ordering is under test.
	srv, err := Serve(t.Context(), v, cfg)
	require.NoError(t, err)
	cleanupRan := false
	srv.shutdownFuncs = append(srv.shutdownFuncs, func(context.Context) error {
		cleanupRan = true
		return nil
	})
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelRequest)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	var requestDone chan struct{}
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !cleanupRan {
			assert.NoError(t, srv.Stop(ctx))
		}
		if srv.httpServer != nil {
			assert.NoError(t, srv.httpServer.Close())
		}
		if requestDone != nil {
			select {
			case <-requestDone:
			case <-ctx.Done():
				t.Error("timeout waiting for HTTP client teardown")
			}
		}
	})

	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.listener = listener
	ownedHTTP := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	srv.httpServer = ownedHTTP
	serveDone := make(chan error, 1)
	go func() { serveDone <- ownedHTTP.Serve(listener) }()

	metadata := map[string]string{"state": "in-flight"}
	created, err := srv.sessionDataStorage.Create(t.Context(), "pending", metadata)
	require.NoError(t, err)
	require.True(t, created)

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://"+listener.Addr().String()+"/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workflow","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "workflow")
	var responseBody []byte
	var responseStatus int
	var requestErr error
	requestDone = make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Do(req)
		if err != nil {
			requestErr = err
			return
		}
		defer resp.Body.Close()
		responseStatus = resp.StatusCode
		responseBody, requestErr = io.ReadAll(resp.Body)
	}()
	select {
	case <-entered:
	case <-requestDone:
		t.Fatalf("request finished before reaching backend: %v, %s", requestErr, responseBody)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for workflow to reach backend")
	}

	for attempt := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		err := srv.Stop(ctx)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded, "drain attempt %d", attempt+1)
		require.False(t, cleanupRan, "incomplete drain must not run shutdown functions")
		require.Same(t, ownedHTTP, srv.httpServer, "retry must retain the server with active connections")
		require.Nil(t, srv.listener, "Shutdown has closed the listener")
		stored, err := srv.sessionDataStorage.Load(t.Context(), "pending")
		require.NoError(t, err, "in-flight session metadata must survive")
		require.Equal(t, metadata, stored)
		select {
		case <-requestDone:
			t.Fatalf("Stop interrupted the active request: %v, %s", requestErr, responseBody)
		default:
		}
	}
	select {
	case err := <-serveDone:
		require.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not stop serving")
	}

	unblock()
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not complete after backend release")
	}
	require.NoError(t, requestErr)
	require.Equal(t, http.StatusOK, responseStatus, "%s", responseBody)
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(responseBody, &envelope))
	require.Empty(t, envelope.Error)
	require.False(t, envelope.Result.IsError)
	require.Equal(t, true, envelope.Result.StructuredContent["ok"])
	require.False(t, cleanupRan)

	data, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	require.Len(t, lines, 2, "started and terminal records must exist before cleanup")
	var started, completed struct {
		Type    string            `json:"type"`
		Outcome string            `json:"outcome"`
		Target  map[string]string `json:"target"`
	}
	require.NoError(t, json.Unmarshal(lines[0], &started))
	require.NoError(t, json.Unmarshal(lines[1], &completed))
	assert.Equal(t, audit.EventTypeWorkflowStarted, started.Type)
	assert.Equal(t, audit.EventTypeWorkflowCompleted, completed.Type)
	assert.Equal(t, audit.OutcomeSuccess, completed.Outcome)
	assert.Equal(t, "workflow", completed.Target[audit.TargetKeyWorkflowName])
	require.NotEmpty(t, started.Target[audit.TargetKeyWorkflowID])
	assert.Equal(t, started.Target[audit.TargetKeyWorkflowID], completed.Target[audit.TargetKeyWorkflowID])

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, srv.Stop(ctx), "fresh-context retry must finish cleanup")
	require.True(t, cleanupRan)
	_, err = srv.sessionDataStorage.Load(t.Context(), "pending")
	require.ErrorIs(t, err, transportsession.ErrSessionNotFound, "successful Stop must close storage")
	if runtime.GOOS == "linux" {
		requireNoAuditFD(t, auditPath)
	}
}

func TestNewServeFailureReleasesWorkflowAuditFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("file descriptor release is checked through /proc/self/fd on Linux")
	}
	ctrl := gomock.NewController(t)
	path := filepath.Join(t.TempDir(), "workflow-audit.log")
	srv, err := New(t.Context(), &Config{
		Aggregator:     aggmocks.NewMockAggregator(ctrl),
		SessionFactory: sessionmocks.NewMockMultiSessionFactory(ctrl),
		AuditConfig:    &audit.Config{LogFile: path},
		SessionStorage: &vmcpconfig.SessionStorageConfig{Provider: "unsupported"},
	}, routermocks.NewMockRouter(ctrl), vmcpmocks.NewMockBackendClient(ctrl), vmcp.NewImmutableRegistry(nil), nil)
	if srv != nil {
		t.Cleanup(func() { assert.NoError(t, srv.Stop(context.Background())) })
	}
	require.Nil(t, srv)
	require.ErrorContains(t, err, "failed to create session data storage: unsupported session storage provider")
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "core.New must have opened the audit file before Serve failed")
	requireNoAuditFD(t, path)
}

func TestStartJoinsHTTPAndStopErrors(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	srv, err := New(t.Context(), &Config{
		Host: "127.0.0.1", Port: 0,
		Aggregator: aggmocks.NewMockAggregator(ctrl), SessionFactory: sessionmocks.NewMockMultiSessionFactory(ctrl),
	}, routermocks.NewMockRouter(ctrl), vmcpmocks.NewMockBackendClient(ctrl), vmcp.NewImmutableRegistry(nil), nil)
	require.NoError(t, err)
	stopErr := errors.New("shutdown sentinel")
	cleanupRan := false
	srv.shutdownFuncs = append(srv.shutdownFuncs, func(context.Context) error {
		cleanupRan = true
		return stopErr
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var startErr error
	go func() {
		defer close(done)
		startErr = srv.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if !cleanupRan {
				assert.ErrorIs(t, srv.Stop(context.Background()), stopErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("timeout waiting for Start teardown")
		}
	})
	select {
	case <-srv.Ready():
	case <-done:
		t.Fatalf("Start failed before readiness: %v", startErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for Start readiness")
	}
	srv.listenerMu.RLock()
	listener := srv.listener
	srv.listenerMu.RUnlock()
	require.NoError(t, listener.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after listener failure")
	}
	assert.ErrorIs(t, startErr, net.ErrClosed)
	assert.ErrorIs(t, startErr, stopErr, "Start must preserve Stop's error chain")
}

func requireNoAuditFD(t *testing.T, path string) {
	t.Helper()
	resolvedPath, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // Descriptors in parallel tests can close during enumeration.
		}
		require.NoError(t, err)
		require.NotEqual(t, resolvedPath, target, "workflow audit file descriptor must be released")
	}
}
