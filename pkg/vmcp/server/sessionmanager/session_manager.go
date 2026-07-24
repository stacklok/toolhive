// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sessionmanager provides session lifecycle management.
//
// This package implements the two-phase session creation pattern that bridges
// the MCP SDK's session management with the vMCP server's backend lifecycle:
//   - Phase 1 (Generate): Creates a placeholder session with no context
//   - Phase 2 (CreateSession): Replaces placeholder with fully-initialized MultiSession
//
// The Manager type implements the server.SessionManager interface and is used by
// the server package.
package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/cache"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	// MetadataKeyTerminated is the session metadata key that marks a placeholder
	// session as explicitly terminated by the client.
	MetadataKeyTerminated = "terminated"

	// MetadataValTrue is the string value stored under MetadataKeyTerminated
	// when a session has been terminated.
	MetadataValTrue = "true"
)

// Manager bridges the domain session lifecycle (MultiSession / MultiSessionFactory)
// to the mcpcompat SDK's SessionIdManager interface.
//
// It implements a two-phase session-creation pattern:
//
//   - Generate(): called by SDK during initialize without context;
//     stores an empty placeholder via storage.
//   - CreateSession(): called from OnRegisterSession hook once
//     context is available; calls factory.MakeSessionWithID(), then
//     persists the session metadata to storage.
//
// # Storage split
//
// MultiSession holds live in-process state (backend HTTP connections, routing
// table) that cannot be serialized or recovered across processes. A separate
// in-process multiSessions map holds the authoritative MultiSession reference
// for this pod. The pluggable SessionDataStorage (LocalSessionDataStorage or
// RedisSessionDataStorage) carries only the lightweight, serialisable session
// metadata required for TTL management, Validate(), and cross-pod visibility.
//
// Because MultiSession objects are node-local, horizontal scaling requires
// sticky routing when session-affinity is desired. When Redis is used as the
// session-storage backend the metadata is durable across pod restarts, and the
// live MultiSession can be re-created via factory.RestoreSession() on a cache miss.
//
// TODO: Long-term, the cache and storage should be layered behind a single
// interface so the session manager does not need to coordinate between them.
// Reads would go through the cache (handling misses, singleflight, and liveness
// transparently); writes go to storage; caching is an implementation detail
// hidden from the caller.
type Manager struct {
	storage    transportsession.DataStorage
	factory    vmcpsession.MultiSessionFactory
	backendReg vmcp.BackendRegistry

	// sessions is a node-local cache of live MultiSession objects, separate
	// from storage because MultiSession contains un-serialisable runtime state
	// (HTTP connections, routing tables). On a cache miss it restores the
	// session from stored metadata; on a cache hit it confirms liveness via
	// storage.Load, which also refreshes the Redis TTL.
	sessions *cache.ValidatingCache[string, vmcpsession.MultiSession]

	// optimizerFactory is the resolved (telemetry-wrapped) optimizer factory, or
	// nil when the optimizer is disabled. Surfaced via OptimizerFactory so the Serve
	// path can build a per-session optimizer over the core's tools. The store and
	// cleanup remain owned by this Manager (cleanup returned from New).
	optimizerFactory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error)
}

