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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/cache"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
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
// to the mark3labs SDK's SessionIdManager interface.
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
	if cfg.CacheCapacity < 1 {
		return nil, nil, fmt.Errorf("sessionmanager.New: CacheCapacity must be >= 1 (got %d)", cfg.CacheCapacity)
	}
	if len(cfg.WorkflowDefs) > 0 && cfg.ComposerFactory == nil {
		return nil, nil, fmt.Errorf("sessionmanager.New: ComposerFactory is required when WorkflowDefs are provided")
	}

	// Resolve optimizer factory from config, applying telemetry wrapping if needed.
	optimizerFactory, optimizerCleanup, err := resolveOptimizer(cfg)
	if err != nil {
		return nil, nil, err
	}

	// Pre-create workflow telemetry instruments once so they are reused across
	// all per-session executor wrappers without re-registering metrics.
	var instruments *workflowExecutorInstruments
	if cfg.TelemetryProvider != nil && len(cfg.WorkflowDefs) > 0 {
		instruments, err = newWorkflowExecutorInstruments(
			cfg.TelemetryProvider.MeterProvider(),
			cfg.TelemetryProvider.TracerProvider(),
		)
		if err != nil {
			if cleanupErr := optimizerCleanup(context.Background()); cleanupErr != nil {
				slog.Warn("failed to clean up optimizer after instrument creation error", "error", cleanupErr)
			}
			return nil, nil, fmt.Errorf("failed to create workflow executor telemetry: %w", err)
		}
	}

	// Build the Manager first so we can reference sm.Terminate and sm.sessions
	// directly in closures, eliminating the forward-reference variable pattern.
	sm := &Manager{
		storage:    storage,
		backendReg: backendRegistry,
	}

	sm.sessions = cache.New(
		cfg.CacheCapacity,
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

	sm.factory = buildDecoratingFactory(cfg, optimizerFactory, instruments, sm.Terminate)

	cleanup := func(ctx context.Context) error {
		return optimizerCleanup(ctx)
	}
	return sm, cleanup, nil
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

// restoreStorageTimeout bounds the storage.Load call in the GetMultiSession
// cache-miss restore path. The operation is a single Redis GETEX, so 3 s is
// generous.
const restoreStorageTimeout = 3 * time.Second

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

// notifyBackendExpiredTimeout bounds each individual storage operation inside
// NotifyBackendExpired() — one Load and one Upsert, each capped independently.
// Each is a single-key Redis operation, so 5 s per call is consistent with
// terminateTimeout and decorateTimeout. Worst-case wall-clock for the function
// is 2 × 5 s = 10 s.
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
// The returned MultiSession can be retrieved later via GetMultiSession().
func (sm *Manager) CreateSession(
	ctx context.Context,
	sessionID string,
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

	// Note: Token hash and salt are computed and stored by the session factory
	// (MakeSessionWithID below). Token binding enforcement happens at the session
	// level via validateCaller(), which uses HMAC-SHA256 with a per-session salt.

	// List all available backends from the registry.
	backends := sm.listAllBackends(ctx)

	// Build the fully-formed MultiSession using the SDK-assigned session ID.
	// Sessions created with an identity are bound to that identity (allowAnonymous=false).
	// Sessions created without an identity allow anonymous access (allowAnonymous=true).
	allowAnonymous := sessiontypes.ShouldAllowAnonymous(identity)
	sess, err := sm.factory.MakeSessionWithID(ctx, sessionID, identity, allowAnonymous, backends)
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
	metadata[MetadataKeyTerminated] = MetadataValTrue
	cleanupCtx, cancel := context.WithTimeout(context.Background(), createSessionStorageTimeout)
	defer cancel()
	if _, err := sm.storage.Update(cleanupCtx, sessionID, metadata); err != nil {
		slog.Warn("Manager.CreateSession: failed to mark failed placeholder as terminated; it will linger until TTL expires",
			"session_id", sessionID, "error", err)
	}
}

// Validate implements the SDK's SessionIdManager.Validate().
//
// Returns (isTerminated=true, nil) for explicitly terminated sessions.
// Returns (false, error) for unknown sessions — per the SDK interface contract,
// a lookup failure is signalled via err, not via isTerminated.
// Returns (false, nil) for valid, active sessions.
func (sm *Manager) Validate(sessionID string) (isTerminated bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("Manager.Validate: empty session ID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
	defer cancel()

	metadata, err := sm.storage.Load(ctx, sessionID)
	if errors.Is(err, transportsession.ErrSessionNotFound) {
		slog.Debug("Manager.Validate: session not found", "session_id", sessionID)
		return false, fmt.Errorf("session not found")
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
//     Validate() returns (false, error) — the same response as "never existed".
//
//   - Placeholder (Phase 1): the session is marked terminated=true and left
//     for TTL cleanup. This prevents CreateSession() from opening backend
//     connections for an already-terminated session (see fast-fail check in
//     CreateSession). The terminated flag also lets Validate() return
//     (isTerminated=true, nil) during the window between termination and TTL
//     expiry, allowing the SDK to distinguish "actively terminated" from
//     "never existed".
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

	if _, isFullSession := metadata[sessiontypes.MetadataKeyTokenHash]; isFullSession {
		// Phase 2 (full MultiSession): delete from storage. The node-local
		// cache self-heals on the next Get: checkSession detects ErrSessionNotFound,
		// evicts the entry, and onEvict closes backend connections.
		if deleteErr := sm.storage.Delete(ctx, sessionID); deleteErr != nil {
			return false, fmt.Errorf("Manager.Terminate: failed to delete session from storage: %w", deleteErr)
		}
		slog.Info("Manager.Terminate: session terminated", "session_id", sessionID)
		return false, nil
	}

	// Phase 1 (placeholder): mark terminated so CreateSession fast-fails and
	// Validate returns isTerminated=true during the TTL window.
	metadata[MetadataKeyTerminated] = MetadataValTrue
	if storeErr := sm.storage.Upsert(ctx, sessionID, metadata); storeErr != nil {
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
// After a successful storage update, the cached entry is not immediately evicted.
// On the next GetMultiSession call, checkSession detects that the stored
// MetadataKeyBackendIDs differs from the cached session's value, evicts the stale
// entry via onEvict, and triggers RestoreSession with the updated metadata.
// On storage error, no eviction occurs and the caller retries on the next access.
//
// This is a best-effort operation. If the session is absent from storage (not
// found or terminated) the call is a silent no-op. Storage errors are logged
// but not returned.
func (sm *Manager) NotifyBackendExpired(sessionID, workloadID string) {
	loadCtx, loadCancel := context.WithTimeout(context.Background(), notifyBackendExpiredTimeout)
	defer loadCancel()
	metadata, err := sm.storage.Load(loadCtx, sessionID)
	if err != nil {
		if !errors.Is(err, transportsession.ErrSessionNotFound) {
			slog.Warn("NotifyBackendExpired: failed to load session from storage",
				"session_id", sessionID,
				"workload_id", workloadID,
				"error", err)
		}
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
	delete(metadata, vmcpsession.MetadataKeyBackendSessionPrefix+workloadID)
	var remaining []string
	for _, p := range strings.Split(backendIDs, ",") {
		if t := strings.TrimSpace(p); t != "" && t != workloadID {
			remaining = append(remaining, t)
		}
	}
	metadata[vmcpsession.MetadataKeyBackendIDs] = strings.Join(remaining, ",")

	if err := sm.updateMetadata(sessionID, metadata); err != nil {
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
// Known limitation: GetMultiSession's signature is fixed by the
// MultiSessionGetter interface and carries no context. Both the liveness
// check and the restore path use context.Background() with per-operation
// timeouts (restoreStorageTimeout / restoreSessionTimeout), so they are
// bounded independently of any caller deadline. The caller's HTTP request
// cancellation cannot propagate here.
// TODO: add context propagation through MultiSessionGetter so the caller's
// deadline can further bound these operations.
func (sm *Manager) GetMultiSession(sessionID string) (vmcpsession.MultiSession, bool) {
	return sm.sessions.Get(sessionID)
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
func (sm *Manager) checkSession(sessionID string, sess vmcpsession.MultiSession) error {
	checkCtx, cancel := context.WithTimeout(context.Background(), restoreStorageTimeout)
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

	// Compare backend IDs to detect cross-pod metadata drift.
	// Only compare when the cached session carries MetadataKeyBackendIDs to
	// avoid spurious evictions for sessions that don't track backend IDs.
	if cachedIDs, present := sess.GetMetadata()[vmcpsession.MetadataKeyBackendIDs]; present {
		if cachedIDs != metadata[vmcpsession.MetadataKeyBackendIDs] {
			return cache.ErrExpired
		}
	}

	return nil
}

// loadSession is the restore function supplied to sessions. It loads session
// metadata from storage and calls factory.RestoreSession to reconnect to
// backends, returning the fully-formed MultiSession on success.
func (sm *Manager) loadSession(sessionID string) (vmcpsession.MultiSession, error) {
	loadCtx, loadCancel := context.WithTimeout(context.Background(), restoreStorageTimeout)
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
	// PreventSessionHijacking always writes MetadataKeyTokenHash during Phase 2
	// (empty sentinel for anonymous, non-empty hash for authenticated). Its
	// absence means Generate() stored this record but CreateSession() never
	// completed — treat it as "not found" rather than "corrupted".
	//
	// Note: this is intentionally different from RestoreSession's fail-closed
	// check (absent key → error). Here we know a placeholder's empty metadata
	// is valid storage state produced by Generate(), so we return the
	// SDK-standard ErrSessionNotFound instead of an error.
	if _, hashPresent := metadata[sessiontypes.MetadataKeyTokenHash]; !hashPresent {
		return nil, transportsession.ErrSessionNotFound
	}

	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), restoreSessionTimeout)
	defer restoreCancel()
	restored, restoreErr := sm.factory.RestoreSession(restoreCtx, sessionID, metadata, sm.listAllBackends(restoreCtx))
	if restoreErr != nil {
		slog.Warn("Manager.loadSession: failed to restore session from storage",
			"session_id", sessionID, "error", restoreErr)
		return nil, restoreErr
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
	sess, ok := sm.GetMultiSession(sessionID)
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

	// Store the decorated session in the node-local cache.
	// If storage.Update below fails or returns (false, nil) because Terminate
	// deleted the storage key, the decorated entry remains in cache briefly;
	// the next Gets checkSession detects the absent key, evicts the entry,
	// and onEvict closes connections.
	sm.sessions.Set(sessionID, decorated)

	// Persist metadata to storage via conditional Update (SET XX).
	decorateCtx, decorateCancel := context.WithTimeout(context.Background(), decorateTimeout)
	defer decorateCancel()
	updated, err := sm.storage.Update(decorateCtx, sessionID, decorated.GetMetadata())
	if err != nil {
		return fmt.Errorf("DecorateSession: failed to store decorated session metadata: %w", err)
	}
	if !updated {
		return fmt.Errorf("DecorateSession: session %q was deleted during decoration", sessionID)
	}
	return nil
}

// GetAdaptedTools returns SDK-format tools for the given session, with handlers
// that delegate tool invocations directly to the session's CallTool() method.
//
// When the session factory is configured with an aggregator (WithAggregator),
// tools are in their final resolved form — overrides and conflict resolution
// applied via ProcessPreQueriedCapabilities. Each handler passes the resolved
// tool name to CallTool, which translates it back to the original backend name
// via GetBackendCapabilityName.
//
// Without an aggregator, raw backend tool names are used as-is (no overrides
// or conflict resolution applied).
func (sm *Manager) GetAdaptedTools(sessionID string) ([]mcpserver.ServerTool, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Manager.GetAdaptedTools: session %q not found or not a multi-session", sessionID)
	}

	domainTools := multiSess.Tools()
	sdkTools := make([]mcpserver.ServerTool, 0, len(domainTools))

	for _, domainTool := range domainTools {
		schemaJSON, err := json.Marshal(domainTool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("Manager.GetAdaptedTools: failed to marshal schema for tool %s: %w", domainTool.Name, err)
		}

		tool := mcp.Tool{
			Name:           domainTool.Name,
			Description:    domainTool.Description,
			RawInputSchema: schemaJSON,
			Annotations:    conversion.ToMCPToolAnnotations(domainTool.Annotations),
		}
		if domainTool.OutputSchema != nil {
			outputSchemaJSON, marshalErr := json.Marshal(domainTool.OutputSchema)
			if marshalErr != nil {
				slog.Warn("failed to marshal tool output schema",
					"tool", domainTool.Name, "error", marshalErr)
			} else {
				tool.RawOutputSchema = outputSchemaJSON
			}
		}

		capturedSess := multiSess
		capturedSessionID := sessionID
		capturedToolName := domainTool.Name
		handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
				slog.Warn("invalid arguments for tool", "tool", capturedToolName, "error", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}

			meta := conversion.FromMCPMeta(req.Params.Meta)
			caller, _ := auth.IdentityFromContext(ctx)

			result, callErr := capturedSess.CallTool(ctx, caller, capturedToolName, args, meta)
			if callErr != nil {
				if errors.Is(callErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(callErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "tool", capturedToolName, "error", callErr)
					if _, termErr := sm.Terminate(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", callErr)), nil
				}
				return mcp.NewToolResultError(callErr.Error()), nil
			}

			return &mcp.CallToolResult{
				Result: mcp.Result{
					Meta: conversion.ToMCPMeta(result.Meta),
				},
				Content:           conversion.ToMCPContents(result.Content),
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		}

		sdkTools = append(sdkTools, mcpserver.ServerTool{
			Tool:    tool,
			Handler: handler,
		})
		slog.Debug("Manager.GetAdaptedTools: adapted tool", "session_id", sessionID, "tool", domainTool.Name)
	}

	return sdkTools, nil
}

// GetAdaptedResources returns SDK-format resources for the given session, with handlers
// that delegate read requests directly to the session's ReadResource() method.
func (sm *Manager) GetAdaptedResources(sessionID string) ([]mcpserver.ServerResource, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Manager.GetAdaptedResources: session %q not found or not a multi-session", sessionID)
	}

	domainResources := multiSess.Resources()
	sdkResources := make([]mcpserver.ServerResource, 0, len(domainResources))

	for _, domainResource := range domainResources {
		resource := mcp.Resource{
			Name:        domainResource.Name,
			URI:         domainResource.URI,
			Description: domainResource.Description,
			MIMEType:    domainResource.MimeType,
		}

		capturedSess := multiSess
		capturedSessionID := sessionID
		capturedResourceURI := domainResource.URI
		handler := func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			caller, _ := auth.IdentityFromContext(ctx)

			result, readErr := capturedSess.ReadResource(ctx, caller, capturedResourceURI)
			if readErr != nil {
				if errors.Is(readErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(readErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "resource", capturedResourceURI, "error", readErr)
					if _, termErr := sm.Terminate(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return nil, fmt.Errorf("unauthorized: %w", readErr)
				}
				return nil, readErr
			}

			return conversion.ToMCPResourceContents(result.Contents), nil
		}

		sdkResources = append(sdkResources, mcpserver.ServerResource{
			Resource: resource,
			Handler:  handler,
		})
		slog.Debug("Manager.GetAdaptedResources: adapted resource", "session_id", sessionID, "uri", domainResource.URI)
	}

	return sdkResources, nil
}

// GetAdaptedPrompts returns SDK-format prompts for the given session, with handlers
// that delegate prompt requests directly to the session's GetPrompt() method.
func (sm *Manager) GetAdaptedPrompts(sessionID string) ([]mcpserver.ServerPrompt, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Manager.GetAdaptedPrompts: session %q not found or not a multi-session", sessionID)
	}

	domainPrompts := multiSess.Prompts()
	sdkPrompts := make([]mcpserver.ServerPrompt, 0, len(domainPrompts))

	for _, domainPrompt := range domainPrompts {
		prompt := mcp.Prompt{
			Name:        domainPrompt.Name,
			Description: domainPrompt.Description,
		}
		for _, arg := range domainPrompt.Arguments {
			prompt.Arguments = append(prompt.Arguments, mcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			})
		}

		capturedSess := multiSess
		capturedSessionID := sessionID
		capturedPromptName := domainPrompt.Name
		handler := func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			caller, _ := auth.IdentityFromContext(ctx)

			args := make(map[string]any, len(req.Params.Arguments))
			for k, v := range req.Params.Arguments {
				args[k] = v
			}
			result, getErr := capturedSess.GetPrompt(ctx, caller, capturedPromptName, args)
			if getErr != nil {
				if errors.Is(getErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(getErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "prompt", capturedPromptName, "error", getErr)
					if _, termErr := sm.Terminate(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return nil, fmt.Errorf("unauthorized: %w", getErr)
				}
				return nil, getErr
			}

			mcpMessages := make([]mcp.PromptMessage, 0, len(result.Messages))
			for _, msg := range result.Messages {
				mcpMessages = append(mcpMessages, mcp.PromptMessage{
					Role:    mcp.Role(msg.Role),
					Content: conversion.ToMCPContent(msg.Content),
				})
			}
			return &mcp.GetPromptResult{
				Description: result.Description,
				Messages:    mcpMessages,
			}, nil
		}

		sdkPrompts = append(sdkPrompts, mcpserver.ServerPrompt{
			Prompt:  prompt,
			Handler: handler,
		})
		slog.Debug("Manager.GetAdaptedPrompts: adapted prompt", "session_id", sessionID, "prompt", domainPrompt.Name)
	}

	return sdkPrompts, nil
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
