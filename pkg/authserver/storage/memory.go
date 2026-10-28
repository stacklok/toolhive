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
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// defaultMaxMemoryCIMDClients caps the live CIMD rows MemoryStorage keeps.
// Each distinct client metadata document URL is one row, so real deployments
// sit far below it.
const defaultMaxMemoryCIMDClients = 10000

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

// exchangeGrantMarker is the replay marker for the subject access token behind
// a top-level exchange grant. It holds the grant's signature so a later
// replay-revoke can find the grant.
type exchangeGrantMarker struct {
	signature string
	expiresAt time.Time
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

	// dcrClientExpiry holds the expiry of every client in clients counted by
	// countsTowardDCRCap, and cimdClientExpiry that of every other client
	// carrying the registration.DCRIssued marker (the CIMD rows), mirroring
	// the key TTL RedisStorage gives the same rows. They are split so each
	// cap walks only the clients it bounds. A client in neither never
	// expires. Every read treats an expired client as absent, and
	// cleanupExpired removes it.
	dcrClientExpiry  map[string]time.Time
	cimdClientExpiry map[string]time.Time

	// configured holds the ID of every client in clients that was last
	// written by ReconcileConfiguredClient. It is a side map, not a wrapper
	// around the stored value: wrapping a SPIFFE placeholder would break
	// isReservedPlaceholder and make a second reconcile of it fail. GetClient
	// wraps non-reserved stamped clients on the way out. Every other write,
	// delete and expiry of an ID clears its entry.
	configured map[string]struct{}

	// maxDCRClients caps the live clients counted by countsTowardDCRCap; zero
	// means unlimited. /oauth/register is unauthenticated and every call
	// mints an entry, so an uncapped map is a memory-exhaustion DoS in the
	// same process as the proxy. At the cap RegisterClient rejects with a
	// *ClientCapacityError and evicts nothing: evicting would let a caller
	// registering at the full rate push out every idle real client. A full
	// map drains as entries expire.
	maxDCRClients int

	// maxCIMDClients caps the live CIMD rows UpsertDCRIssuedClient writes.
	// Unauthenticated /oauth/authorize traffic mints them, bounded otherwise
	// only by the per-process CIMD rate limit and their 30-day TTL. At the cap
	// a new row is refused with a *ClientCapacityError. The CIMD decorator's
	// write-through is best-effort, and this backend keeps sessions as Go
	// values rather than rehydrating their client from the row, so a refused
	// row costs at most a document refetch. RedisStorage has no such cap.
	maxCIMDClients int

	// now returns the current time. It governs client expiry and exchange
	// grant expiry; the other maps use time.Now.
	now func() time.Time

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
	consentSessions       map[string]*timedEntry[*ConsentSession]
	clientApprovals       map[[2]string]*timedEntry[*ClientApproval]

	// deviceRequests maps device_code -> timedEntry[*DeviceRequest]. The
	// canonical store; TTL-bounded like pendingAuthorizations.
	deviceRequests map[string]*timedEntry[*DeviceRequest]

	// deviceRequestsByUserCode is a secondary index, user_code -> device_code,
	// so the verification page can look up a request without scanning
	// deviceRequests. Kept in lockstep with deviceRequests: every store/delete
	// touches both maps under the same lock.
	deviceRequestsByUserCode map[string]string

	// pendingDeviceLogins tracks device-flow verification-page logins
	// awaiting the upstream IDP callback, keyed by state -- the same role
	// pendingAuthorizations plays for the client authorization_code flow.
	pendingDeviceLogins map[string]*timedEntry[*PendingDeviceLogin]

	// pendingDeviceConfirmations tracks resolved device-flow logins awaiting
	// an explicit Approve/Deny decision at the verification page, keyed by
	// an opaque confirmation token.
	pendingDeviceConfirmations map[string]*timedEntry[*PendingDeviceConfirmation]

	// invalidatedCodes tracks auth codes that have been used/invalidated.
	// Kept separate from authCodes to return the Requester with ErrInvalidatedAuthorizeCode.
	invalidatedCodes map[string]*timedEntry[bool]

	// clientAssertionJWTs tracks JTIs to prevent JWT replay attacks per RFC 7523.
	clientAssertionJWTs map[string]time.Time

	// assertionJWTs tracks consumed assertion JWTs by purpose, issuer, and JTI.
	assertionJWTs map[assertionJWTKey]time.Time

	// exchangeGrants maps grant signature -> record for refresh tokens issued
	// through token exchange. Records are stored as private copies and removed by
	// cleanupExpired after ExpiresAt.
	exchangeGrants map[string]*ExchangeGrant

	// exchangeGrantJTIs holds the replay markers of top-level exchange grants,
	// keyed like assertionJWTs (purpose, issuer, jti). It is separate from
	// assertionJWTs because its values carry the grant signature, and so
	// ConsumeAssertionJWT behaves exactly as before. A marker lives until the
	// subject token's exp plus ExchangeGrantMarkerMargin.
	exchangeGrantJTIs map[assertionJWTKey]exchangeGrantMarker

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
	// handler, so unlike clients (bounded by maxDCRClients and the client expiry maps because
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

// WithMaxDCRClients sets the cap on live clients counted by
// countsTowardDCRCap; zero means unlimited. At the cap RegisterClient returns a
// *ClientCapacityError and evicts nothing. Operator-configured and CIMD
// clients are never counted. It panics on a negative n, which
// RunConfig.ResolvedMaxDCRClients already rejects.
func WithMaxDCRClients(n int) MemoryStorageOption {
	if n < 0 {
		panic(fmt.Sprintf("max DCR clients must not be negative, got %d", n))
	}
	return func(s *MemoryStorage) {
		s.maxDCRClients = n
	}
}

// withMaxCIMDClients overrides defaultMaxMemoryCIMDClients, so tests can
// reach the CIMD cap.
func withMaxCIMDClients(n int) MemoryStorageOption {
	return func(s *MemoryStorage) {
		s.maxCIMDClients = n
	}
}

// withClientClock sets the clock used for client expiry and exchange grants,
// so tests can move past a TTL without sleeping.
func withClientClock(now func() time.Time) MemoryStorageOption {
	return func(s *MemoryStorage) {
		s.now = now
	}
}

// NewMemoryStorage creates a new MemoryStorage instance with initialized maps
// and starts the background cleanup goroutine.
func NewMemoryStorage(opts ...MemoryStorageOption) *MemoryStorage {
	s := &MemoryStorage{
		clients:                    make(map[string]fosite.Client),
		authCodes:                  make(map[string]*timedEntry[fosite.Requester]),
		accessTokens:               make(map[string]*timedEntry[fosite.Requester]),
		refreshTokens:              make(map[string]*timedEntry[fosite.Requester]),
		pkceRequests:               make(map[string]*timedEntry[fosite.Requester]),
		upstreamTokens:             make(map[upstreamKey]*timedEntry[*UpstreamTokens]),
		pendingAuthorizations:      make(map[string]*timedEntry[*PendingAuthorization]),
		consentSessions:            make(map[string]*timedEntry[*ConsentSession]),
		clientApprovals:            make(map[[2]string]*timedEntry[*ClientApproval]),
		deviceRequests:             make(map[string]*timedEntry[*DeviceRequest]),
		deviceRequestsByUserCode:   make(map[string]string),
		pendingDeviceLogins:        make(map[string]*timedEntry[*PendingDeviceLogin]),
		pendingDeviceConfirmations: make(map[string]*timedEntry[*PendingDeviceConfirmation]),
		invalidatedCodes:           make(map[string]*timedEntry[bool]),
		clientAssertionJWTs:        make(map[string]time.Time),
		assertionJWTs:              make(map[assertionJWTKey]time.Time),
		exchangeGrants:             make(map[string]*ExchangeGrant),
		exchangeGrantJTIs:          make(map[assertionJWTKey]exchangeGrantMarker),
		users:                      make(map[string]*User),
		providerIdentities:         make(map[string]*ProviderIdentity),
		dcrCredentials:             make(map[DCRKey]*DCRCredentials),
		cleanupInterval:            DefaultCleanupInterval,
		dcrClientExpiry:            make(map[string]time.Time),
		cimdClientExpiry:           make(map[string]time.Time),
		configured:                 make(map[string]struct{}),
		maxDCRClients:              DefaultMaxDCRClients,
		maxCIMDClients:             defaultMaxMemoryCIMDClients,
		now:                        time.Now,
		stopCleanup:                make(chan struct{}),
		cleanupDone:                make(chan struct{}),
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

	var expiredConsentSessions []string
	var expiredClientApprovals [][2]string
	for k, v := range s.consentSessions {
		if !now.Before(v.expiresAt) {
			expiredConsentSessions = append(expiredConsentSessions, k)
		}
	}
	for k, v := range s.clientApprovals {
		if !now.Before(v.expiresAt) {
			expiredClientApprovals = append(expiredClientApprovals, k)
		}
	}

	var expiredDeviceRequests []string
	for k, v := range s.deviceRequests {
		if now.After(v.expiresAt) {
			expiredDeviceRequests = append(expiredDeviceRequests, k)
		}
	}

	var expiredPendingDeviceLogins []string
	for k, v := range s.pendingDeviceLogins {
		if now.After(v.expiresAt) {
			expiredPendingDeviceLogins = append(expiredPendingDeviceLogins, k)
		}
	}

	var expiredPendingDeviceConfirmations []string
	for k, v := range s.pendingDeviceConfirmations {
		if now.After(v.expiresAt) {
			expiredPendingDeviceConfirmations = append(expiredPendingDeviceConfirmations, k)
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

	storeNow := s.now()
	var expiredExchangeGrants []string
	for k, g := range s.exchangeGrants {
		if storeNow.After(g.ExpiresAt) {
			expiredExchangeGrants = append(expiredExchangeGrants, k)
		}
	}

	var expiredExchangeGrantJTIs []assertionJWTKey
	for k, m := range s.exchangeGrantJTIs {
		if storeNow.After(m.expiresAt) {
			expiredExchangeGrantJTIs = append(expiredExchangeGrantJTIs, k)
		}
	}

	clientNow := s.now()
	var expiredClients []string
	for _, expiry := range []map[string]time.Time{s.dcrClientExpiry, s.cimdClientExpiry} {
		for k, v := range expiry {
			if !clientNow.Before(v) {
				expiredClients = append(expiredClients, k)
			}
		}
	}

	s.mu.RUnlock()

	// Phase 2: Early return if nothing to delete (no write lock needed)
	if len(expiredClients) == 0 &&
		len(expiredAuthCodes) == 0 &&
		len(expiredInvalidatedCodes) == 0 &&
		len(expiredAccessTokens) == 0 &&
		len(expiredRefreshTokens) == 0 &&
		len(expiredPKCERequests) == 0 &&
		len(expiredUpstreamTokens) == 0 &&
		len(expiredPendingAuthorizations) == 0 &&
		len(expiredConsentSessions) == 0 &&
		len(expiredClientApprovals) == 0 &&
		len(expiredDeviceRequests) == 0 &&
		len(expiredPendingDeviceLogins) == 0 &&
		len(expiredPendingDeviceConfirmations) == 0 &&
		len(expiredJWTs) == 0 &&
		len(expiredAssertionJWTs) == 0 &&
		len(expiredExchangeGrants) == 0 &&
		len(expiredExchangeGrantJTIs) == 0 {
		return
	}

	// Phase 3: Delete collected keys under write lock
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, k := range expiredClients {
		// RenewClientTTL may have extended the client between the phases.
		if exp, ok := s.clientExpiryLocked(k); ok && !clientNow.Before(exp) {
			delete(s.clients, k)
			delete(s.dcrClientExpiry, k)
			delete(s.cimdClientExpiry, k)
			delete(s.configured, k)
		}
	}

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

	for _, k := range expiredConsentSessions {
		if v, ok := s.consentSessions[k]; ok && !now.Before(v.expiresAt) {
			delete(s.consentSessions, k)
		}
	}
	for _, k := range expiredClientApprovals {
		if v, ok := s.clientApprovals[k]; ok && !now.Before(v.expiresAt) {
			delete(s.clientApprovals, k)
		}
	}

	for _, k := range expiredDeviceRequests {
		if entry, ok := s.deviceRequests[k]; ok {
			delete(s.deviceRequestsByUserCode, entry.value.UserCode)
		}
		delete(s.deviceRequests, k)
	}

	for _, k := range expiredPendingDeviceLogins {
		delete(s.pendingDeviceLogins, k)
	}

	for _, k := range expiredPendingDeviceConfirmations {
		delete(s.pendingDeviceConfirmations, k)
	}

	for _, k := range expiredJWTs {
		delete(s.clientAssertionJWTs, k)
	}

	for _, k := range expiredAssertionJWTs {
		if exp, ok := s.assertionJWTs[k]; ok && now.After(exp) {
			delete(s.assertionJWTs, k)
		}
	}

	for _, k := range expiredExchangeGrants {
		if g, ok := s.exchangeGrants[k]; ok && storeNow.After(g.ExpiresAt) {
			delete(s.exchangeGrants, k)
		}
	}

	for _, k := range expiredExchangeGrantJTIs {
		if m, ok := s.exchangeGrantJTIs[k]; ok && storeNow.After(m.expiresAt) {
			delete(s.exchangeGrantJTIs, k)
		}
	}
}

// snapshotRequester copies request-owned data while retaining the concrete client reference.
// GetID may initialize an empty ID on the caller's Request during creation.
func snapshotRequester(request fosite.Requester) *fosite.Request {
	form := request.GetRequestForm()
	var formCopy url.Values
	if form != nil {
		formCopy = make(url.Values, len(form))
		for key, values := range form {
			formCopy[key] = slices.Clone(values)
		}
	}

	snapshot := &fosite.Request{
		ID:                request.GetID(),
		RequestedAt:       request.GetRequestedAt(),
		Client:            request.GetClient(),
		RequestedScope:    slices.Clone(request.GetRequestedScopes()),
		GrantedScope:      slices.Clone(request.GetGrantedScopes()),
		RequestedAudience: slices.Clone(request.GetRequestedAudience()),
		GrantedAudience:   slices.Clone(request.GetGrantedAudience()),
		Form:              formCopy,
	}
	if session := request.GetSession(); session != nil {
		snapshot.Session = session.Clone()
	}
	if langContext, ok := request.(fosite.G11NContext); ok {
		snapshot.Lang = langContext.GetLang()
	}
	// Requester implementations other than fosite.Request may return an empty ID.
	// Initialize the copy before publishing it so Get never writes under RLock.
	snapshot.GetID()
	return snapshot
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
	if registration.IsConfigured(client) {
		return fmt.Errorf("client %q must not carry the configured stamp; use ReconcileConfiguredClient", id)
	}
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if _, exists := s.liveClientLocked(id, now); exists {
		return fmt.Errorf("%w: client %q", ErrAlreadyExists, id)
	}
	if !registration.DCRIssued(client) {
		s.putPermanentClientLocked(id, client)
		return nil
	}
	if s.maxDCRClients > 0 && countsTowardDCRCap(client) {
		if count, oldestExpiry := countLiveLocked(s.dcrClientExpiry, now); count >= s.maxDCRClients {
			return newClientCapacityError(s.maxDCRClients, oldestExpiry, now)
		}
	}
	s.putExpiringClientLocked(id, client, now.Add(DefaultUnusedDCRClientTTL))
	return nil
}

// UpsertDCRIssuedClient creates or replaces a DCR-issued client at
// client.GetID(). Unlike RegisterClient, an existing row is replaced (and its
// expiry reset to DefaultDCRClientTTL) rather than rejected -- but only when
// the existing row is itself DCR-issued; a configured/SPIFFE-reconciled row at
// the same ID is protected and refuses with ErrAlreadyExists. See the
// ClientRegistry interface doc for the full contract. A client counted
// against the DCR cap is refused, so this path cannot bypass the cap. A new
// CIMD row beyond maxCIMDClients is refused with a *ClientCapacityError;
// replacing an existing one always succeeds.
func (s *MemoryStorage) UpsertDCRIssuedClient(_ context.Context, client fosite.Client) error {
	if !registration.DCRIssued(client) {
		return fmt.Errorf("client %q must carry the DCR-issued marker to use UpsertDCRIssuedClient", client.GetID())
	}
	id := client.GetID()
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}
	if countsTowardDCRCap(client) {
		return fmt.Errorf("client %q counts against the DCR client cap and must use RegisterClient", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	existing, exists := s.liveClientLocked(id, now)
	if exists && !registration.DCRIssued(existing) {
		return fmt.Errorf("%w: client %q", ErrAlreadyExists, id)
	}
	if !exists && s.maxCIMDClients > 0 {
		if count, oldestExpiry := countLiveLocked(s.cimdClientExpiry, now); count >= s.maxCIMDClients {
			return newClientCapacityError(s.maxCIMDClients, oldestExpiry, now)
		}
	}
	s.putExpiringClientLocked(id, client, now.Add(DefaultDCRClientTTL))
	return nil
}

// ReconcileConfiguredClient applies an operator-declared client: creates it
// if absent, otherwise replaces the existing record unless
// configuredClientConflict refuses (or the record is DCR-issued), in which
// case it returns ErrAlreadyExists. See the ClientRegistry interface doc for
// the full contract.
func (s *MemoryStorage) ReconcileConfiguredClient(_ context.Context, client fosite.Client) error {
	if registration.DCRIssued(client) {
		return fmt.Errorf("configured client %q must not carry the DCR-issued marker", client.GetID())
	}
	if err := checkConfiguredClientType(client); err != nil {
		return err
	}
	id := client.GetID()
	if err := ValidateRegisterableClientID(id); err != nil {
		return err
	}

	existingFP, incomingFP, replaced, err := s.reconcileConfiguredClientLocked(id, client)
	if err != nil {
		return err
	}
	if replaced {
		warnConfiguredClientReplaced(id, existingFP, incomingFP)
	}
	return nil
}

// reconcileConfiguredClientLocked stores client under s.mu and reports the
// fingerprints of the row it replaced, or replaced=false when it created a
// new row. The caller logs the replacement after the lock is released.
func (s *MemoryStorage) reconcileConfiguredClientLocked(
	id string, client fosite.Client,
) (existingFP, incomingFP clientFingerprint, replaced bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	existing, exists := s.liveClientLocked(id, now)
	if !exists {
		s.putPermanentClientLocked(id, client)
		s.configured[id] = struct{}{}
		return existingFP, incomingFP, false, nil
	}
	if registration.DCRIssued(existing) {
		return existingFP, incomingFP, false, fmt.Errorf(
			"%w: client %q is DCR-issued, refusing to overwrite with a configured client",
			ErrAlreadyExists, id)
	}
	existingFP, incomingFP = fingerprintOfClient(existing), fingerprintOfClient(client)
	if err := configuredClientConflict(id, existingFP, incomingFP); err != nil {
		return existingFP, incomingFP, false, err
	}

	// Replace the stored value so secret rotation and policy changes take
	// effect.
	s.putPermanentClientLocked(id, client)
	s.configured[id] = struct{}{}
	return existingFP, incomingFP, true, nil
}

// liveClientLocked returns the client stored at id, or false when there is
// none or it has expired. Callers must hold s.mu.
func (s *MemoryStorage) liveClientLocked(id string, now time.Time) (fosite.Client, bool) {
	client, ok := s.clients[id]
	if !ok {
		return nil, false
	}
	if exp, ok := s.clientExpiryLocked(id); ok && !now.Before(exp) {
		return nil, false
	}
	return client, true
}

// clientExpiryLocked returns the expiry of the client stored at id, or false
// when it never expires. Callers must hold s.mu.
func (s *MemoryStorage) clientExpiryLocked(id string) (time.Time, bool) {
	if exp, ok := s.dcrClientExpiry[id]; ok {
		return exp, true
	}
	exp, ok := s.cimdClientExpiry[id]
	return exp, ok
}

// expiryMapFor returns the expiry map a DCR-issued client belongs in.
func (s *MemoryStorage) expiryMapFor(client fosite.Client) map[string]time.Time {
	if countsTowardDCRCap(client) {
		return s.dcrClientExpiry
	}
	return s.cimdClientExpiry
}

// putExpiringClientLocked stores a DCR-issued client at id, expiring at
// expiry. Callers must hold s.mu for writing.
func (s *MemoryStorage) putExpiringClientLocked(id string, client fosite.Client, expiry time.Time) {
	s.clients[id] = client
	delete(s.dcrClientExpiry, id)
	delete(s.cimdClientExpiry, id)
	delete(s.configured, id)
	s.expiryMapFor(client)[id] = expiry
}

// putPermanentClientLocked stores a client without the DCR-issued marker at
// id; it never expires. It clears the configured stamp; the caller sets it
// afterwards when the write is a reconcile. Callers must hold s.mu for writing.
func (s *MemoryStorage) putPermanentClientLocked(id string, client fosite.Client) {
	s.clients[id] = client
	delete(s.dcrClientExpiry, id)
	delete(s.cimdClientExpiry, id)
	delete(s.configured, id)
}

// countLiveLocked returns the number of entries in expiry that have not
// expired by now and the earliest expiry among them. Callers must hold s.mu.
// It walks one expiry map, which its cap bounds (plus entries expired since
// the last cleanup), on every write that map's cap checks and on a metric
// collection.
func countLiveLocked(expiry map[string]time.Time, now time.Time) (int, time.Time) {
	var count int
	var oldest time.Time
	for _, exp := range expiry {
		if !now.Before(exp) {
			continue
		}
		count++
		if oldest.IsZero() || exp.Before(oldest) {
			oldest = exp
		}
	}
	return count, oldest
}

// CountDCRClients implements DCRClientCounter.
func (s *MemoryStorage) CountDCRClients(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count, _ := countLiveLocked(s.dcrClientExpiry, s.now())
	return int64(count), nil
}

// RenewClientTTL sets a live DCR-issued client's expiry to DefaultDCRClientTTL
// from now on proven use (a successful token exchange/refresh), the same
// signal RedisStorage.RenewClientTTL uses. A pre-provisioned client never
// expires, so it needs no renewal. A client that is absent or expired stays
// absent, matching RedisStorage's PEXPIRE no-op on a missing key.
func (s *MemoryStorage) RenewClientTTL(_ context.Context, client fosite.Client) error {
	if client == nil || !registration.DCRIssued(client) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := client.GetID()
	now := s.now()
	stored, exists := s.liveClientLocked(id, now)
	if !exists || !registration.DCRIssued(stored) {
		return nil
	}
	s.expiryMapFor(stored)[id] = now.Add(DefaultDCRClientTTL)
	return nil
}

// -----------------------
// fosite.ClientManager
// -----------------------

// GetClient loads the client by its ID or returns an error if the client does not exist.
func (s *MemoryStorage) GetClient(_ context.Context, id string) (fosite.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	client, ok := s.liveClientLocked(id, s.now())
	if !ok {
		slog.Debug("client not found", "client_id", id)
		return nil, notFoundRFC6749Error("Client not found")
	}
	if dcrErr := registration.ValidatePrivateKeyJWTClientKeys(client); dcrErr != nil {
		slog.Debug("rejecting client whose signing keys no longer meet server policy",
			"client_id", id, "reason", dcrErr.ErrorDescription)
		return nil, fosite.ErrInvalidClient.WithHint("client signing keys no longer meet server policy")
	}
	if _, stamped := s.configured[id]; stamped && !isReservedPlaceholder(client) {
		marked, ok := registration.MarkConfigured(client)
		if !ok {
			return nil, notFoundRFC6749Error("Client not found")
		}
		return marked, nil
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

// exchangeGrantAssertionPurpose scopes exchange-grant replay markers, mirroring
// the purpose argument of ConsumeAssertionJWT. The literal pins the wire string
// the Redis backend must match.
const exchangeGrantAssertionPurpose = "exchange-grant"

// cloneExchangeGrant returns a deep copy of g, so neither the caller nor the
// store can change the other's slices.
func cloneExchangeGrant(g *ExchangeGrant) *ExchangeGrant {
	c := *g
	c.IssuableTokenTypes = slices.Clone(g.IssuableTokenTypes)
	return &c
}

// -----------------------
// ExchangeGrantStorage
// -----------------------

// CreateExchangeGrant stores a copy of g and, when g.SourceJTI is set, its
// replay marker. A grant whose shape is invalid, or whose ExpiresAt is already
// past, is refused. All checks run first and the writes follow under one lock,
// so a failed call leaves nothing behind.
//
// Check order: a duplicate signature is reported before a reused jti. A retry
// of a create that already succeeded (same signature, same jti) therefore gets
// ErrAlreadyExists, which says this exact grant exists, instead of ErrJTIKnown.
func (s *MemoryStorage) CreateExchangeGrant(_ context.Context, g *ExchangeGrant) error {
	if err := validateExchangeGrantShape(g); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if now.After(g.ExpiresAt) {
		return errors.New("exchange grant is already expired")
	}
	if existing, ok := s.exchangeGrants[g.Signature]; ok && !now.After(existing.ExpiresAt) {
		return ErrAlreadyExists
	}

	var key assertionJWTKey
	if g.SourceJTI != "" {
		key = assertionJWTKey{purpose: exchangeGrantAssertionPurpose, issuer: g.SourceIssuer, jti: g.SourceJTI}
		if m, ok := s.exchangeGrantJTIs[key]; ok && !now.After(m.expiresAt) {
			return fosite.ErrJTIKnown
		}
	}

	s.exchangeGrants[g.Signature] = cloneExchangeGrant(g)
	if g.SourceJTI != "" {
		s.exchangeGrantJTIs[key] = exchangeGrantMarker{
			signature: g.Signature, expiresAt: g.SourceExpiresAt.Add(ExchangeGrantMarkerMargin),
		}
	}
	return nil
}

// GetExchangeGrant returns a copy of the grant, including revoked grants. A
// record past its ExpiresAt is reported as ErrNotFound even if cleanup has not
// removed it yet.
func (s *MemoryStorage) GetExchangeGrant(_ context.Context, signature string) (*ExchangeGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.exchangeGrants[signature]
	if !ok || s.now().After(g.ExpiresAt) {
		return nil, ErrNotFound
	}
	return cloneExchangeGrant(g), nil
}

// RevokeExchangeGrant sets RevokedAt to now unless the grant is already
// revoked, in which case the first RevokedAt is kept. A missing or expired
// record returns ErrNotFound.
func (s *MemoryStorage) RevokeExchangeGrant(_ context.Context, signature string, now time.Time) error {
	if err := validateExchangeGrantRevokeTime(now); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// now is only the timestamp written; expiry follows the store clock.
	g, ok := s.exchangeGrants[signature]
	if !ok || s.now().After(g.ExpiresAt) {
		return ErrNotFound
	}
	if g.RevokedAt.IsZero() {
		g.RevokedAt = now
	}
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
	stored := snapshotRequester(request)
	expiresAt := getExpirationFromRequester(stored, fosite.AuthorizeCode, DefaultAuthCodeTTL)

	s.authCodes[code] = &timedEntry[fosite.Requester]{
		value:     stored,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetAuthorizeCodeSession retrieves the authorization request for a given code.
// If the authorization code has been invalidated, it returns ErrInvalidatedAuthorizeCode
// along with the request (as required by fosite). Expired codes are rejected
// with a not-found error, after the invalidation check so reuse is still detected.
func (s *MemoryStorage) GetAuthorizeCodeSession(_ context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.authCodes[code]
	if !ok {
		slog.Debug("authorization code not found")
		return nil, notFoundRFC6749Error("Authorization code not found")
	}

	// Check if the code has been invalidated
	if s.invalidatedCodes[code] != nil {
		// Must return the request along with the error as per fosite documentation
		return snapshotRequester(entry.value), fosite.ErrInvalidatedAuthorizeCode
	}

	if time.Now().After(entry.expiresAt) {
		slog.Debug("authorization code expired")
		return nil, notFoundRFC6749Error("Authorization code expired")
	}

	return snapshotRequester(entry.value), nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
// Subsequent calls to GetAuthorizeCodeSession will return ErrInvalidatedAuthorizeCode.
func (s *MemoryStorage) InvalidateAuthorizeCodeSession(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.authCodes[code]; !ok {
		slog.Debug("authorization code not found for invalidation")
		return notFoundRFC6749Error("Authorization code not found")
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
	stored := snapshotRequester(request)
	expiresAt := getExpirationFromRequester(stored, fosite.AccessToken, DefaultAccessTokenTTL)

	s.accessTokens[signature] = &timedEntry[fosite.Requester]{
		value:     stored,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetAccessTokenSession retrieves the access token session by its signature.
//
// The session parameter is a prototype for deserialization in persistent backends.
// Our in-memory implementation ignores it because it snapshots Requesters in memory.
// A persistent backend may instead use it to choose a deserialization type.
func (s *MemoryStorage) GetAccessTokenSession(_ context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.accessTokens[signature]
	if !ok {
		slog.Debug("access token not found")
		return nil, notFoundRFC6749Error("Access token not found")
	}
	return snapshotRequester(entry.value), nil
}

// DeleteAccessTokenSession removes the access token session.
func (s *MemoryStorage) DeleteAccessTokenSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accessTokens[signature]; !ok {
		return notFoundRFC6749Error("Access token not found")
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
	stored := snapshotRequester(request)
	expiresAt := getExpirationFromRequester(stored, fosite.RefreshToken, DefaultRefreshTokenTTL)

	s.refreshTokens[signature] = &timedEntry[fosite.Requester]{
		value:     stored,
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
		return nil, notFoundRFC6749Error("Refresh token not found")
	}
	return snapshotRequester(entry.value), nil
}

// DeleteRefreshTokenSession removes the refresh token session.
func (s *MemoryStorage) DeleteRefreshTokenSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.refreshTokens[signature]; !ok {
		return notFoundRFC6749Error("Refresh token not found")
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
	stored := snapshotRequester(request)
	expiresAt := getExpirationFromRequester(stored, fosite.AuthorizeCode, DefaultPKCETTL)

	s.pkceRequests[signature] = &timedEntry[fosite.Requester]{
		value:     stored,
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
		return nil, notFoundRFC6749Error("PKCE request not found")
	}
	return snapshotRequester(entry.value), nil
}

// DeletePKCERequestSession removes the PKCE request session.
func (s *MemoryStorage) DeletePKCERequestSession(_ context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pkceRequests[signature]; !ok {
		return notFoundRFC6749Error("PKCE request not found")
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

	s.storeUpstreamTokensLocked(upstreamKey{sessionID, providerName}, tokens)
	return nil
}

// CompareAndSwapUpstreamTokens stores tokens for (sessionID, providerName)
// only if the refresh token currently held there equals expectedRefreshToken.
// See the interface doc (UpstreamTokenStorage.CompareAndSwapUpstreamTokens)
// for the coordination contract. The compare-then-write happens under s.mu,
// so it is atomic with respect to every other reader/writer of this backend.
func (s *MemoryStorage) CompareAndSwapUpstreamTokens(
	_ context.Context, sessionID, providerName, expectedRefreshToken string, tokens *UpstreamTokens,
) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := upstreamKey{sessionID, providerName}
	var currentRefreshToken string
	if entry, ok := s.upstreamTokens[key]; ok && entry.value != nil {
		currentRefreshToken = entry.value.RefreshToken
	}
	if currentRefreshToken != expectedRefreshToken {
		return ErrConcurrentRefresh
	}

	s.storeUpstreamTokensLocked(key, tokens)
	return nil
}

// storeUpstreamTokensLocked writes tokens for key, replacing any existing
// entry. Callers must hold s.mu for writing.
func (s *MemoryStorage) storeUpstreamTokensLocked(key upstreamKey, tokens *UpstreamTokens) {
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

	s.upstreamTokens[key] = &timedEntry[*UpstreamTokens]{
		value:     cloneUpstreamTokens(tokens),
		createdAt: now,
		expiresAt: expiresAt,
	}
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
		return nil, notFoundRFC6749Error("Upstream tokens not found")
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
		return notFoundRFC6749Error("Upstream tokens not found")
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
		return nil, notFoundRFC6749Error("Upstream tokens not found")
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
	expiresAt := pending.CreatedAt.Add(DefaultPendingAuthorizationTTL)
	if !expiresAt.After(now) {
		return ErrExpired
	}

	// Make a defensive copy to prevent aliasing issues
	pendingCopy := &PendingAuthorization{
		ClientID:                pending.ClientID,
		RedirectURI:             pending.RedirectURI,
		State:                   pending.State,
		PKCEChallenge:           pending.PKCEChallenge,
		PKCEMethod:              pending.PKCEMethod,
		Scopes:                  slices.Clone(pending.Scopes),
		Resource:                pending.Resource,
		ConsentStage:            pending.ConsentStage,
		RememberConsent:         pending.RememberConsent,
		FirstProviderSubject:    pending.FirstProviderSubject,
		ExpectedUserID:          pending.ExpectedUserID,
		ExpectedProviderSubject: pending.ExpectedProviderSubject,
		ConsentSessionDigest:    pending.ConsentSessionDigest,
		InternalState:           pending.InternalState,
		UpstreamPKCEVerifier:    pending.UpstreamPKCEVerifier,
		UpstreamNonce:           pending.UpstreamNonce,
		BrowserBindingHash:      pending.BrowserBindingHash,
		UpstreamProviderName:    pending.UpstreamProviderName,
		SessionID:               pending.SessionID,
		ResolvedUserID:          pending.ResolvedUserID,
		ResolvedUserName:        pending.ResolvedUserName,
		ResolvedUserEmail:       pending.ResolvedUserEmail,
		VerifiedUpstream:        pending.VerifiedUpstream,
		SingleLeg:               pending.SingleLeg,
		ChainUpstreams:          slices.Clone(pending.ChainUpstreams),
		CreatedAt:               pending.CreatedAt,
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
		return nil, notFoundRFC6749Error("Pending authorization not found")
	}

	// Check if expired
	if !entry.expiresAt.After(time.Now()) {
		slog.Debug("pending authorization expired")
		return nil, ErrExpired
	}

	// Return a defensive copy to prevent aliasing issues
	pending := entry.value
	if pending == nil {
		return nil, nil
	}
	return &PendingAuthorization{
		ClientID:                pending.ClientID,
		RedirectURI:             pending.RedirectURI,
		State:                   pending.State,
		PKCEChallenge:           pending.PKCEChallenge,
		PKCEMethod:              pending.PKCEMethod,
		Scopes:                  slices.Clone(pending.Scopes),
		Resource:                pending.Resource,
		ConsentStage:            pending.ConsentStage,
		RememberConsent:         pending.RememberConsent,
		FirstProviderSubject:    pending.FirstProviderSubject,
		ExpectedUserID:          pending.ExpectedUserID,
		ExpectedProviderSubject: pending.ExpectedProviderSubject,
		ConsentSessionDigest:    pending.ConsentSessionDigest,
		InternalState:           pending.InternalState,
		UpstreamPKCEVerifier:    pending.UpstreamPKCEVerifier,
		UpstreamNonce:           pending.UpstreamNonce,
		BrowserBindingHash:      pending.BrowserBindingHash,
		UpstreamProviderName:    pending.UpstreamProviderName,
		SessionID:               pending.SessionID,
		ResolvedUserID:          pending.ResolvedUserID,
		ResolvedUserName:        pending.ResolvedUserName,
		ResolvedUserEmail:       pending.ResolvedUserEmail,
		VerifiedUpstream:        pending.VerifiedUpstream,
		SingleLeg:               pending.SingleLeg,
		ChainUpstreams:          slices.Clone(pending.ChainUpstreams),
		CreatedAt:               pending.CreatedAt,
	}, nil
}

// DeletePendingAuthorization removes a pending authorization.
func (s *MemoryStorage) DeletePendingAuthorization(_ context.Context, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pendingAuthorizations[state]; !ok {
		return notFoundRFC6749Error("Pending authorization not found")
	}
	delete(s.pendingAuthorizations, state)
	return nil
}

// -----------------------
// Pending Device Login Storage
// -----------------------

// StorePendingDeviceLogin stores a pending device-flow verification-page
// login, keyed by the internal state used to correlate the upstream IDP
// callback.
func (s *MemoryStorage) StorePendingDeviceLogin(_ context.Context, state string, pending *PendingDeviceLogin) error {
	if state == "" {
		return fosite.ErrInvalidRequest.WithHint("state cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending device login cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pendingCopy := &PendingDeviceLogin{
		DeviceCode:           pending.DeviceCode,
		UserCode:             pending.UserCode,
		UpstreamPKCEVerifier: pending.UpstreamPKCEVerifier,
		UpstreamNonce:        pending.UpstreamNonce,
		BrowserBindingHash:   pending.BrowserBindingHash,
		UpstreamProviderName: pending.UpstreamProviderName,
		CreatedAt:            pending.CreatedAt,
	}

	s.pendingDeviceLogins[state] = &timedEntry[*PendingDeviceLogin]{
		value:     pendingCopy,
		createdAt: now,
		expiresAt: now.Add(DefaultDeviceLoginTTL),
	}
	return nil
}

// LoadPendingDeviceLogin retrieves a pending device login by state. Returns a
// defensive copy to prevent aliasing issues.
func (s *MemoryStorage) LoadPendingDeviceLogin(_ context.Context, state string) (*PendingDeviceLogin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.pendingDeviceLogins[state]
	if !ok {
		return nil, notFoundRFC6749Error("Pending device login not found")
	}
	if time.Now().After(entry.expiresAt) {
		return nil, ErrExpired
	}

	pending := entry.value
	return &PendingDeviceLogin{
		DeviceCode:           pending.DeviceCode,
		UserCode:             pending.UserCode,
		UpstreamPKCEVerifier: pending.UpstreamPKCEVerifier,
		UpstreamNonce:        pending.UpstreamNonce,
		BrowserBindingHash:   pending.BrowserBindingHash,
		UpstreamProviderName: pending.UpstreamProviderName,
		CreatedAt:            pending.CreatedAt,
	}, nil
}

// DeletePendingDeviceLogin removes a pending device login.
func (s *MemoryStorage) DeletePendingDeviceLogin(_ context.Context, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pendingDeviceLogins[state]; !ok {
		return notFoundRFC6749Error("Pending device login not found")
	}
	delete(s.pendingDeviceLogins, state)
	return nil
}

// -----------------------
// Pending Device Confirmation Storage
// -----------------------

// StorePendingDeviceConfirmation stores a pending device-flow confirmation,
// keyed by an opaque token minted by the caller. See
// PendingDeviceConfirmation's doc comment for why the resolved identity is
// addressed by token rather than round-tripped through the browser.
func (s *MemoryStorage) StorePendingDeviceConfirmation(
	_ context.Context, token string, pending *PendingDeviceConfirmation,
) error {
	if token == "" {
		return fosite.ErrInvalidRequest.WithHint("token cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending device confirmation cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pendingCopy := &PendingDeviceConfirmation{
		DeviceCode:        pending.DeviceCode,
		UserCode:          pending.UserCode,
		ResolvedUserID:    pending.ResolvedUserID,
		ResolvedUserName:  pending.ResolvedUserName,
		ResolvedUserEmail: pending.ResolvedUserEmail,
		VerifiedUpstream:  pending.VerifiedUpstream,
		UpstreamTokens:    cloneUpstreamTokens(pending.UpstreamTokens),
		Synthetic:         pending.Synthetic,
		CreatedAt:         pending.CreatedAt,
	}

	s.pendingDeviceConfirmations[token] = &timedEntry[*PendingDeviceConfirmation]{
		value:     pendingCopy,
		createdAt: now,
		expiresAt: now.Add(DefaultDeviceLoginTTL),
	}
	return nil
}

// LoadPendingDeviceConfirmation retrieves a pending device confirmation by
// token. Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) LoadPendingDeviceConfirmation(
	_ context.Context, token string,
) (*PendingDeviceConfirmation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.pendingDeviceConfirmations[token]
	if !ok {
		return nil, notFoundRFC6749Error("Pending device confirmation not found")
	}
	if time.Now().After(entry.expiresAt) {
		return nil, ErrExpired
	}

	pending := entry.value
	return &PendingDeviceConfirmation{
		DeviceCode:        pending.DeviceCode,
		UserCode:          pending.UserCode,
		ResolvedUserID:    pending.ResolvedUserID,
		ResolvedUserName:  pending.ResolvedUserName,
		ResolvedUserEmail: pending.ResolvedUserEmail,
		VerifiedUpstream:  pending.VerifiedUpstream,
		UpstreamTokens:    cloneUpstreamTokens(pending.UpstreamTokens),
		Synthetic:         pending.Synthetic,
		CreatedAt:         pending.CreatedAt,
	}, nil
}

// DeletePendingDeviceConfirmation removes a pending device confirmation.
func (s *MemoryStorage) DeletePendingDeviceConfirmation(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pendingDeviceConfirmations[token]; !ok {
		return notFoundRFC6749Error("Pending device confirmation not found")
	}
	delete(s.pendingDeviceConfirmations, token)
	return nil
}

// -----------------------
// Device Code Storage
// -----------------------

// cloneDeviceRequest returns a defensive copy of device, cloning its slice
// fields so neither the caller nor the store can mutate the other's data
// through a shared backing array.
func cloneDeviceRequest(device *DeviceRequest) *DeviceRequest {
	return &DeviceRequest{
		DeviceCode:        device.DeviceCode,
		UserCode:          device.UserCode,
		ClientID:          device.ClientID,
		Scopes:            slices.Clone(device.Scopes),
		Audience:          slices.Clone(device.Audience),
		Status:            device.Status,
		Interval:          device.Interval,
		LastPolledAt:      device.LastPolledAt,
		ResolvedUserID:    device.ResolvedUserID,
		ResolvedUserName:  device.ResolvedUserName,
		ResolvedUserEmail: device.ResolvedUserEmail,
		VerifiedUpstream:  device.VerifiedUpstream,
		SessionID:         device.SessionID,
		CreatedAt:         device.CreatedAt,
	}
}

// getUnexpiredDeviceRequestEntry looks up the device request entry keyed by
// deviceCode, returning ErrNotFound if absent or ErrExpired if its TTL has
// elapsed. Callers must hold s.mu (read or write lock).
func (s *MemoryStorage) getUnexpiredDeviceRequestEntry(deviceCode string) (*timedEntry[*DeviceRequest], error) {
	entry, ok := s.deviceRequests[deviceCode]
	if !ok {
		return nil, fmt.Errorf("%w: device request not found", ErrNotFound)
	}
	if time.Now().After(entry.expiresAt) {
		return nil, ErrExpired
	}
	return entry, nil
}

// StoreDeviceRequest stores a new pending device request, indexed by both
// DeviceCode and UserCode.
func (s *MemoryStorage) StoreDeviceRequest(_ context.Context, device *DeviceRequest) error {
	if device == nil {
		return fosite.ErrInvalidRequest.WithHint("device request cannot be nil")
	}
	if device.DeviceCode == "" {
		return fosite.ErrInvalidRequest.WithHint("device code cannot be empty")
	}
	if device.UserCode == "" {
		return fosite.ErrInvalidRequest.WithHint("user code cannot be empty")
	}
	if device.Status != DeviceRequestStatusPending {
		return fosite.ErrInvalidRequest.WithHint("device request must be created with pending status")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.deviceRequests[device.DeviceCode]; ok {
		return fmt.Errorf("%w: device code %q", ErrAlreadyExists, device.DeviceCode)
	}
	if _, ok := s.deviceRequestsByUserCode[device.UserCode]; ok {
		return fmt.Errorf("%w: user code %q", ErrAlreadyExists, device.UserCode)
	}

	now := time.Now()
	s.deviceRequests[device.DeviceCode] = &timedEntry[*DeviceRequest]{
		value:     cloneDeviceRequest(device),
		createdAt: now,
		expiresAt: now.Add(DefaultDeviceRequestTTL),
	}
	s.deviceRequestsByUserCode[device.UserCode] = device.DeviceCode
	return nil
}

// LoadDeviceRequestByDeviceCode retrieves a device request by its device_code.
// Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) LoadDeviceRequestByDeviceCode(_ context.Context, deviceCode string) (*DeviceRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, err := s.getUnexpiredDeviceRequestEntry(deviceCode)
	if err != nil {
		return nil, err
	}
	return cloneDeviceRequest(entry.value), nil
}

// LoadDeviceRequestByUserCode retrieves a device request by its user_code,
// for the verification page. Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) LoadDeviceRequestByUserCode(_ context.Context, userCode string) (*DeviceRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deviceCode, ok := s.deviceRequestsByUserCode[userCode]
	if !ok {
		return nil, fmt.Errorf("%w: device request not found", ErrNotFound)
	}
	entry, err := s.getUnexpiredDeviceRequestEntry(deviceCode)
	if err != nil {
		return nil, err
	}
	return cloneDeviceRequest(entry.value), nil
}

// MarkDeviceRequestAuthorized transitions a pending device request to
// authorized, attaching the resolved identity.
func (s *MemoryStorage) MarkDeviceRequestAuthorized(
	_ context.Context, deviceCode string, auth DeviceAuthorization,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.getUnexpiredDeviceRequestEntry(deviceCode)
	if err != nil {
		return err
	}
	if entry.value.Status != DeviceRequestStatusPending {
		return fmt.Errorf("%w: device request is %q, not pending", ErrInvalidState, entry.value.Status)
	}

	entry.value.Status = DeviceRequestStatusAuthorized
	entry.value.ResolvedUserID = auth.UserID
	entry.value.ResolvedUserName = auth.UserName
	entry.value.ResolvedUserEmail = auth.UserEmail
	entry.value.VerifiedUpstream = auth.VerifiedUpstream
	entry.value.SessionID = auth.SessionID
	return nil
}

// MarkDeviceRequestDenied transitions a pending device request to denied.
func (s *MemoryStorage) MarkDeviceRequestDenied(_ context.Context, deviceCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.getUnexpiredDeviceRequestEntry(deviceCode)
	if err != nil {
		return err
	}
	if entry.value.Status != DeviceRequestStatusPending {
		return fmt.Errorf("%w: device request is %q, not pending", ErrInvalidState, entry.value.Status)
	}

	entry.value.Status = DeviceRequestStatusDenied
	return nil
}

// UpdateDeviceRequestLastPolledAt records the time of the most recent poll.
func (s *MemoryStorage) UpdateDeviceRequestLastPolledAt(_ context.Context, deviceCode string, polledAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.getUnexpiredDeviceRequestEntry(deviceCode)
	if err != nil {
		return err
	}
	entry.value.LastPolledAt = polledAt
	return nil
}

// DeleteDeviceRequest removes a device request, e.g. once its token has been issued.
func (s *MemoryStorage) DeleteDeviceRequest(_ context.Context, deviceCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.deviceRequests[deviceCode]
	if !ok {
		return fmt.Errorf("%w: device request not found", ErrNotFound)
	}
	delete(s.deviceRequests, deviceCode)
	delete(s.deviceRequestsByUserCode, entry.value.UserCode)
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

// UpdateDCRCredentialsIfPresent replaces the entry at creds.Key with creds
// when one physically exists, and returns ErrNotFound (wrapped) otherwise. A
// defensive copy is stored so subsequent caller mutations do not affect
// persisted state, mirroring StoreDCRCredentialsIfAbsent.
//
// Presence is a plain map lookup, deliberately NOT the TTL-aware "absent"
// check StoreDCRCredentialsIfAbsent uses: an expired-but-present entry is
// still updatable here. See the DCRCredentialStore interface docs for why
// Update gates on physical presence rather than liveness. The in-memory
// backend has no native TTL, so ClientSecretExpiresAt is retained verbatim on
// the rewritten entry exactly as StoreDCRCredentialsIfAbsent retains it.
//
// Validation is delegated to validateDCRCredentialsForStore so the rejection
// set stays in sync with sibling backends.
func (s *MemoryStorage) UpdateDCRCredentialsIfPresent(_ context.Context, creds *DCRCredentials) (*DCRCredentials, error) {
	if err := validateDCRCredentialsForStore(creds); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.dcrCredentials[creds.Key]; !ok {
		return nil, notFoundRFC6749Error("DCR credentials not found")
	}

	s.dcrCredentials[creds.Key] = cloneDCRCredentials(creds)
	return cloneDCRCredentials(creds), nil
}

// UpdateDCRCredentialsIfUnchanged replaces the entry at creds.Key with creds
// only when the stored entry still equals expected, returning ErrNotFound
// (wrapped) when no entry exists and ErrDCRCredentialsChanged when it differs.
// The compare and the write happen under s.mu, so no other writer can slip in
// between them. Presence and ClientSecretExpiresAt handling are identical to
// UpdateDCRCredentialsIfPresent.
func (s *MemoryStorage) UpdateDCRCredentialsIfUnchanged(
	_ context.Context, creds, expected *DCRCredentials,
) (*DCRCredentials, error) {
	if err := validateDCRCompareAndSet(creds, expected); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.dcrCredentials[creds.Key]
	if !ok {
		return nil, notFoundRFC6749Error("DCR credentials not found")
	}
	if !dcrCredentialsEqual(existing, expected) {
		return nil, ErrDCRCredentialsChanged
	}

	s.dcrCredentials[creds.Key] = cloneDCRCredentials(creds)
	return cloneDCRCredentials(creds), nil
}

// dcrCredentialsEqual reports whether a and b hold the same persisted values.
// Time fields are compared with time.Time.Equal rather than ==, so two values
// for the same instant compare equal regardless of monotonic-clock reading or
// location.
//
// The final == covers every other field and only compiles while
// DCRCredentials stays comparable. A new time.Time field added to
// DCRCredentials MUST be compared with Equal and zeroed here alongside the
// existing two; == on it would reintroduce the monotonic-clock/location
// mismatch. TestDCRCredentialsEqual_TimeFieldsCompareByInstant fails if one
// is missed.
func dcrCredentialsEqual(a, b *DCRCredentials) bool {
	ac, bc := *a, *b
	if !ac.CreatedAt.Equal(bc.CreatedAt) || !ac.ClientSecretExpiresAt.Equal(bc.ClientSecretExpiresAt) {
		return false
	}
	ac.CreatedAt, bc.CreatedAt = time.Time{}, time.Time{}
	ac.ClientSecretExpiresAt, bc.ClientSecretExpiresAt = time.Time{}, time.Time{}
	return ac == bc
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
		return nil, notFoundRFC6749Error("DCR credentials not found")
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
	_ Storage                          = (*MemoryStorage)(nil)
	_ PendingAuthorizationStorage      = (*MemoryStorage)(nil)
	_ DeviceCodeStorage                = (*MemoryStorage)(nil)
	_ PendingDeviceLoginStorage        = (*MemoryStorage)(nil)
	_ PendingDeviceConfirmationStorage = (*MemoryStorage)(nil)
	_ ClientRegistry                   = (*MemoryStorage)(nil)
	_ UpstreamTokenStorage             = (*MemoryStorage)(nil)
	_ UserStorage                      = (*MemoryStorage)(nil)
	_ DCRCredentialStore               = (*MemoryStorage)(nil)
	_ AssertionJWTConsumer             = (*MemoryStorage)(nil)
	_ ExchangeGrantStorage             = (*MemoryStorage)(nil)
)
