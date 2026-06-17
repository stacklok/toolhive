// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package backendregistry provides public constructors for vMCP backend
// registries that hide the Kubernetes watch substrate from embedders.
//
// An embedder that wants live, Kubernetes-driven backend discovery would
// otherwise have to replicate the registry+watcher wiring from
// pkg/vmcp/cli/serve.go — constructing a DynamicRegistry, obtaining an
// in-cluster rest.Config, building a k8s.BackendWatcher, and starting its
// goroutine — which forces a direct dependency on pkg/vmcp/k8s and
// k8s.io/client-go/rest. NewKubernetesBackendRegistry bundles that wiring behind
// one call so the embedder depends only on this package plus the public pkg/vmcp
// and pkg/vmcp/server types it already uses.
//
// Per RFC THV-0076/D9 the Kubernetes watch substrate stays internal to vMCP:
// callers receive a vmcp.BackendRegistry (which they treat as read-only) and a
// server.Watcher readiness handle, never the k8s.BackendWatcher itself. The watcher still runs in the
// embedder's Pod, so controller-runtime / k8s.io remain compiled into the binary
// (a transitive dependency); this package removes only the direct import surface.
package backendregistry

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// options holds the optional settings for NewKubernetesBackendRegistry.
type options struct {
	restConfig *rest.Config
}

// Option configures NewKubernetesBackendRegistry.
type Option func(*options)

// WithRESTConfig overrides the default in-cluster Kubernetes REST config used to
// build the backend watcher. The default (no option) is rest.InClusterConfig,
// which is what a vMCP server running inside a Pod needs; supply this option for
// out-of-cluster operation or tests. A caller that uses this option imports
// k8s.io/client-go/rest itself — the default in-cluster path does not.
func WithRESTConfig(cfg *rest.Config) Option {
	return func(o *options) {
		o.restConfig = cfg
	}
}

// NewKubernetesBackendRegistry builds a live, Kubernetes-populated backend
// registry for an embedder, hiding the pkg/vmcp/k8s watch substrate.
//
// It bundles the registry+watcher wiring that pkg/vmcp/cli/serve.go performs by
// hand: it creates an empty DynamicRegistry, builds a k8s.BackendWatcher against
// it (in-cluster rest.Config by default; override with WithRESTConfig), and
// starts the watcher in a background goroutine bound to ctx. The watcher's
// initial informer sync populates the registry — so the registry starts empty,
// and the constructor needs only pkg/vmcp/k8s, not the static-discovery path's
// pkg/groups / pkg/container/runtime dependencies.
//
// Parameters:
//   - ctx: bounds the watcher goroutine's lifetime; cancel it to stop the watcher.
//   - namespace: the Kubernetes namespace to watch for backend resources.
//   - group: the MCPGroup reference whose backends are tracked (parity with
//     cli.Serve's vmcpCfg.Group).
//
// Returns:
//   - vmcp.BackendRegistry: the live registry the watcher mutates, kept current as
//     backends come and go. The narrow return type hides the mutators at compile
//     time but the value is NOT immutable (a type assertion can reach them);
//     embedders should treat it as read-only and let the watcher own all writes.
//     Pass it to core.New (core.Config.BackendRegistry) and server.Serve
//     (server.ServerConfig.BackendRegistry).
//   - server.Watcher: a readiness handle for the /readyz endpoint; pass it to
//     server.ServerConfig.Watcher to gate readiness on informer cache sync.
//
// Lifecycle and failure semantics (the watcher runs in a background goroutine, so
// the contract is not visible in the signature):
//   - The goroutine runs until ctx is cancelled. The caller MUST cancel ctx to
//     release the goroutine and the watcher's informer caches/connections; there
//     is no separate Stop/Close handle, so a caller that never cancels ctx leaks
//     them.
//   - A successful return means the watcher was constructed and started, NOT that
//     it has connected or synced. If watcher.Start later fails (e.g. the REST
//     config points nowhere), the error is logged, not returned — the caller holds
//     a registry that never populates. The returned server.Watcher is the only
//     signal that distinguishes a watcher that started cleanly from one that
//     failed, so the caller MUST wire it into server.ServerConfig.Watcher to gate
//     /readyz on cache sync; otherwise a never-starting watcher goes undetected.
func NewKubernetesBackendRegistry(
	ctx context.Context,
	namespace, group string,
	opts ...Option,
) (vmcp.BackendRegistry, server.Watcher, error) {
	if namespace == "" {
		return nil, nil, fmt.Errorf("namespace cannot be empty")
	}
	if group == "" {
		return nil, nil, fmt.Errorf("group cannot be empty")
	}

	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	restConfig := o.restConfig
	if restConfig == nil {
		var err error
		restConfig, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}
	}

	// The registry+watcher+goroutine wiring below is a near-verbatim copy of
	// cli/serve.go's dynamic-discovery branch (the "discovered" outgoingAuth
	// source). Deduplicating cli.Serve onto this constructor is deferred (it
	// would discard cli.Serve's seeded backends for this start-empty path), so
	// the two copies must stay in sync until that follow-up lands — keep any
	// lifecycle change here mirrored there.
	//
	// Start empty; the watcher's initial informer sync populates the registry.
	dynamicRegistry := vmcp.NewDynamicRegistry(nil)

	watcher, err := k8s.NewBackendWatcher(restConfig, namespace, group, dynamicRegistry)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create backend watcher: %w", err)
	}

	go func() {
		slog.Info("starting Kubernetes backend watcher in background")
		if err := watcher.Start(ctx); err != nil {
			slog.Error("backend watcher stopped", "error", err)
		}
	}()

	return dynamicRegistry, watcher, nil
}
