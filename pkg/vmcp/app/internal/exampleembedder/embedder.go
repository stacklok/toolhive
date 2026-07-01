// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exampleembedder demonstrates an embedder that obtains a running vMCP
// server from the public app.Builder — the single assembly entry point — extending
// the decorator example from issue #5447.
//
// This package backs the acceptance tests for issue #5581: verifying that the public
// API plugs directly into a working server, and demonstrating the complete
// NewBuilder → Decorate → Finish flow. The builder owns "construct once / wire once":
// the embedder does not build telemetry, the backend registry, or the elicitation
// requester, and does not choreograph the late-bind — it hands over config, decorates
// the core, and receives the server plus a single cleanup func.
package exampleembedder

import (
	"context"

	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp/app"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// BuildServer assembles a vMCP server from a serialised vmcpconfig.Config via the
// app.Builder, applying the provided decorator (the RFC THV-0076 extension seam)
// before serving. It returns the server and a cleanup func the caller must invoke
// when done; server.Serve only assembles the server, so the caller must still call
// (*server.Server).Start to begin serving HTTP.
//
// For Kubernetes discovered mode (vmcpCfg.OutgoingAuth.Source == "discovered") the
// builder constructs the backend registry + watcher itself (VMCP_NAMESPACE must be
// set); an embedder that wants to own that registry can inject it via
// app.WithBackendRegistry in extraOpts.
func BuildServer(
	ctx context.Context,
	vmcpCfg *vmcpconfig.Config,
	decorate func(core.VMCP) core.VMCP,
	extraOpts ...app.Option,
) (*vmcpserver.Server, func(), error) {
	opts := append([]app.Option{app.WithVersion(versions.Version)}, extraOpts...)

	// One call replaces the manual BuildCore → decorate → BuildServerConfig →
	// server.Serve → elicitation.Bind sequence and its cleanup choreography: the
	// builder shares collaborators across both derivations, wires elicitation
	// internally, and returns a single cleanup func.
	srv, _, cleanup, err := app.NewBuilder(ctx, vmcpCfg, opts...).
		Decorate(decorate).
		Finish()
	if err != nil {
		return nil, nil, err
	}
	return srv, cleanup, nil
}
