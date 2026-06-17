// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exampleembedder demonstrates an embedder that obtains a live,
// Kubernetes-backed vMCP backend registry through the public
// backendregistry.NewKubernetesBackendRegistry constructor and wires it into the
// core+Serve assembly WITHOUT importing pkg/vmcp/k8s or k8s.io/client-go/rest.
//
// It backs the acceptance tests for issue #5541: the import-graph test
// (backendregistry/importgraph_test.go) parses this package and asserts it
// imports neither the watch substrate nor the Kubernetes REST package, and the
// package compiling at all proves the constructor's return types plug directly
// into core.Config and server.ServerConfig.
package exampleembedder

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/backendregistry"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// Deps are the core/transport collaborators the embedder wires itself. Only the
// BackendRegistry and the readiness Watcher come from the registry seam; the
// aggregator, router, backend client, and session manager remain the embedder's
// responsibility (out of scope for issue #5541).
type Deps struct {
	Aggregator           aggregator.Aggregator
	Router               router.Router
	BackendClient        vmcp.BackendClient
	SessionManagerConfig *sessionmanager.FactoryConfig
}

// BuildAndServe builds a live backend registry via the public constructor and
// wires it into core.New and server.Serve, returning the constructed
// *server.Server.
func BuildAndServe(ctx context.Context, namespace, group string, deps Deps) (*server.Server, error) {
	// One call replaces the NewDynamicRegistry + rest.InClusterConfig +
	// k8s.NewBackendWatcher + watcher-goroutine wiring an embedder would
	// otherwise copy from cli/serve.go — and removes the direct pkg/vmcp/k8s and
	// k8s.io/client-go/rest imports.
	reg, watcher, err := backendregistry.NewKubernetesBackendRegistry(ctx, namespace, group)
	if err != nil {
		return nil, err
	}

	v, err := core.New(&core.Config{
		BackendRegistry: reg,
		Aggregator:      deps.Aggregator,
		Router:          deps.Router,
		BackendClient:   deps.BackendClient,
	})
	if err != nil {
		return nil, err
	}

	return server.Serve(ctx, v, &server.ServerConfig{
		BackendRegistry:      reg,
		Watcher:              watcher,
		SessionManagerConfig: deps.SessionManagerConfig,
	})
}