// New creates a Manager backed by the given SessionDataStorage and backend
// registry. It builds the decorating session factory from cfg, wiring the
// optimizer and composite tool layers internally.
//
// The returned cleanup function releases any resources allocated during
// construction (e.g. the optimizer's SQLite store). Callers must invoke it
// on shutdown. If no cleanup is needed, a no-op function is returned.
func New(
	storage transportsession.DataStorage,
	cfg *FactoryConfig,
	backendRegistry vmcp.BackendRegistry,
) (*Manager, func(context.Context) error, error) {
	if cfg == nil || cfg.Base == nil {
		return nil, nil, fmt.Errorf("sessionmanager.New: FactoryConfig.Base (SessionFactory) is required")
	}
	if cfg.CacheCapacity < 0 {
		return nil, nil, fmt.Errorf("sessionmanager.New: CacheCapacity must be >= 0 (got %d)", cfg.CacheCapacity)
	}
	capacity := cfg.CacheCapacity
	if capacity == 0 {
		capacity = defaultCacheCapacity
	}
	// Resolve optimizer factory from config, applying telemetry wrapping if needed.
	optimizerFactory, optimizerCleanup, err := resolveOptimizer(cfg)
	if err != nil {
		return nil, nil, err
	}

	// Build the Manager first so we can reference sm.Terminate and sm.sessions
	// directly in closures, eliminating the forward-reference variable pattern.
	sm := &Manager{
		storage:    storage,
		backendReg: backendRegistry,
	}

	// Surface the resolved optimizer factory to the Serve path ONLY when
	// AdvertiseFromCore is set. That makes the two store writers mutually exclusive:
	// the session-factory decorator runs iff !AdvertiseFromCore (buildDecoratingFactory
	// below), and OptimizerFactory() returns a non-nil factory iff AdvertiseFromCore —
	// so a Serve composition root that enables the optimizer but forgets the flag gets a
	// nil factory (no Serve-layer optimizer) rather than a silent double-index of the
	// shared FTS5 store. The legacy server.New path leaves AdvertiseFromCore false and
	// never calls OptimizerFactory(), so its decorator is unaffected.
	if cfg.AdvertiseFromCore {
		sm.optimizerFactory = optimizerFactory
	}

	sm.sessions = cache.New(
		capacity,
		sm.loadSession,
		sm.checkSession,
		func(id string, sess vmcpsession.MultiSession) {
			if closeErr := sess.Close(); closeErr != nil {
				slog.Warn("session cache: error closing evicted session",
					"session_id", id, "error", closeErr)
			}
			slog.Warn("session cache: session evicted from node-local cache",
				"session_id", id)
		},
	)

	sm.factory = buildDecoratingFactory(cfg, optimizerFactory, sm.Terminate)

	cleanup := func(ctx context.Context) error {
		return optimizerCleanup(ctx)
	}
	return sm, cleanup, nil
}

// OptimizerFactory returns the resolved (telemetry-wrapped) optimizer factory, or
// nil when the optimizer is disabled OR FactoryConfig.AdvertiseFromCore is false.
//
// It is consumed by the Serve path (FactoryConfig.AdvertiseFromCore), which builds
// a per-session optimizer over the core's advertised tool set rather than via the
// session decorator. Gating on AdvertiseFromCore makes the decorator and this getter
// mutually exclusive store writers, so the shared FTS5 store can never be double-indexed
// (see New). The optimizer's shared store and its cleanup remain owned by this Manager
// (the cleanup function returned from New). On the legacy server.New path the factory is
// applied internally via the session decorator and this getter is unused.
func (m *Manager) OptimizerFactory() func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error) {
	return m.optimizerFactory
}

// generateTimeout is the context deadline applied to the storage operations
// inside Generate(). It provides a safety net in addition to the go-redis
// client-level read/write timeouts.
const generateTimeout = 5 * time.Second

// createSessionStorageTimeout bounds each individual storage operation inside
// CreateSession() (two Load checks and one final Store). The caller's ctx is
// used as the parent so auth values and request-level cancellation still
// propagate; this constant adds an upper bound so a slow or unreachable Redis
// cannot block session creation indefinitely. 5 s is consistent with
// generateTimeout and terminateTimeout — all are single-key Redis operations.
const createSessionStorageTimeout = 5 * time.Second

// validateTimeout is the context deadline applied to the storage Load inside
// Validate(). Validate() is called on every incoming HTTP request, so a tight
// timeout bounds how long a slow or unreachable Redis can stall a request goroutine.
const validateTimeout = 3 * time.Second

// restoreStorageTimeout bounds storage.Load calls (GETEX) in the
// GetMultiSession restore path (loadSession) and in the checkSession liveness
// check. Both are single-key Redis reads; 3 s is generous.
const restoreStorageTimeout = 3 * time.Second

// restoreMetadataWriteTimeout bounds the storage.Update call that persists
// the restored session's metadata back to Redis after a successful
// RestoreSession. Single-key Redis SET XX operation; 5 s is consistent with
// other write timeouts (createSessionStorageTimeout, terminateTimeout,
// decorateTimeout, notifyBackendExpiredTimeout).
const restoreMetadataWriteTimeout = 5 * time.Second

// restoreSessionTimeout bounds factory.RestoreSession in the GetMultiSession
// cache-miss path. RestoreSession opens HTTP connections to each backend, so
// we allow more time than a simple storage read. Aligned with discoveryTimeout
// (15 s) since both involve backend HTTP round-trips.
const restoreSessionTimeout = 15 * time.Second

// terminateTimeout is the context deadline applied to storage operations inside
// Terminate(). Terminate() is called on client DELETE requests and on auth
// failures, each of which performs at most one Delete + one Load + one Store
// (all single-key Redis operations). 5 s matches generateTimeout and is
// generous for these operations while still bounding slow/unreachable Redis.
const terminateTimeout = 5 * time.Second

