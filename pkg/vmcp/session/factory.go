// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/hex"
	"fmt"
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
	"github.com/stacklok/toolhive/pkg/vmcp/session/security"
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

	// MetadataKeyTokenHash is the transport-session metadata key that holds
	// the HMAC-SHA256 hash of the bearer token used to create the session.
	// For authenticated sessions this is hex(HMAC-SHA256(bearerToken)).
	// For anonymous sessions (no bearer token) this is the empty string sentinel.
	// The raw token is never stored — only the hash.
	MetadataKeyTokenHash = "vmcp.token.hash" //nolint:gosec // This is a metadata key name, not a credential.

	// MetadataKeyTokenSalt is the transport-session metadata key that holds
	// the hex-encoded random salt used for HMAC-SHA256 token hashing.
	// Each session has a unique salt to prevent attacks across multiple sessions.
	MetadataKeyTokenSalt = "vmcp.token.salt" //nolint:gosec // This is a metadata key name, not a credential.
)

var (
	// defaultHMACSecret is the fallback HMAC secret used when WithHMACSecret is not provided.
	// WARNING: This is INSECURE and should ONLY be used for testing/development.
	// Production deployments MUST provide a secure secret via WithHMACSecret option.
	defaultHMACSecret = []byte("insecure-default-for-testing-only-change-in-production")
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
	//
	// TODO(sessionManagementV2): MakeSession is only used by tests for convenience
	// (auto-generates UUID). Production code uses MakeSessionWithID exclusively.
	// This method can be removed once the sessionManagementV2 migration is complete
	// and VMCPSession is deleted. Tests can be updated to generate their own UUIDs.
	MakeSession(ctx context.Context, identity *auth.Identity, backends []*vmcp.Backend) (MultiSession, error)

	// MakeSessionWithID creates a new MultiSession with a specific session ID.
	// This is used by SessionManager to create sessions using the SDK-assigned ID
	// rather than generating a new UUID internally.
	//
	// The id parameter must be non-empty and should be a valid MCP session ID
	// (visible ASCII characters, 0x21 to 0x7E per the MCP specification).
	//
	// The allowAnonymous parameter controls whether the session allows nil caller
	// identity. If false, all session method calls must provide a valid caller
	// that matches the session creator's identity.
	//
	// All other behaviour (partial initialisation, bounded concurrency, etc.)
	// is identical to MakeSession.
	MakeSessionWithID(
		ctx context.Context,
		id string,
		identity *auth.Identity,
		allowAnonymous bool,
		backends []*vmcp.Backend,
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
	hmacSecret         []byte // Server-managed secret for HMAC-SHA256 token hashing
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

// WithHMACSecret sets the server-managed secret used for HMAC-SHA256 token hashing.
// The secret should be 32+ bytes and loaded from secure configuration (e.g., environment
// variable, secret management system).
//
// The secret is defensively copied to prevent external modification after assignment.
// Empty or nil secrets are rejected (function is a no-op) to prevent accidental security downgrades.
//
// If not set, a default insecure secret is used (NOT RECOMMENDED for production).
func WithHMACSecret(secret []byte) MultiSessionFactoryOption {
	return func(f *defaultMultiSessionFactory) {
		// Reject empty/nil secrets to prevent silent security downgrade
		if len(secret) == 0 {
			slog.Warn("WithHMACSecret: empty or nil secret rejected, falling back to default insecure secret",
				"recommendation", "provide a secure secret via VMCP_SESSION_HMAC_SECRET environment variable")
			return
		}
		// Make a defensive copy to prevent external modification
		f.hmacSecret = append([]byte(nil), secret...)
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
		hmacSecret:         defaultHMACSecret, // Initialize with default (insecure) secret
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

// ShouldAllowAnonymous determines if a session should allow anonymous access
// based on the creator's identity. Sessions without an identity (nil) or with
// an empty token are anonymous; sessions with a non-empty bearer token are
// bound to that token.
//
// This helper consolidates the anonymous session logic used by both
// MakeSession and external callers like SessionManager, and aligns with the
// validation logic in MakeSessionWithID.
func ShouldAllowAnonymous(identity *auth.Identity) bool {
	return identity == nil || identity.Token == ""
}

// MakeSession implements MultiSessionFactory.
func (f *defaultMultiSessionFactory) MakeSession(
	ctx context.Context,
	identity *auth.Identity,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	// Sessions created with an identity are bound to that identity (allowAnonymous=false).
	// Sessions created without an identity allow anonymous access (allowAnonymous=true).
	allowAnonymous := ShouldAllowAnonymous(identity)
	return f.makeSession(ctx, uuid.New().String(), identity, allowAnonymous, backends)
}

// MakeSessionWithID implements MultiSessionFactory.
func (f *defaultMultiSessionFactory) MakeSessionWithID(
	ctx context.Context,
	id string,
	identity *auth.Identity,
	allowAnonymous bool,
	backends []*vmcp.Backend,
) (MultiSession, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}

	// Validate allowAnonymous is consistent with identity to prevent security footguns.
	// If identity has a token, allowAnonymous must be false (caller wants a bound session).
	// If identity is nil or has no token, allowAnonymous should be true (anonymous session).
	if identity != nil && identity.Token != "" && allowAnonymous {
		return nil, fmt.Errorf(
			"invalid session configuration: cannot create anonymous session " +
				"(allowAnonymous=true) with bearer token (identity.Token is non-empty)",
		)
	}
	if (identity == nil || identity.Token == "") && !allowAnonymous {
		return nil, fmt.Errorf(
			"invalid session configuration: cannot create bound session " +
				"(allowAnonymous=false) without bearer token (identity is nil or has empty token)",
		)
	}

	return f.makeSession(ctx, id, identity, allowAnonymous, backends)
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

// computeTokenBinding computes the token hash and salt for session-level binding security.
// For authenticated sessions this returns hex(HMAC-SHA256(bearerToken)) and a random salt.
// For anonymous sessions empty values are returned. The raw token is never stored.
func (f *defaultMultiSessionFactory) computeTokenBinding(
	identity *auth.Identity,
	allowAnonymous bool,
) (boundTokenHash string, tokenSalt []byte, err error) {
	if !allowAnonymous && identity != nil && identity.Token != "" {
		// Generate unique salt for this session
		tokenSalt, err = security.GenerateSalt()
		if err != nil {
			return "", nil, fmt.Errorf("failed to generate token salt: %w", err)
		}
		// Compute HMAC-SHA256 hash with server secret and per-session salt
		boundTokenHash = security.HashToken(identity.Token, f.hmacSecret, tokenSalt)
	}
	return boundTokenHash, tokenSalt, nil
}

// populateBackendMetadata adds backend IDs to session metadata.
// IDs are extracted from the already-sorted results slice to avoid a second sort.
func populateBackendMetadata(transportSess transportsession.Session, results []initResult) {
	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.target.WorkloadID
		}
		transportSess.SetMetadata(MetadataKeyBackendIDs, strings.Join(ids, ","))
	}
}

// makeSession is the shared implementation for MakeSession and MakeSessionWithID.
// It initialises backends in parallel, builds the routing table, and returns
// a fully-formed MultiSession using the provided sessID.
func (f *defaultMultiSessionFactory) makeSession(
	ctx context.Context,
	sessID string,
	identity *auth.Identity,
	allowAnonymous bool,
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

	transportSess := transportsession.NewStreamableSession(sessID)

	// Populate serialisable metadata so that the embedded transport session
	// carries the identity reference and connected backend list when persisted
	// via transportsession.Storage.
	if identity != nil && identity.Subject != "" {
		transportSess.SetMetadata(MetadataKeyIdentitySubject, identity.Subject)
	}

	// Compute token hash and salt once for session-level binding security.
	// These values are used in TWO places:
	// 1. Passed to HijackPreventionDecorator for runtime validation in validateCaller()
	// 2. Stored in session metadata for persistence, auditing, and backward compatibility
	// Computing once ensures consistency between validation and stored metadata.
	boundTokenHash, tokenSalt, err := f.computeTokenBinding(identity, allowAnonymous)
	if err != nil {
		return nil, err
	}
	// Store in metadata for persistence, auditing, and backward compatibility
	transportSess.SetMetadata(MetadataKeyTokenHash, boundTokenHash)
	if len(tokenSalt) > 0 {
		transportSess.SetMetadata(MetadataKeyTokenSalt, hex.EncodeToString(tokenSalt))
	}

	populateBackendMetadata(transportSess, results)

	// Create the base session without token binding
	baseSession := &defaultMultiSession{
		Session:         transportSess,
		connections:     connections,
		routingTable:    routingTable,
		tools:           allTools,
		resources:       allResources,
		prompts:         allPrompts,
		backendSessions: backendSessions,
		queue:           newAdmissionQueue(),
	}

	// Wrap with HijackPreventionDecorator for token binding validation
	// The decorator adds validation logic without modifying the core session
	// Pass the already-computed hash and salt to ensure consistency with metadata
	decorated := NewHijackPreventionDecorator(
		baseSession,
		allowAnonymous,
		f.hmacSecret,
		boundTokenHash,
		tokenSalt,
	)

	return decorated, nil
}
