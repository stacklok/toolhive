// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// Builder is the primary way to assemble a vMCP server from a vmcpconfig.Config.
//
// It owns the single "construct once / wire once" pass that the two derivation
// primitives (BuildCore and BuildServerConfig) leave to the caller: it builds the
// shared stateful collaborators exactly once (telemetry from cfg.Telemetry; the
// Kubernetes backend registry + watcher for the "discovered" outgoing-auth source),
// wires composite-tool elicitation internally (creating and binding the
// LateBoundElicitationRequester so callers never see the construction-order
// chicken-and-egg), applies the caller's decorator, serves, and collapses all
// cleanup into a single func. Finish returns the assembled core alongside the
// server so callers can use it directly for other purposes.
//
// The lower-level BuildCore and BuildServerConfig remain public for advanced
// embedders that need to compose the derived pieces themselves — e.g. to inspect or
// modify the *server.ServerConfig before serving, to reuse the core.VMCP elsewhere,
// or to drive their own serve loop. Builder is implemented on top of them, so the
// two surfaces stay in sync.
//
// # Overrides
//
// Any collaborator the Builder would construct can be replaced by passing the
// corresponding functional Option (they are interface-typed): WithBackendRegistry
// to supply a custom vmcp.BackendRegistry implementation, WithTelemetryProvider,
// WithStatusReporter, WithElicitation, etc. An injected collaborator is used as-is
// and the Builder does not construct its own.
type Builder struct {
	// ctx bounds the assembly and any background goroutines it starts (the
	// Kubernetes backend watcher in discovered mode). Stored at construction to
	// match the NewBuilder(ctx, ...) → Finish() shape; the Builder is a short-lived
	// construction helper, not a long-lived value.
	ctx      context.Context
	cfg      *vmcpconfig.Config
	opts     []Option
	decorate func(core.VMCP) core.VMCP
}

// NewBuilder creates a Builder that assembles a server from cfg and opts. opts carry
// the runtime injectables and any collaborator overrides (see Builder docs).
func NewBuilder(ctx context.Context, cfg *vmcpconfig.Config, opts ...Option) *Builder {
	return &Builder{ctx: ctx, cfg: cfg, opts: opts}
}

// Decorate registers a decorator applied to the assembled core.VMCP before it is
// served — the RFC THV-0076 extension seam. Decorators may only SUBTRACT
// reachability (filter ListTools, deny CallTool); they cannot widen access because
// they hold only the inner VMCP. Returns the Builder for chaining. Calling Decorate
// more than once replaces the previous decorator.
func (b *Builder) Decorate(fn func(core.VMCP) core.VMCP) *Builder {
	b.decorate = fn
	return b
}

// Finish runs the single construction pass and returns the running-ready server, the
// assembled (decorated) core, and a cleanup func that releases everything Finish
// acquired (telemetry provider, rate-limit middleware, embedded auth server, and the
// core's backend connections). The caller must invoke cleanup when done; the server
// is not started (call srv.Start).
//
// A collaborator supplied via options is used as-is; otherwise Finish builds it:
//   - Telemetry: built from cfg.Telemetry when set (unless WithTelemetryProvider).
//   - Backend registry: built ONCE for every mode (unless WithBackendRegistry) and
//     injected into both derivations, so core aggregation and the session manager
//     operate on one snapshot. For the discovered source this is the K8s registry +
//     watcher (VMCP_NAMESPACE must be set); for static/dynamic it runs discovery once.
//   - Elicitation: a LateBoundElicitationRequester is created, threaded into the
//     core, and bound to the SDK server after serving (unless WithElicitation).
func (b *Builder) Finish() (*vmcpserver.Server, core.VMCP, func(), error) {
	if b.cfg == nil {
		return nil, nil, noop, fmt.Errorf("%w: nil vmcp config", vmcp.ErrInvalidConfig)
	}

	o := applyOptions(b.opts)

	// Copy the config and inject token-exchange subject-provider names ONCE, before
	// the single registry build, so the core's backend client and the transport's
	// session factory derive from the same backend-auth metadata (they would
	// otherwise disagree — only BuildServerConfig injects on its own copy).
	cfg, err := b.prepareConfig(o)
	if err != nil {
		return nil, nil, noop, err
	}

	// Build the shared collaborators once, collecting the extra options that inject
	// them into both BuildCore and BuildServerConfig, the cleanups they require, and
	// the elicitation requester to bind after serving (nil if caller-provided).
	extraOpts, cleanups, elicitation, err := b.buildSharedCollaborators(cfg, o)
	if err != nil {
		runCleanup(cleanups)
		return nil, nil, noop, err
	}
	opts := b.combinedOptions(extraOpts)

	coreVMCP, coreCleanup, err := BuildCore(b.ctx, cfg, opts...)
	if err != nil {
		runCleanup(cleanups)
		return nil, nil, noop, err
	}
	cleanups = append(cleanups, coreCleanup)

	if b.decorate != nil {
		coreVMCP = b.decorate(coreVMCP)
	}

	serverCfg, srvCleanup, err := BuildServerConfig(b.ctx, cfg, opts...)
	if err != nil {
		runCleanup(cleanups)
		return nil, nil, noop, err
	}
	cleanups = append(cleanups, srvCleanup)

	srv, err := vmcpserver.Serve(b.ctx, coreVMCP, serverCfg)
	if err != nil {
		runCleanup(cleanups)
		return nil, nil, noop, fmt.Errorf("failed to serve vMCP: %w", err)
	}

	// Bind the late-bound elicitation to the SDK server so composite-tool
	// elicitation steps reach the right mcp-go session. Skipped when the caller
	// supplied their own requester via WithElicitation (they own binding).
	if elicitation != nil {
		elicitation.Bind(vmcpserver.NewSDKElicitationAdapter(srv.MCPServer()))
	}

	cleanup := func() { runCleanup(cleanups) }
	return srv, coreVMCP, cleanup, nil
}