// decorateTimeout bounds the storage.Store call inside DecorateSession().
// DecorateSession is called during session setup (OnRegisterSession hook) and
// performs a single Redis SET. 5 s is consistent with terminateTimeout.
const decorateTimeout = 5 * time.Second

// notifyBackendExpiredTimeout bounds the storage.Update call inside
// NotifyBackendExpired() — a single-key Redis operation, consistent with
// terminateTimeout and decorateTimeout.
const notifyBackendExpiredTimeout = 5 * time.Second

// Generate implements the SDK's SessionIdManager.Generate().
//
// Phase 1 of the two-phase creation pattern: creates a unique session ID,
// stores an empty placeholder via storage, and returns the ID to the SDK.
// No context is available at this point.
//
// The placeholder is replaced by CreateSession() in Phase 2 once context
// is available via the OnRegisterSession hook.
func (sm *Manager) Generate() string {
	// Two attempts: the second handles both storage transients and the
	// astronomically unlikely (but now correctly detected) UUID collision.
	// Each attempt gets its own context so an expired deadline on attempt 0
	// does not immediately abort attempt 1.
	for attempt := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), generateTimeout)
		sessionID := uuid.New().String()

		// Create is an atomic SET NX on Redis, eliminating the TOCTOU
		// race that a Load+Upsert would have in a multi-pod deployment.
		stored, err := sm.storage.Create(ctx, sessionID, map[string]string{})
		cancel()
		if err != nil {
			slog.Error("Manager: failed to store placeholder session",
				"session_id", sessionID, "attempt", attempt+1, "error", err)
			continue
		}
		if !stored {
			slog.Warn("Manager: UUID collision detected; retrying", "session_id", sessionID)
			continue
		}

		slog.Debug("Manager: generated placeholder session", "session_id", sessionID)
		return sessionID
	}

	slog.Error("Manager: failed to generate unique session ID after 2 attempts")
	return ""
}

// CreateSession is Phase 2 of the two-phase creation pattern.
//
// It is called from the OnRegisterSession hook once the request context is
// available. It:
//  1. Resolves the caller identity from the context.
//  2. Lists available backends from the registry.
//  3. Calls MultiSessionFactory.MakeSessionWithID() to build a fully-formed
//     MultiSession (which opens real HTTP connections to each backend).
//  4. Persists session metadata to storage and caches the live MultiSession
//     in the node-local map.
//
// sink, when non-nil, is threaded to MultiSessionFactory.MakeSessionWithID so
// every backend connector opened for this session can report an asynchronous
// backend notification (#5748); pass nil to disable that consumption.
//
// The returned MultiSession can be retrieved later via GetMultiSession().
func (sm *Manager) CreateSession(
	ctx context.Context,
	sessionID string,
	sink vmcpsession.ListChangedSink,
) (vmcpsession.MultiSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("Manager.CreateSession: session ID must not be empty")
	}

	// Fast-fail before opening any backend connections: verify the phase-1
	// placeholder still exists and has not been marked terminated. A client
	// DELETE between Generate() and this hook sets terminated=true on the
	// placeholder (or removes it entirely). Opening backend connections first
	// and checking afterwards would waste those resources and could silently
	// resurrect a session the client intentionally ended.
	loadCtx1, loadCancel1 := context.WithTimeout(ctx, createSessionStorageTimeout)
	placeholder, err := sm.storage.Load(loadCtx1, sessionID)
	loadCancel1()
	if errors.Is(err, transportsession.ErrSessionNotFound) {
		return nil, fmt.Errorf(
			"Manager.CreateSession: placeholder for session %q not found (terminated concurrently?)",
			sessionID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("Manager.CreateSession: failed to load placeholder for session %q: %w", sessionID, err)
	}
	if placeholder[MetadataKeyTerminated] == MetadataValTrue {
		return nil, fmt.Errorf(
			"Manager.CreateSession: session %q was terminated before backend connections could be opened",
			sessionID,
		)
	}

	// Resolve the caller identity (may be nil for anonymous access).
	identity, _ := auth.IdentityFromContext(ctx)

	// List all available backends from the registry.
	backends := sm.listAllBackends(ctx)

	// Build the fully-formed MultiSession using the SDK-assigned session ID.
	sess, err := sm.factory.MakeSessionWithID(ctx, sessionID, identity, backends, sink)
	if err != nil {
		sm.cleanupFailedPlaceholder(sessionID, placeholder)
		return nil, fmt.Errorf("Manager.CreateSession: failed to create multi-session: %w", err)
	}

	// Re-check that the placeholder is still present AND not terminated after
	// the (potentially slow) MakeSessionWithID call. A concurrent DELETE could:
	//   1. Delete the placeholder entirely (caught by ErrSessionNotFound), OR
	//   2. Mark it terminated=true (caught by terminated flag check)
	// Without this second check, storage.Store would silently resurrect a
	// session the client already terminated, wasting backend connections.
	loadCtx2, loadCancel2 := context.WithTimeout(ctx, createSessionStorageTimeout)
	placeholder2, err := sm.storage.Load(loadCtx2, sessionID)
	loadCancel2()
	if errors.Is(err, transportsession.ErrSessionNotFound) {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"Manager.CreateSession: placeholder for session %q disappeared during backend init (terminated concurrently)",
			sessionID,
		)
	}
	if err != nil {
		_ = sess.Close()
		sm.cleanupFailedPlaceholder(sessionID, placeholder)
		return nil, fmt.Errorf(
			"Manager.CreateSession: failed to re-check placeholder for session %q after backend init: %w",
			sessionID, err,
		)
	}
	if placeholder2[MetadataKeyTerminated] == MetadataValTrue {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"Manager.CreateSession: session %q was terminated during backend init (marked after first check)",
			sessionID,
		)
	}

	// Persist the serialisable session metadata to the pluggable backend (e.g.
	// Redis) so that Validate() and TTL management work correctly. The live
	// MultiSession itself is cached in the node-local multiSessions map below.
	//
	// Use Update (SET XX) rather than Upsert to close the TOCTOU window between
	// the second placeholder check above and this write. If Terminate deleted the
	// key in that window, Update returns (false, nil) and we bail without
	// resurrecting the deleted session.
	storeCtx, storeCancel := context.WithTimeout(ctx, createSessionStorageTimeout)
	defer storeCancel()
	stored, err := sm.storage.Update(storeCtx, sessionID, sess.GetMetadata())
	if err != nil {
		_ = sess.Close()
		sm.cleanupFailedPlaceholder(sessionID, placeholder2)
		return nil, fmt.Errorf("Manager.CreateSession: failed to store session metadata: %w", err)
	}
	if !stored {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"Manager.CreateSession: session %q was terminated between placeholder check and metadata store",
			sessionID,
		)
	}

	// Cache the live MultiSession so that GetMultiSession can retrieve it.
	sm.sessions.Set(sessionID, sess)

	slog.Debug("Manager: created multi-session",
		"session_id", sessionID,
		"backend_count", len(backends))
	return sess, nil
}

