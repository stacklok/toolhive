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
// +kubebuilder:deprecatedversion. The controller-runtime cache LIST/resync
// path surfaces this header even when zero MCPRegistry CRs exist
// (https://github.com/stacklok/toolhive/issues/6346).
const mcpRegistryDeprecationWarningMarker = "MCPRegistry is deprecated"

// mcpRegistryOnceWarningHandler logs the MCPRegistry CRD deprecation at most
// once (first cache LIST / controller startup) and forwards every other API
// warning to next unchanged.
type mcpRegistryOnceWarningHandler struct {
	once sync.Once
	next rest.WarningHandlerWithContext
}

var (
	_ rest.WarningHandler            = (*mcpRegistryOnceWarningHandler)(nil)
	_ rest.WarningHandlerWithContext = (*mcpRegistryOnceWarningHandler)(nil)
)

func newMCPRegistryOnceWarningHandler(next rest.WarningHandlerWithContext) *mcpRegistryOnceWarningHandler {
	if next == nil {
		next = log.NewKubeAPIWarningLogger(log.KubeAPIWarningLoggerOptions{})
	}
	return &mcpRegistryOnceWarningHandler{next: next}
}

func (h *mcpRegistryOnceWarningHandler) HandleWarningHeader(code int, agent, text string) {
	h.HandleWarningHeaderWithContext(context.Background(), code, agent, text)
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
// resync. Other API warnings keep the default controller-runtime logger.
func installMCPRegistryWarningHandler(cfg *rest.Config) {
	h := newMCPRegistryOnceWarningHandler(rest.WarningLogger{})
	cfg.WarningHandler = h
	cfg.WarningHandlerWithContext = h
	rest.SetDefaultWarningHandlerWithContext(h)
}
