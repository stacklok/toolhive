// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exampleembedder demonstrates an embedder that obtains a running vMCP
// server from the public app.BuildCore + app.BuildServerConfig assembly API,
// extending the decorator example from issue #5447.
//
// This package backs the acceptance tests for issue #5581: verifying that the
// public API types plug directly into server.Serve, and demonstrating the
// complete BuildCore → decorate → BuildServerConfig → server.Serve flow.
package exampleembedder

import (
	"context"

	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp/app"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// BuildServer builds a vMCP server from a serialised vmcpconfig.Config, applying
// the provided decorator before serving. The decoration step is the RFC THV-0076
// extension mechanism: the embedder receives the assembled core.VMCP, wraps it with
// additional behaviour (policy enforcement, observability, scoping), and passes the
// decorated value to server.Serve — without reimplementing vMCP's internal assembly.
//
// For Kubernetes dynamic mode (vmcpCfg.OutgoingAuth.Source == "discovered"):
// call backendregistry.NewKubernetesBackendRegistry once and pass the result via
// app.WithBackendRegistry to share a single K8s informer between BuildCore and
// BuildServerConfig.
//
// server.Serve only assembles the server; the caller must call
// (*server.Server).Start to begin serving HTTP.
func BuildServer(
	ctx context.Context,
	vmcpCfg *vmcpconfig.Config,
	decorate func(core.VMCP) core.VMCP,
	extraOpts ...app.Option,
) (*vmcpserver.Server, error) {
	// Create a late-bound elicitation requester so composite-tool elicitation steps
	// can resolve after the mcp-go server is built by server.Serve.
	elicitation := vmcpserver.NewLateBoundElicitationRequester()

	opts := append([]app.Option{
		app.WithVersion(versions.Version),
		app.WithElicitation(elicitation),
	}, extraOpts...)

	// Build the domain core (config in → VMCP out).
	coreVMCP, coreCleanup, err := app.BuildCore(ctx, vmcpCfg, opts...)
	if err != nil {
		return nil, err
	}
	// Guard: if anything below fails, release the core.
	cleanupCoreOnErr := true
	defer func() {
		if cleanupCoreOnErr {
			coreCleanup()
		}
	}()

	// Apply the embedder-supplied decoration (the RFC THV-0076 extension mechanism).
	// Decorators may only SUBTRACT reachability; they have no path to backends except
	// through the inner VMCP, so they cannot widen access.
	decoratedCore := coreVMCP
	if decorate != nil {
		decoratedCore = decorate(coreVMCP)
	}

	// Build the transport config (same opts → same shared collaborators).
	serverCfg, srvCleanup, err := app.BuildServerConfig(ctx, vmcpCfg, opts...)
	if err != nil {
		return nil, err
	}
	cleanupSrvOnErr := true
	defer func() {
		if cleanupSrvOnErr {
			srvCleanup()
		}
	}()

	srv, err := vmcpserver.Serve(ctx, decoratedCore, serverCfg)
	if err != nil {
		return nil, err
	}

	// Bind the late-bound elicitation to the SDK server so composite-tool
	// elicitation steps reach the right mcp-go session.
	elicitation.Bind(vmcpserver.NewSDKElicitationAdapter(srv.MCPServer()))

	// Success: server.Serve owns the core and server config lifecycles; disarm guards.
	cleanupCoreOnErr = false
	cleanupSrvOnErr = false
	return srv, nil
}