// cleanupFailedPlaceholder marks a placeholder session as terminated in storage
// after a CreateSession failure. This prevents Validate() from returning
// (false, nil) for an orphaned placeholder (which would make the SDK treat it
// as a valid session), and prevents repeated Validate() calls from refreshing
// the Redis TTL and keeping the placeholder alive indefinitely.
//
// Uses Update (SET XX) so that a Terminate() that already deleted the key is
// not inadvertently resurrected as a terminated entry.
//
// Cleanup is best-effort: errors are logged but not returned, since the caller
// already has an error to report.
func (sm *Manager) cleanupFailedPlaceholder(sessionID string, metadata map[string]string) {
	// Copy before mutating so the caller's map is not modified.
	terminated := make(map[string]string, len(metadata)+1)
	for k, v := range metadata {
		terminated[k] = v
	}
	terminated[MetadataKeyTerminated] = MetadataValTrue
	cleanupCtx, cancel := context.WithTimeout(context.Background(), createSessionStorageTimeout)
	defer cancel()
	if _, err := sm.storage.Update(cleanupCtx, sessionID, terminated); err != nil {
		slog.Warn("Manager.CreateSession: failed to mark failed placeholder as terminated; it will linger until TTL expires",
			"session_id", sessionID, "error", err)
	}
}

