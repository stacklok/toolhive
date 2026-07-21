// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"

	tcredis "github.com/stacklok/toolhive-core/redis"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
)

// nullMarker is used to store nil upstream tokens in Redis.
const nullMarker = "null"

// pastExpiryDCRTTL is the bounded TTL applied when a caller writes DCR
// credentials whose ClientSecretExpiresAt is already in the past. The row is
// still accepted (the resolver decides when to re-register) but it self-evicts
// almost immediately so the store does not hold an already-expired secret
// forever. One second is small enough that no caller can usefully read the
// row, large enough that real Redis applies the TTL reliably (sub-second TTLs
// are technically supported via PEXPIRE but the second-grain TTL command is
// the broadly-tested path), and short enough that operational metrics do not
// confuse this row with a healthy long-lived registration.
const pastExpiryDCRTTL = time.Second

// warnOnCleanupErr logs a warning when a best-effort cleanup operation fails.
//
// Secondary index cleanup in Redis (SRem from reverse-lookup sets, Del of orphaned
// keys) is intentionally best-effort: the primary operation has already succeeded,
// and failing the overall request due to an index cleanup error would be worse than
// leaving a stale entry. Stale index entries are bounded by TTL expiration of the
// underlying keys and are tolerated on read (readers skip missing entries).
//
// TODO: Add a periodic background cleanup job to scan and remove stale entries
// from secondary index sets (KeyTypeReqIDAccess, KeyTypeReqIDRefresh,
// KeyTypeUserUpstream, KeyTypeUserProviders) to prevent unbounded growth.
func warnOnCleanupErr(err error, operation, key string) {
	if err != nil {
		slog.Warn("best-effort index cleanup failed",
			"operation", operation, "key", key, "error", err)
	}
}

// filterIndexMembersByPrefix splits SMEMBERS results into members that share
// this storage instance's key prefix and those that do not. Multi-key
// operations like MGet and Del fail with CROSSSLOT under Redis Cluster when
// any member hashes to a different slot, and a stray un-prefixed entry
// (legacy data, an external admin op, a test fixture) is the most likely
// source of such drift. Filtering at read time turns a hard cluster failure
// into a logged warning, while remaining a no-op on standalone Redis.
func filterIndexMembersByPrefix(prefix string, members []string) (kept, dropped []string) {
	for _, m := range members {
		if strings.HasPrefix(m, prefix) {
			kept = append(kept, m)
		} else {
			dropped = append(dropped, m)
		}
	}
	return kept, dropped
}

// warnDroppedIndexMembers emits a warning for each foreign member returned by
// filterIndexMembersByPrefix so operators can identify the source of cluster-
// incompatible entries.
func warnDroppedIndexMembers(operation, indexKey, expectedPrefix string, dropped []string) {
	for _, m := range dropped {
		slog.Warn("dropping foreign index member to prevent CROSSSLOT",
			"operation", operation, "indexKey", indexKey,
			"expectedPrefix", expectedPrefix, "member", m)
	}
}

// RedisStorage implements the Storage interface backed by Redis.
// Supports standalone mode (single endpoint), Sentinel failover mode, and
// Cluster mode. It provides distributed storage for OAuth2 tokens, authorization
// codes, user data, and pending authorizations, enabling horizontal scaling.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	// tokenEnc, when non-nil, enables AES-256-GCM envelope encryption for
	// upstream OAuth token values at rest. When nil, writes are plaintext
	// (current behavior), reads of plaintext pass through, and reads of
	// envelope values fail loudly rather than returning corrupt tokens.
	tokenEnc tokenenc.Keyring
}

// RedisStorageOption configures optional RedisStorage behavior at construction.
type RedisStorageOption func(*RedisStorage)

// WithTokenEncryption enables envelope encryption of upstream OAuth token
// values at rest using kr as the key-encryption-key source. The keyring is
// validated by tokenenc.NewStaticKeyring before it reaches this option; a nil
// keyring leaves encryption disabled.
func WithTokenEncryption(kr tokenenc.Keyring) RedisStorageOption {
	return func(s *RedisStorage) {
		s.tokenEnc = kr
	}
}

// storedSession is a serializable wrapper for fosite.Requester.
// This allows storing fosite sessions in Redis as JSON.
type storedSession struct {
	ClientID          string              `json:"client_id"`
	RequestedAt       time.Time           `json:"requested_at"`
	RequestedScopes   []string            `json:"requested_scopes"`
	GrantedScopes     []string            `json:"granted_scopes"`
	RequestedAudience []string            `json:"requested_audience"`
	GrantedAudience   []string            `json:"granted_audience"`
	Form              map[string][]string `json:"form"`
	RequestID         string              `json:"request_id"`
	Session           json.RawMessage     `json:"session"`
}

// NewRedisStorage creates Redis-backed storage. Connection-mode topology,
// timeouts, TLS, and credentials are configured through cfg; keyPrefix is the
// per-tenant key prefix (e.g. "thv:auth:{ns}:{name}:") and must be non-empty.
//
// Connection-mode validation, timeout defaults, client construction (standalone,
// cluster, or sentinel), TLS plumbing, and connectivity verification are
// delegated to the shared toolhive-core redis package. cfg.Password may be
// empty when the Redis server does not require authentication (the auth server
// does not mandate ACL auth); the keyPrefix is storage-specific and required.
func NewRedisStorage(
	ctx context.Context, cfg tcredis.Config, keyPrefix string, opts ...RedisStorageOption,
) (*RedisStorage, error) {
	if keyPrefix == "" {
		return nil, errors.New("invalid redis configuration: key prefix is required")
	}

	client, err := tcredis.NewClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}

	s := &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// NewRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// This is useful for testing with miniredis.
func NewRedisStorageWithClient(
	client redis.UniversalClient, keyPrefix string, opts ...RedisStorageOption,
) *RedisStorage {
	s := &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// defaultSessionFactory creates session prototypes for deserialization.
// Name and email are empty because they are preserved in the JWT Extra map
// from the serialized session data during deserialization.
func defaultSessionFactory(subject, idpSessionID, clientID string) fosite.Session {
	return session.New(subject, idpSessionID, clientID, session.UserClaims{})
}

// Health checks Redis connectivity.
func (s *RedisStorage) Health(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis health check failed: %w", err)
	}
	return nil
}

// Close closes the Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

// -----------------------
// ClientRegistry
// -----------------------

// storedClient is a serializable wrapper for OAuth clients.
type storedClient struct {
	ID            string   `json:"id"`
	Secret        []byte   `json:"secret,omitempty"` //nolint:gosec // G117: field legitimately holds sensitive data
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
	Scopes        []string `json:"scopes"`
	Audience      []string `json:"audience"`
	Public        bool     `json:"public"`
}

// redisClient implements fosite.Client for deserialization.
type redisClient struct {
	storedClient
}

func (c *redisClient) GetID() string                      { return c.ID }
func (c *redisClient) GetHashedSecret() []byte            { return c.Secret }
func (c *redisClient) GetRedirectURIs() []string          { return c.RedirectURIs }
func (c *redisClient) GetGrantTypes() fosite.Arguments    { return c.GrantTypes }
func (c *redisClient) GetResponseTypes() fosite.Arguments { return c.ResponseTypes }
func (c *redisClient) GetScopes() fosite.Arguments        { return c.Scopes }
func (c *redisClient) GetAudience() fosite.Arguments      { return c.Audience }
func (c *redisClient) IsPublic() bool                     { return c.Public }

// RegisterClient adds or updates a client in the storage.
func (s *RedisStorage) RegisterClient(ctx context.Context, client fosite.Client) error {
	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())

	stored := storedClient{
		ID:            client.GetID(),
		Secret:        client.GetHashedSecret(),
		RedirectURIs:  client.GetRedirectURIs(),
		GrantTypes:    client.GetGrantTypes(),
		ResponseTypes: client.GetResponseTypes(),
		Scopes:        client.GetScopes(),
		Audience:      client.GetAudience(),
		Public:        client.IsPublic(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal client: %w", err)
	}

	// Public clients (from DCR) expire to prevent unbounded growth; RenewClientTTL
	// refreshes this on proven use so actively-used clients are not evicted.
	// Confidential clients don't expire.
	ttl := time.Duration(0)
	if client.IsPublic() {
		ttl = DefaultPublicClientTTL
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetClient loads the client by its ID.
func (s *RedisStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	key := redisKey(s.keyPrefix, KeyTypeClient, id)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Client not found"))
		}
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	var stored storedClient
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client: %w", err)
	}

	return &redisClient{storedClient: stored}, nil
}

