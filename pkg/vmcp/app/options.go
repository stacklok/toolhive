// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app provides the assembly API for creating vMCP servers from a
// vmcpconfig.Config. It encapsulates the wiring that turns configuration into a
// running server, so embedders hand over config and get back a core.VMCP (for
// decoration) and a transport *server.ServerConfig, without reimplementing vMCP's
// internal assembly rules.
//
// # Typical usage
//
//	opts := []app.Option{
//	    app.WithVersion(versions.Version),
//	    app.WithHost(host, port),
//	    app.WithTelemetryProvider(telemetryProvider), // built once, shared
//	    app.WithElicitation(elicitation),             // LateBoundElicitationRequester
//	}
//
//	// For Kubernetes discovered mode (outgoingAuth.source: discovered), also pass the
//	// pre-built registry to share the single informer cache between both Build calls:
//	//   opts = append(opts, app.WithBackendRegistry(reg, watcher))
//
//	core, coreCleanup, err := app.BuildCore(ctx, vmcpCfg, opts...)
//	defer coreCleanup()
//
//	serverCfg, srvCleanup, err := app.BuildServerConfig(ctx, vmcpCfg, opts...)
//	defer srvCleanup()
//
//	srv, err := server.Serve(ctx, core, serverCfg)
//	elicitation.Bind(server.NewSDKElicitationAdapter(srv.MCPServer()))
//	return srv.Start(ctx)
//
// # Shared stateful collaborators
//
// The telemetry provider and (in Kubernetes discovered mode) the backend registry +
// watcher are stateful and must be built once and shared between both Build calls.
// Use WithTelemetryProvider and WithBackendRegistry for this. Callers that omit
// these options accept the following behavior:
//
//   - Telemetry: neither BuildCore nor BuildServerConfig initializes or wires a
//     telemetry provider on their own. Callers who want telemetry MUST build the
//     provider externally and inject it via WithTelemetryProvider. Omitting this
//     option means no telemetry is wired in either function, regardless of what
//     vmcpCfg.Telemetry contains.
//   - Backend registry: for the "discovered" (Kubernetes) outgoingAuth source,
//     WithBackendRegistry is REQUIRED; both BuildCore and BuildServerConfig return an
//     error if it is absent. For static (Backends non-empty) and dynamic (groups
//     manager) modes, each function builds its OWN registry by running discovery
//     independently — so the two registries can snapshot local groups/backend state at
//     different moments and need not be identical. If consistency between the BuildCore
//     and BuildServerConfig registries matters, build one registry yourself and pass it
//     to both via WithBackendRegistry.
package app