// Validate implements the SDK's SessionIdManager.Validate().
//
// Returns (isTerminated=true, nil) for a session that is definitively gone:
// explicitly terminated (placeholder marked terminated=true) OR absent from
// storage (a full MultiSession is deleted on Terminate, TTL-expired, or never
// existed). All of these must reject the request as a hard termination so the
// client re-initializes rather than retrying a dead session.
//
// Returns (false, error) only for a genuine, transient storage error (e.g. the
// backing store is unreachable) — the caller should treat this as retryable and
// NOT drop session state.
//
// Returns (false, nil) for valid, active sessions.
//
// This distinction is load-bearing: the streamable transport maps
// (isTerminated=true) to HTTP 404 (-> client ErrSessionTerminated -> re-init)
// and a non-terminated error to HTTP 503 (retryable). Reporting a genuinely-gone
// session as an error would surface as 503 and make a client whose session was
// terminated on another replica retry the dead session forever. Terminate still
// DELETES the key (unchanged), so the resurrection-race guarantee is preserved;
// only the way an absent key is reported here changes.
func (sm *Manager) Validate(sessionID string) (isTerminated bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("Manager.Validate: empty session ID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
	defer cancel()

	metadata, err := sm.storage.Load(ctx, sessionID)
	if errors.Is(err, transportsession.ErrSessionNotFound) {
		// The session is gone (terminated + deleted, TTL-expired, or never
		// existed). Report it as terminated so the transport answers 404 and the
		// client re-initializes, rather than as an error (which the transport
		// would treat as a transient 503 and the client would retry indefinitely).
		slog.Debug("Manager.Validate: session not found; reporting as terminated", "session_id", sessionID)
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("Manager.Validate: storage error for session %q: %w", sessionID, err)
	}

	if metadata[MetadataKeyTerminated] == MetadataValTrue {
		slog.Debug("Manager.Validate: session is terminated", "session_id", sessionID)
		return true, nil
	}

	return false, nil
}

// Terminate implements the SDK's SessionIdManager.Terminate().
//
// The two session types are handled asymmetrically to prevent a race condition
// where client termination during the Phase 1→Phase 2 window could resurrect
// sessions with open backend connections:
//
//   - MultiSession (Phase 2): the storage key is deleted. The node-local cache
//     self-heals on the next Get: checkSession detects ErrSessionNotFound,
//     evicts the entry, and onEvict closes backend connections. After deletion
//     Validate() reports the absent key as (isTerminated=true, nil), so the next
//     request — on this or any other replica — is rejected with a definitive 404
//     and the client re-initializes instead of retrying the dead session.
//
//   - Placeholder (Phase 1): the session is marked terminated=true and left
//     for TTL cleanup. This prevents CreateSession() from opening backend
//     connections for an already-terminated session (see fast-fail check in
//     CreateSession). The terminated flag also lets Validate() return
//     (isTerminated=true, nil) during the window between termination and TTL
//     expiry — the same terminated response the deleted case now gives.
//
// Returns (isNotAllowed=false, nil) on success; client termination is always permitted.
func (sm *Manager) Terminate(sessionID string) (isNotAllowed bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("Manager.Terminate: empty session ID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), terminateTimeout)
	defer cancel()

	// Load current metadata to determine session phase.
	metadata, loadErr := sm.storage.Load(ctx, sessionID)
	if errors.Is(loadErr, transportsession.ErrSessionNotFound) {
		// Already gone (concurrent termination or TTL expiry).
		slog.Debug("Manager.Terminate: session not found (already expired?)", "session_id", sessionID)
		return false, nil
	}
	if loadErr != nil {
		return false, fmt.Errorf("Manager.Terminate: failed to load session %q: %w", sessionID, loadErr)
	}

	if _, isFullSession := metadata[sessiontypes.MetadataKeyIdentityBinding]; isFullSession {
		// Phase 2 (full MultiSession): delete from storage. The cache entry will be
		// evicted lazily on the next Get when checkSession finds the session gone.
		if deleteErr := sm.storage.Delete(ctx, sessionID); deleteErr != nil {
			return false, fmt.Errorf("Manager.Terminate: failed to delete session from storage: %w", deleteErr)
		}
		slog.Info("Manager.Terminate: session terminated", "session_id", sessionID)
		return false, nil
	}

	// Phase 1 (placeholder): mark terminated so CreateSession fast-fails and
	// Validate returns isTerminated=true during the TTL window.
	// Use Update (SET XX) rather than Upsert so we never resurrect a key that
	// was concurrently deleted or expired between the Load above and this write.
	// (false, nil) means already gone — treat as success.
	metadata[MetadataKeyTerminated] = MetadataValTrue
	updated, storeErr := sm.storage.Update(ctx, sessionID, metadata)
	if storeErr != nil {
		slog.Warn("Manager.Terminate: failed to persist terminated flag for placeholder; attempting delete fallback",
			"session_id", sessionID, "error", storeErr)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), terminateTimeout)
		if deleteErr := sm.storage.Delete(deleteCtx, sessionID); deleteErr != nil {
			deleteCancel()
			return false, fmt.Errorf(
				"Manager.Terminate: failed to persist terminated flag and delete placeholder: storeErr=%v, deleteErr=%w",
				storeErr, deleteErr)
		}
		deleteCancel()
	} else if !updated {
		// Session expired or was concurrently deleted between Load and Update — already gone.
		slog.Debug("Manager.Terminate: placeholder already gone before terminated flag could be set", "session_id", sessionID)
	}

	slog.Info("Manager.Terminate: session terminated", "session_id", sessionID)
	return false, nil
}

