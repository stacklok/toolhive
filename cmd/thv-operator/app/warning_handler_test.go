// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// mcpRegistryDeprecationWarningText is the exact Warning header kube-apiserver
// emits for the MCPRegistry +kubebuilder:deprecatedversion marker. Matches
// issue https://github.com/stacklok/toolhive/issues/6346.
const mcpRegistryDeprecationWarningText = "MCPRegistry is deprecated and will be removed in a future release; " +
	"install the ToolHive registry server via the toolhive-registry-server Helm chart " +
	"(https://github.com/stacklok/toolhive-registry-server) instead"

// recordingWarningHandler records every warning the operator handler forwards,
// standing in for the controller-runtime/cache logger.
type recordingWarningHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *recordingWarningHandler) HandleWarningHeaderWithContext(
	_ context.Context, _ int, _ string, text string,
) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, text)
}

func (h *recordingWarningHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.messages))
	copy(out, h.messages)
	return out
}

var _ rest.WarningHandlerWithContext = (*recordingWarningHandler)(nil)

// TestMCPRegistryDeprecationWarningLoggedOnceAcrossResyncs reproduces #6346:
// the cache LIST path delivers the MCPRegistry deprecation Warning header on
// every resync even when no MCPRegistry CRs exist. The operator must surface
// that warning at most once.
func TestMCPRegistryDeprecationWarningLoggedOnceAcrossResyncs(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	// Two cache resyncs / two warning-path invocations, no CRs involved.
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)

	got := recorder.snapshot()
	require.NotEmpty(t, got, "the first cache LIST must still surface the deprecation warning")
	assert.Len(t, got, 1,
		"MCPRegistry deprecation warning must be logged once, not on every cache resync; got %d: %v",
		len(got), got)
	assert.Contains(t, got[0], mcpRegistryDeprecationWarningMarker)
}

func TestMCPRegistryOnceWarningHandler_otherWarningsPassThrough(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	other := "toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", other)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", other)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", other)

	got := recorder.snapshot()
	require.Equal(t, []string{other, other, mcpRegistryDeprecationWarningText, other}, got)
}

func TestMCPRegistryOnceWarningHandler_concurrentResyncsLogOnce(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent warning-handler goroutines")
	}

	assert.Len(t, recorder.snapshot(), 1)
}

func TestInstallMCPRegistryWarningHandler(t *testing.T) {
	// Mutates the process-wide client-go default warning handler.
	t.Cleanup(func() {
		rest.SetDefaultWarningHandlerWithContext(rest.WarningLogger{})
	})

	cfg := &rest.Config{}
	installMCPRegistryWarningHandler(cfg)

	require.NotNil(t, cfg.WarningHandler)
	require.NotNil(t, cfg.WarningHandlerWithContext)

	recorder := &recordingWarningHandler{}
	h, ok := cfg.WarningHandlerWithContext.(*mcpRegistryOnceWarningHandler)
	require.True(t, ok)
	h.next = recorder

	legacy, ok := cfg.WarningHandler.(rest.WarningHandler)
	require.True(t, ok)
	legacy.HandleWarningHeader(299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	cfg.WarningHandlerWithContext.HandleWarningHeaderWithContext(
		t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)

	require.Len(t, recorder.snapshot(), 1)
}
