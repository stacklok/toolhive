// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:generate mockgen -destination=mocks/mock_factory.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/session MultiSessionFactory

package session

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	defaultMaxBackendInitConcurrency = 10
	defaultBackendInitTimeout        = 30 * time.Second

	// MetadataKeyBackendIDs is the transport-session metadata key that holds
	// a comma-separated, sorted list of successfully-connected backend IDs.
	// The key is always written, even as an empty string for zero-backend
	// sessions. Key presence distinguishes an explicit zero-backend state from
	// absent/corrupted metadata in RestoreSession.
	MetadataKeyBackendIDs = "vmcp.backend.ids"

	// MetadataKeyBackendSessionPrefix is the key prefix for per-backend session IDs.
	// Full key: MetadataKeyBackendSessionPrefix + workloadID → backend_session_id.
	// Used by RestoreSession to reconnect backends with the correct session hint.
	MetadataKeyBackendSessionPrefix = "vmcp.backend.session."
)

// MultiSessionFactory creates new MultiSessions for connecting clients.
type MultiSessionFactory interface {
	// MakeSessionWithID creates a new MultiSession with a specific session ID.
	// This is used by SessionManager to create sessions using the SDK-assigned ID
	// rather than generating a new UUID internally.
	//
	// The id parameter must be non-empty and should be a valid MCP session ID
	// (visible ASCII characters, 0x21 to 0x7E per the MCP specification).
	//
	// Whether the session allows anonymous (nil) caller identity is derived
	// internally from identity via ShouldAllowAnonymous.
	//
	// All other behaviour (partial initialisation, bounded concurrency, etc.)
	// is identical to MakeSession.
	MakeSessionWithID(
		ctx context.Context,
		id string,
		identity *auth.Identity,
		backends []*vmcp.Backend,
	) (MultiSession, error)

	// RestoreSession reconstructs a live MultiSession from persisted metadata.
	// It reconnects to the backends whose IDs are listed in storedMetadata under
	// MetadataKeyBackendIDs, rebuilds the routing table, and reapplies the
	// session-binding decorator from the stored identity binding.
	//
	// Use this when the node-local session cache misses — for example after a
	// pod restart or when a request is routed to a different pod. It is more
	// expensive than a cache hit because it opens new backend connections.
	// Because MCP clients cannot be serialised, sticky sessions (session affinity
	// at the load balancer) minimise how often this path is taken.
	//
	// allBackends is the current backend list from the registry; RestoreSession
	// filters it to the subset originally included in this session.
	RestoreSession(
		ctx context.Context,
		id string,
		storedMetadata map[string]string,
		allBackends []*vmcp.Backend,
	) (MultiSession, error)
}

// backendConnector creates a connected, initialised backend Session for use
// within a single MultiSession. It is called once per backend during MakeSession.
//
// The connector is responsible for:
//  1. Creating and starting the MCP client transport.
//  2. Running the MCP Initialize handshake.
//  3. Querying backend capabilities (tools, resources, prompts).
//
// sessionHint is the backend-assigned session ID from a prior connection (stored
// in Redis metadata). When non-empty the connector should send it as the
// Mcp-Session-Id hint during Initialize so the backend can resume rather than
// re-initialize. Pass an empty string for brand-new sessions.
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
	sessionHint string,
) (backend.Session, *vmcp.CapabilityList, error)