// NotifyBackendExpired updates session metadata in storage to reflect that the
// backend identified by workloadID is no longer connected. It removes the
// per-backend session ID key and rebuilds MetadataKeyBackendIDs so that a
// cross-pod RestoreSession call does not attempt to reconnect to the expired
// backend session.
//
// The caller supplies the session metadata it already holds (e.g. from
// MultiSession.GetMetadata). Passing nil metadata is treated as "no metadata
// available" and is a silent no-op, avoiding a redundant storage round-trip.
//
// After a successful storage update, the cached entry is not immediately evicted.
// On the next GetMultiSession call, checkSession detects that the stored
// MetadataKeyBackendIDs differs from the cached session's value, evicts the stale
// entry via onEvict, and triggers RestoreSession with the updated metadata.
// On storage error, no eviction occurs and the caller retries on the next access.
//
// This is a best-effort operation. If the session key is absent from storage
// (terminated or expired), updateMetadata's SET XX is a no-op. Storage errors
// are logged but not returned.
func (sm *Manager) NotifyBackendExpired(sessionID, workloadID string, metadata map[string]string) {
	if metadata == nil {
		return
	}
	if metadata[MetadataKeyTerminated] == MetadataValTrue {
		return
	}

	// MetadataKeyBackendIDs must be present. An absent key means the metadata
	// is corrupted or was never fully initialised; clobbering it with "" would
	// silently drop all remaining backends from subsequent restores.
	backendIDs, backendIDsPresent := metadata[vmcpsession.MetadataKeyBackendIDs]
	if !backendIDsPresent {
		slog.Warn("NotifyBackendExpired: MetadataKeyBackendIDs absent from session metadata; skipping update",
			"session_id", sessionID,
			"workload_id", workloadID)
		return
	}

	// Build updated metadata: remove the expired backend's session-ID key and
	// rebuild MetadataKeyBackendIDs. Always write the key (even as "") to match
	// populateBackendMetadata, which uses key presence to distinguish an
	// explicit zero-backend state from absent/corrupted metadata in
	// RestoreSession. Trim spaces and drop empty parts for robustness.
	//
	// Copy before mutating so the caller's map is not modified. Mutating the
	// caller's map would silently corrupt the in-memory session state, which
	// would defeat lazy eviction: checkSession compares stored vs cached
	// MetadataKeyBackendIDs to detect drift, so the values must differ after
	// this update for eviction to trigger on the next GetMultiSession call.
	updated := make(map[string]string, len(metadata))
	for k, v := range metadata {
		updated[k] = v
	}
	delete(updated, vmcpsession.MetadataKeyBackendSessionPrefix+workloadID)
	var remaining []string
	for _, p := range strings.Split(backendIDs, ",") {
		if t := strings.TrimSpace(p); t != "" && t != workloadID {
			remaining = append(remaining, t)
		}
	}
	updated[vmcpsession.MetadataKeyBackendIDs] = strings.Join(remaining, ",")

	if err := sm.updateMetadata(sessionID, updated); err != nil {
		slog.Warn("NotifyBackendExpired: failed to persist backend expiry to storage",
			"session_id", sessionID,
			"workload_id", workloadID,
			"error", err)
	}
}

// updateMetadata writes a complete metadata snapshot to storage using a
// conditional Update (SET XX). If the key is absent at update time (concurrent
// Delete), the call is a no-op. The cache self-heals on the next GetMultiSession
// call: checkSession detects metadata drift, evicts the stale entry, and
// RestoreSession reloads with fresh state.
func (sm *Manager) updateMetadata(sessionID string, metadata map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), notifyBackendExpiredTimeout)
	defer cancel()

	// Update only succeeds if the key still exists. A concurrent Delete (same
	// pod or cross-pod) returns (false, nil), and we bail without resurrecting.
	updated, err := sm.storage.Update(ctx, sessionID, metadata)
	if err != nil {
		return err
	}
	if !updated {
		return nil // session was terminated; nothing to update
	}
	// The cache self-heals lazily: on the next GetMultiSession, checkSession detects
	// either the absent storage key or stale MetadataKeyBackendIDs and evicts the
	// entry, triggering a fresh RestoreSession.
	return nil
}

