// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"
	"sync"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// mcpRegistryDeprecationWarningMarker is the unique prefix of the Warning
// header kube-apiserver sends because the MCPRegistry CRD is marked
// +kubebuilder:deprecatedversion. The cache LIST/WATCH path surfaces this
// header even when zero MCPRegistry CRs exist — the repeating trigger is
// client-go re-establishing the WATCH every 5–10m (reflector timeout), not
// the 10h default resync (https://github.com/stacklok/toolhive/issues/6346).
const mcpRegistryDeprecationWarningMarker = "MCPRegistry is deprecated"

// mcpRegistryOnceWarningHandler logs the MCPRegistry CRD deprecation at most
// once (first WATCH) and forwards every other API warning to next unchanged.
// next is the controller-runtime KubeAPIWarningLogger (slog JSON) when
// constructed with nil. We occupy cfg.WarningHandlerWithContext, so CRT does
// not auto-install its own handler — other warnings stay on slog because
// next *is* that CRT logger, not because CRT auto-installs one.
type mcpRegistryOnceWarningHandler struct {
	once sync.Once
	next rest.WarningHandlerWithContext
}

var _ rest.WarningHandlerWithContext = (*mcpRegistryOnceWarningHandler)(nil)

func newMCPRegistryOnceWarningHandler(next rest.WarningHandlerWithContext) *mcpRegistryOnceWarningHandler {
	if next == nil {
		next = log.NewKubeAPIWarningLogger(log.KubeAPIWarningLoggerOptions{})
	}
	return &mcpRegistryOnceWarningHandler{next: next}
}

func (h *mcpRegistryOnceWarningHandler) HandleWarningHeaderWithContext(
	ctx context.Context, code int, agent, text string,
) {
	if code == 299 && strings.Contains(text, mcpRegistryDeprecationWarningMarker) {
		h.once.Do(func() {
			h.next.HandleWarningHeaderWithContext(ctx, code, agent, text)
		})
		return
	}
	h.next.HandleWarningHeaderWithContext(ctx, code, agent, text)
}

// installMCPRegistryWarningHandler attaches a once-only handler for the
// MCPRegistry CRD deprecation so the cache informer cannot spam it on every
// WATCH re-establishment (client-go re-watches every 5–10m). Other API
// warnings keep the controller-runtime slog JSON path because next is
// KubeAPIWarningLogger — not because CRT auto-installs one (we occupy the
// slot). Pass nil for next in production so the constructor installs that
// CRT logger; rest.WarningLogger{} would reroute other warnings to klog.
func installMCPRegistryWarningHandler(cfg *rest.Config, next rest.WarningHandlerWithContext) *mcpRegistryOnceWarningHandler {
	h := newMCPRegistryOnceWarningHandler(next)
	cfg.WarningHandlerWithContext = h
	return h
}