import (
	"time"

	authserverconfig "github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// options holds the runtime-injectable collaborators that cannot be derived from the
// serialized vmcpconfig.Config alone.
type options struct {
	// version is the server version string exposed in the MCP protocol.
	version string

	// host and port are the transport bind address. Empty/zero uses server defaults.
	host string
	port int

	// sessionTTL overrides the default session time-to-live. Zero uses the server default.
	sessionTTL time.Duration

	// telemetryProvider is a pre-built telemetry provider shared between BuildCore
	// and BuildServerConfig. See package doc for the consequence of omitting it.
	telemetryProvider *telemetry.Provider

	// backendRegistry and watcher are a pre-built backend registry and its Kubernetes
	// readiness handle. When set, both BuildCore and BuildServerConfig use them directly
	// and skip backend discovery. Required for the "discovered" outgoingAuth source.
	backendRegistry vmcp.BackendRegistry
	watcher         vmcpserver.Watcher

	// authServerRunConfig is the parsed embedded auth server run config. When non-nil,
	// BuildServerConfig creates the EmbeddedAuthServer from it.
	authServerRunConfig *authserverconfig.RunConfig

	// statusReporter is a pre-built status reporter. When nil, BuildServerConfig
	// calls vmcpstatus.NewReporter(), which auto-detects the deployment environment.
	statusReporter vmcpstatus.Reporter

	// elicitation is passed to core.New as the ElicitationRequester. Required when
	// any configured composite tool workflow contains an elicitation step; core.New
	// fails at startup otherwise. Callers using a LateBoundElicitationRequester must
	// call its Bind method after server.Serve returns.
	elicitation vmcp.ElicitationRequester
}

// Option configures BuildCore and BuildServerConfig. Both functions accept the same
// Option slice; an option that applies only to one is silently ignored by the other.
type Option func(*options)

// WithVersion sets the server version string exposed in the MCP protocol.
func WithVersion(v string) Option {
	return func(o *options) { o.version = v }
}

// WithHost sets the transport bind address and port for BuildServerConfig.
func WithHost(host string, port int) Option {
	return func(o *options) {
		o.host = host
		o.port = port
	}
}

// WithSessionTTL overrides the session time-to-live in BuildServerConfig.
func WithSessionTTL(d time.Duration) Option {
	return func(o *options) { o.sessionTTL = d }
}

// WithTelemetryProvider injects a pre-built telemetry provider shared between
// BuildCore and BuildServerConfig. Callers that configure telemetry SHOULD pass
// the same provider to both calls via this option to avoid duplicate OTEL pipelines.
func WithTelemetryProvider(p *telemetry.Provider) Option {
	return func(o *options) { o.telemetryProvider = p }
}

// WithBackendRegistry injects a pre-built backend registry and its Kubernetes
// readiness watcher into both BuildCore and BuildServerConfig. Both functions use
// the same registry instance, ensuring the core aggregates and the session manager
// opens connections to exactly the same set of backends.
//
// Required for the "discovered" (Kubernetes) outgoingAuth source. For static and
// dynamic modes it is optional: each function builds its own registry from vmcpCfg.
//
// For Kubernetes mode, construct the registry with
// backendregistry.NewKubernetesBackendRegistry and pass both return values here.
//
// It panics if reg is nil: a nil registry is a programmer error (the option would
// silently fall through to discovery, defeating its purpose and producing a confusing
// "WithBackendRegistry is required" error downstream). w may be nil (no readiness gating).
func WithBackendRegistry(reg vmcp.BackendRegistry, w vmcpserver.Watcher) Option {
	if reg == nil {
		panic("app.WithBackendRegistry: reg must not be nil")
	}
	return func(o *options) {
		o.backendRegistry = reg
		o.watcher = w
	}
}

// WithAuthServerRunConfig provides the parsed embedded auth server run config.
// BuildServerConfig creates the EmbeddedAuthServer from it when non-nil.
// The caller is responsible for loading this from the authserver-config.yaml sibling
// file (see pkg/vmcp/cli for the load helper).
func WithAuthServerRunConfig(rc *authserverconfig.RunConfig) Option {
	return func(o *options) { o.authServerRunConfig = rc }
}

// WithStatusReporter injects a pre-built status reporter into BuildServerConfig.
// When nil, BuildServerConfig calls vmcpstatus.NewReporter(), which auto-detects
// whether to use the Kubernetes or no-op reporter.
func WithStatusReporter(r vmcpstatus.Reporter) Option {
	return func(o *options) { o.statusReporter = r }
}

// WithElicitation provides the ElicitationRequester passed to core.New by BuildCore.
// It is required when any configured composite tool workflow contains an elicitation
// step; core.New returns vmcp.ErrInvalidConfig at startup otherwise.
//
// Typically callers provide a *server.LateBoundElicitationRequester and call its Bind
// method with server.NewSDKElicitationAdapter(srv.MCPServer()) after server.Serve returns.
func WithElicitation(e vmcp.ElicitationRequester) Option {
	return func(o *options) { o.elicitation = e }
}

// applyOptions initialises an options struct from the provided Option slice.
// A nil Option is skipped so conditional `append`-based option lists (which may
// leave a nil entry) do not panic.
func applyOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	return o
}
