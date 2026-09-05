// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// timedEntry wraps a value with its creation time for TTL tracking.
type timedEntry[T any] struct {
	value     T
	createdAt time.Time
	expiresAt time.Time
}

// upstreamKey is the composite key for the flat upstream token map.
type upstreamKey struct {
	sessionID    string
	providerName string
}

// assertionJWTKey identifies a consumed assertion JWT without sharing JTIs
// across assertion purposes or authorization-server issuers.
type assertionJWTKey struct {
	purpose string
	issuer  string
	jti     string
}

// clientOrderEntry is one position in MemoryStorage.clientOrder: a client ID
// and the time it was placed at the back of the eviction queue (registration
// or renewal). touchedAt backs the minClientAge grace window.
type clientOrderEntry struct {
	id        string
	touchedAt time.Time
}

// MemoryStorage implements the Storage interface with in-memory maps.
// This implementation is thread-safe and suitable for development and testing.
// For production use, consider implementing a persistent storage backend.
//
// # Fosite Storage Design
//
// Token maps store fosite.Requester (not just token strings) because fosite needs
// the full authorization context for validation and introspection. The Requester
// contains the Client, granted scopes, Session (with expiration times), and more.
//
// Maps are keyed by "signature" (cryptographic token identifier) for O(1) token
// lookup. Revocation by "request ID" requires O(n) scan; production implementations
// should maintain a reverse index for efficiency.
type MemoryStorage struct {
	mu sync.RWMutex

	// clients maps client_id -> Client for client lookup (fosite.ClientManager).
	// A SPIFFE static-client durable placeholder (see staticClientPlaceholder
	// in spiffe_decorator.go) is stored here as the live inertPlaceholderClient
	// Go value, so its overridden GetGrantTypes/GetResponseTypes are called
	// directly on every read — unlike RedisStorage, which serializes the
	// client's fields and must re-derive the same guarantee on read-back via
	// storedClient.Reserved (see clientFromStored in redis.go). No equivalent
	// marker is needed here.
	clients map[string]fosite.Client

	// clientOrder is the least-recently-proven-used order of clients: a
	// registration starts at the back, and RenewClientTTL moves a DCR-issued
	// client to the back again on proven use (a successful token
	// exchange/refresh), mirroring RedisStorage.RenewClientTTL's TTL bump.
	// Each entry's touchedAt records when it was placed at the back
	// (registration or renewal), which oldestEvictableClientIndex uses to
	// apply the minClientAge grace window. Eviction in RegisterClient walks
	// from the front and takes the first entry that both carries the
	// registration.DCRIssued marker and has aged past minClientAge.
	// /oauth/register is unauthenticated and every call mints an entry, so an
	// uncapped map is a memory-exhaustion DoS in the same process as the
	// proxy. RegisterClient evicts an aged DCR-issued client when safe;
	// otherwise it rejects the registration to preserve the configured cap.
	clientOrder []clientOrderEntry

	// maxClients caps the client map; zero means unlimited. RegisterClient
	// evicts the oldest DCR-issued entry once the cap is reached; a
	// pre-provisioned client (no DCRIssued marker) is never evicted.
	maxClients int

	// minClientAge is the grace window below which a DCR-issued client is
	// never evicted, regardless of position in clientOrder. Without it, a
	// legitimately registered but not-yet-used client sits at the front of
	// the queue (RenewClientTTL only fires on a successful token exchange),
	// so an attacker filling the map at capacity can evict that specific
	// victim on demand — the rate limiter bounds the speed of this but not
	// the outcome. The floor removes that targeted-victim primitive while
	// keeping the anti-bloat bound: if every current entry is younger than
	// the floor, RegisterClient returns ErrClientCapacity.
	minClientAge time.Duration

	// authCodes maps authorization code -> Requester. Codes are one-time-use;
	// invalidatedCodes tracks used codes to return ErrInvalidatedAuthorizeCode.
	authCodes map[string]*timedEntry[fosite.Requester]

	// accessTokens maps token signature -> Requester. The signature is derived
	// from the token value, enabling O(1) lookup when validating bearer tokens.
	accessTokens map[string]*timedEntry[fosite.Requester]

	// refreshTokens maps token signature -> Requester. Linked to access tokens
	// via request ID for token rotation per RFC 6749.
	refreshTokens map[string]*timedEntry[fosite.Requester]

	// pkceRequests maps code signature -> Requester containing the PKCE challenge.
	// Validated during token exchange per RFC 7636.
	pkceRequests map[string]*timedEntry[fosite.Requester]

	// upstreamTokens maps (sessionID, providerName) -> timedEntry for multi-provider support.
	upstreamTokens map[upstreamKey]*timedEntry[*UpstreamTokens]

	// pendingAuthorizations tracks authorization requests awaiting upstream IDP callback
	pendingAuthorizations map[string]*timedEntry[*PendingAuthorization]

	// invalidatedCodes tracks auth codes that have been used/invalidated.
	// Kept separate from authCodes to return the Requester with ErrInvalidatedAuthorizeCode.
	invalidatedCodes map[string]*timedEntry[bool]

	// clientAssertionJWTs tracks JTIs to prevent JWT replay attacks per RFC 7523.
	clientAssertionJWTs map[string]time.Time

	// assertionJWTs tracks consumed assertion JWTs by purpose, issuer, and JTI.
	assertionJWTs map[assertionJWTKey]time.Time

	// users maps user ID -> User for user account lookup.
	// Users are not subject to TTL-based cleanup as they represent persistent accounts.
	users map[string]*User

	// providerIdentities maps "providerID:providerSubject" -> ProviderIdentity for identity lookup.
	// This enables O(1) lookup during authentication callbacks.
	providerIdentities map[string]*ProviderIdentity

	// dcrCredentials maps DCRKey -> DCRCredentials for RFC 7591 Dynamic Client
	// Registration credentials. These entries come from OUTBOUND DCR: ToolHive
	// acting as a DCR client to register itself against a configured upstream
	// IdP (see pkg/auth/dcr/store.go, the only caller of StoreDCRCredentialsIfAbsent).
	// This is not reachable from the inbound, unauthenticated /oauth/register
	// handler, so unlike clients (bounded by maxClients/clientOrder because
	// every inbound registration call mints an entry), growth here is bounded
	// by the number of configured upstreams, which the operator controls, not
	// by request volume — hence no eviction or cap on this map.
	//
	// Entries are intentionally excluded from the periodic cleanupExpired
	// loop: DCR registrations are long-lived and the authoritative expiry
	// signal is RFC 7591 client_secret_expires_at, which is honored at read
	// time by callers (and by the future Redis backend's SetEX TTL). Growth
	// is bounded by upstream count × distinct scope sets ever registered for
	// each upstream during the process lifetime; for a stable configuration
	// this collapses to the upstream count, but rotating scope sets
	// (operator-driven scope changes, or upstream scopes_supported rotations
	// re-derived by the resolver) accumulate stale entries that survive until
	// process restart. The Redis backend's SetEX TTL mitigates this in
	// production deployments.
	dcrCredentials map[DCRKey]*DCRCredentials

	// cleanupInterval is how often the background cleanup runs
	cleanupInterval time.Duration

	// stopCleanup is used to signal the cleanup goroutine to stop
	stopCleanup chan struct{}

	// cleanupDone is closed when the cleanup goroutine has fully stopped
	cleanupDone chan struct{}
}

