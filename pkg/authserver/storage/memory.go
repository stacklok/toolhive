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
	"slices"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/logger"
)

// timedEntry wraps a value with its creation time for TTL tracking.
type timedEntry[T any] struct {
	value     T
	createdAt time.Time
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
	clients map[string]fosite.Client

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

	// upstreamTokens maps session ID -> tokens from upstream IDP (ToolHive extension).
	upstreamTokens map[string]*timedEntry[*UpstreamTokens]

	// pendingAuthorizations tracks authorization requests awaiting upstream IDP callback
	pendingAuthorizations map[string]*timedEntry[*PendingAuthorization]

	// invalidatedCodes tracks auth codes that have been used/invalidated.
	// Kept separate from authCodes to return the Requester with ErrInvalidatedAuthorizeCode.
	invalidatedCodes map[string]*timedEntry[bool]

	// clientAssertionJWTs tracks JTIs to prevent JWT replay attacks per RFC 7523.
	clientAssertionJWTs map[string]time.Time

	// users maps user ID -> User for user account lookup.
	// Users are not subject to TTL-based cleanup as they represent persistent accounts.
	users map[string]*User

	// providerIdentities maps "providerID:providerSubject" -> ProviderIdentity for identity lookup.
	// This enables O(1) lookup during authentication callbacks.
	providerIdentities map[string]*ProviderIdentity

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

// NewMemoryStorage creates a new MemoryStorage instance with initialized maps
// and starts the background cleanup goroutine.
func NewMemoryStorage(opts ...MemoryStorageOption) *MemoryStorage {
	s := &MemoryStorage{
		clients:               make(map[string]fosite.Client),
		authCodes:             make(map[string]*timedEntry[fosite.Requester]),
		accessTokens:          make(map[string]*timedEntry[fosite.Requester]),
		refreshTokens:         make(map[string]*timedEntry[fosite.Requester]),
		pkceRequests:          make(map[string]*timedEntry[fosite.Requester]),
		upstreamTokens:        make(map[string]*timedEntry[*UpstreamTokens]),
		pendingAuthorizations: make(map[string]*timedEntry[*PendingAuthorization]),
		invalidatedCodes:      make(map[string]*timedEntry[bool]),
		clientAssertionJWTs:   make(map[string]time.Time),
		users:                 make(map[string]*User),
		providerIdentities:    make(map[string]*ProviderIdentity),
		cleanupInterval:       DefaultCleanupInterval,
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

	var expiredUpstreamTokens []string
	for k, v := range s.upstreamTokens {
		if now.After(v.expiresAt) {
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

	s.mu.RUnlock()

	// Phase 2: Early return if nothing to delete (no write lock needed)
	if len(expiredAuthCodes) == 0 &&
		len(expiredInvalidatedCodes) == 0 &&
		len(expiredAccessTokens) == 0 &&
		len(expiredRefreshTokens) == 0 &&
		len(expiredPKCERequests) == 0 &&
		len(expiredUpstreamTokens) == 0 &&
		len(expiredPendingAuthorizations) == 0 &&
		len(expiredJWTs) == 0 {
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

// RegisterClient adds or updates a client in the storage.
// This is useful for setting up test clients.
func (s *MemoryStorage) RegisterClient(_ context.Context, client fosite.Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[client.GetID()] = client
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
		logger.Debugw("client not found", "client_id", id)
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Client not found"))
	}
	return client, nil
}

// ClientAssertionJWTValid returns an error if the JTI is known or the DB check failed,
// and nil if the JTI is not known (meaning it can be used).
func (s *MemoryStorage) ClientAssertionJWTValid(_ context.Context, jti string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if exp, ok := s.clientAssertionJWTs[jti]; ok {
		if time.Now().Before(exp) {
			return fosite.ErrJTIKnown
		}
	}
	return nil
}

// SetClientAssertionJWT marks a JTI as known for the given expiry time.
// Before inserting the new JTI, it will clean up any existing JTIs that have expired.
func (s *MemoryStorage) SetClientAssertionJWT(_ context.Context, jti string, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up expired JTIs
	now := time.Now()
	for k, v := range s.clientAssertionJWTs {
		if now.After(v) {
			delete(s.clientAssertionJWTs, k)
		}
	}

	s.clientAssertionJWTs[jti] = exp
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
		logger.Debugw("authorization code not found")
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
		logger.Debugw("authorization code not found for invalidation")
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
		logger.Debugw("access token not found")
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
		logger.Debugw("refresh token not found")
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
		logger.Debugw("PKCE request not found")
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

// StoreUpstreamTokens stores the upstream IDP tokens for a session.
// A defensive copy is made to prevent aliasing issues.
func (s *MemoryStorage) StoreUpstreamTokens(_ context.Context, sessionID string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var expiresAt time.Time
	if tokens != nil && !tokens.ExpiresAt.IsZero() {
		expiresAt = tokens.ExpiresAt
	} else {
		expiresAt = now.Add(DefaultAccessTokenTTL)
	}

	// Make a defensive copy to prevent aliasing issues
	var tokensCopy *UpstreamTokens
	if tokens != nil {
		tokensCopy = &UpstreamTokens{
			ProviderID:      tokens.ProviderID,
			AccessToken:     tokens.AccessToken,
			RefreshToken:    tokens.RefreshToken,
			IDToken:         tokens.IDToken,
			ExpiresAt:       tokens.ExpiresAt,
			UserID:          tokens.UserID,
			UpstreamSubject: tokens.UpstreamSubject,
			ClientID:        tokens.ClientID,
		}
	}

	s.upstreamTokens[sessionID] = &timedEntry[*UpstreamTokens]{
		value:     tokensCopy,
		createdAt: now,
		expiresAt: expiresAt,
	}
	return nil
}

// GetUpstreamTokens retrieves the upstream IDP tokens for a session.
// Returns a defensive copy to prevent aliasing issues.
func (s *MemoryStorage) GetUpstreamTokens(_ context.Context, sessionID string) (*UpstreamTokens, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.upstreamTokens[sessionID]
	if !ok {
		logger.Debugw("upstream tokens not found", "session_id", sessionID)
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		logger.Debugw("upstream tokens expired", "session_id", sessionID)
		return nil, ErrExpired
	}

	// Return a defensive copy to prevent aliasing issues
	tokens := entry.value
	if tokens == nil {
		return nil, nil
	}
	return &UpstreamTokens{
		ProviderID:      tokens.ProviderID,
		AccessToken:     tokens.AccessToken,
		RefreshToken:    tokens.RefreshToken,
		IDToken:         tokens.IDToken,
		ExpiresAt:       tokens.ExpiresAt,
		UserID:          tokens.UserID,
		UpstreamSubject: tokens.UpstreamSubject,
		ClientID:        tokens.ClientID,
	}, nil
}

// DeleteUpstreamTokens removes the upstream IDP tokens for a session.
func (s *MemoryStorage) DeleteUpstreamTokens(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.upstreamTokens[sessionID]; !ok {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}
	delete(s.upstreamTokens, sessionID)
	return nil
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
		logger.Debugw("pending authorization not found")
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		logger.Debugw("pending authorization expired")
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
	for sessionID, entry := range s.upstreamTokens {
		if entry.value != nil && entry.value.UserID == id {
			delete(s.upstreamTokens, sessionID)
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
	}
}

// Compile-time interface compliance checks
var (
	_ Storage                     = (*MemoryStorage)(nil)
	_ PendingAuthorizationStorage = (*MemoryStorage)(nil)
	_ ClientRegistry              = (*MemoryStorage)(nil)
	_ UpstreamTokenStorage        = (*MemoryStorage)(nil)
	_ UserStorage                 = (*MemoryStorage)(nil)
)