// prepareConfig returns a copy of b.cfg with token-exchange subject-provider names
// injected when an embedded auth server run config is present. Injection is
// idempotent (it only fills empty names), so BuildServerConfig re-running it on its
// own copy is a harmless no-op. It returns an error if subject-provider defaulting
// fails (e.g. a strategy references an unknown provider).
func (b *Builder) prepareConfig(o *options) (*vmcpconfig.Config, error) {
	cfgCopy := *b.cfg
	cfg := &cfgCopy
	if o.authServerRunConfig != nil {
		if cfg.OutgoingAuth != nil {
			cfg.OutgoingAuth = cfg.OutgoingAuth.DeepCopy()
		}
		if err := vmcpconfig.InjectSubjectProviderNames(cfg, o.authServerRunConfig); err != nil {
			return nil, fmt.Errorf("failed to default outgoing auth subject provider names: %w", err)
		}
	}
	return cfg, nil
}

// buildSharedCollaborators constructs the stateful collaborators the Builder owns
// (telemetry, the Kubernetes backend registry, and the elicitation requester) unless
// the caller injected them. It returns the options that inject the built instances,
// the cleanups they require (in acquisition order), and the requester to bind after
// serving (nil when caller-provided).
func (b *Builder) buildSharedCollaborators(
	cfg *vmcpconfig.Config, o *options,
) ([]Option, []func(), *vmcpserver.LateBoundElicitationRequester, error) {
	var extra []Option
	var cleanups []func()

	// Telemetry: built from cfg once, shared by both derivations.
	if o.telemetryProvider == nil && cfg.Telemetry != nil {
		provider, err := telemetry.NewProvider(b.ctx, *cfg.Telemetry)
		if err != nil {
			return nil, cleanups, nil, fmt.Errorf("failed to create telemetry provider: %w", err)
		}
		extra = append(extra, WithTelemetryProvider(provider))
		cleanups = append(cleanups, func() {
			if shutdownErr := provider.Shutdown(b.ctx); shutdownErr != nil {
				slog.Error("failed to shutdown telemetry provider", "error", shutdownErr)
			}
		})
	}

	// Backend registry: built ONCE for every mode (static, dynamic, and discovered)
	// and injected into both derivations, so core aggregation and the session manager
	// operate on one snapshot rather than each running discovery independently. For the
	// discovered source this is also the single informer cache + readiness handle.
	if o.backendRegistry == nil {
		reg, watcher, err := resolveBackendRegistry(b.ctx, cfg, o)
		if err != nil {
			return nil, cleanups, nil, err
		}
		extra = append(extra, WithBackendRegistry(reg, watcher))
	}

	// Elicitation: wired internally so the construction-order late-bind never leaks
	// to the caller. Only when the caller did not supply their own requester.
	var elicitation *vmcpserver.LateBoundElicitationRequester
	if o.elicitation == nil {
		elicitation = vmcpserver.NewLateBoundElicitationRequester()
		extra = append(extra, WithElicitation(elicitation))
	}

	return extra, cleanups, elicitation, nil
}

// combinedOptions returns b.opts followed by the Builder-built extras, without
// mutating b.opts. The extras appear last so they take effect (applyOptions applies
// in order); a caller override for the same collaborator is skipped upstream, so the
// extra for it is never appended.
func (b *Builder) combinedOptions(extra []Option) []Option {
	out := make([]Option, 0, len(b.opts)+len(extra))
	out = append(out, b.opts...)
	out = append(out, extra...)
	return out
}

// isDiscoveredSource reports whether cfg uses the Kubernetes "discovered" outgoing-auth
// source (backends discovered at runtime via the K8s watcher).
func isDiscoveredSource(cfg *vmcpconfig.Config) bool {
	return cfg.OutgoingAuth != nil && cfg.OutgoingAuth.Source == vmcpconfig.OutgoingAuthSourceDiscovered
}