// defaultMultiSessionFactory is the production MultiSessionFactory implementation.
type defaultMultiSessionFactory struct {
	connector          backendConnector
	maxConcurrency     int
	backendInitTimeout time.Duration
	aggregator         aggregator.Aggregator // Optional: applies tool transforms (overrides, conflict resolution, filter)
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

// WithAggregator configures the factory to apply per-backend tool overrides,
// conflict resolution, and advertising filters when building sessions.
// If not set, raw backend tool names are used unchanged.
func WithAggregator(agg aggregator.Aggregator) MultiSessionFactoryOption {
	return func(f *defaultMultiSessionFactory) {
		f.aggregator = agg
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
	sessionHint string,
) *initResult {
	bCtx, cancel := context.WithTimeout(ctx, f.backendInitTimeout)
	defer cancel()

	target := vmcp.BackendToTarget(b)
	conn, caps, err := f.connector(bCtx, target, identity, sessionHint)
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

// buildRoutingTableWithAggregator applies the aggregator's full transformation
// pipeline (overrides, conflict resolution, advertising filter) to the raw
// backend capabilities in results, producing resolved tool names identical to
// the standard aggregation path. Resources and prompts pass through unchanged.
//
// Returns the routing table, advertised tools (for MCP clients), all resolved
// tools (for schema lookup), resources, prompts, and any error.
func buildRoutingTableWithAggregator(
	ctx context.Context,
	agg aggregator.Aggregator,
	results []initResult,
) (*vmcp.RoutingTable, []vmcp.Tool, []vmcp.Tool, []vmcp.Resource, []vmcp.Prompt, error) {
	toolsByBackend := make(map[string][]vmcp.Tool, len(results))
	targets := make(map[string]*vmcp.BackendTarget, len(results))
	for i := range results {
		r := &results[i]
		toolsByBackend[r.target.WorkloadID] = r.caps.Tools
		targets[r.target.WorkloadID] = r.target
	}

	advertisedTools, allResolvedTools, toolsRouting, err := agg.ProcessPreQueriedCapabilities(ctx, toolsByBackend, targets)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	rt := &vmcp.RoutingTable{
		Tools:     toolsRouting,
		Resources: make(map[string]*vmcp.BackendTarget),
		Prompts:   make(map[string]*vmcp.BackendTarget),
	}

	var allResources []vmcp.Resource
	var allPrompts []vmcp.Prompt
	for _, r := range results {
		for _, res := range r.caps.Resources {
			if _, ok := rt.Resources[res.URI]; !ok {
				allResources = append(allResources, res)
				rt.Resources[res.URI] = r.target
			}
		}
		for _, prompt := range r.caps.Prompts {
			if _, ok := rt.Prompts[prompt.Name]; !ok {
				allPrompts = append(allPrompts, prompt)
				rt.Prompts[prompt.Name] = r.target
			}
		}
	}

	return rt, advertisedTools, allResolvedTools, allResources, allPrompts, nil
}

// MakeSessionWithID implements MultiSessionFactory.
func (f *defaultMultiSessionFactory) MakeSessionWithID(
	ctx context.Context,
	id string,
	identity *auth.Identity,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	return f.makeSession(ctx, id, identity, backends)
}

// validateSessionID checks that id is non-empty and contains only visible
// ASCII characters (0x21–0x7E) as required by the MCP specification.
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session ID must not be empty")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c < 0x21 || c > 0x7E {
			return fmt.Errorf("session ID contains invalid character at index %d (0x%02X): must be visible ASCII (0x21–0x7E)", i, c)
		}
	}
	return nil
}

// populateBackendMetadata writes backend metadata to the transport session.
// It writes MetadataKeyBackendIDs (comma-separated, sorted workload IDs) and,
// for each backend that reports a non-empty session ID,
// MetadataKeyBackendSessionPrefix+workloadID. Backends with an empty session ID
// (e.g. SSE transports) are included in MetadataKeyBackendIDs but have no
// per-session-ID key, so downstream restore logic can treat key presence as a
// usable hint. IDs are extracted from the already-sorted results slice to avoid
// a second sort.
func populateBackendMetadata(transportSess transportsession.Session, results []initResult) {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.target.WorkloadID
		if sessID := r.conn.SessionID(); sessID != "" {
			transportSess.SetMetadata(MetadataKeyBackendSessionPrefix+r.target.WorkloadID, sessID)
		}
	}
	// Always write MetadataKeyBackendIDs — key presence distinguishes explicit
	// zero-backend from absent/corrupted metadata (see const doc).
	transportSess.SetMetadata(MetadataKeyBackendIDs, strings.Join(ids, ","))
}