// GetMultiSession retrieves the fully-formed MultiSession for a given SDK session ID.
// Returns (nil, false) if the session does not exist or has not yet been
// upgraded from placeholder to MultiSession.
//
// On a cache hit, liveness is confirmed via storage.Load (which also refreshes
// the Redis TTL). On a cache miss, the session is restored from storage via
// factory.RestoreSession, enabling cross-pod session recovery when Redis is
// used as the storage backend.
//
// The context is propagated to storage and restore operations using
// context.WithoutCancel so caller identity (e.g. *auth.Identity in ctx) reaches
// the backend Initialize handshake during cross-pod session restore.
func (sm *Manager) GetMultiSession(ctx context.Context, sessionID string) (vmcpsession.MultiSession, bool) {
	return sm.sessions.Get(ctx, sessionID)
}

// checkSession is the liveness check supplied to sessions. It confirms the
// storage entry is still alive and refreshes the Redis TTL as a side effect.
// It returns ErrExpired when the session has been deleted or terminated
// (including termination by another pod), so the cache evicts the entry and
// onEvict closes backend connections.
//
// Cross-pod propagation: if the stored backend list differs from the cached
// session's, ErrExpired is returned to evict the stale entry. The next
// GetMultiSession call triggers RestoreSession with the up-to-date metadata,
// replacing the old session and its backend connections. This ensures that a
// backend-expiry update written by pod A propagates to pod B on the next
// cache access rather than waiting for natural TTL expiry.
func (sm *Manager) checkSession(ctx context.Context, sessionID string, sess vmcpsession.MultiSession) error {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreStorageTimeout)
	defer cancel()
	metadata, err := sm.storage.Load(checkCtx, sessionID)
	if errors.Is(err, transportsession.ErrSessionNotFound) {
		return cache.ErrExpired
	}
	if err != nil {
		return err // transient storage error — keep cached
	}
	if metadata[MetadataKeyTerminated] == MetadataValTrue {
		return cache.ErrExpired
	}

	// Evict if the backend ID list has drifted (e.g. NotifyBackendExpired removed a
	// backend), so the next Get calls RestoreSession with the updated backend list.
	//
	// We intentionally compare only MetadataKeyBackendIDs rather than the full
	// metadata map. Per-backend session IDs (MetadataKeyBackendSessionPrefix+*)
	// are the session IDs negotiated by each pod's independent RestoreSession call.
	// Backends that do not honor Mcp-Session-Id hints (e.g. SSE transports, some
	// StreamableHTTP backends) assign a fresh ID on every restore, so different pods
	// legitimately hold different per-backend IDs for the same session. Comparing
	// the full map would cause each pod's loadSession write-back to invalidate all
	// other pods' cached sessions, creating an infinite eviction storm that prevents
	// tools from ever being served in multi-pod deployments.
	sessBackendIDs := sess.GetMetadata()[vmcpsession.MetadataKeyBackendIDs]
	if sessBackendIDs != metadata[vmcpsession.MetadataKeyBackendIDs] {
		return cache.ErrExpired
	}

	return nil
}