// RenewClientTTL extends a public client's registration TTL to DefaultPublicClientTTL.
//
// Call this on a proven-use signal — a successful token exchange/refresh — not on a
// client read. GetClient is reached from the unauthenticated front-channel
// /oauth/authorize handler before any authentication, and public clients have no
// secret, so renewing there would let any caller who knows a public client_id keep
// its row alive indefinitely and defeat the anti-bloat TTL. Renewing on token
// issuance ties registration survival to actual use.
//
// Only public clients carry a TTL; confidential clients are stored without one and
// are left untouched. EXPIRE on a missing key is a no-op, so a client whose row has
// already been evicted (or a non-persisted CIMD client) is safely ignored.
func (s *RedisStorage) RenewClientTTL(ctx context.Context, client fosite.Client) error {
	if client == nil || !client.IsPublic() {
		return nil
	}
	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())
	return s.client.Expire(ctx, key, DefaultPublicClientTTL).Err()
}

// ClientAssertionJWTValid returns an error if the JTI is known.
func (s *RedisStorage) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	key := redisKey(s.keyPrefix, KeyTypeJWT, jti)

	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check JWT: %w", err)
	}
	if exists > 0 {
		return fosite.ErrJTIKnown
	}
	return nil
}

// SetClientAssertionJWT marks a JTI as known for the given expiry time.
// If the JWT has already expired (exp is in the past), this is a no-op: there is
// no need to store it for replay detection since it will be rejected on expiry
// checks before reaching the JTI lookup.
func (s *RedisStorage) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	key := redisKey(s.keyPrefix, KeyTypeJWT, jti)

	ttl := time.Until(exp)
	if ttl <= 0 {
		slog.Debug("skipping storage of already-expired client assertion JWT",
			"jti", jti, "exp", exp)
		return nil
	}

	return s.client.Set(ctx, key, "1", ttl).Err()
}

// -----------------------
// oauth2.AuthorizeCodeStorage
// -----------------------

// CreateAuthorizeCodeSession stores the authorization request for a given authorization code.
func (s *RedisStorage) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	if code == "" {
		return fosite.ErrInvalidRequest.WithHint("authorization code cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)
	ttl := getTTLFromRequester(request, fosite.AuthorizeCode, DefaultAuthCodeTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetAuthorizeCodeSession retrieves the authorization request for a given code.
// Matches memory.go's pattern: get the auth code data first, then check if
// invalidated. InvalidateAuthorizeCodeSession extends the auth code TTL to match
// the invalidation marker, so the data is always available when the marker exists.
func (s *RedisStorage) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
		}
		return nil, fmt.Errorf("failed to get authorization code: %w", err)
	}

	request, err := unmarshalRequester(ctx, data, s)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request: %w", err)
	}

	// Check if the code has been invalidated.
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	invalidated, err := s.client.Exists(ctx, invalidatedKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check invalidation status: %w", err)
	}
	if invalidated > 0 {
		// Must return the request along with the error as per fosite documentation.
		// Fosite needs the Requester to extract the request ID for token revocation
		// during replay attack handling (RFC 6819).
		return request, fosite.ErrInvalidatedAuthorizeCode
	}

	return request, nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
// It extends the auth code key's TTL to match the invalidation marker, ensuring
// GetAuthorizeCodeSession can always return the Requester alongside
// ErrInvalidatedAuthorizeCode as required by fosite for token revocation.
func (s *RedisStorage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)

	// Check if the code exists
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check authorization code: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
	}

	// Atomically: create invalidation marker and extend auth code TTL to match.
	// The auth code data must outlive the invalidation marker so that
	// GetAuthorizeCodeSession can return the Requester for replay detection.
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, invalidatedKey, "1", DefaultInvalidatedCodeTTL)
	pipe.Expire(ctx, key, DefaultInvalidatedCodeTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// -----------------------
// oauth2.AccessTokenStorage
// -----------------------

// CreateAccessTokenSession stores the access token session.
func (s *RedisStorage) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("access token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)
	ttl := getTTLFromRequester(request, fosite.AccessToken, DefaultAccessTokenTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Store token, add to request ID index, and set index TTL atomically.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, request.GetID())
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, key, data, ttl)
	pipe.SAdd(ctx, reqIDKey, signature)
	pipe.Expire(ctx, reqIDKey, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// GetAccessTokenSession retrieves the access token session by its signature.
func (s *RedisStorage) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
		}
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeleteAccessTokenSession removes the access token session.
func (s *RedisStorage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)

	// Get the request first to find the request ID for cleaning up the index
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
		}
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// Delete the token
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete access token: %w", err)
	}

	// Best-effort secondary index cleanup (see warnOnCleanupErr).
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, stored.RequestID)
		warnOnCleanupErr(s.client.SRem(ctx, reqIDKey, signature).Err(), "SRem", reqIDKey)
	}

	return nil
}

// -----------------------
// oauth2.RefreshTokenStorage
// -----------------------

// CreateRefreshTokenSession stores the refresh token session.
func (s *RedisStorage) CreateRefreshTokenSession(
	ctx context.Context, signature string, _ string, request fosite.Requester,
) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("refresh token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)
	ttl := getTTLFromRequester(request, fosite.RefreshToken, DefaultRefreshTokenTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Store token, add to request ID index, and set index TTL atomically.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, request.GetID())
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, key, data, ttl)
	pipe.SAdd(ctx, reqIDKey, signature)
	pipe.Expire(ctx, reqIDKey, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// GetRefreshTokenSession retrieves the refresh token session by its signature.
func (s *RedisStorage) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
		}
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeleteRefreshTokenSession removes the refresh token session.
func (s *RedisStorage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)

	// Get the request first to find the request ID for cleaning up the index
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
		}
		return fmt.Errorf("failed to get refresh token: %w", err)
	}

	// Delete the token
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}

	// Best-effort secondary index cleanup (see warnOnCleanupErr).
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, stored.RequestID)
		warnOnCleanupErr(s.client.SRem(ctx, reqIDKey, signature).Err(), "SRem", reqIDKey)
	}

	return nil
}

// RotateRefreshToken invalidates a refresh token and all its related token data.
// This is a no-op if the token does not exist (returns nil), matching the behavior
// of the in-memory implementation. All cleanup operations are best-effort
// (see warnOnCleanupErr); the new refresh token has already been issued by fosite,
// so partial cleanup is acceptable.
func (s *RedisStorage) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	// Delete the specific refresh token. Del returns the number of keys removed;
	// 0 means the token did not exist (already rotated or never created).
	refreshKey := redisKey(s.keyPrefix, KeyTypeRefresh, refreshTokenSignature)
	deleted, err := s.client.Del(ctx, refreshKey).Result()
	if err != nil {
		warnOnCleanupErr(err, "Del", refreshKey)
	}
	if deleted == 0 {
		slog.Debug("refresh token not found during rotation, treating as no-op",
			"request_id", requestID, "signature", refreshTokenSignature)
	}

	// Remove from the request ID index
	reqIDRefreshKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, requestID)
	warnOnCleanupErr(s.client.SRem(ctx, reqIDRefreshKey, refreshTokenSignature).Err(), "SRem", reqIDRefreshKey)

	// Delete all access tokens associated with this request ID
	reqIDAccessKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDAccessKey).Result()
	if err == nil {
		for _, sig := range signatures {
			accessKey := redisKey(s.keyPrefix, KeyTypeAccess, sig)
			warnOnCleanupErr(s.client.Del(ctx, accessKey).Err(), "Del", accessKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, reqIDAccessKey).Err(), "Del", reqIDAccessKey)
	}

	return nil
}

// -----------------------
// oauth2.TokenRevocationStorage
// -----------------------

// RevokeAccessToken marks an access token as revoked by request ID.
func (s *RedisStorage) RevokeAccessToken(ctx context.Context, requestID string) error {
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get access token signatures: %w", err)
	}

	for _, sig := range signatures {
		accessKey := redisKey(s.keyPrefix, KeyTypeAccess, sig)
		warnOnCleanupErr(s.client.Del(ctx, accessKey).Err(), "Del", accessKey)
	}

	// Clean up the index
	warnOnCleanupErr(s.client.Del(ctx, reqIDKey).Err(), "Del", reqIDKey)

	return nil
}

