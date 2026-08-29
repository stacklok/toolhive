// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
	crtlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// mcpRegistryDeprecationWarningText is the exact Warning header kube-apiserver
// emits for the MCPRegistry +kubebuilder:deprecatedversion marker. Matches
// issue https://github.com/stacklok/toolhive/issues/6346.
const mcpRegistryDeprecationWarningText = "MCPRegistry is deprecated and will be removed in a future release; " +
	"install the ToolHive registry server via the toolhive-registry-server Helm chart " +
	"(https://github.com/stacklok/toolhive-registry-server) instead"

// recordedWarning is one invocation of the next handler. Tests assert
// handlerType (concrete next), call count, and forwarded text so the
// production log path is pinned: *log.KubeAPIWarningLogger (CRT slog JSON),
// never rest.WarningLogger (klog + "Warning: " prefix).
type recordedWarning struct {
	handlerType string
	code        int
	text        string
}

// recordingWarningHandler records every warning the operator handler forwards.
// If inner is set, each call is also delivered to inner and handlerType is
// inner's concrete type (used to prove production next is the CRT logger).
type recordingWarningHandler struct {
	inner rest.WarningHandlerWithContext
	mu    sync.Mutex
	calls []recordedWarning
}

func (h *recordingWarningHandler) HandleWarningHeaderWithContext(
	ctx context.Context, code int, agent, text string,
) {
	typ := fmt.Sprintf("%T", h)
	if h.inner != nil {
		typ = fmt.Sprintf("%T", h.inner)
	}
	h.mu.Lock()
	h.calls = append(h.calls, recordedWarning{handlerType: typ, code: code, text: text})
	h.mu.Unlock()
	if h.inner != nil {
		h.inner.HandleWarningHeaderWithContext(ctx, code, agent, text)
	}
}

func (h *recordingWarningHandler) snapshot() []recordedWarning {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedWarning, len(h.calls))
	copy(out, h.calls)
	return out
}

func (h *recordingWarningHandler) texts() []string {
	got := h.snapshot()
	out := make([]string, len(got))
	for i, c := range got {
		out[i] = c.text
	}
	return out
}

var _ rest.WarningHandlerWithContext = (*recordingWarningHandler)(nil)

// TestMCPRegistryOnceWarningHandler_deprecationLoggedOnceAcrossWatchReestablishments
// reproduces #6346: kube-apiserver sends the MCPRegistry deprecation Warning
// header on every WATCH re-establishment (client-go reflector timeout every
// 5–10m), even when no MCPRegistry CRs exist. The operator must surface that
// warning at most once.
func TestMCPRegistryOnceWarningHandler_deprecationLoggedOnceAcrossWatchReestablishments(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	// Two WATCH re-establishments / two warning-path invocations, no CRs involved.
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)

	got := recorder.snapshot()
	require.NotEmpty(t, got, "the first WATCH must still surface the deprecation warning")
	assert.Len(t, got, 1,
		"MCPRegistry deprecation warning must be logged once, not on every WATCH re-establishment; got %d: %v",
		len(got), recorder.texts())
	assert.Equal(t, "*app.recordingWarningHandler", got[0].handlerType)
	assert.Equal(t, 299, got[0].code)
	assert.Contains(t, got[0].text, mcpRegistryDeprecationWarningMarker)
}

func TestMCPRegistryOnceWarningHandler_forwards(t *testing.T) {
	t.Parallel()

	other := "toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"

	tests := []struct {
		name  string
		calls []struct {
			code int
			text string
		}
		want []string
	}{
		{
			name: "unrelated 299 warnings pass through every time",
			calls: []struct {
				code int
				text string
			}{
				{299, other},
				{299, other},
			},
			want: []string{other, other},
		},
		{
			name: "MCPRegistry deprecation 299 is forwarded once",
			calls: []struct {
				code int
				text string
			}{
				{299, mcpRegistryDeprecationWarningText},
				{299, mcpRegistryDeprecationWarningText},
			},
			want: []string{mcpRegistryDeprecationWarningText},
		},
		{
			name: "non-299 with marker passes through every time",
			calls: []struct {
				code int
				text string
			}{
				{199, mcpRegistryDeprecationWarningText},
				{199, mcpRegistryDeprecationWarningText},
			},
			want: []string{mcpRegistryDeprecationWarningText, mcpRegistryDeprecationWarningText},
		},
		{
			name: "mixed: other warnings always, deprecation once, non-299 marker always",
			calls: []struct {
				code int
				text string
			}{
				{299, other},
				{299, other},
				{299, mcpRegistryDeprecationWarningText},
				{299, mcpRegistryDeprecationWarningText},
				{299, other},
				{199, mcpRegistryDeprecationWarningText},
			},
			want: []string{other, other, mcpRegistryDeprecationWarningText, other, mcpRegistryDeprecationWarningText},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingWarningHandler{}
			handler := newMCPRegistryOnceWarningHandler(recorder)
			for _, c := range tc.calls {
				handler.HandleWarningHeaderWithContext(t.Context(), c.code, "kube-apiserver", c.text)
			}
			got := recorder.snapshot()
			require.Equal(t, tc.want, recorder.texts())
			require.Len(t, got, len(tc.want))
			for _, c := range got {
				assert.Equal(t, "*app.recordingWarningHandler", c.handlerType)
			}
		})
	}
}