// loadSession is the restore function supplied to sessions. It loads session
// metadata from storage and calls factory.RestoreSession to reconnect to
// backends, returning the fully-formed MultiSession on success.
func (sm *Manager) loadSession(ctx context.Context, sessionID string) (vmcpsession.MultiSession, error) {
	loadCtx, loadCancel := context.WithTimeout(context.WithoutCancel(ctx), restoreStorageTimeout)
	defer loadCancel()
	metadata, loadErr := sm.storage.Load(loadCtx, sessionID)
	if loadErr != nil {
		if !errors.Is(loadErr, transportsession.ErrSessionNotFound) {
			slog.Warn("Manager.loadSession: storage error; treating as not found",
				"session_id", sessionID, "error", loadErr)
		}
		return nil, loadErr
	}

	// Don't restore terminated sessions.
	if metadata[MetadataKeyTerminated] == MetadataValTrue {
		return nil, transportsession.ErrSessionNotFound
	}

	// Don't restore placeholder sessions (Phase 2 never ran).
	// BindSession always writes MetadataKeyIdentityBinding during Phase 2
	// (the unauthenticated sentinel for anonymous sessions, a bound (iss, sub)
	// binding for authenticated ones). Its absence means Generate() stored
	// this record but CreateSession() never completed — treat it as "not
	// found" rather than "corrupted".
	//
	// Note: this is intentionally different from RestoreSession's fail-closed
	// check (absent key → error). Here we know a placeholder's empty metadata
	// is valid storage state produced by Generate(), so we return the
	// SDK-standard ErrSessionNotFound instead of an error.
	if _, bindingPresent := metadata[sessiontypes.MetadataKeyIdentityBinding]; !bindingPresent {
		return nil, transportsession.ErrSessionNotFound
	}

	restoreCtx, restoreCancel := context.WithTimeout(context.WithoutCancel(ctx), restoreSessionTimeout)
	defer restoreCancel()
	restored, restoreErr := sm.factory.RestoreSession(restoreCtx, sessionID, metadata, sm.listAllBackends(restoreCtx))
	if restoreErr != nil {
		slog.Warn("Manager.loadSession: failed to restore session from storage",
			"session_id", sessionID, "error", restoreErr)
		return nil, restoreErr
	}

	// Persist the restored session's metadata back to Redis so that
	// per-backend session IDs are kept current. Backends that do not honor
	// Mcp-Session-Id hints (e.g. SSE transports) assign a fresh ID on every
	// restore; without this write the stale IDs would persist in Redis
	// indefinitely.
	//
	// We use Update (SET XX) rather than Upsert so we never resurrect a key
	// that was concurrently deleted (Terminate / TTL expiry). A (false, nil)
	// result means the key is already gone — treat it as not found so the
	// cache never serves a session that no longer exists in storage.
	updateCtx, updateCancel := context.WithTimeout(context.WithoutCancel(ctx), restoreMetadataWriteTimeout)
	defer updateCancel()
	updated, updateErr := sm.storage.Update(updateCtx, sessionID, restored.GetMetadata())
	if updateErr != nil {
		slog.Warn("Manager.loadSession: failed to persist restored session metadata",
			"session_id", sessionID, "error", updateErr)
		// Non-fatal: the session is still usable on this pod. checkSession
		// will detect metadata drift on the next liveness check and evict,
		// triggering a fresh restore that will retry the write.
	} else if !updated {
		// Session was concurrently deleted (Terminate / TTL expiry) between
		// RestoreSession and this write — do not cache the restored session.
		slog.Debug("Manager.loadSession: session already gone before metadata could be persisted; treating as not found",
			"session_id", sessionID)
		if closeErr := restored.Close(); closeErr != nil {
			slog.Warn("Manager.loadSession: failed to close restored session after concurrent deletion",
				"session_id", sessionID, "error", closeErr)
		}
		return nil, transportsession.ErrSessionNotFound
	}

	slog.Debug("Manager.loadSession: restored session from storage", "session_id", sessionID)
	return restored, nil
}

// DecorateSession retrieves the MultiSession for sessionID, applies fn to it,
// and stores the result back. Returns an error if the session is not found or
// has not yet been upgraded from placeholder to MultiSession.
//
// storage.Update is the concurrency guard. If it returns (false, nil), the
// session was deleted; the cache entry will be evicted on the next Get when
// checkSession detects ErrSessionNotFound.
func (sm *Manager) DecorateSession(sessionID string, fn func(sessiontypes.MultiSession) sessiontypes.MultiSession) error {
	// context.Background() is intentional: DecorateSession is called from
	// OnRegisterSession during session setup, not from a live authenticated
	// HTTP request, so there is no caller identity to propagate.
	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	if !ok {
		return fmt.Errorf("DecorateSession: session %q not found or not a multi-session", sessionID)
	}
	decorated := fn(sess)
	if decorated == nil {
		return fmt.Errorf("DecorateSession: decorator returned nil session")
	}
	if decorated.ID() != sessionID {
		return fmt.Errorf("DecorateSession: decorator changed session ID from %q to %q", sessionID, decorated.ID())
	}

	// Persist metadata to storage first via conditional Update (SET XX).
	// Only update the node-local cache after a successful write so that a
	// storage error or a concurrent delete never leaves a decorated (but
	// unpersisted) value in the cache where retries could stack decorations.
	decorateCtx, decorateCancel := context.WithTimeout(context.Background(), decorateTimeout)
	defer decorateCancel()
	updated, err := sm.storage.Update(decorateCtx, sessionID, decorated.GetMetadata())
	if err != nil {
		return fmt.Errorf("DecorateSession: failed to store decorated session metadata: %w", err)
	}
	if !updated {
		// Session was deleted (by Terminate or TTL) between Get and Update.
		// The cache entry will be evicted lazily on the next Get when checkSession
		// finds the session gone from storage.
		return fmt.Errorf("DecorateSession: session %q was deleted during decoration", sessionID)
	}
	sm.sessions.Set(sessionID, decorated)
	return nil
}

// listAllBackends returns all backends from the registry as a pointer slice.
func (sm *Manager) listAllBackends(ctx context.Context) []*vmcp.Backend {
	raw := sm.backendReg.List(ctx)
	backends := make([]*vmcp.Backend, len(raw))
	for i := range raw {
		backends[i] = &raw[i]
	}
	return backends
}