// RevokeRefreshToken marks a refresh token as revoked by request ID.
func (s *RedisStorage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get refresh token signatures: %w", err)
	}

	for _, sig := range signatures {
		refreshKey := redisKey(s.keyPrefix, KeyTypeRefresh, sig)
		warnOnCleanupErr(s.client.Del(ctx, refreshKey).Err(), "Del", refreshKey)
	}

	// Clean up the index
	warnOnCleanupErr(s.client.Del(ctx, reqIDKey).Err(), "Del", reqIDKey)

	return nil
}

// RevokeRefreshTokenMaybeGracePeriod marks a refresh token as revoked, optionally allowing
// a grace period. For this implementation, we revoke immediately.
func (s *RedisStorage) RevokeRefreshTokenMaybeGracePeriod(ctx context.Context, requestID string, _ string) error {
	return s.RevokeRefreshToken(ctx, requestID)
}

// -----------------------
// pkce.PKCERequestStorage
// -----------------------

// CreatePKCERequestSession stores the PKCE request session.
func (s *RedisStorage) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("PKCE signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)
	ttl := getTTLFromRequester(request, fosite.AuthorizeCode, DefaultPKCETTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetPKCERequestSession retrieves the PKCE request session by its signature.
func (s *RedisStorage) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
		}
		return nil, fmt.Errorf("failed to get PKCE request: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeletePKCERequestSession removes the PKCE request session.
func (s *RedisStorage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)

	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete PKCE request: %w", err)
	}
	if result == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
	}

	return nil
}

// -----------------------
// Upstream Token Storage
// -----------------------

// storedUpstreamTokens is a serializable wrapper for UpstreamTokens.
// Time fields use int64 Unix epoch; 0 is the sentinel meaning "not set".
// Neither time field uses omitempty so that 0 is always present and the
// read path can use a consistent != 0 check for both.
type storedUpstreamTokens struct {
	ProviderID       string `json:"provider_id"`
	AccessToken      string `json:"access_token"`  //nolint:gosec // G117: field legitimately holds sensitive data
	RefreshToken     string `json:"refresh_token"` //nolint:gosec // G117: field legitimately holds sensitive data
	IDToken          string `json:"id_token"`
	ExpiresAt        int64  `json:"expires_at"`
	SessionExpiresAt int64  `json:"session_expires_at"`
	UserID           string `json:"user_id"`
	UpstreamSubject  string `json:"upstream_subject"`
	ClientID         string `json:"client_id"`
}

// toUpstreamTokens converts the stored int64 epoch fields to time.Time and returns
// the populated UpstreamTokens. Zero epoch values are preserved as zero time.Time.
func (s *storedUpstreamTokens) toUpstreamTokens() *UpstreamTokens {
	var expiresAt time.Time
	if s.ExpiresAt != 0 {
		expiresAt = time.Unix(s.ExpiresAt, 0)
	}
	var sessionExpiresAt time.Time
	if s.SessionExpiresAt != 0 {
		sessionExpiresAt = time.Unix(s.SessionExpiresAt, 0)
	}
	return &UpstreamTokens{
		ProviderID:       s.ProviderID,
		AccessToken:      s.AccessToken,
		RefreshToken:     s.RefreshToken,
		IDToken:          s.IDToken,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: sessionExpiresAt,
		UserID:           s.UserID,
		UpstreamSubject:  s.UpstreamSubject,
		ClientID:         s.ClientID,
	}
}

// storeUpstreamTokensScript atomically writes new token data, updates the
// session index set, and updates user reverse-index sets, preventing a race
// condition where concurrent writes for the same session could leave orphaned
// entries in user sets.
//
// UserID bookkeeping is Go-side: the Go write path issues one best-effort GET
// immediately before this script and passes the previous row's UserID as
// ARGV[3]. This indirection exists because token values may be encrypted
// envelopes (see WithTokenEncryption), which cjson cannot decode; the GO side
// holds the keyring and extracts the UserID in the clear. The extra read is
// non-atomic with the script, so a concurrent writer changing a row's UserID
// between the GET and the script can leave a stale member in the old user's
// reverse-index set. That staleness is bounded by the member key's TTL and
// tolerated by readers (they skip missing rows); it matches the best-effort
// reverse-index cleanup posture used throughout this file.
//
// KEYS[1] = per-provider token key (e.g. "thv:auth:{ns:name}:upstream:{sessionID}:{providerName}")
// KEYS[2] = session index set key (e.g. "thv:auth:{ns:name}:upstream:idx:{sessionID}")
// ARGV[1] = new token data (JSON or "null" marker; an encrypted envelope when encryption is enabled)
// ARGV[2] = TTL in milliseconds
// ARGV[3] = previous row's UserID, from the Go-side GET ("" if none/unknown)
// ARGV[4] = new UserID ("" if no user)
// ARGV[5] = user upstream set key prefix (e.g. "thv:auth:{ns:name}:user:upstream:")
//
// Cluster slot invariant: all set keys are constructed inside the script as
// `ARGV[5] .. userID` rather than passed as declared KEYS. Every
// dynamically-built key MUST therefore inherit the `{ns:name}` hash tag that
// is baked into ARGV[5] (s.keyPrefix). All callers derive ARGV[5] from
// s.keyPrefix, which DeriveKeyPrefix builds with the `{ns:name}` hash tag, so
// user-set keys land on the same Redis Cluster slot as KEYS[1] and KEYS[2]. A
// future refactor that strips the hash tag — or rebuilds setPrefix from raw
// inputs without the tag — will silently pass on standalone Redis and fail
// with CROSSSLOT under Cluster.
var storeUpstreamTokensScript = redis.NewScript(`
local ttlMs = tonumber(ARGV[2])
if ttlMs > 0 then
    redis.call('SET', KEYS[1], ARGV[1], 'PX', ttlMs)
else
    redis.call('SET', KEYS[1], ARGV[1])
end

-- Maintain the session index set's TTL.
--
-- Invariant: the index set must outlive every per-provider key it points to.
--   * If ANY member is non-expiring (ttlMs == 0), the index set must also be
--     persistent. Otherwise the index evicts and we lose the ability to find
--     (and clean up) the per-provider key, leaking it forever.
--   * If ALL members are expiring, the index TTL must be at least the longest
--     member TTL.
--
-- Known trade-off: an in-place rewrite of the same (sessionID, providerName)
-- slot from non-expiring to expiring leaves the index PERSIST'd, even though
-- the rewritten member was the sole persistence anchor. Detecting this would
-- require tracking per-member TTL state in the index, which adds complexity.
-- We accept the trade-off because DeleteUpstreamTokens and session GC clean
-- the index up anyway. This behaviour is pinned by the test
-- "same provider rewrite from non-expiring to expiring keeps PERSIST'd until
-- rewrite" in redis_test.go — a future maintainer who tightens the rule will
-- see that test fail and can find the rationale here.
--
-- We discriminate two cases that look identical AFTER SADD (both have PTTL == -1):
--   1. A fresh set our SADD just created. We own the TTL decision unconditionally.
--   2. An existing set previously PERSIST'd by a non-expiring member. Must stay
--      persistent — applying a TTL here is the bug this script must avoid.
--
-- The trick: read EXISTS BEFORE SADD. SADD creates the set as a side-effect,
-- so post-SADD any "no TTL" state is ambiguous; pre-SADD it is not.
local idxExisted = redis.call('EXISTS', KEYS[2])
redis.call('SADD', KEYS[2], KEYS[1])

if ttlMs == 0 then
    -- This member never expires. Make the index persistent (no-op if already so).
    redis.call('PERSIST', KEYS[2])
elseif idxExisted == 0 then
    -- Fresh set; our SADD created it. Apply our TTL.
    redis.call('PEXPIRE', KEYS[2], ttlMs)
else
    -- Existing set; its TTL summarises prior members.
    local idxTTL = redis.call('PTTL', KEYS[2])
    if idxTTL == -1 then
        -- A previous non-expiring write PERSIST'd it. Leave it alone —
        -- applying a TTL here is the bug we are fixing.
    elseif idxTTL < ttlMs then
        -- Existing TTL is shorter than this member's. Extend.
        redis.call('PEXPIRE', KEYS[2], ttlMs)
    end
    -- else: idxTTL >= ttlMs, index already outlives this member.
end

local oldUserID = ARGV[3]
local newUserID = ARGV[4]
local setPrefix = ARGV[5]

if oldUserID ~= "" and oldUserID ~= newUserID then
    redis.call('SREM', setPrefix .. oldUserID, KEYS[1])
end

if newUserID ~= "" then
    redis.call('SADD', setPrefix .. newUserID, KEYS[1])
end

return 1
`)

