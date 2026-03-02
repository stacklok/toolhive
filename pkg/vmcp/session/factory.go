// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

const (
	defaultMaxBackendInitConcurrency = 10
	defaultBackendInitTimeout        = 30 * time.Second

	// MetadataKeyIdentitySubject is the transport-session metadata key that
	// holds the subject claim of the authenticated caller (identity.Subject).
	// Set at session creation; empty for anonymous callers.
	MetadataKeyIdentitySubject = "vmcp.identity.subject"

	// MetadataKeyBackendIDs is the transport-session metadata key that holds
	// a comma-separated, sorted list of successfully-connected backend IDs.
	// The key is omitted entirely when no backends connected.
	MetadataKeyBackendIDs = "vmcp.backend.ids"
)

// MultiSessionFactory creates new MultiSessions for connecting clients.
type MultiSessionFactory interface {
	// MakeSession creates a new MultiSession for the given identity against the
	// provided set of backends. Backend clients are initialised in parallel
	// with bounded concurrency (see WithMaxBackendInitConcurrency).
	//
	// Partial initialisation: if a backend fails to initialise, a warning is
	// logged and the session continues with the remaining backends. The caller
	// receives a valid session as long as at least one backend succeeded.
	//
	// If all backends fail, MakeSession still returns a valid (empty) session
	// rather than an error, allowing clients to connect even when all backends
	// are temporarily unavailable.
	MakeSession(ctx context.Context, identity *auth.Identity, backends []*vmcp.Backend) (MultiSession, error)
}

// backendConnector creates a connected, initialised backend Session for use
// within a single MultiSession. It is called once per backend during MakeSession.
//
// The connector is responsible for:
//  1. Creating and starting the MCP client transport.
//  2. Running the MCP Initialize handshake.
//  3. Querying backend capabilities (tools, resources, prompts).
//
// The returned backend.Session owns the underlying transport connection and
// must be closed when the session ends. The returned CapabilityList is used
// to populate the session's routing table and capability lists.
//
// On error the factory treats the failure as a partial failure: a warning is
// logged and the backend is excluded from the session.
type backendConnector func(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
) (backend.Session, *vmcp.CapabilityList, error)

// defaultMultiSessionFactory is the production MultiSessionFactory implementation.
type defaultMultiSessionFactory struct {
	connector          backendConnector
	maxConcurrency     int
	backendInitTimeout time.Duration
}

// MultiSessionFactoryOption configures a defaultMultiSessionFactory.
type MultiSessionFactoryOption func(*defaultMultiSessionFactory)

// WithMaxBackendInitConcurrency sets the maximum number of backends that are
// initialised concurrently during MakeSession. Defaults to 10.
func WithMaxBackendInitConcurrency(n int) MultiSessionFactoryOption {
	return func(f *defaultMultiSessionFactory) {
		if n > 0 {
			f.maxConcurrency = n
		}
	}
}

// WithBackendInitTimeout sets the per-backend timeout during MakeSession.
// Defaults to 30 s.
func WithBackendInitTimeout(d time.Duration) MultiSessionFactoryOption {
	return func(f *defaultMultiSessionFactory) {
		if d > 0 {
			f.backendInitTimeout = d
		}
	}
}

// NewSessionFactory creates a MultiSessionFactory that connects to backends
// over HTTP using the given outgoing auth registry.
func NewSessionFactory(registry vmcpauth.OutgoingAuthRegistry, opts ...MultiSessionFactoryOption) MultiSessionFactory {
	return newSessionFactoryWithConnector(backend.NewHTTPConnector(registry), opts...)
}