func TestMCPRegistryOnceWarningHandler_concurrentWatchReestablishmentsLogOnce(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
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

	got := recorder.snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, "*app.recordingWarningHandler", got[0].handlerType)
	assert.Equal(t, mcpRegistryDeprecationWarningText, got[0].text)
}

func TestMCPRegistryOnceWarningHandler_productionNextIsCRTLogger(t *testing.T) {
	t.Parallel()

	// Production next must be *log.KubeAPIWarningLogger (controller-runtime
	// slog JSON via log.FromContext). rest.WarningLogger would prepend
	// "Warning: " and write through klog, bypassing ctrl.SetLogger.
	prod := newMCPRegistryOnceWarningHandler(nil)
	require.IsType(t, &crtlog.KubeAPIWarningLogger{}, prod.next,
		"production next must be *log.KubeAPIWarningLogger, never rest.WarningLogger")
	_, isRestWarningLogger := prod.next.(rest.WarningLogger)
	require.False(t, isRestWarningLogger, "production next must never be rest.WarningLogger")

	// Wrap that same CRT logger so we record type + call count + text
	// without mutating next after construction.
	recorder := &recordingWarningHandler{inner: prod.next}
	handler := newMCPRegistryOnceWarningHandler(recorder)

	other := "admission: would violate PodSecurity"
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", other)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	handler.HandleWarningHeaderWithContext(t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)

	got := recorder.snapshot()
	require.Len(t, got, 2, "other warning always + MCPRegistry deprecation once")
	assert.Equal(t, "*log.KubeAPIWarningLogger", got[0].handlerType)
	assert.Equal(t, 299, got[0].code)
	assert.Equal(t, other, got[0].text)
	assert.Equal(t, "*log.KubeAPIWarningLogger", got[1].handlerType)
	assert.Equal(t, 299, got[1].code)
	assert.Equal(t, mcpRegistryDeprecationWarningText, got[1].text)
}

func TestInstallMCPRegistryWarningHandler(t *testing.T) {
	t.Parallel()

	recorder := &recordingWarningHandler{}
	cfg := &rest.Config{}
	h := installMCPRegistryWarningHandler(cfg, recorder)

	require.Nil(t, cfg.WarningHandler,
		"legacy WarningHandler must stay unset; client-go WithContext wins")
	require.Same(t, h, cfg.WarningHandlerWithContext)

	cfg.WarningHandlerWithContext.HandleWarningHeaderWithContext(
		t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)
	cfg.WarningHandlerWithContext.HandleWarningHeaderWithContext(
		t.Context(), 299, "kube-apiserver", mcpRegistryDeprecationWarningText)

	got := recorder.snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, "*app.recordingWarningHandler", got[0].handlerType)
	assert.Equal(t, mcpRegistryDeprecationWarningText, got[0].text)
}

func TestInstallMCPRegistryWarningHandler_productionNextIsCRTLogger(t *testing.T) {
	t.Parallel()

	cfg := &rest.Config{}
	h := installMCPRegistryWarningHandler(cfg, nil)

	require.Nil(t, cfg.WarningHandler)
	require.Same(t, h, cfg.WarningHandlerWithContext)
	require.IsType(t, &crtlog.KubeAPIWarningLogger{}, h.next,
		"production next must be *log.KubeAPIWarningLogger, never rest.WarningLogger")
	_, isRestWarningLogger := h.next.(rest.WarningLogger)
	require.False(t, isRestWarningLogger)
}

// TestMCPRegistryDeprecationWarningMarkerMatchesCRD parses the generated
// MCPRegistry CRD YAML (the generator hard-wraps deprecationWarning
// mid-sentence) and asserts both served versions still contain the sentinel
// the once-handler matches. A grepped string test would miss the wrap and
// stay green after a marker reword that silently re-enables 5–10m spam.
func TestMCPRegistryDeprecationWarningMarkerMatchesCRD(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "deploy", "charts", "operator-crds", "files", "crds",
		"toolhive.stacklok.dev_mcpregistries.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	var crd struct {
		Spec struct {
			Versions []struct {
				Name               string `yaml:"name"`
				Deprecated         bool   `yaml:"deprecated"`
				DeprecationWarning string `yaml:"deprecationWarning"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd),
		"parse CRD YAML (do not grep: generator hard-wraps deprecationWarning)")

	found := map[string]string{}
	for _, v := range crd.Spec.Versions {
		if !v.Deprecated && v.DeprecationWarning == "" {
			continue
		}
		require.Contains(t, v.DeprecationWarning, mcpRegistryDeprecationWarningMarker,
			"CRD version %s deprecationWarning drifted from mcpRegistryDeprecationWarningMarker", v.Name)
		found[v.Name] = v.DeprecationWarning
	}
	require.Contains(t, found, "v1alpha1", "expected v1alpha1 deprecationWarning in generated CRD")
	require.Contains(t, found, "v1beta1", "expected v1beta1 deprecationWarning in generated CRD")
}