// marshalUpstreamTokensWithTTL marshals tokens and calculates TTL. When
// envelope encryption is enabled (WithTokenEncryption), the marshaled JSON is
// sealed with redisKey as AAD; the nil-token tombstone is never sealed (it
// carries no secret).
func (s *RedisStorage) marshalUpstreamTokensWithTTL(
	tokens *UpstreamTokens, redisKey string,
) ([]byte, time.Duration, error) {
	if tokens == nil {
		return []byte(nullMarker), DefaultAccessTokenTTL, nil
	}

	// Store 0 for zero time to use as a sentinel meaning "no expiry".
	// time.Time{}.Unix() returns -62135596800 which is not a useful sentinel.
	var expiresAtUnix int64
	if !tokens.ExpiresAt.IsZero() {
		expiresAtUnix = tokens.ExpiresAt.Unix()
	}

	var sessionExpiresAtUnix int64
	if !tokens.SessionExpiresAt.IsZero() {
		sessionExpiresAtUnix = tokens.SessionExpiresAt.Unix()
	}

	stored := storedUpstreamTokens{
		ProviderID:       tokens.ProviderID,
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		IDToken:          tokens.IDToken,
		ExpiresAt:        expiresAtUnix,
		SessionExpiresAt: sessionExpiresAtUnix,
		UserID:           tokens.UserID,
		UpstreamSubject:  tokens.UpstreamSubject,
		ClientID:         tokens.ClientID,
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal upstream tokens: %w", err)
	}

	if s.tokenEnc != nil {
		data, err = tokenenc.Seal(s.tokenEnc, redisKey, data)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to encrypt upstream tokens: %w", err)
		}
	}

	// Add DefaultRefreshTokenTTL beyond access token expiry so the refresh token
	// survives in storage for transparent token refresh by the middleware.
	// Zero ExpiresAt means the token never expires; return ttl=0 so the Lua script
	// stores the key without a Redis TTL.
	var ttl time.Duration // zero means no Redis TTL (non-expiring token with no known session bound)
	if !tokens.ExpiresAt.IsZero() {
		ttl = time.Until(tokens.ExpiresAt) + DefaultRefreshTokenTTL
		if ttl <= 0 {
			// Access token and its refresh grace period have both passed — evict promptly.
			ttl = time.Second
		}
	} else if !tokens.SessionExpiresAt.IsZero() {
		ttl = time.Until(tokens.SessionExpiresAt) + DefaultRefreshTokenTTL
		if ttl <= 0 {
			// Session bound and its refresh grace period have both passed — evict promptly.
			ttl = time.Second
		}
	}

	return data, ttl, nil
}

// StoreUpstreamTokens stores the upstream IDP tokens for a session and provider.
// Uses a Lua script to atomically write token data, update the session index set,
// and update user reverse-index sets, preventing race conditions on concurrent writes.
func (s *RedisStorage) StoreUpstreamTokens(ctx context.Context, sessionID, providerName string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	data, ttl, err := s.marshalUpstreamTokensWithTTL(tokens, key)
	if err != nil {
		return err
	}

	// Read the previous row's UserID for reverse-index maintenance. The Lua
	// script cannot do this itself because the stored value may be an
	// encrypted envelope. Best-effort: on any failure pass "" — the stale
	// reverse-index member is TTL-bounded and tolerated by readers.
	oldUserID := s.readUserIDForCleanup(ctx, key)

	newUserID := ""
	if tokens != nil {
		newUserID = tokens.UserID
	}

	userSetKeyPrefix := s.keyPrefix + KeyTypeUserUpstream + ":"

	_, err = storeUpstreamTokensScript.Run(ctx, s.client,
		[]string{key, idxKey},
		string(data),
		ttl.Milliseconds(),
		oldUserID,
		newUserID,
		userSetKeyPrefix,
	).Result()
	if err != nil {
		return fmt.Errorf("failed to store upstream tokens: %w", err)
	}

	return nil
}

// GetUpstreamTokens retrieves the upstream IDP tokens for a session and provider.
// Returns a new UpstreamTokens struct deserialized from Redis, which acts as
// a defensive copy - callers cannot modify the stored data by mutating the return value.
// Binding validation (see ExpectedBinding) runs after deserialization and before
// expiry is surfaced: a mismatched row returns (nil, ErrInvalidBinding).
func (s *RedisStorage) GetUpstreamTokens(
	ctx context.Context, sessionID, providerName string, expected *ExpectedBinding,
) (*UpstreamTokens, error) {
	if sessionID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	tokens, err := s.getUpstreamTokensFromKey(ctx, key)
	if err != nil && !errors.Is(err, ErrExpired) {
		return nil, err
	}

	// Binding is checked before the ErrExpired carried by the unmarshal is
	// surfaced: a mismatched row must never release refresh material.
	if bindErr := checkUpstreamBinding(ctx, tokens, expected); bindErr != nil {
		return nil, bindErr
	}
	warnIfAssertedUserOnLegacyRow(tokens, expected, sessionID, providerName)

	return tokens, err
}

// GetAllUpstreamTokens retrieves all upstream IDP tokens for a session across all providers.
// Uses SMEMBERS on the session index set to find all provider keys, then MGET to fetch them.
// Returns a map of providerName -> tokens. Returns an empty map for unknown sessions.
func (s *RedisStorage) GetAllUpstreamTokens(
	ctx context.Context, sessionID string, expected *ExpectedBinding,
) (map[string]*UpstreamTokens, error) {
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)
	result := make(map[string]*UpstreamTokens)

	// Get all provider keys from the session index set
	providerKeys, err := s.client.SMembers(ctx, idxKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return result, nil
		}
		return nil, fmt.Errorf("failed to get upstream token index: %w", err)
	}

	providerKeys, dropped := filterIndexMembersByPrefix(s.keyPrefix, providerKeys)
	warnDroppedIndexMembers("GetAllUpstreamTokens", idxKey, s.keyPrefix, dropped)

	if len(providerKeys) == 0 {
		return result, nil
	}

	// Fetch all provider tokens in a single MGET
	values, err := s.client.MGet(ctx, providerKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// The per-provider key format is "{prefix}upstream:{sessionID}:{providerName}"
	// Extract providerName by stripping the prefix + "upstream:{sessionID}:"
	keyPrefix := fmt.Sprintf("%s%s:%s:", s.keyPrefix, KeyTypeUpstream, sessionID)

	for i, val := range values {
		providerName, tokens := s.parseBulkUpstreamRow(ctx, providerKeys[i], keyPrefix, val, sessionID, expected)
		if providerName != "" {
			result[providerName] = tokens
		}
	}

	return result, nil
}

// parseBulkUpstreamRow decrypts and decodes one MGET value of a
// GetAllUpstreamTokens bulk read into its provider name and tokens. Rows that
// are missing, corrupt, undecryptable, or fail per-row binding validation are
// skipped (empty provider name returned); exclusions are logged at WARN with
// metadata only.
func (s *RedisStorage) parseBulkUpstreamRow(
	ctx context.Context,
	key, keyPrefix string,
	val any,
	sessionID string,
	expected *ExpectedBinding,
) (string, *UpstreamTokens) {
	if val == nil {
		return "", nil
	}

	data, ok := val.(string)
	if !ok {
		slog.Warn("skipping upstream token entry: unexpected type", "key", key)
		return "", nil
	}

	tokens, parseErr := s.decodeUpstreamTokens(ctx, key, []byte(data))
	if parseErr != nil && !errors.Is(parseErr, ErrExpired) {
		slog.Warn("skipping corrupt upstream token entry", "key", key, "error", parseErr)
		return "", nil
	}

	// Extract provider name from the key
	providerName := ""
	if len(key) > len(keyPrefix) {
		providerName = key[len(keyPrefix):]
	}
	if providerName == "" && tokens != nil {
		providerName = tokens.ProviderID
	}
	if providerName == "" {
		return "", nil
	}

	// Binding validation applies per row: a mismatched row is excluded
	// rather than failing the whole read.
	if bindErr := checkUpstreamBinding(ctx, tokens, expected); bindErr != nil {
		slog.Warn("excluding upstream token row: binding validation failed",
			"session_id", sessionID,
			"provider", providerName,
			"dimension", bindingMismatchDimension(bindErr),
		)
		return "", nil
	}
	warnIfAssertedUserOnLegacyRow(tokens, expected, sessionID, providerName)

	return providerName, tokens
}