// MemoryStorageOption configures a MemoryStorage instance.
type MemoryStorageOption func(*MemoryStorage)

// WithCleanupInterval sets a custom cleanup interval.
func WithCleanupInterval(interval time.Duration) MemoryStorageOption {
	return func(s *MemoryStorage) {
		s.cleanupInterval = interval
	}
}

// DefaultMaxClients is the default cap on the in-memory client map.
// /oauth/register is unauthenticated and mints an entry per call; the cap
// bounds memory growth while staying far above any legitimate client count
// (clients register once and reuse their client_id).
const DefaultMaxClients = 10000

// WithMaxClients sets the client-map cap. Once reached, RegisterClient evicts
// the oldest DCR-issued registration that has aged past minClientAge
// (oldest-first). A pre-provisioned client (no registration.DCRIssued marker)
// is never evicted; if no DCR-issued client can be safely evicted,
// RegisterClient returns ErrClientCapacity.
func WithMaxClients(n int) MemoryStorageOption {
	return func(s *MemoryStorage) {
		s.maxClients = n
	}
}

// DefaultMinClientAge is the default grace window below which a DCR-issued
// client is never evicted. See the minClientAge field doc for why the floor
// exists: without it, a freshly registered but not-yet-used client can be
// evicted on demand by an attacker filling the map to capacity.
const DefaultMinClientAge = time.Hour

// WithMinClientAge overrides the grace window below which a DCR-issued
// client is protected from eviction regardless of queue position. Tests use
// this to drive the floor down (e.g. to zero) instead of sleeping for real
// wall-clock time.
func WithMinClientAge(d time.Duration) MemoryStorageOption {
	return func(s *MemoryStorage) {
		s.minClientAge = d
	}
}

// NewMemoryStorage creates a new MemoryStorage instance with initialized maps
// and starts the background cleanup goroutine.
func NewMemoryStorage(opts ...MemoryStorageOption) *MemoryStorage {
	s := &MemoryStorage{
		clients:               make(map[string]fosite.Client),
		authCodes:             make(map[string]*timedEntry[fosite.Requester]),
		accessTokens:          make(map[string]*timedEntry[fosite.Requester]),
		refreshTokens:         make(map[string]*timedEntry[fosite.Requester]),
		pkceRequests:          make(map[string]*timedEntry[fosite.Requester]),
		upstreamTokens:        make(map[upstreamKey]*timedEntry[*UpstreamTokens]),
		pendingAuthorizations: make(map[string]*timedEntry[*PendingAuthorization]),
		invalidatedCodes:      make(map[string]*timedEntry[bool]),
		clientAssertionJWTs:   make(map[string]time.Time),
		assertionJWTs:         make(map[assertionJWTKey]time.Time),
		users:                 make(map[string]*User),
		providerIdentities:    make(map[string]*ProviderIdentity),
		dcrCredentials:        make(map[DCRKey]*DCRCredentials),
		cleanupInterval:       DefaultCleanupInterval,
		maxClients:            DefaultMaxClients,
		minClientAge:          DefaultMinClientAge,
		stopCleanup:           make(chan struct{}),
		cleanupDone:           make(chan struct{}),
	}

	for _, opt := range opts {
		opt(s)
	}

	// Start background cleanup goroutine
	go s.cleanupLoop()

	return s
}

// Health is a no-op for in-memory storage since it is always available.
func (*MemoryStorage) Health(_ context.Context) error {
	return nil
}

// Close stops the background cleanup goroutine and waits for it to finish.
// This should be called when the storage is no longer needed.
func (s *MemoryStorage) Close() error {
	close(s.stopCleanup)
	<-s.cleanupDone
	return nil
}

