// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:generate mockgen -destination=mocks/mock_factory.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/session MultiSessionFactory

package session

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
	//
	// sink, when non-nil, is threaded to every backend connector for this
	// session and invoked (best-effort, on the backend receive-loop goroutine)
	// when a backend emits a notification consumed asynchronously — currently
	// only notifications/tools/list_changed (#5748). Pass nil to leave that
	// consumption disabled for this session.
	MakeSessionWithID(
		ctx context.Context,
		id string,
		identity *auth.Identity,
		backends []*vmcp.Backend,
		sink ListChangedSink,
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
// within a single MultiSession. It is called once per backend that is not
// skipped as Modern (see initOneBackend) during MakeSession.
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
// sink, when non-nil, is invoked (best-effort, possibly from a receive-loop
// goroutine) whenever this backend emits a notification the connector consumes
// asynchronously (currently only notifications/tools/list_changed). Pass nil to
// leave that consumption disabled; a connector that does not support it may
// ignore the parameter entirely. See backend.ListChangedSink.
//
// The returned backend.Session owns the underlying transport connection and
// must be closed when the session ends. The returned CapabilityList is used
// to populate the session's routing table and capability lists.
//
// On error the factory treats the failure as a partial failure: a warning is
// logged and the backend is excluded from the session — except when the
// backend's revision has meanwhile resolved to Modern, in which case
// initOneBackend logs at DEBUG instead (see initOneBackend's doc comment for
// why a Modern backend's session-establishment sequence fails there).
type backendConnector func(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	sessionHint string,
	sink backend.ListChangedSink,
) (backend.Session, *vmcp.CapabilityList, error)

// defaultMultiSessionFactory is the production MultiSessionFactory implementation.
type defaultMultiSessionFactory struct {
	connector          backendConnector
	maxConcurrency     int
	backendInitTimeout time.Duration
	revisionLookup     func(workloadID string) (mcpparser.Revision, bool)
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

// WithRevisionLookup supplies a function that reports a backend's cached MCP
// revision (Legacy vs. Modern) by workload ID, so initOneBackend can skip
// connecting to a backend known to speak the stateless Modern (2026-07-28)
// revision. The connect attempt fails there anyway, and skipping it avoids
// both the resulting WARN and the wasted work — see initOneBackend's doc
// comment for the exact failure mechanism.
//
// The lookup's second return distinguishes "known Modern" from "unprobed"
// (including RevisionLegacy's zero value): only a confirmed Modern result
// skips the connect. A nil lookup (the default) or a cache miss reproduces
// today's behaviour exactly — every backend gets an unconditional connect
// attempt.
//
// A func value, not an interface, per the option-pattern rule in
// .claude/rules/go-style.md: it stays compile-time safe without exposing
// CachedRevision on vmcp.BackendClient for every implementation to satisfy.
//
// Concurrency: lookup is called from the per-backend init goroutines started
// by makeBaseSession, up to maxConcurrency at once, so it must be safe for
// concurrent use.
func WithRevisionLookup(lookup func(workloadID string) (mcpparser.Revision, bool)) MultiSessionFactoryOption {
	return func(f *defaultMultiSessionFactory) {
		f.revisionLookup = lookup
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
// skipped. The second return is true when the skip is a deliberate Modern
// (2026-07-28 revision) skip rather than a failure.
//
// Why the connect is worth skipping — the sequence fails, but NOT at a Legacy
// handshake. Measured against a real stateless backend: go-sdk v1.7's Connect
// is Modern-first, so server/discover SUCCEEDS and Modern is negotiated; no
// raw Legacy initialize is ever sent. Connect then opens a
// subscriptions/listen stream, because mcpcompat's Initialize unconditionally
// installs the three list-changed notification handlers
// (mcpcompat/client.installNotificationHandlers) and go-sdk opens that stream
// whenever any of them is set. The stateless server rejects it with
// "session not found", which fails Connect, tears the connection down, and in
// turn fails the follow-up tools/list in initAndQueryCapabilities. The connector
// therefore returns (nil, nil, err).
//
// That rejection is a go-sdk v1.7.0-pre.3 artifact, NOT a spec requirement: the
// Modern revision has no sessions, so nothing in it says subscriptions/listen
// needs one. Do not read this as "Modern requires a session" — it is the SDK's
// stateless server refusing a subscribe it has no session to hang the stream on.
//
// So the wasted work is a whole sequence (discover, a failed subscribe, a
// failed tools/list, teardown), not one round-trip, and it ends in a WARN that
// looks exactly like a genuinely dead backend. Skipping is also correct on its
// own terms: all three purposes of a persistent per-backend connection are
// inapplicable to a Modern backend — list_changed propagation (Modern removed
// server-initiated notifications; its push channel is subscriptions/listen,
// which vMCP does not implement), backend-session-id resume hints (no
// Mcp-Session-Id), and identity-binding metadata. The first of those is
// precisely what makes the connect fail: vMCP asks for a notification stream
// the backend cannot give it.
//
// Known caveat (#5992): the direction that matters here — a backend redeployed
// stateless→stateful, so a cached Modern label is now wrong — self-corrects only
// on error. The Modern call fails, dispatch's isRevisionMismatch fires and
// reclassify flips the cache (pkg/vmcp/client/client.go). (The opposite
// direction, Legacy→Modern, does self-heal on success: legacyInit flips the
// cache when initialize negotiates Modern, added in #5997.) So calls are never
// wrong for long, but until some call has re-probed, this method keeps skipping
// a backend that now wants a connection, and that session holds none for it —
// list_changed propagation is lost until a NEW session is created after the
// reclassification. Closing that window needs a periodic re-probe, which is
// #5992's remaining scope, not this method's.
//
// Cold start: the revision cache is populated by the first backend call
// through dispatch (probeRevision) or, when configured, the health monitor's
// periodic check — neither is ordered before the first client session. So EVERY
// session created before the first backend call still pays the failing sequence
// above for each Modern backend, and its WARN is not suppressed, because the
// cache is still cold when the post-error re-check below runs. N clients that
// connect without calling anything each pay it; there is no per-process bound.
// Sessions created after the cache is warm skip the connect entirely.
func (f *defaultMultiSessionFactory) initOneBackend(
	ctx context.Context,
	b *vmcp.Backend,
	identity *auth.Identity,
	sessionHint string,
	sink backend.ListChangedSink,
) (*initResult, bool) {
	target := vmcp.BackendToTarget(b)

	if f.isKnownModern(target.WorkloadID) {
		slog.Debug("Skipping backend connection for Modern backend; no session to hold",
			"backendID", b.ID,
			"backendName", b.Name,
		)
		return nil, true
	}

	bCtx, cancel := context.WithTimeout(ctx, f.backendInitTimeout)
	defer cancel()

	conn, caps, err := f.connector(bCtx, target, identity, sessionHint, sink)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		// Narrows the WARN to DEBUG when the backend has resolved to Modern
		// since the pre-connect check — either an era mismatch that lost the
		// cold-start race (the expected case, see this method's doc comment) or
		// an unrelated genuine failure (transport, auth) on a backend that
		// happens to be Modern. Both are cases where a WARN would misinform.
		if f.isKnownModern(target.WorkloadID) {
			slog.Debug("Backend failed to initialise and is now known Modern; skipping",
				"backendID", b.ID,
				"backendName", b.Name,
				"error", err,
			)
			return nil, true
		}
		slog.Warn("Failed to initialise backend for session; continuing without it",
			"backendID", b.ID,
			"backendName", b.Name,
			"error", err,
		)
		return nil, false
	}
	if conn == nil || caps == nil {
		if conn != nil {
			_ = conn.Close()
		}
		slog.Warn("Backend connector returned nil conn or caps with no error; skipping backend",
			"backendID", b.ID,
			"backendName", b.Name,
		)
		return nil, false
	}
	return &initResult{target: target, conn: conn, caps: caps}, false
}

// isKnownModern reports whether workloadID's cached revision is confirmed
// Modern. Returns false for an unprobed backend or when no lookup is
// configured — indistinguishable from "attempt the connect" in either case.
func (f *defaultMultiSessionFactory) isKnownModern(workloadID string) bool {
	if f.revisionLookup == nil {
		return false
	}
	rev, known := f.revisionLookup(workloadID)
	return known && rev == mcpparser.RevisionModern
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

// MakeSessionWithID implements MultiSessionFactory.
func (f *defaultMultiSessionFactory) MakeSessionWithID(
	ctx context.Context,
	id string,
	identity *auth.Identity,
	backends []*vmcp.Backend,
	sink ListChangedSink,
) (MultiSession, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	return f.makeSession(ctx, id, identity, backends, sink)
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
	sink backend.ListChangedSink,
) *defaultMultiSession {
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
	modernSkipped := make([]bool, len(backends))
	sem := make(chan struct{}, f.maxConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(backends))
	for i, b := range backends {
		go func(i int, b *vmcp.Backend) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rawResults[i], modernSkipped[i] = f.initOneBackend(ctx, b, identity, sessionHints[b.ID], sink)
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

	// Only warn when at least one backend was actually expected to hold a
	// session — a set that skipped every backend as Modern is working as
	// designed, not a failure. The message deliberately does NOT say the session
	// has no capabilities: on the Serve path those come from the core, not from
	// these connections, so a session with zero held connections still lists and
	// calls tools normally. What is actually lost is list_changed propagation.
	if len(results) == 0 && len(backends) > 0 && slices.Contains(modernSkipped, false) {
		slog.Warn("No backend held a session for this session; every non-Modern backend failed to initialise",
			"backendCount", len(backends))
	}

	// The core is the single source of capability aggregation/advertising (the factory never
	// aggregates), so the routing table is built from the raw backend capabilities with no
	// overrides/conflict-resolution/filter; advertised and resolved tools are identical.
	routingTable, advertisedTools, allResources, allPrompts := buildRoutingTable(results)
	allResolvedTools := advertisedTools

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
	}
}

// makeSession is the shared implementation for MakeSession and MakeSessionWithID.
// It builds the base session via makeBaseSession, then applies the session-binding
// security wrapper using the caller's identity.
func (f *defaultMultiSessionFactory) makeSession(
	ctx context.Context,
	sessID string,
	identity *auth.Identity,
	backends []*vmcp.Backend,
	sink backend.ListChangedSink,
) (MultiSession, error) {
	baseSession := f.makeBaseSession(ctx, sessID, identity, backends, nil, sink)

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
// identity binding. Because the original bearer token is not persisted, backend
// connectors receive nil identity; live requests carry a fully-populated identity
// on req.Context() from TokenValidator.Middleware.
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

	// Validate and read the stored identity binding. This key is written by
	// BindSession at session-creation time and identifies whether the session
	// was bound to an authenticated identity or was anonymous.
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

	// Validate that the stored binding is parsable (or the unauthenticated
	// sentinel) before proceeding. A malformed value indicates corrupted metadata.
	// We do NOT construct a partial *auth.Identity here: the original bearer
	// token is not persisted, so UpstreamTokens cannot be recovered. Fabricating
	// a struct with empty Token and UpstreamTokens would violate the contract that
	// a non-nil *auth.Identity is always fully populated (see pkg/auth/identity.go).
	// Backend connectors receive nil identity; live tool calls already carry a
	// complete identity on req.Context() from TokenValidator.Middleware. See #5336.
	if !binding.IsUnauthenticated(storedBinding) {
		if _, _, ok := binding.Parse(storedBinding); !ok {
			return nil, fmt.Errorf("RestoreSession: stored identity binding is malformed: %q", storedBinding)
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
	// security wrapper. Pass nil identity — see comment above. Pass nil sink: a
	// cross-pod restore has no SDK ClientSession to resync (the restoring pod may
	// not be the one serving the client's connection), so tools list_changed
	// propagation on this path is a follow-up (#5748 scope: the CreateSession
	// path only).
	baseSession := f.makeBaseSession(ctx, id, nil, filteredBackends, sessionHints, nil)

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