// DeleteUpstreamTokens removes all upstream IDP tokens for a session (all providers).
// Uses the session index set to find all provider keys and deletes them atomically.
func (s *RedisStorage) DeleteUpstreamTokens(ctx context.Context, sessionID string) error {
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	// Get all provider keys from the session index set
	providerKeys, err := s.client.SMembers(ctx, idxKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return fmt.Errorf("failed to get upstream token index: %w", err)
	}

	providerKeys, dropped := filterIndexMembersByPrefix(s.keyPrefix, providerKeys)
	warnDroppedIndexMembers("DeleteUpstreamTokens", idxKey, s.keyPrefix, dropped)

	if len(providerKeys) == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	// Collect UserIDs for reverse-index cleanup before deleting. Decryption
	// failures skip that row's cleanup (best-effort) without failing the delete.
	var userIDs []string
	for _, providerKey := range providerKeys {
		if userID := s.readUserIDForCleanup(ctx, providerKey); userID != "" {
			userIDs = append(userIDs, userID)
		}
	}

	// Delete all provider keys and the index set
	keysToDelete := append(slices.Clone(providerKeys), idxKey)
	if err := s.client.Del(ctx, keysToDelete...).Err(); err != nil {
		return fmt.Errorf("failed to delete upstream tokens: %w", err)
	}

	// Best-effort secondary index cleanup for user:upstream sets — one variadic
	// SRem per user instead of a round-trip per provider.
	for _, userID := range userIDs {
		userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)
		members := make([]any, len(providerKeys))
		for i, providerKey := range providerKeys {
			members[i] = providerKey
		}
		warnOnCleanupErr(s.client.SRem(ctx, userUpstreamSetKey, members...).Err(), "SRem", userUpstreamSetKey)
	}

	return nil
}

// DeleteUpstreamTokensForProvider removes tokens for a single (sessionID, providerName),
// leaving sibling providers' rows intact. Absent row returns nil.
func (s *RedisStorage) DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, providerName string) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	// Best-effort: read UserID for reverse-index cleanup before deleting.
	// Absent rows, tombstones, and decrypt/unmarshal failures all yield ""
	// and skip user-index cleanup gracefully.
	userID := s.readUserIDForCleanup(ctx, key)

	// Del returns count 0 on absent key — treat as nil (not ErrNotFound).
	if delErr := s.client.Del(ctx, key).Err(); delErr != nil {
		return fmt.Errorf("failed to delete upstream tokens for provider: %w", delErr)
	}

	// Best-effort secondary index cleanup — leave sibling members in idxKey intact.
	// If this was the last provider for the session, the set becomes empty and
	// expires by its own TTL; we do not delete the set key itself (intentional,
	// matching the best-effort cleanup posture of DeleteUpstreamTokens).
	warnOnCleanupErr(s.client.SRem(ctx, idxKey, key).Err(), "SRem", idxKey)
	if userID != "" {
		userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)
		warnOnCleanupErr(s.client.SRem(ctx, userUpstreamSetKey, key).Err(), "SRem", userUpstreamSetKey)
	}

	return nil
}

// GetLatestUpstreamTokensForUser returns the upstream token row for (userID, providerID)
// with the highest ExpiresAt across all sessions.
//
// Expired tokens (past ExpiresAt) are returned so callers can use the refresh
// token; filtering by access-token expiry is the caller's responsibility.
// See the interface declaration in types.go for the full contract.
func (s *RedisStorage) GetLatestUpstreamTokensForUser(ctx context.Context, userID, providerID string) (*UpstreamTokens, error) {
	if userID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}
	if providerID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider ID cannot be empty")
	}

	setKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)

	members, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get user upstream index: %w", err)
	}

	members, dropped := filterIndexMembersByPrefix(s.keyPrefix, members)
	warnDroppedIndexMembers("GetLatestUpstreamTokensForUser", setKey, s.keyPrefix, dropped)

	if len(members) == 0 {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	values, err := s.client.MGet(ctx, members...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	var winner *storedUpstreamTokens
	for i, val := range values {
		stored, ok := s.parseUserUpstreamEntry(ctx, val, providerID, members[i])
		if !ok {
			continue
		}

		// Tie-breaker: prefer non-expiring rows (ExpiresAt == 0, the no-expiry
		// sentinel for providers like Slack and GitHub OAuth Apps). Among finite
		// expiries, the latest wins. This aligns with the rest of the package
		// treating zero ExpiresAt as "alive forever".
		if winner == nil || compareExpiryInt64(stored.ExpiresAt, winner.ExpiresAt) > 0 {
			winner = stored
		}
	}

	if winner == nil {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	return winner.toUpstreamTokens(), nil
}

// compareExpiryInt64 is the int64 (Unix-epoch) variant of compareExpiry. Zero
// (no-expiry sentinel) ranks latest; among finite epochs, the larger ranks
// latest. Returns -1/0/+1.
func compareExpiryInt64(a, b int64) int {
	switch {
	case a == 0 && b == 0:
		return 0
	case a == 0:
		return 1
	case b == 0:
		return -1
	case a > b:
		return 1
	case a < b:
		return -1
	}
	return 0
}

// parseUserUpstreamEntry parses one raw Redis value from the user-upstream index
// and returns the decoded storedUpstreamTokens together with a match flag.
// It returns (nil, false) for nil values, type mismatches, deletion tombstones,
// decryption or JSON decode errors, and rows whose ProviderID does not match
// providerID. keyName is the member key the value was fetched under — it is
// both the envelope AAD and the identifier used in warning log messages.
func (s *RedisStorage) parseUserUpstreamEntry(
	ctx context.Context, val any, providerID, keyName string,
) (*storedUpstreamTokens, bool) {
	if val == nil {
		// Dangling set member: the per-provider key has been TTL-evicted.
		// Skip it; the next write will clean up the index entry (best-effort).
		return nil, false
	}

	data, ok := val.(string)
	if !ok {
		slog.Warn("skipping upstream token entry: unexpected type", "key", keyName)
		return nil, false
	}

	if data == nullMarker {
		// Deletion tombstone — treat as absent.
		return nil, false
	}

	plaintext, legacy, err := tokenenc.Open(s.tokenEnc, keyName, []byte(data))
	if err != nil {
		slog.Warn("skipping upstream token entry: decryption failed", "key", keyName, "error", err)
		return nil, false
	}
	if !legacy && tokenenc.NeedsRotation(s.tokenEnc, []byte(data)) {
		s.lazyReseal(ctx, keyName, plaintext)
	}

	var stored storedUpstreamTokens
	if unmarshalErr := json.Unmarshal(plaintext, &stored); unmarshalErr != nil {
		slog.Warn("skipping corrupt upstream token entry", "key", keyName, "error", unmarshalErr)
		return nil, false
	}

	if stored.ProviderID != providerID {
		return nil, false
	}

	return &stored, true
}

// getUpstreamTokensFromKey retrieves and deserializes upstream tokens from a specific Redis key.
func (s *RedisStorage) getUpstreamTokensFromKey(ctx context.Context, key string) (*UpstreamTokens, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	return s.decodeUpstreamTokens(ctx, key, data)
}

// decodeUpstreamTokens decrypts (when the value is an envelope) and
// deserializes one upstream-token value stored under redisKey. The redisKey
// is the AAD the envelope was sealed against. The nullMarker tombstone check
// happens inside unmarshalUpstreamTokens, before decryption would matter
// (tombstones are never sealed).
func (s *RedisStorage) decodeUpstreamTokens(ctx context.Context, redisKey string, data []byte) (*UpstreamTokens, error) {
	if string(data) == nullMarker {
		return unmarshalUpstreamTokens(data)
	}

	plaintext, legacy, err := tokenenc.Open(s.tokenEnc, redisKey, data)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt upstream tokens: %w", err)
	}
	if legacy && s.tokenEnc != nil {
		slog.Debug("read legacy plaintext upstream token row with encryption enabled",
			"key", redisKey)
	}

	// Lazy re-encryption: a readable envelope sealed under a retired key is
	// re-sealed with the active key so rotation converges without a sweep.
	if !legacy && tokenenc.NeedsRotation(s.tokenEnc, data) {
		s.lazyReseal(ctx, redisKey, plaintext)
	}

	return unmarshalUpstreamTokens(plaintext)
}

// readUserIDForCleanup best-effort extracts the UserID from the row stored
// under key, decrypting envelope values first. It returns "" on any failure:
// callers treat UserID extraction for reverse-index cleanup as optional and
// never fail their primary operation over it.
func (s *RedisStorage) readUserIDForCleanup(ctx context.Context, key string) string {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil || string(data) == nullMarker {
		return ""
	}
	plaintext, _, err := tokenenc.Open(s.tokenEnc, key, data)
	if err != nil {
		slog.Warn("skipping user-index cleanup: failed to decrypt upstream token row",
			"key", key, "error", err)
		return ""
	}
	var stored storedUpstreamTokens
	if err := json.Unmarshal(plaintext, &stored); err != nil {
		return ""
	}
	return stored.UserID
}

// lazyReseal re-seals plaintext with the active key, overwriting the row at
// redisKey while preserving its remaining TTL. Best-effort: failures are
// logged and the read that triggered this still succeeds. The re-seal is a
// plain SET, so a concurrent StoreUpstreamTokens can be clobbered back to a
// retired-kid envelope — harmless, since the next read re-seals again.
func (s *RedisStorage) lazyReseal(ctx context.Context, redisKey string, plaintext []byte) {
	sealed, err := tokenenc.Seal(s.tokenEnc, redisKey, plaintext)
	if err != nil {
		slog.Warn("lazy token re-encryption failed", "key", redisKey, "error", err)
		return
	}
	pttl, err := s.client.PTTL(ctx, redisKey).Result()
	if err != nil {
		slog.Warn("lazy token re-encryption failed", "key", redisKey, "error", err)
		return
	}
	// PTTL returns -1ns for "no expiry" and -2ns for "key missing" (go-redis
	// passes the sentinel integers through as durations).
	switch {
	case pttl > 0:
		err = s.client.Set(ctx, redisKey, sealed, pttl).Err()
	case pttl == -1:
		// Key exists with no expiry; preserve that.
		err = s.client.Set(ctx, redisKey, sealed, 0).Err()
	default:
		// Key vanished between read and re-seal; nothing to do.
		return
	}
	if err != nil {
		slog.Warn("lazy token re-encryption failed", "key", redisKey, "error", err)
	}
}

// unmarshalUpstreamTokens deserializes upstream tokens from JSON bytes.
func unmarshalUpstreamTokens(data []byte) (*UpstreamTokens, error) {
	// Handle null marker
	if string(data) == nullMarker {
		return nil, nil
	}

	var stored storedUpstreamTokens
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal upstream tokens: %w", err)
	}

	tokens := stored.toUpstreamTokens()

	// tokens.ExpiresAt is zero when the stored epoch was 0 (the write path stores
	// 0 for zero time.Time, which toUpstreamTokens leaves as the zero time). Skip
	// the expiry check in that case since Redis TTL handles the actual expiration.
	// Return tokens along with ErrExpired so callers can use the refresh token.
	if !tokens.ExpiresAt.IsZero() && time.Now().After(tokens.ExpiresAt) {
		return tokens, ErrExpired
	}

	return tokens, nil
}