// cleanupLoop runs periodic cleanup of expired entries.
func (s *MemoryStorage) cleanupLoop() {
	defer close(s.cleanupDone)

	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCleanup:
			return
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

// cleanupExpired removes all expired entries from storage.
// Uses collect-then-delete pattern: collects expired keys under read lock,
// then deletes under write lock. This minimizes write lock hold time.
//
//nolint:gocyclo // Function is straightforward, just repetitive cleanup loops for each storage type
func (s *MemoryStorage) cleanupExpired() {
	now := time.Now()

	// Phase 1: Collect expired keys under read lock
	s.mu.RLock()

	var expiredAuthCodes []string
	for k, v := range s.authCodes {
		if now.After(v.expiresAt) {
			expiredAuthCodes = append(expiredAuthCodes, k)
		}
	}

	var expiredInvalidatedCodes []string
	for k, v := range s.invalidatedCodes {
		if now.After(v.expiresAt) {
			expiredInvalidatedCodes = append(expiredInvalidatedCodes, k)
		}
	}

	var expiredAccessTokens []string
	for k, v := range s.accessTokens {
		if now.After(v.expiresAt) {
			expiredAccessTokens = append(expiredAccessTokens, k)
		}
	}

	var expiredRefreshTokens []string
	for k, v := range s.refreshTokens {
		if now.After(v.expiresAt) {
			expiredRefreshTokens = append(expiredRefreshTokens, k)
		}
	}

	var expiredPKCERequests []string
	for k, v := range s.pkceRequests {
		if now.After(v.expiresAt) {
			expiredPKCERequests = append(expiredPKCERequests, k)
		}
	}

	var expiredUpstreamTokens []upstreamKey
	for k, v := range s.upstreamTokens {
		// Zero expiresAt is the sentinel for "no TTL" (non-expiring token with no session
		// bound). Other entry types never use zero expiresAt, so only upstream tokens need
		// this guard — without it, time.Time{} would compare as before any real time and
		// every non-expiring token would be swept on the next tick.
		if !v.expiresAt.IsZero() && now.After(v.expiresAt) {
			expiredUpstreamTokens = append(expiredUpstreamTokens, k)
		}
	}

	var expiredPendingAuthorizations []string
	for k, v := range s.pendingAuthorizations {
		if now.After(v.expiresAt) {
			expiredPendingAuthorizations = append(expiredPendingAuthorizations, k)
		}
	}

	var expiredJWTs []string
	for k, v := range s.clientAssertionJWTs {
		if now.After(v) {
			expiredJWTs = append(expiredJWTs, k)
		}
	}

	var expiredAssertionJWTs []assertionJWTKey
	for k, v := range s.assertionJWTs {
		if now.After(v) {
			expiredAssertionJWTs = append(expiredAssertionJWTs, k)
		}
	}

	s.mu.RUnlock()

	// Phase 2: Early return if nothing to delete (no write lock needed)
	if len(expiredAuthCodes) == 0 &&
		len(expiredInvalidatedCodes) == 0 &&
		len(expiredAccessTokens) == 0 &&
		len(expiredRefreshTokens) == 0 &&
		len(expiredPKCERequests) == 0 &&
		len(expiredUpstreamTokens) == 0 &&
		len(expiredPendingAuthorizations) == 0 &&
		len(expiredJWTs) == 0 &&
		len(expiredAssertionJWTs) == 0 {
		return
	}

	// Phase 3: Delete collected keys under write lock
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, k := range expiredAuthCodes {
		delete(s.authCodes, k)
		delete(s.invalidatedCodes, k) // Also clean up associated invalidated code
	}

	for _, k := range expiredInvalidatedCodes {
		delete(s.invalidatedCodes, k)
	}

	for _, k := range expiredAccessTokens {
		delete(s.accessTokens, k)
	}

	for _, k := range expiredRefreshTokens {
		delete(s.refreshTokens, k)
	}

	for _, k := range expiredPKCERequests {
		delete(s.pkceRequests, k)
	}

	for _, k := range expiredUpstreamTokens {
		delete(s.upstreamTokens, k)
	}

	for _, k := range expiredPendingAuthorizations {
		delete(s.pendingAuthorizations, k)
	}

	for _, k := range expiredJWTs {
		delete(s.clientAssertionJWTs, k)
	}

	for _, k := range expiredAssertionJWTs {
		if exp, ok := s.assertionJWTs[k]; ok && now.After(exp) {
			delete(s.assertionJWTs, k)
		}
	}
}

// getExpirationFromRequester extracts expiration time from a fosite.Requester session.
// Returns the provided default if expiration cannot be extracted.
//
// This demonstrates why GetExpiresAt lives on fosite.Session: different token types
// (AccessToken, RefreshToken, AuthorizeCode) have different lifetimes, and Session
// stores per-token-type expiration. The Session is the natural container for token
// metadata, while Requester holds the full authorization context.
func getExpirationFromRequester(request fosite.Requester, tokenType fosite.TokenType, defaultTTL time.Duration) time.Time {
	if request == nil {
		return time.Now().Add(defaultTTL)
	}

	session := request.GetSession()
	if session == nil {
		return time.Now().Add(defaultTTL)
	}

	expTime := session.GetExpiresAt(tokenType)
	if expTime.IsZero() {
		return time.Now().Add(defaultTTL)
	}

	return expTime
}

// RegisterClient creates a client in storage. Always create-only: it returns
// ErrAlreadyExists when a client with the same ID is already registered,
// regardless of the new client's origin. /oauth/register is unauthenticated,
// so this must never clobber an existing client; operator-declared clients
// reconcile through ReconcileConfiguredClient instead.
func (s *MemoryStorage) RegisterClient(_ context.Context, client fosite.Client) error {
	id := client.GetID()
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.clients[id]; exists {
		return fmt.Errorf("%w: client %q", ErrAlreadyExists, id)
	}
	return s.insertClientLocked(id, client)
}

// UpsertDCRIssuedClient creates or replaces a DCR-issued client at
// client.GetID(). Unlike RegisterClient, an existing row is replaced (and its
// eviction position refreshed) rather than rejected -- but only when the
// existing row is itself DCR-issued; a configured/SPIFFE-reconciled row at
// the same ID is protected and refuses with ErrAlreadyExists. See the
// ClientRegistry interface doc for the full contract.
func (s *MemoryStorage) UpsertDCRIssuedClient(_ context.Context, client fosite.Client) error {
	if !registration.DCRIssued(client) {
		return fmt.Errorf("client %q must carry the DCR-issued marker to use UpsertDCRIssuedClient", client.GetID())
	}
	id := client.GetID()
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, exists := s.clients[id]
	if !exists {
		return s.insertClientLocked(id, client)
	}
	if !registration.DCRIssued(existing) {
		return fmt.Errorf("%w: client %q", ErrAlreadyExists, id)
	}

	s.clientOrder = slices.DeleteFunc(s.clientOrder, func(e clientOrderEntry) bool { return e.id == id })
	s.clientOrder = append(s.clientOrder, clientOrderEntry{id: id, touchedAt: time.Now()})
	s.clients[id] = client
	return nil
}

// ReconcileConfiguredClient applies an operator-declared client: creates it
// if absent, idempotently replaces a matching-fingerprint configured client
// (the restart-with-unchanged-config case), or refuses with ErrAlreadyExists
// when the existing record is DCR-issued or a different configured client.
// See the ClientRegistry interface doc for the full contract.
func (s *MemoryStorage) ReconcileConfiguredClient(_ context.Context, client fosite.Client) error {
	if registration.DCRIssued(client) {
		return fmt.Errorf("configured client %q must not carry the DCR-issued marker", client.GetID())
	}
	id := client.GetID()
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, exists := s.clients[id]
	if !exists {
		return s.insertClientLocked(id, client)
	}
	if registration.DCRIssued(existing) {
		return fmt.Errorf("%w: client %q is DCR-issued, refusing to overwrite with a configured client",
			ErrAlreadyExists, id)
	}
	if !fingerprintOfClient(existing).equal(fingerprintOfClient(client)) {
		return fmt.Errorf("%w: client %q is already registered as a different configured client",
			ErrAlreadyExists, id)
	}

	// Idempotent restart with unchanged config: refresh the eviction position
	// and replace the stored value so secret rotation still takes effect.
	s.clientOrder = slices.DeleteFunc(s.clientOrder, func(e clientOrderEntry) bool { return e.id == id })
	s.clientOrder = append(s.clientOrder, clientOrderEntry{id: id, touchedAt: time.Now()})
	s.clients[id] = client
	return nil
}

// insertClientLocked inserts client under id, evicting the oldest eligible
// DCR-issued client if the map is at capacity. Callers must hold s.mu and
// must have already verified that no client currently exists at id.
func (s *MemoryStorage) insertClientLocked(id string, client fosite.Client) error {
	now := time.Now()
	if s.maxClients > 0 && len(s.clients) >= s.maxClients {
		if idx := s.oldestEvictableClientIndex(now); idx >= 0 {
			victim := s.clientOrder[idx].id
			s.clientOrder = append(s.clientOrder[:idx], s.clientOrder[idx+1:]...)
			delete(s.clients, victim)
			slog.Debug("evicted oldest DCR-issued client registration at capacity",
				"client_id", victim, "max_clients", s.maxClients)
		} else {
			slog.Debug("client map at capacity with no safely evictable DCR-issued client",
				"max_clients", s.maxClients)
			return fmt.Errorf("%w: no DCR-issued client can be safely evicted", ErrClientCapacity)
		}
	}
	s.clientOrder = append(s.clientOrder, clientOrderEntry{id: id, touchedAt: now})
	s.clients[id] = client
	return nil
}

// oldestEvictableClientIndex returns the clientOrder index of the oldest
// (frontmost) client that carries the registration.DCRIssued marker and has
// aged past minClientAge as of now, or -1 if no current entry is evictable.
// Callers must hold s.mu.
func (s *MemoryStorage) oldestEvictableClientIndex(now time.Time) int {
	for i, entry := range s.clientOrder {
		if now.Sub(entry.touchedAt) < s.minClientAge {
			continue
		}
		if client, ok := s.clients[entry.id]; ok && registration.DCRIssued(client) {
			return i
		}
	}
	return -1
}

// RenewClientTTL moves a DCR-issued client to the back of clientOrder on
// proven use (a successful token exchange/refresh), the same signal
// RedisStorage.RenewClientTTL uses to extend a Redis TTL. The in-memory
// backend holds no wall-clock TTL, so "renewal" here means "make this client
// the least-eligible-for-eviction" rather than resetting an expiry: FIFO
// eviction order thereby becomes least-recently-proven-used order. Only
// DCR-issued clients are repositioned; a pre-provisioned client is never
// evicted so it needs no renewal. A client absent from the map (already
// evicted, or never persisted) is a no-op, matching RedisStorage's EXPIRE
// no-op on a missing key.
func (s *MemoryStorage) RenewClientTTL(_ context.Context, client fosite.Client) error {
	if client == nil || !registration.DCRIssued(client) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := client.GetID()
	if _, exists := s.clients[id]; !exists {
		return nil
	}
	s.clientOrder = slices.DeleteFunc(s.clientOrder, func(e clientOrderEntry) bool { return e.id == id })
	s.clientOrder = append(s.clientOrder, clientOrderEntry{id: id, touchedAt: time.Now()})
	return nil
}

// -----------------------
// fosite.ClientManager
// -----------------------

// GetClient loads the client by its ID or returns an error if the client does not exist.
func (s *MemoryStorage) GetClient(_ context.Context, id string) (fosite.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	client, ok := s.clients[id]
	if !ok {
		slog.Debug("client not found", "client_id", id)
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Client not found"))
	}
	return client, nil
}

// assertionReservationTTL bounds how long ClientAssertionJWTValid's atomic
// reservation lives before SetClientAssertionJWT extends it to the
// assertion's real (bounded) expiry. Short and fixed: if a caller wins the
// reservation and then never calls SetClientAssertionJWT (crash, error path),
// the jti becomes reusable again after this window rather than being stuck
// or, worse, permanently reserved.
const assertionReservationTTL = 30 * time.Second

// ClientAssertionJWTValid atomically reserves jti for a short window,
// returning fosite.ErrJTIKnown if it is already reserved or was previously
// consumed and not yet expired. This is the only atomic checkpoint in
// fosite's two-call client-assertion replay protocol (Valid, then
// SetClientAssertionJWT with the real expiry once the rest of the assertion
// has been validated) — without it, two concurrent requests presenting the
// same assertion could both observe jti as unused before either recorded it.
// jti length is bounded here too, before it's ever stored, since fosite
// hands it to us before parsing exp.
func (s *MemoryStorage) ClientAssertionJWTValid(_ context.Context, jti string) error {
	if len(jti) > server.MaxAssertionJTILength {
		return fmt.Errorf("jti exceeds maximum length of %d bytes", server.MaxAssertionJTILength)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if exp, ok := s.clientAssertionJWTs[jti]; ok && now.Before(exp) {
		return fosite.ErrJTIKnown
	}

	// Clean up expired JTIs while the lock is already held.
	for k, v := range s.clientAssertionJWTs {
		if now.After(v) {
			delete(s.clientAssertionJWTs, k)
		}
	}

	s.clientAssertionJWTs[jti] = now.Add(assertionReservationTTL)
	return nil
}

// SetClientAssertionJWT extends jti's replay marker to its real expiry,
// rejecting an exp further than MaxAssertionLifespan in the future. This
// bounds how long a replay-tracking entry survives regardless of what the
// client's own assertion claims — otherwise a registered caller could set
// exp decades out and grow this map indefinitely. Unconditional overwrite,
// not a fresh atomic check: only the caller that already won
// ClientAssertionJWTValid's reservation reaches this call.
func (s *MemoryStorage) SetClientAssertionJWT(_ context.Context, jti string, exp time.Time) error {
	if time.Until(exp) > server.MaxAssertionLifespan {
		return fmt.Errorf("assertion lifetime exceeds maximum of %s", server.MaxAssertionLifespan)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.clientAssertionJWTs[jti] = exp
	return nil
}

// ConsumeAssertionJWT atomically records an assertion JWT as consumed until its
// expiry. Reusing an unexpired (purpose, issuer, jti) tuple returns
// fosite.ErrJTIKnown. Expired assertions are not retained because a caller must
// reject them during JWT validation before attempting replay consumption.
func (s *MemoryStorage) ConsumeAssertionJWT(_ context.Context, purpose, issuer, jti string, exp time.Time) error {
	key := assertionJWTKey{purpose: purpose, issuer: issuer, jti: jti}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if !exp.After(now) {
		return nil
	}

	if storedExp, ok := s.assertionJWTs[key]; ok {
		if storedExp.After(now) {
			return fosite.ErrJTIKnown
		}
		delete(s.assertionJWTs, key)
	}

	s.assertionJWTs[key] = exp
	return nil
}

// -----------------------
// oauth2.AuthorizeCodeStorage
// -----------------------

// CreateAuthorizeCodeSession stores the authorization request for a given authorization code.
func (s *MemoryStorage) CreateAuthorizeCodeSession(_ context.Context, code string, request fosite.Requester) error {
	if code == "" {
		return fosite.ErrInvalidRequest.WithHint("authorization code cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := getExpirationFromRequester(request, fosite.AuthorizeCode, DefaultAuthCodeTTL)

	s.authCodes[code] = &timedEntry[fosite.Requester]{
		value:     request,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetAuthorizeCodeSession retrieves the authorization request for a given code.
// If the authorization code has been invalidated, it returns ErrInvalidatedAuthorizeCode
// along with the request (as required by fosite).
func (s *MemoryStorage) GetAuthorizeCodeSession(_ context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.authCodes[code]
	if !ok {
		slog.Debug("authorization code not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
	}

	// Check if the code has been invalidated
	if s.invalidatedCodes[code] != nil {
		// Must return the request along with the error as per fosite documentation
		return entry.value, fosite.ErrInvalidatedAuthorizeCode
	}

	return entry.value, nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
// Subsequent calls to GetAuthorizeCodeSession will return ErrInvalidatedAuthorizeCode.
func (s *MemoryStorage) InvalidateAuthorizeCodeSession(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.authCodes[code]; !ok {
		slog.Debug("authorization code not found for invalidation")
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
	}

	now := time.Now()
	s.invalidatedCodes[code] = &timedEntry[bool]{
		value:     true,
		createdAt: now,
		expiresAt: now.Add(DefaultInvalidatedCodeTTL),
	}
	return nil
}

// -----------------------
// oauth2.AccessTokenStorage
// -----------------------

// CreateAccessTokenSession stores the access token session.
func (s *MemoryStorage) CreateAccessTokenSession(_ context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("access token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := getExpirationFromRequester(request, fosite.AccessToken, DefaultAccessTokenTTL)

	s.accessTokens[signature] = &timedEntry[fosite.Requester]{
		value:     request,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetAccessTokenSession retrieves the access token session by its signature.
//
// The session parameter is a prototype for deserialization in persistent backends.
// Our in-memory implementation ignores it since we store live Requester objects.
// Persistent backends (SQL, Redis) use it to know what concrete type to deserialize into.
func (s *MemoryStorage) GetAccessTokenSession(_ context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.accessTokens[signature]
	if !ok {
		slog.Debug("access token not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
	}
	return entry.value, nil
}

// DeleteAccessTokenSession removes the access token session.
func (s *MemoryStorage) DeleteAccessTokenSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accessTokens[signature]; !ok {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
	}
	delete(s.accessTokens, signature)
	return nil
}

// -----------------------
// oauth2.RefreshTokenStorage
// -----------------------

// CreateRefreshTokenSession stores the refresh token session.
// The accessSignature parameter is used to link the refresh token to its access token.
// TODO: Store the accessSignature in a refreshToAccess map to enable direct lookup
// during token rotation instead of O(n) scan by request ID in RotateRefreshToken.
func (s *MemoryStorage) CreateRefreshTokenSession(_ context.Context, signature string, _ string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("refresh token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := getExpirationFromRequester(request, fosite.RefreshToken, DefaultRefreshTokenTTL)

	s.refreshTokens[signature] = &timedEntry[fosite.Requester]{
		value:     request,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetRefreshTokenSession retrieves the refresh token session by its signature.
func (s *MemoryStorage) GetRefreshTokenSession(_ context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refreshTokens[signature]
	if !ok {
		slog.Debug("refresh token not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
	}
	return entry.value, nil
}

// DeleteRefreshTokenSession removes the refresh token session.
func (s *MemoryStorage) DeleteRefreshTokenSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.refreshTokens[signature]; !ok {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
	}
	delete(s.refreshTokens, signature)
	return nil
}

// RotateRefreshToken invalidates a refresh token and all its related token data.
// This is called during token refresh to implement refresh token rotation.
func (s *MemoryStorage) RotateRefreshToken(_ context.Context, requestID string, refreshTokenSignature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete the specific refresh token
	delete(s.refreshTokens, refreshTokenSignature)

	// TODO: Use the refreshToAccess map (once implemented) for direct access token lookup
	// instead of O(n) scan by request ID, which may delete unrelated tokens sharing the same ID.
	// Also delete any access tokens associated with this request ID
	for sig, entry := range s.accessTokens {
		if entry.value.GetID() == requestID {
			delete(s.accessTokens, sig)
		}
	}

	return nil
}

// -----------------------
// oauth2.TokenRevocationStorage
// -----------------------

// RevokeAccessToken marks an access token as revoked.
// This method implements the oauth2.TokenRevocationStorage interface.
//
// Note: This takes requestID, not signature. Per RFC 7009, revoking by request ID
// enables revoking ALL tokens from the same authorization grant. This is why we
// store the full Requester (with its ID) rather than just token values.
//
// The O(n) scan by request ID is acceptable for in-memory storage. Production
// implementations should maintain a reverse index (request_id -> signatures).
func (s *MemoryStorage) RevokeAccessToken(_ context.Context, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find and remove all access tokens associated with this request ID.
	// Uses Requester.GetID() to match the grant identifier, not the token signature.
	for sig, entry := range s.accessTokens {
		if entry.value.GetID() == requestID {
			delete(s.accessTokens, sig)
		}
	}

	return nil
}

// RevokeRefreshToken marks a refresh token as revoked.
// This method implements the oauth2.TokenRevocationStorage interface.
//
// Like RevokeAccessToken, this takes requestID to find all refresh tokens from
// the same authorization grant. Per RFC 7009 Section 2.1, implementations SHOULD
// also revoke associated access tokens, which RotateRefreshToken handles.
func (s *MemoryStorage) RevokeRefreshToken(_ context.Context, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find and remove all refresh tokens associated with this request ID.
	for sig, entry := range s.refreshTokens {
		if entry.value.GetID() == requestID {
			delete(s.refreshTokens, sig)
		}
	}

	return nil
}

// RevokeRefreshTokenMaybeGracePeriod marks a refresh token as revoked, optionally allowing
// a grace period during which the old token is still valid.
// For this implementation, we don't support grace periods and revoke immediately.
func (s *MemoryStorage) RevokeRefreshTokenMaybeGracePeriod(ctx context.Context, requestID string, _ string) error {
	return s.RevokeRefreshToken(ctx, requestID)
}

// -----------------------
// pkce.PKCERequestStorage
// -----------------------

// CreatePKCERequestSession stores the PKCE request session.
func (s *MemoryStorage) CreatePKCERequestSession(_ context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("PKCE signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := getExpirationFromRequester(request, fosite.AuthorizeCode, DefaultPKCETTL)

	s.pkceRequests[signature] = &timedEntry[fosite.Requester]{
		value:     request,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetPKCERequestSession retrieves the PKCE request session by its signature.
func (s *MemoryStorage) GetPKCERequestSession(_ context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.pkceRequests[signature]
	if !ok {
		slog.Debug("pkce request not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
	}
	return entry.value, nil
}

// DeletePKCERequestSession removes the PKCE request session.
func (s *MemoryStorage) DeletePKCERequestSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pkceRequests[signature]; !ok {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
	}
	delete(s.pkceRequests, signature)
	return nil
}

// -----------------------
// Upstream Token Storage
// -----------------------

// ResolveUpstreamTokenRowID returns an opaque ID for process-local refresh
// coordination of a logical upstream-token row.
func (*MemoryStorage) ResolveUpstreamTokenRowID(_ context.Context, sessionID, providerName string) (UpstreamTokenRowID, error) {
	if sessionID == "" {
		return "", fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return "", fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}
	return upstreamTokenRowID(sessionID, providerName), nil
}

// StoreUpstreamTokens stores the upstream IDP tokens for a session and provider.
// A defensive copy is made to prevent aliasing issues.
func (s *MemoryStorage) StoreUpstreamTokens(_ context.Context, sessionID, providerName string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	// Add DefaultRefreshTokenTTL beyond access token expiry so the refresh token
	// survives in storage for transparent token refresh by the middleware.
	// Zero ExpiresAt means the token never expires; no TTL is applied.
	expiresAt := func() time.Time {
		if tokens == nil {
			return now.Add(DefaultAccessTokenTTL + DefaultRefreshTokenTTL)
		}
		if !tokens.ExpiresAt.IsZero() {
			return tokens.ExpiresAt.Add(DefaultRefreshTokenTTL)
		}
		if !tokens.SessionExpiresAt.IsZero() {
			return tokens.SessionExpiresAt.Add(DefaultRefreshTokenTTL)
		}
		return time.Time{} // non-expiring token with no known session bound
	}()

	// Make a defensive copy to prevent aliasing issues
	var tokensCopy *UpstreamTokens
	if tokens != nil {
		tokensCopy = &UpstreamTokens{
			ProviderID:       tokens.ProviderID,
			AccessToken:      tokens.AccessToken,
			RefreshToken:     tokens.RefreshToken,
			IDToken:          tokens.IDToken,
			ExpiresAt:        tokens.ExpiresAt,
			SessionExpiresAt: tokens.SessionExpiresAt,
			UserID:           tokens.UserID,
			UpstreamSubject:  tokens.UpstreamSubject,
			ClientID:         tokens.ClientID,
		}
	}

	s.upstreamTokens[upstreamKey{sessionID, providerName}] = &timedEntry[*UpstreamTokens]{
		value:     tokensCopy,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// cloneUpstreamTokens returns a field-by-field copy of t, or nil if t is nil.
func cloneUpstreamTokens(t *UpstreamTokens) *UpstreamTokens {
	if t == nil {
		return nil
	}
	return &UpstreamTokens{
		ProviderID:       t.ProviderID,
		AccessToken:      t.AccessToken,
		RefreshToken:     t.RefreshToken,
		IDToken:          t.IDToken,
		ExpiresAt:        t.ExpiresAt,
		SessionExpiresAt: t.SessionExpiresAt,
		UserID:           t.UserID,
		UpstreamSubject:  t.UpstreamSubject,
		ClientID:         t.ClientID,
	}
}

// GetUpstreamTokens retrieves the upstream IDP tokens for a session and provider.
// Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) GetUpstreamTokens(_ context.Context, sessionID, providerName string) (*UpstreamTokens, error) {
	if sessionID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.upstreamTokens[upstreamKey{sessionID, providerName}]
	if !ok {
		slog.Debug("upstream tokens not found", "session_id", sessionID, "provider_name", providerName)
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	// Return a defensive copy to prevent aliasing issues
	result := cloneUpstreamTokens(entry.value)
	if result == nil {
		return nil, nil
	}

	// Check the token's own ExpiresAt (access token expiry), not the entry's expiresAt
	// (storage TTL which includes DefaultRefreshTokenTTL buffer for refresh token survival).
	// Return tokens along with ErrExpired so callers can use the refresh token.
	if !result.ExpiresAt.IsZero() && time.Now().After(result.ExpiresAt) {
		slog.Debug("upstream tokens expired", "session_id", sessionID, "provider_name", providerName)
		return result, ErrExpired
	}

	return result, nil
}

// GetAllUpstreamTokens retrieves all upstream IDP tokens for a session across all providers.
// Returns a map of providerName -> tokens with defensive copies.
// Returns an empty map (not error) for unknown sessions.
// Includes expired tokens (no expiry filtering at bulk-read level).
func (s *MemoryStorage) GetAllUpstreamTokens(_ context.Context, sessionID string) (map[string]*UpstreamTokens, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*UpstreamTokens)
	for key, entry := range s.upstreamTokens {
		if key.sessionID != sessionID {
			continue
		}
		// Defensive copy (cloneUpstreamTokens handles nil)
		result[key.providerName] = cloneUpstreamTokens(entry.value)
	}

	return result, nil
}

// DeleteUpstreamTokens removes all upstream IDP tokens for a session (all providers).
func (s *MemoryStorage) DeleteUpstreamTokens(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for key := range s.upstreamTokens {
		if key.sessionID == sessionID {
			delete(s.upstreamTokens, key)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}
	return nil
}

// DeleteUpstreamTokensForProvider removes tokens for a single (sessionID, providerName),
// leaving sibling providers' rows intact. Absent row returns nil (not ErrNotFound).
func (s *MemoryStorage) DeleteUpstreamTokensForProvider(_ context.Context, sessionID, providerName string) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.upstreamTokens, upstreamKey{sessionID, providerName})
	return nil
}

// compareExpiry orders ExpiresAt values for the GetLatestUpstreamTokensForUser
// tie-breaker. Non-expiring rows (zero ExpiresAt — "alive forever") rank latest;
// among finite expiries, later ranks latest. Mirrors time.Compare but with the
// zero sentinel reinterpreted. Returns -1/0/+1.
func compareExpiry(a, b time.Time) int {
	aZero, bZero := a.IsZero(), b.IsZero()
	switch {
	case aZero && bZero:
		return 0
	case aZero:
		return 1
	case bZero:
		return -1
	}
	return a.Compare(b)
}

// GetLatestUpstreamTokensForUser implements UpstreamTokenStorage.
//
// Expired tokens (past ExpiresAt) are returned so callers can use the refresh
// token; filtering by access-token expiry is the caller's responsibility.
// See the interface declaration in types.go for the full contract.
func (s *MemoryStorage) GetLatestUpstreamTokensForUser(_ context.Context, userID, providerID string) (*UpstreamTokens, error) {
	if userID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}
	if providerID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider ID cannot be empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var winner *UpstreamTokens
	for _, entry := range s.upstreamTokens {
		if entry.value == nil || entry.value.UserID != userID || entry.value.ProviderID != providerID {
			continue
		}
		if winner == nil || compareExpiry(entry.value.ExpiresAt, winner.ExpiresAt) > 0 {
			winner = entry.value
		}
	}

	if winner == nil {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	return cloneUpstreamTokens(winner), nil
}

// -----------------------
// Pending Authorization Storage
// -----------------------

// StorePendingAuthorization stores a pending authorization request.
// The pending authorization is keyed by the internal state used to correlate
// the upstream IDP callback.
func (s *MemoryStorage) StorePendingAuthorization(_ context.Context, state string, pending *PendingAuthorization) error {
	if state == "" {
		return fosite.ErrInvalidRequest.WithHint("state cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending authorization cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := now.Add(DefaultPendingAuthorizationTTL)

	// Make a defensive copy to prevent aliasing issues
	pendingCopy := &PendingAuthorization{
		ClientID:             pending.ClientID,
		RedirectURI:          pending.RedirectURI,
		State:                pending.State,
		PKCEChallenge:        pending.PKCEChallenge,
		PKCEMethod:           pending.PKCEMethod,
		Scopes:               slices.Clone(pending.Scopes),
		InternalState:        pending.InternalState,
		UpstreamPKCEVerifier: pending.UpstreamPKCEVerifier,
		UpstreamNonce:        pending.UpstreamNonce,
		UpstreamProviderName: pending.UpstreamProviderName,
		SessionID:            pending.SessionID,
		ResolvedUserID:       pending.ResolvedUserID,
		ResolvedUserName:     pending.ResolvedUserName,
		ResolvedUserEmail:    pending.ResolvedUserEmail,
		SingleLeg:            pending.SingleLeg,
		ChainUpstreams:       slices.Clone(pending.ChainUpstreams),
		CreatedAt:            pending.CreatedAt,
	}

	s.pendingAuthorizations[state] = &timedEntry[*PendingAuthorization]{
		value:     pendingCopy,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// LoadPendingAuthorization retrieves a pending authorization by internal state.
// Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) LoadPendingAuthorization(_ context.Context, state string) (*PendingAuthorization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.pendingAuthorizations[state]
	if !ok {
		slog.Debug("pending authorization not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		slog.Debug("pending authorization expired")
		return nil, ErrExpired
	}

	// Return a defensive copy to prevent aliasing issues
	pending := entry.value
	if pending == nil {
		return nil, nil
	}
	return &PendingAuthorization{
		ClientID:             pending.ClientID,
		RedirectURI:          pending.RedirectURI,
		State:                pending.State,
		PKCEChallenge:        pending.PKCEChallenge,
		PKCEMethod:           pending.PKCEMethod,
		Scopes:               slices.Clone(pending.Scopes),
		InternalState:        pending.InternalState,
		UpstreamPKCEVerifier: pending.UpstreamPKCEVerifier,
		UpstreamNonce:        pending.UpstreamNonce,
		UpstreamProviderName: pending.UpstreamProviderName,
		SessionID:            pending.SessionID,
		ResolvedUserID:       pending.ResolvedUserID,
		ResolvedUserName:     pending.ResolvedUserName,
		ResolvedUserEmail:    pending.ResolvedUserEmail,
		SingleLeg:            pending.SingleLeg,
		ChainUpstreams:       slices.Clone(pending.ChainUpstreams),
		CreatedAt:            pending.CreatedAt,
	}, nil
}

// DeletePendingAuthorization removes a pending authorization.
func (s *MemoryStorage) DeletePendingAuthorization(_ context.Context, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pendingAuthorizations[state]; !ok {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
	}
	delete(s.pendingAuthorizations, state)
	return nil
}

// -----------------------
// User Storage
// -----------------------

// providerIdentityKey creates a unique key for a provider identity.
// The key format is "len(providerID):providerID:providerSubject" for O(1) lookup.
// The length prefix ensures collision-free keys even if providerID or providerSubject
// contain colons (which is valid per RFC 7519 StringOrURI semantics for OIDC subjects).
func providerIdentityKey(providerID, providerSubject string) string {
	return fmt.Sprintf("%d:%s:%s", len(providerID), providerID, providerSubject)
}

// CreateUser creates a new user account.
// Returns ErrAlreadyExists if a user with the same ID already exists.
func (s *MemoryStorage) CreateUser(_ context.Context, user *User) error {
	if user == nil {
		return fosite.ErrInvalidRequest.WithHint("user cannot be nil")
	}
	if user.ID == "" {
		return fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.users[user.ID]; exists {
		return fmt.Errorf("%w: user already exists", ErrAlreadyExists)
	}

	// Make a defensive copy
	s.users[user.ID] = &User{
		ID:        user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	}
	return nil
}

// GetUser retrieves a user by their internal ID.
// Returns ErrNotFound if the user does not exist.
func (s *MemoryStorage) GetUser(_ context.Context, id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return nil, fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Return a defensive copy
	return &User{
		ID:        user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	}, nil
}

// DeleteUser removes a user account and all associated provider identities.
// Returns ErrNotFound if the user does not exist.
func (s *MemoryStorage) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.users[id]; !exists {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Delete all associated provider identities
	for key, identity := range s.providerIdentities {
		if identity.UserID == id {
			delete(s.providerIdentities, key)
		}
	}

	// Delete all associated upstream tokens
	for key, entry := range s.upstreamTokens {
		if entry.value != nil && entry.value.UserID == id {
			delete(s.upstreamTokens, key)
		}
	}

	delete(s.users, id)
	return nil
}

// CreateProviderIdentity links a provider identity to a user.
// Returns ErrAlreadyExists if this provider identity is already linked.
func (s *MemoryStorage) CreateProviderIdentity(_ context.Context, identity *ProviderIdentity) error {
	if identity == nil {
		return fosite.ErrInvalidRequest.WithHint("identity cannot be nil")
	}
	if identity.UserID == "" {
		return fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}
	if identity.ProviderID == "" {
		return fosite.ErrInvalidRequest.WithHint("provider ID cannot be empty")
	}
	if identity.ProviderSubject == "" {
		return fosite.ErrInvalidRequest.WithHint("provider subject cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify user exists before linking identity
	if _, exists := s.users[identity.UserID]; !exists {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	key := providerIdentityKey(identity.ProviderID, identity.ProviderSubject)
	if _, exists := s.providerIdentities[key]; exists {
		return fmt.Errorf("%w: provider identity already linked", ErrAlreadyExists)
	}

	// Make a defensive copy
	s.providerIdentities[key] = &ProviderIdentity{
		UserID:          identity.UserID,
		ProviderID:      identity.ProviderID,
		ProviderSubject: identity.ProviderSubject,
		LinkedAt:        identity.LinkedAt,
		LastUsedAt:      identity.LastUsedAt,
	}
	return nil
}

// GetProviderIdentity retrieves a provider identity by provider ID and subject.
// This is the primary lookup path during authentication callbacks.
// Returns ErrNotFound if the identity does not exist.
func (s *MemoryStorage) GetProviderIdentity(_ context.Context, providerID, providerSubject string) (*ProviderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := providerIdentityKey(providerID, providerSubject)
	identity, ok := s.providerIdentities[key]
	if !ok {
		return nil, fmt.Errorf("%w: provider identity not found", ErrNotFound)
	}

	// Return a defensive copy
	return &ProviderIdentity{
		UserID:          identity.UserID,
		ProviderID:      identity.ProviderID,
		ProviderSubject: identity.ProviderSubject,
		LinkedAt:        identity.LinkedAt,
		LastUsedAt:      identity.LastUsedAt,
	}, nil
}

// UpdateProviderIdentityLastUsed updates the LastUsedAt timestamp for a provider identity.
// This should be called after each successful authentication via this identity.
// Returns ErrNotFound if the identity does not exist.
func (s *MemoryStorage) UpdateProviderIdentityLastUsed(
	_ context.Context, providerID, providerSubject string, lastUsedAt time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := providerIdentityKey(providerID, providerSubject)
	identity, ok := s.providerIdentities[key]
	if !ok {
		return fmt.Errorf("%w: provider identity not found", ErrNotFound)
	}

	identity.LastUsedAt = lastUsedAt
	return nil
}

// GetUserProviderIdentities returns all provider identities linked to a user.
// Returns an empty slice (not error) if the user exists but has no linked identities.
// Returns ErrNotFound if the user does not exist.
func (s *MemoryStorage) GetUserProviderIdentities(_ context.Context, userID string) ([]*ProviderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Verify user exists
	if _, exists := s.users[userID]; !exists {
		return nil, fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Collect all identities for this user
	var identities []*ProviderIdentity
	for _, identity := range s.providerIdentities {
		if identity.UserID == userID {
			// Return defensive copies
			identities = append(identities, &ProviderIdentity{
				UserID:          identity.UserID,
				ProviderID:      identity.ProviderID,
				ProviderSubject: identity.ProviderSubject,
				LinkedAt:        identity.LinkedAt,
				LastUsedAt:      identity.LastUsedAt,
			})
		}
	}

	return identities, nil
}

// -----------------------
// DCR Credentials Storage
// -----------------------

// cloneDCRCredentials returns a field-by-field copy of c, or nil if c is nil.
// All fields are values (no slices, maps, or pointers), so a shallow copy is
// sufficient — adding a new reference-typed field requires updating this
// helper to deep-copy that field.
func cloneDCRCredentials(c *DCRCredentials) *DCRCredentials {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// StoreDCRCredentialsIfAbsent claims creds.Key for creds. The credentials
// are stored under their own Key field; callers must populate it before
// calling. A defensive copy is made so subsequent caller mutations do not
// affect persisted state.
//
// Create-if-absent, not overwrite: a single process's dcrFlight singleflight
// (see pkg/auth/dcr) already prevents concurrent same-key writers within
// this process, so the check below is not closing a live race here — it is
// contract symmetry with RedisStorage.StoreDCRCredentialsIfAbsent, which
// DOES need it to prevent two replicas from independently registering RFC
// 7591 clients for the same Key and racing on which write wins.
//
// The in-memory backend has no native TTL, so "absent" cannot be a plain
// map-presence check: the Redis backend's rows self-evict via TTL derived
// from ClientSecretExpiresAt, so a claim there naturally succeeds again once
// the old row expires. To keep behaviour symmetric, an existing entry whose
// ClientSecretExpiresAt is non-zero and already in the past is treated as
// absent — the claim proceeds and overwrites it — so a secret's expiry does
// not permanently block re-registration on this backend the way a naive
// presence check would.
//
// Validation is delegated to validateDCRCredentialsForStore so the rejection
// set stays in sync with sibling backends.
func (s *MemoryStorage) StoreDCRCredentialsIfAbsent(_ context.Context, creds *DCRCredentials) (*DCRCredentials, error) {
	if err := validateDCRCredentialsForStore(creds); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.dcrCredentials[creds.Key]; ok {
		expired := !existing.ClientSecretExpiresAt.IsZero() && time.Now().After(existing.ClientSecretExpiresAt)
		if !expired {
			return cloneDCRCredentials(existing), nil
		}
	}

	s.dcrCredentials[creds.Key] = cloneDCRCredentials(creds)
	return cloneDCRCredentials(creds), nil
}

// GetDCRCredentials retrieves DCR credentials by key.
// Returns a defensive copy; returns ErrNotFound (wrapped) on miss.
func (s *MemoryStorage) GetDCRCredentials(_ context.Context, key DCRKey) (*DCRCredentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.dcrCredentials[key]
	if !ok {
		slog.Debug("dcr credentials not found",
			"issuer", key.Issuer,
			"upstream_id", key.UpstreamID,
			"redirect_uri", key.RedirectURI,
		)
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("DCR credentials not found"))
	}

	return cloneDCRCredentials(entry), nil
}

// -----------------------
// Metrics/Stats (for testing and monitoring)
// -----------------------

// Stats contains statistics about the storage contents.
type Stats struct {
	Clients               int
	AuthCodes             int
	AccessTokens          int
	RefreshTokens         int
	PKCERequests          int
	UpstreamTokens        int
	PendingAuthorizations int
	InvalidatedCodes      int
	ClientAssertionJWTs   int
	Users                 int
	ProviderIdentities    int
	DCRCredentials        int
}

// Stats returns current statistics about storage contents.
// This is useful for testing and monitoring.
func (s *MemoryStorage) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return Stats{
		Clients:               len(s.clients),
		AuthCodes:             len(s.authCodes),
		AccessTokens:          len(s.accessTokens),
		RefreshTokens:         len(s.refreshTokens),
		PKCERequests:          len(s.pkceRequests),
		UpstreamTokens:        len(s.upstreamTokens),
		PendingAuthorizations: len(s.pendingAuthorizations),
		InvalidatedCodes:      len(s.invalidatedCodes),
		ClientAssertionJWTs:   len(s.clientAssertionJWTs),
		Users:                 len(s.users),
		ProviderIdentities:    len(s.providerIdentities),
		DCRCredentials:        len(s.dcrCredentials),
	}
}

// Compile-time interface compliance checks
var (
	_ Storage                     = (*MemoryStorage)(nil)
	_ PendingAuthorizationStorage = (*MemoryStorage)(nil)
	_ ClientRegistry              = (*MemoryStorage)(nil)
	_ UpstreamTokenStorage        = (*MemoryStorage)(nil)
	_ UserStorage                 = (*MemoryStorage)(nil)
	_ DCRCredentialStore          = (*MemoryStorage)(nil)
	_ AssertionJWTConsumer        = (*MemoryStorage)(nil)
)