// makeBaseSession initialises backends and assembles a defaultMultiSession
// WITHOUT applying the session-binding security wrapper.
// Callers are responsible for wrapping the result with the appropriate decorator
// (BindSession for new sessions, RestoreSessionBinding for restored ones).
func (f *defaultMultiSessionFactory) makeBaseSession(
	ctx context.Context,
	sessID string,
	identity *auth.Identity,
	backends []*vmcp.Backend,
	sessionHints map[string]string,
) (*defaultMultiSession, error) {
	filtered := make([]*vmcp.Backend, 0, len(backends))
	for _, b := range backends {
		if b == nil {
			slog.Warn("Skipping nil backend entry during session creation")
			continue
		}
		filtered = append(filtered, b)
	}
	backends = filtered

	rawResults := make([]*initResult, len(backends))
	sem := make(chan struct{}, f.maxConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(backends))
	for i, b := range backends {
		go func(i int, b *vmcp.Backend) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rawResults[i] = f.initOneBackend(ctx, b, identity, sessionHints[b.ID])
		}(i, b)
	}
	wg.Wait()

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

	var (
		routingTable     *vmcp.RoutingTable
		advertisedTools  []vmcp.Tool
		allResolvedTools []vmcp.Tool
		allResources     []vmcp.Resource
		allPrompts       []vmcp.Prompt
	)
	if f.aggregator != nil {
		var aggErr error
		routingTable, advertisedTools, allResolvedTools, allResources, allPrompts, aggErr =
			buildRoutingTableWithAggregator(ctx, f.aggregator, results)
		if aggErr != nil {
			return nil, fmt.Errorf("failed to process backend capabilities: %w", aggErr)
		}
	} else {
		routingTable, advertisedTools, allResources, allPrompts = buildRoutingTable(results)
		allResolvedTools = advertisedTools // no filter when no aggregator
	}

	transportSess := transportsession.NewStreamableSession(sessID)
	populateBackendMetadata(transportSess, results)

	return &defaultMultiSession{
		Session:         transportSess,
		connections:     connections,
		routingTable:    routingTable,
		tools:           advertisedTools,
		allTools:        allResolvedTools,
		resources:       allResources,
		prompts:         allPrompts,
		backendSessions: backendSessions,
		queue:           newAdmissionQueue(),
	}, nil
}

// makeSession is the shared implementation for MakeSession and MakeSessionWithID.
// It builds the base session via makeBaseSession, then applies the session-binding
// security wrapper using the caller's identity.
func (f *defaultMultiSessionFactory) makeSession(
	ctx context.Context,
	sessID string,
	identity *auth.Identity,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	baseSession, err := f.makeBaseSession(ctx, sessID, identity, backends, nil)
	if err != nil {
		return nil, err
	}

	// Apply session binding: extracts the (iss, sub) identity tuple, stores it in
	// session metadata under MetadataKeyIdentityBinding, and wraps the session with
	// validation logic that checks every subsequent caller against that binding.
	decorated, err := security.BindSession(baseSession, identity)
	if err != nil {
		_ = baseSession.Close()
		return nil, err
	}
	return decorated, nil
}