// -----------------------
// DCR Credentials Storage
// -----------------------

// storedDCRCredentials is the on-the-wire JSON representation of DCRCredentials.
// Time fields use int64 Unix epoch; 0 is the sentinel meaning "not set", matching
// the storedUpstreamTokens convention. ClientSecretExpiresAt == 0 specifically
// encodes the RFC 7591 §3.2.1 "client_secret does not expire" semantics, in
// which case StoreDCRCredentials persists the entry without a Redis TTL.
type storedDCRCredentials struct {
	// Embed the canonical key so a row recovered without its lookup key
	// (e.g. via SCAN during diagnostics) still self-identifies.
	KeyIssuer      string `json:"key_issuer"`
	KeyUpstreamID  string `json:"key_upstream_id"`
	KeyRedirectURI string `json:"key_redirect_uri"`
	KeyScopesHash  string `json:"key_scopes_hash"`

	ProviderName string `json:"provider_name,omitempty"`

	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret,omitempty"` //nolint:gosec // G117: field legitimately holds sensitive data
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`

	// Bearer token for the RFC 7592 management endpoint.
	//nolint:gosec // G117: field legitimately holds sensitive data
	RegistrationAccessToken string `json:"registration_access_token,omitempty"`
	RegistrationClientURI   string `json:"registration_client_uri,omitempty"`

	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `json:"token_endpoint,omitempty"`

	CreatedAt             int64 `json:"created_at"`
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at"`
}

// toDCRCredentials decodes the stored form back into the public type, mirroring
// storedUpstreamTokens.toUpstreamTokens. Zero epoch values become the zero
// time.Time, preserving the "not set" sentinel.
func (s *storedDCRCredentials) toDCRCredentials() *DCRCredentials {
	var createdAt time.Time
	if s.CreatedAt != 0 {
		createdAt = time.Unix(s.CreatedAt, 0)
	}
	var clientSecretExpiresAt time.Time
	if s.ClientSecretExpiresAt != 0 {
		clientSecretExpiresAt = time.Unix(s.ClientSecretExpiresAt, 0)
	}
	return &DCRCredentials{
		Key: DCRKey{
			Issuer:      s.KeyIssuer,
			UpstreamID:  s.KeyUpstreamID,
			RedirectURI: s.KeyRedirectURI,
			ScopesHash:  s.KeyScopesHash,
		},
		ProviderName:            s.ProviderName,
		ClientID:                s.ClientID,
		ClientSecret:            s.ClientSecret,
		TokenEndpointAuthMethod: s.TokenEndpointAuthMethod,
		RegistrationAccessToken: s.RegistrationAccessToken,
		RegistrationClientURI:   s.RegistrationClientURI,
		AuthorizationEndpoint:   s.AuthorizationEndpoint,
		TokenEndpoint:           s.TokenEndpoint,
		CreatedAt:               createdAt,
		ClientSecretExpiresAt:   clientSecretExpiresAt,
	}
}

// StoreDCRCredentials persists DCR credentials, overwriting any existing entry
// for the same Key. Defensive copy is provided implicitly by JSON serialisation —
// caller mutations after the call cannot reach the persisted bytes.
//
// # TTL
//
// When creds.ClientSecretExpiresAt is non-zero (the upstream advertised an
// RFC 7591 §3.2.1 client_secret_expires_at), the entry is stored with a Redis
// TTL derived from time.Until(ClientSecretExpiresAt) so the row evicts before
// the upstream rejects the secret at the token endpoint. When zero (RFC 7591
// "never"), Set with TTL=0 is used and the entry is long-lived.
//
// If ClientSecretExpiresAt is already in the past at call time, the entry is
// written with the bounded TTL pastExpiryDCRTTL (1 second) rather than rejected
// or stored long-lived. This keeps the store's "already-expired secret" window
// narrow even if the resolver never re-reads the row, and matches the
// fail-loud-but-tolerant posture: the caller's expiry timestamp round-trips so
// a downstream reader can still observe it and trigger re-registration.
//
// Validation is delegated to validateDCRCredentialsForStore so the rejection
// set stays in sync with MemoryStorage and any future backend.
func (s *RedisStorage) StoreDCRCredentials(ctx context.Context, creds *DCRCredentials) error {
	if err := validateDCRCredentialsForStore(creds); err != nil {
		return err
	}

	key := redisDCRKey(s.keyPrefix, creds.Key)

	stored := storedDCRCredentials{
		KeyIssuer:               creds.Key.Issuer,
		KeyUpstreamID:           creds.Key.UpstreamID,
		KeyRedirectURI:          creds.Key.RedirectURI,
		KeyScopesHash:           creds.Key.ScopesHash,
		ProviderName:            creds.ProviderName,
		ClientID:                creds.ClientID,
		ClientSecret:            creds.ClientSecret,
		TokenEndpointAuthMethod: creds.TokenEndpointAuthMethod,
		RegistrationAccessToken: creds.RegistrationAccessToken,
		RegistrationClientURI:   creds.RegistrationClientURI,
		AuthorizationEndpoint:   creds.AuthorizationEndpoint,
		TokenEndpoint:           creds.TokenEndpoint,
	}
	if !creds.CreatedAt.IsZero() {
		stored.CreatedAt = creds.CreatedAt.Unix()
	}
	if !creds.ClientSecretExpiresAt.IsZero() {
		stored.ClientSecretExpiresAt = creds.ClientSecretExpiresAt.Unix()
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal dcr credentials: %w", err)
	}

	// Derive Redis TTL from ClientSecretExpiresAt:
	//   * Zero (unset)   -> TTL=0 (no expiration) per RFC 7591 §3.2.1 "never".
	//   * Future expiry  -> TTL = time.Until(expiry).
	//   * Past expiry    -> TTL = pastExpiryDCRTTL (bounded eviction window).
	// See the function docstring for the past-expiry rationale.
	ttl := time.Duration(0)
	if !creds.ClientSecretExpiresAt.IsZero() {
		if until := time.Until(creds.ClientSecretExpiresAt); until > 0 {
			ttl = until
		} else {
			ttl = pastExpiryDCRTTL
		}
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store dcr credentials: %w", err)
	}
	return nil
}