// newSessionFactoryWithConnector creates a MultiSessionFactory backed by an
// arbitrary connector. Used by tests to inject a fake connector without
// requiring real HTTP backends.
func newSessionFactoryWithConnector(connector backendConnector, opts ...MultiSessionFactoryOption) MultiSessionFactory {
	f := &defaultMultiSessionFactory{
		connector:          connector,
		maxConcurrency:     defaultMaxBackendInitConcurrency,
		backendInitTimeout: defaultBackendInitTimeout,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// initResult captures the outcome of initialising a single backend.
type initResult struct {
	target *vmcp.BackendTarget
	conn   backend.Session
	caps   *vmcp.CapabilityList
}

// initOneBackend attempts to connect and initialise a single backend.
// It is called from a goroutine inside MakeSession and handles all partial-
// initialisation cases: connector errors, and nil conn/caps without an error.
// Returns a non-nil *initResult on success, nil when the backend should be
// skipped (failure already logged as a warning).
func (f *defaultMultiSessionFactory) initOneBackend(
	ctx context.Context,
	b *vmcp.Backend,
	identity *auth.Identity,
) *initResult {
	bCtx, cancel := context.WithTimeout(ctx, f.backendInitTimeout)
	defer cancel()

	target := vmcp.BackendToTarget(b)
	conn, caps, err := f.connector(bCtx, target, identity)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		slog.Warn("Failed to initialise backend for session; continuing without it",
			"backendID", b.ID,
			"backendName", b.Name,
			"error", err,
		)
		return nil
	}
	if conn == nil || caps == nil {
		if conn != nil {
			_ = conn.Close()
		}
		slog.Warn("Backend connector returned nil conn or caps with no error; skipping backend",
			"backendID", b.ID,
			"backendName", b.Name,
		)
		return nil
	}
	return &initResult{target: target, conn: conn, caps: caps}
}

// buildRoutingTable populates a RoutingTable and capability lists from a sorted
// slice of initResults. Results must be pre-sorted by WorkloadID so that the
// alphabetically-earlier backend wins when two backends share a capability name.
func buildRoutingTable(results []initResult) (*vmcp.RoutingTable, []vmcp.Tool, []vmcp.Resource, []vmcp.Prompt) {
	rt := &vmcp.RoutingTable{
		Tools:     make(map[string]*vmcp.BackendTarget),
		Resources: make(map[string]*vmcp.BackendTarget),
		Prompts:   make(map[string]*vmcp.BackendTarget),
	}
	var tools []vmcp.Tool
	var resources []vmcp.Resource
	var prompts []vmcp.Prompt

	for _, r := range results {
		for _, tool := range r.caps.Tools {
			if _, ok := rt.Tools[tool.Name]; !ok {
				tools = append(tools, tool)
				rt.Tools[tool.Name] = r.target
			}
		}
		for _, res := range r.caps.Resources {
			if _, ok := rt.Resources[res.URI]; !ok {
				resources = append(resources, res)
				rt.Resources[res.URI] = r.target
			}
		}
		for _, prompt := range r.caps.Prompts {
			if _, ok := rt.Prompts[prompt.Name]; !ok {
				prompts = append(prompts, prompt)
				rt.Prompts[prompt.Name] = r.target
			}
		}
	}
	return rt, tools, resources, prompts
}

// MakeSession implements MultiSessionFactory.
func (f *defaultMultiSessionFactory) MakeSession(
	ctx context.Context,
	identity *auth.Identity,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	// Filter nil entries upfront so that every downstream dereference of a
	// *vmcp.Backend is safe. Nil entries are logged and skipped, consistent
	// with the partial-initialisation approach used for failed backends.
	filtered := make([]*vmcp.Backend, 0, len(backends))
	for _, b := range backends {
		if b == nil {
			slog.Warn("Skipping nil backend entry during session creation")
			continue
		}
		filtered = append(filtered, b)
	}
	backends = filtered

	// Initialise backends in parallel with bounded concurrency.
	// Each goroutine writes to its own index so no lock on the slice is needed.
	rawResults := make([]*initResult, len(backends))
	sem := make(chan struct{}, f.maxConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(backends))
	for i, b := range backends {
		go func(i int, b *vmcp.Backend) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rawResults[i] = f.initOneBackend(ctx, b, identity)
		}(i, b)
	}
	wg.Wait()

	// Collect successful results; sort by WorkloadID so that capability-name
	// conflicts are resolved deterministically: the alphabetically-earlier
	// backend always wins.
	connections := make(map[string]backend.Session, len(backends))
	backendSessions := make(map[string]string, len(backends))
	results := make([]initResult, 0, len(backends))
	for _, r := range rawResults {
		if r == nil {
			continue
		}
		connections[r.target.WorkloadID] = r.conn
		backendSessions[r.target.WorkloadID] = r.conn.SessionID()
		results = append(results, *r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].target.WorkloadID < results[j].target.WorkloadID
	})

	if len(results) == 0 && len(backends) > 0 {
		slog.Warn("All backends failed to initialise; session will have no capabilities",
			"backendCount", len(backends))
	}

	// Build the routing table; first-writer (alphabetically) wins on conflicts.
	routingTable, allTools, allResources, allPrompts := buildRoutingTable(results)

	sessID := uuid.New().String()
	transportSess := transportsession.NewStreamableSession(sessID)

	// Populate serialisable metadata so that the embedded transport session
	// carries the identity reference and connected backend list when persisted
	// via transportsession.Storage.
	if identity != nil && identity.Subject != "" {
		transportSess.SetMetadata(MetadataKeyIdentitySubject, identity.Subject)
	}
	if len(results) > 0 {
		// IDs are extracted from the already-sorted results slice to avoid a
		// second sort of the connections map.
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.target.WorkloadID
		}
		transportSess.SetMetadata(MetadataKeyBackendIDs, strings.Join(ids, ","))
	}

	return &defaultMultiSession{
		Session:         transportSess,
		connections:     connections,
		routingTable:    routingTable,
		tools:           allTools,
		resources:       allResources,
		prompts:         allPrompts,
		backendSessions: backendSessions,
		queue:           newAdmissionQueue(),
	}, nil
}