// RestoreSession implements MultiSessionFactory.
// It reconnects to the backends whose IDs are listed in storedMetadata, rebuilds
// the routing table, and reapplies the session-binding decorator from the stored
// identity binding. Because the original bearer token is not available at restore
// time, identity is reconstructed from the (iss, sub) tuple in
// MetadataKeyIdentityBinding.
func (f *defaultMultiSessionFactory) RestoreSession(
	ctx context.Context,
	id string,
	storedMetadata map[string]string,
	allBackends []*vmcp.Backend,
) (MultiSession, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}

	// MetadataKeyBackendIDs must be present. An absent key means the metadata
	// was never fully initialised (placeholder session) or is corrupted; treat
	// it as a hard error so we don't silently connect to zero backends when a
	// non-empty list was expected.
	storedBackendIDs, backendIDsPresent := storedMetadata[MetadataKeyBackendIDs]
	if !backendIDsPresent {
		return nil, fmt.Errorf("RestoreSession: %q metadata key absent (corrupted or placeholder metadata)",
			MetadataKeyBackendIDs)
	}

	// Filter allBackends to the subset originally connected in this session.
	filteredBackends := filterBackendsByStoredIDs(allBackends, storedBackendIDs)

	// Reconstruct identity from the stored identity binding so makeBaseSession
	// can pass it to backend connectors (e.g. for outgoing auth injection).
	// The original bearer token is not available at restore time — only the
	// (iss, sub) tuple stored in MetadataKeyIdentityBinding is used.
	storedBinding, hasBinding := storedMetadata[sessiontypes.MetadataKeyIdentityBinding]
	if !hasBinding {
		// Legacy token-hash key present confirms not corrupted — safe to invalidate.
		if _, hasLegacy := storedMetadata[sessiontypes.MetadataKeyTokenHash]; hasLegacy {
			slog.Warn("RestoreSession: legacy session missing identity binding; invalidating",
				"reason", "legacy_session_missing_identity_binding",
			)
			return nil, transportsession.ErrSessionNotFound
		}
		return nil, fmt.Errorf("RestoreSession: %q metadata key absent (corrupted session metadata)",
			sessiontypes.MetadataKeyIdentityBinding)
	}

	// Restore identity from the stored (iss, sub) binding. Token is intentionally
	// empty — the original bearer token is not persisted. Outgoing-auth strategies
	// must derive credentials from Claims or a token store keyed by tsid; a
	// strategy that reads identity.Token will reject the request with an
	// identity-has-no-token error. The anonymous sentinel (identity == nil) is
	// handled by the IsUnauthenticated guard below.
	var identity *auth.Identity
	if !binding.IsUnauthenticated(storedBinding) {
		iss, sub, ok := binding.Parse(storedBinding)
		if !ok {
			return nil, fmt.Errorf("RestoreSession: stored identity binding is malformed: %q", storedBinding)
		}
		identity = &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: sub,
				Claims: map[string]any{
					"iss": iss,
					"sub": sub,
				},
			},
		}
	}

	// Extract stored per-backend session IDs as hints so each backend can
	// resume its session (via Mcp-Session-Id) rather than starting a new one.
	sessionHints := make(map[string]string, len(filteredBackends))
	for _, b := range filteredBackends {
		if hint := storedMetadata[MetadataKeyBackendSessionPrefix+b.ID]; hint != "" {
			sessionHints[b.ID] = hint
		}
	}

	// Build the base session (backend connections + routing table) without the
	// security wrapper. The wrapper is applied separately below.
	baseSession, err := f.makeBaseSession(ctx, id, identity, filteredBackends, sessionHints)
	if err != nil {
		return nil, fmt.Errorf("RestoreSession: failed to rebuild backend connections: %w", err)
	}

	// Restore only the identity-binding key from stored metadata. The other
	// keys (MetadataKeyBackendIDs, MetadataKeyBackendSessionPrefix.*) are
	// freshly computed by makeBaseSession from the actual reconnected backends;
	// overwriting them with stored values would make metadata inconsistent if
	// any backend failed to reconnect during restore.
	baseSession.SetMetadata(sessiontypes.MetadataKeyIdentityBinding, storedBinding)

	restored, err := security.RestoreSessionBinding(baseSession, storedBinding)
	if err != nil {
		_ = baseSession.Close()
		return nil, fmt.Errorf("RestoreSession: failed to restore session binding: %w", err)
	}
	return restored, nil
}

// filterBackendsByStoredIDs returns the subset of allBackends whose ID appears in
// the comma-separated storedIDs string. If storedIDs is empty, nil is returned (no backends).
//
// The empty-string case intentionally returns nil rather than all backends: callers
// that store an explicit empty string mean "zero backends connected", and callers that
// omit the key entirely (corrupted/absent metadata) must be handled by the caller before
// invoking this function — relying on empty-string to mean "all backends" is a footgun.
func filterBackendsByStoredIDs(allBackends []*vmcp.Backend, storedIDs string) []*vmcp.Backend {
	if storedIDs == "" {
		return nil
	}
	parts := strings.Split(storedIDs, ",")
	idSet := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			idSet[t] = struct{}{}
		}
	}
	filtered := make([]*vmcp.Backend, 0, len(idSet))
	for _, b := range allBackends {
		if b == nil {
			continue
		}
		if _, ok := idSet[b.ID]; ok {
			filtered = append(filtered, b)
		}
	}
	return filtered
}