// GetDCRCredentials retrieves the credentials previously persisted under key.
// Returns ErrNotFound (wrapped) when no entry exists. The returned value is a
// fresh struct decoded from JSON, which acts as a defensive copy.
//
// An unpopulated key (empty Issuer, UpstreamID, RedirectURI, or ScopesHash) cannot match
// any stored row because StoreDCRCredentials rejects such keys, so a Get
// against one is a normal miss — ErrNotFound — matching
// MemoryStorage.GetDCRCredentials and the DCRCredentialStore interface contract.
func (s *RedisStorage) GetDCRCredentials(ctx context.Context, key DCRKey) (*DCRCredentials, error) {
	redisKey := redisDCRKey(s.keyPrefix, key)
	data, err := s.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("DCR credentials not found"))
		}
		return nil, fmt.Errorf("failed to get dcr credentials: %w", err)
	}

	var stored storedDCRCredentials
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dcr credentials: %w", err)
	}

	return stored.toDCRCredentials(), nil
}

// -----------------------
// Pending Authorization Storage
// -----------------------

// storedPendingAuthorization is a serializable wrapper for PendingAuthorization.
type storedPendingAuthorization struct {
	ClientID             string   `json:"client_id"`
	RedirectURI          string   `json:"redirect_uri"`
	State                string   `json:"state"`
	PKCEChallenge        string   `json:"pkce_challenge"`
	PKCEMethod           string   `json:"pkce_method"`
	Scopes               []string `json:"scopes"`
	InternalState        string   `json:"internal_state"`
	UpstreamPKCEVerifier string   `json:"upstream_pkce_verifier"`
	UpstreamNonce        string   `json:"upstream_nonce"`
	UpstreamProviderName string   `json:"upstream_provider_name,omitempty"`
	SessionID            string   `json:"session_id,omitempty"`
	ResolvedUserID       string   `json:"resolved_user_id,omitempty"`
	ResolvedUserName     string   `json:"resolved_user_name,omitempty"`
	ResolvedUserEmail    string   `json:"resolved_user_email,omitempty"`
	SingleLeg            bool     `json:"single_leg,omitempty"`
	ChainUpstreams       []string `json:"chain_upstreams,omitempty"`
	CreatedAt            int64    `json:"created_at"`
}

// StorePendingAuthorization stores a pending authorization request.
func (s *RedisStorage) StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error {
	if state == "" {
		return fosite.ErrInvalidRequest.WithHint("state cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending authorization cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypePending, state)

	stored := storedPendingAuthorization{
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
		CreatedAt:            pending.CreatedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal pending authorization: %w", err)
	}

	return s.client.Set(ctx, key, data, DefaultPendingAuthorizationTTL).Err()
}

// LoadPendingAuthorization retrieves a pending authorization by internal state.
func (s *RedisStorage) LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error) {
	key := redisKey(s.keyPrefix, KeyTypePending, state)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
		}
		return nil, fmt.Errorf("failed to get pending authorization: %w", err)
	}

	var stored storedPendingAuthorization
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending authorization: %w", err)
	}

	createdAt := time.Unix(stored.CreatedAt, 0)

	// Check if expired (TTL should handle this, but double-check)
	if time.Since(createdAt) > DefaultPendingAuthorizationTTL {
		return nil, ErrExpired
	}

	return &PendingAuthorization{
		ClientID:             stored.ClientID,
		RedirectURI:          stored.RedirectURI,
		State:                stored.State,
		PKCEChallenge:        stored.PKCEChallenge,
		PKCEMethod:           stored.PKCEMethod,
		Scopes:               slices.Clone(stored.Scopes),
		InternalState:        stored.InternalState,
		UpstreamPKCEVerifier: stored.UpstreamPKCEVerifier,
		UpstreamNonce:        stored.UpstreamNonce,
		UpstreamProviderName: stored.UpstreamProviderName,
		SessionID:            stored.SessionID,
		ResolvedUserID:       stored.ResolvedUserID,
		ResolvedUserName:     stored.ResolvedUserName,
		ResolvedUserEmail:    stored.ResolvedUserEmail,
		SingleLeg:            stored.SingleLeg,
		ChainUpstreams:       slices.Clone(stored.ChainUpstreams),
		CreatedAt:            createdAt,
	}, nil
}

// DeletePendingAuthorization removes a pending authorization.
func (s *RedisStorage) DeletePendingAuthorization(ctx context.Context, state string) error {
	key := redisKey(s.keyPrefix, KeyTypePending, state)

	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete pending authorization: %w", err)
	}
	if result == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
	}

	return nil
}

// -----------------------
// User Storage
// -----------------------

// storedUser is a serializable wrapper for User.
type storedUser struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreateUser creates a new user account.
func (s *RedisStorage) CreateUser(ctx context.Context, user *User) error {
	if user == nil {
		return fosite.ErrInvalidRequest.WithHint("user cannot be nil")
	}
	if user.ID == "" {
		return fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}

	key := redisKey(s.keyPrefix, KeyTypeUser, user.ID)

	stored := storedUser{
		ID:        user.ID,
		CreatedAt: user.CreatedAt.Unix(),
		UpdatedAt: user.UpdatedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	// Use SetNX for atomic check-and-set to prevent race conditions.
	// Users don't expire (TTL=0).
	result, err := s.client.SetNX(ctx, key, data, 0).Result()
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	if !result {
		return fmt.Errorf("%w: user already exists", ErrAlreadyExists)
	}

	return nil
}

// GetUser retrieves a user by their internal ID.
func (s *RedisStorage) GetUser(ctx context.Context, id string) (*User, error) {
	key := redisKey(s.keyPrefix, KeyTypeUser, id)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: user not found", ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	var stored storedUser
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	return &User{
		ID:        stored.ID,
		CreatedAt: time.Unix(stored.CreatedAt, 0),
		UpdatedAt: time.Unix(stored.UpdatedAt, 0),
	}, nil
}

// DeleteUser removes a user account and all associated data.
func (s *RedisStorage) DeleteUser(ctx context.Context, id string) error {
	key := redisKey(s.keyPrefix, KeyTypeUser, id)

	// Check if user exists
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Delete the user
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	// Best-effort cascade deletion of associated data (see warnOnCleanupErr).
	// The user record is already deleted; orphaned keys will expire via TTL.
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, id)
	providerKeys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err == nil {
		for _, providerKey := range providerKeys {
			warnOnCleanupErr(s.client.Del(ctx, providerKey).Err(), "Del", providerKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, userProviderSetKey).Err(), "Del", userProviderSetKey)
	}

	// Delete associated upstream tokens
	userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, id)
	upstreamKeys, err := s.client.SMembers(ctx, userUpstreamSetKey).Result()
	if err == nil {
		for _, upstreamKey := range upstreamKeys {
			warnOnCleanupErr(s.client.Del(ctx, upstreamKey).Err(), "Del", upstreamKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, userUpstreamSetKey).Err(), "Del", userUpstreamSetKey)
	}

	return nil
}

// -----------------------
// Provider Identity Storage
// -----------------------

// storedProviderIdentity is a serializable wrapper for ProviderIdentity.
type storedProviderIdentity struct {
	UserID          string `json:"user_id"`
	ProviderID      string `json:"provider_id"`
	ProviderSubject string `json:"provider_subject"`
	LinkedAt        int64  `json:"linked_at"`
	LastUsedAt      int64  `json:"last_used_at"`
}

// CreateProviderIdentity links a provider identity to a user.
func (s *RedisStorage) CreateProviderIdentity(ctx context.Context, identity *ProviderIdentity) error {
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

	// Verify user exists
	userKey := redisKey(s.keyPrefix, KeyTypeUser, identity.UserID)
	exists, err := s.client.Exists(ctx, userKey).Result()
	if err != nil {
		return fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	key := redisProviderKey(s.keyPrefix, identity.ProviderID, identity.ProviderSubject)

	stored := storedProviderIdentity{
		UserID:          identity.UserID,
		ProviderID:      identity.ProviderID,
		ProviderSubject: identity.ProviderSubject,
		LinkedAt:        identity.LinkedAt.Unix(),
		LastUsedAt:      identity.LastUsedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal identity: %w", err)
	}

	// Use SetNX for atomic check-and-set to prevent race conditions.
	// Provider identities don't expire (TTL=0).
	result, err := s.client.SetNX(ctx, key, data, 0).Result()
	if err != nil {
		return fmt.Errorf("failed to store identity: %w", err)
	}
	if !result {
		return fmt.Errorf("%w: provider identity already linked", ErrAlreadyExists)
	}

	// Add to user's provider identity set for reverse lookup.
	// Note: This set may contain stale references if identities are deleted independently.
	// Stale entries are cleaned up lazily during GetUserProviderIdentities reads.
	// A future DeleteProviderIdentity method should also clean up this set.
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, identity.UserID)
	return s.client.SAdd(ctx, userProviderSetKey, key).Err()
}

// GetProviderIdentity retrieves a provider identity by provider ID and subject.
func (s *RedisStorage) GetProviderIdentity(ctx context.Context, providerID, providerSubject string) (*ProviderIdentity, error) {
	key := redisProviderKey(s.keyPrefix, providerID, providerSubject)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: provider identity not found", ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get identity: %w", err)
	}

	var stored storedProviderIdentity
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal identity: %w", err)
	}

	return &ProviderIdentity{
		UserID:          stored.UserID,
		ProviderID:      stored.ProviderID,
		ProviderSubject: stored.ProviderSubject,
		LinkedAt:        time.Unix(stored.LinkedAt, 0),
		LastUsedAt:      time.Unix(stored.LastUsedAt, 0),
	}, nil
}

// updateLastUsedScript is a Lua script that atomically updates the LastUsedAt field
// of a provider identity. This prevents race conditions when multiple requests
// try to update the same identity concurrently.
// Returns 1 on success, 0 if the key doesn't exist.
var updateLastUsedScript = redis.NewScript(`
local data = redis.call('GET', KEYS[1])
if not data then
	return 0
end
local identity = cjson.decode(data)
identity.last_used_at = tonumber(ARGV[1])
redis.call('SET', KEYS[1], cjson.encode(identity))
return 1
`)

// UpdateProviderIdentityLastUsed updates the LastUsedAt timestamp for a provider identity.
// Uses a Lua script to ensure atomicity and prevent race conditions.
func (s *RedisStorage) UpdateProviderIdentityLastUsed(
	ctx context.Context, providerID, providerSubject string, lastUsedAt time.Time,
) error {
	key := redisProviderKey(s.keyPrefix, providerID, providerSubject)

	result, err := updateLastUsedScript.Run(ctx, s.client, []string{key}, lastUsedAt.Unix()).Int()
	if err != nil {
		return fmt.Errorf("failed to update identity: %w", err)
	}

	// Script returns 0 if the key doesn't exist
	if result == 0 {
		return fmt.Errorf("%w: provider identity not found", ErrNotFound)
	}

	return nil
}

// GetUserProviderIdentities returns all provider identities linked to a user.
func (s *RedisStorage) GetUserProviderIdentities(ctx context.Context, userID string) ([]*ProviderIdentity, error) {
	// Verify user exists
	userKey := redisKey(s.keyPrefix, KeyTypeUser, userID)
	exists, err := s.client.Exists(ctx, userKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Get user's provider identity keys
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, userID)
	keys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get provider identity keys: %w", err)
	}

	var identities []*ProviderIdentity
	for _, key := range keys {
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				// Stale set entry — the identity key has expired or was deleted.
				// Skip silently; stale entries are cleaned up during write
				// operations (DeleteUser, CreateProviderIdentity) to avoid
				// mutation side effects in this read-only method.
				continue
			}
			return nil, fmt.Errorf("failed to get identity: %w", err)
		}

		var stored storedProviderIdentity
		if err := json.Unmarshal(data, &stored); err != nil {
			return nil, fmt.Errorf("failed to unmarshal identity: %w", err)
		}

		identities = append(identities, &ProviderIdentity{
			UserID:          stored.UserID,
			ProviderID:      stored.ProviderID,
			ProviderSubject: stored.ProviderSubject,
			LinkedAt:        time.Unix(stored.LinkedAt, 0),
			LastUsedAt:      time.Unix(stored.LastUsedAt, 0),
		})
	}

	return identities, nil
}

// -----------------------
// Serialization Helpers
// -----------------------

// marshalRequester serializes a fosite.Requester to JSON.
// The full session is serialized as a JSON blob to preserve all session data
// including JWT claims, headers, and upstream session IDs. This is critical
// for token refresh — fosite's JWT strategy requires the session to implement
// oauth2.JWTSessionContainer, which redisSession did not.
func marshalRequester(request fosite.Requester) ([]byte, error) {
	sessionData, err := json.Marshal(request.GetSession())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	stored := storedSession{
		ClientID:          request.GetClient().GetID(),
		RequestedAt:       request.GetRequestedAt(),
		RequestedScopes:   request.GetRequestedScopes(),
		GrantedScopes:     request.GetGrantedScopes(),
		RequestedAudience: request.GetRequestedAudience(),
		GrantedAudience:   request.GetGrantedAudience(),
		Form:              request.GetRequestForm(),
		RequestID:         request.GetID(),
		Session:           sessionData,
	}

	return json.Marshal(stored)
}

// unmarshalRequester deserializes a fosite.Requester from JSON.
// It requires storage access to look up the client and a session factory
// to create the correct session type for deserialization.
func unmarshalRequester(ctx context.Context, data []byte, s *RedisStorage) (fosite.Requester, error) {
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Look up the client
	client, err := s.GetClient(ctx, stored.ClientID)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for session: %w", err)
	}

	// Create a session prototype via factory, then deserialize the full session
	// blob into it. This preserves JWT claims, headers, and upstream session IDs.
	sess := defaultSessionFactory("", "", "")
	if len(stored.Session) > 0 {
		if err := json.Unmarshal(stored.Session, sess); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session data: %w", err)
		}
	}

	return &fosite.Request{
		ID:                stored.RequestID,
		RequestedAt:       stored.RequestedAt,
		Client:            client,
		RequestedScope:    stored.RequestedScopes,
		GrantedScope:      stored.GrantedScopes,
		RequestedAudience: stored.RequestedAudience,
		GrantedAudience:   stored.GrantedAudience,
		Form:              url.Values(stored.Form),
		Session:           sess,
	}, nil
}

// getTTLFromRequester extracts the TTL from a fosite.Requester session.
func getTTLFromRequester(request fosite.Requester, tokenType fosite.TokenType, defaultTTL time.Duration) time.Duration {
	if request == nil {
		return defaultTTL
	}

	sess := request.GetSession()
	if sess == nil {
		return defaultTTL
	}

	expTime := sess.GetExpiresAt(tokenType)
	if expTime.IsZero() {
		return defaultTTL
	}

	ttl := time.Until(expTime)
	if ttl <= 0 {
		return defaultTTL
	}

	return ttl
}

// Compile-time interface compliance checks
var (
	_ Storage                     = (*RedisStorage)(nil)
	_ PendingAuthorizationStorage = (*RedisStorage)(nil)
	_ ClientRegistry              = (*RedisStorage)(nil)
	_ UpstreamTokenStorage        = (*RedisStorage)(nil)
	_ UserStorage                 = (*RedisStorage)(nil)
	_ DCRCredentialStore          = (*RedisStorage)(nil)
)
