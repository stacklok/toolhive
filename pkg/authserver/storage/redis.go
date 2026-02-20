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
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

// Default timeouts for Redis operations.
const (
	DefaultDialTimeout  = 5 * time.Second
	DefaultReadTimeout  = 3 * time.Second
	DefaultWriteTimeout = 3 * time.Second
)

// nullMarker is used to store nil upstream tokens in Redis.
const nullMarker = "null"

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

// RedisConfig holds Redis connection configuration for runtime use.
type RedisConfig struct {
	// SentinelConfig is required - Sentinel-only deployment.
	SentinelConfig *SentinelConfig

	// ACLUserConfig is required - ACL user authentication only.
	ACLUserConfig *ACLUserConfig

	// KeyPrefix for multi-tenancy: "thv:auth:{ns}:{name}:".
	KeyPrefix string

	// Timeouts (defaults: Dial=5s, Read=3s, Write=3s).
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// SentinelConfig contains Redis Sentinel configuration.
type SentinelConfig struct {
	MasterName    string
	SentinelAddrs []string
	DB            int
}

// ACLUserConfig contains Redis ACL user authentication configuration.
type ACLUserConfig struct {
	Username string
	Password string //nolint:gosec // G117: field legitimately holds sensitive data
}

// RedisStorage implements the Storage interface with Redis Sentinel backend.
// It provides distributed storage for OAuth2 tokens, authorization codes,
// user data, and pending authorizations, enabling horizontal scaling.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
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

// NewRedisStorage creates Redis-backed storage with Sentinel failover support.
// Returns error if configuration validation fails or connection cannot be established.
func NewRedisStorage(ctx context.Context, cfg RedisConfig) (*RedisStorage, error) {
	if err := validateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid redis configuration: %w", err)
	}

	// Apply defaults
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = DefaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}

	client := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    cfg.SentinelConfig.MasterName,
		SentinelAddrs: cfg.SentinelConfig.SentinelAddrs,
		DB:            cfg.SentinelConfig.DB,
		Username:      cfg.ACLUserConfig.Username,
		Password:      cfg.ACLUserConfig.Password,
		DialTimeout:   cfg.DialTimeout,
		ReadTimeout:   cfg.ReadTimeout,
		WriteTimeout:  cfg.WriteTimeout,
	})

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		// Close the client to prevent resource leak
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisStorage{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
	}, nil
}

// NewRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// This is useful for testing with miniredis.
func NewRedisStorageWithClient(client redis.UniversalClient, keyPrefix string) *RedisStorage {
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// defaultSessionFactory creates session prototypes for deserialization.
func defaultSessionFactory(subject, idpSessionID, clientID string) fosite.Session {
	return session.New(subject, idpSessionID, clientID)
}

func validateConfig(cfg *RedisConfig) error {
	if cfg.SentinelConfig == nil {
		return errors.New("sentinel configuration is required")
	}
	if cfg.SentinelConfig.MasterName == "" {
		return errors.New("sentinel master name is required")
	}
	if len(cfg.SentinelConfig.SentinelAddrs) == 0 {
		return errors.New("at least one sentinel address is required")
	}
	if cfg.ACLUserConfig == nil {
		return errors.New("ACL user configuration is required")
	}
	if cfg.ACLUserConfig.Username == "" {
		return errors.New("ACL username is required")
	}
	if cfg.ACLUserConfig.Password == "" {
		return errors.New("ACL password is required")
	}
	if cfg.KeyPrefix == "" {
		return errors.New("key prefix is required")
	}
	return nil
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

	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("failed to marshal client: %w", err)
	}

	// Public clients (from DCR) expire to prevent unbounded growth.
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
type storedUpstreamTokens struct {
	ProviderID      string `json:"provider_id"`
	AccessToken     string `json:"access_token"`  //nolint:gosec // G117: field legitimately holds sensitive data
	RefreshToken    string `json:"refresh_token"` //nolint:gosec // G117: field legitimately holds sensitive data
	IDToken         string `json:"id_token"`
	ExpiresAt       int64  `json:"expires_at"`
	UserID          string `json:"user_id"`
	UpstreamSubject string `json:"upstream_subject"`
	ClientID        string `json:"client_id"`
}

// storeUpstreamTokensScript atomically reads the existing UserID, writes new token
// data, and updates user reverse-index sets. This prevents a race condition where
// concurrent writes for the same session could leave orphaned entries in user sets.
//
// KEYS[1] = token key
// ARGV[1] = new token data (JSON or "null" marker)
// ARGV[2] = TTL in milliseconds
// ARGV[3] = new UserID ("" if no user)
// ARGV[4] = user upstream set key prefix (e.g. "thv:auth:{ns:name}:user:upstream:")
var storeUpstreamTokensScript = redis.NewScript(`
local oldUserID = ""
local existing = redis.call('GET', KEYS[1])
if existing and existing ~= "null" then
    local ok, decoded = pcall(cjson.decode, existing)
    if ok and type(decoded) == "table" and decoded.user_id and decoded.user_id ~= "" then
        oldUserID = decoded.user_id
    end
end

local ttlMs = tonumber(ARGV[2])
if ttlMs > 0 then
    redis.call('SET', KEYS[1], ARGV[1], 'PX', ttlMs)
else
    redis.call('SET', KEYS[1], ARGV[1])
end

local newUserID = ARGV[3]
local setPrefix = ARGV[4]

if oldUserID ~= "" and oldUserID ~= newUserID then
    redis.call('SREM', setPrefix .. oldUserID, KEYS[1])
end

if newUserID ~= "" then
    redis.call('SADD', setPrefix .. newUserID, KEYS[1])
end

return 1
`)

// marshalUpstreamTokensWithTTL marshals tokens and calculates TTL.
func marshalUpstreamTokensWithTTL(tokens *UpstreamTokens) ([]byte, time.Duration, error) {
	if tokens == nil {
		return []byte(nullMarker), DefaultAccessTokenTTL, nil
	}

	// Store 0 for zero time to use as a sentinel meaning "no expiry".
	// time.Time{}.Unix() returns -62135596800 which is not a useful sentinel.
	var expiresAtUnix int64
	if !tokens.ExpiresAt.IsZero() {
		expiresAtUnix = tokens.ExpiresAt.Unix()
	}

	stored := storedUpstreamTokens{
		ProviderID:      tokens.ProviderID,
		AccessToken:     tokens.AccessToken,
		RefreshToken:    tokens.RefreshToken,
		IDToken:         tokens.IDToken,
		ExpiresAt:       expiresAtUnix,
		UserID:          tokens.UserID,
		UpstreamSubject: tokens.UpstreamSubject,
		ClientID:        tokens.ClientID,
	}

	data, err := json.Marshal(stored)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal upstream tokens: %w", err)
	}

	ttl := DefaultAccessTokenTTL
	if !tokens.ExpiresAt.IsZero() {
		ttl = time.Until(tokens.ExpiresAt)
		if ttl < 0 {
			ttl = DefaultAccessTokenTTL
		}
	}

	return data, ttl, nil
}

// StoreUpstreamTokens stores the upstream IDP tokens for a session.
// Uses a Lua script to atomically read the old UserID, write new token data,
// and update user reverse-index sets, preventing race conditions on concurrent writes.
func (s *RedisStorage) StoreUpstreamTokens(ctx context.Context, sessionID string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}

	key := redisKey(s.keyPrefix, KeyTypeUpstream, sessionID)

	data, ttl, err := marshalUpstreamTokensWithTTL(tokens)
	if err != nil {
		return err
	}

	newUserID := ""
	if tokens != nil {
		newUserID = tokens.UserID
	}

	userSetKeyPrefix := s.keyPrefix + KeyTypeUserUpstream + ":"

	_, err = storeUpstreamTokensScript.Run(ctx, s.client,
		[]string{key},
		string(data),
		ttl.Milliseconds(),
		newUserID,
		userSetKeyPrefix,
	).Result()
	if err != nil {
		return fmt.Errorf("failed to store upstream tokens: %w", err)
	}

	return nil
}

// GetUpstreamTokens retrieves the upstream IDP tokens for a session.
// Returns a new UpstreamTokens struct deserialized from Redis, which acts as
// a defensive copy - callers cannot modify the stored data by mutating the return value.
func (s *RedisStorage) GetUpstreamTokens(ctx context.Context, sessionID string) (*UpstreamTokens, error) {
	key := redisKey(s.keyPrefix, KeyTypeUpstream, sessionID)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// Handle null marker
	if string(data) == nullMarker {
		return nil, nil
	}

	var stored storedUpstreamTokens
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal upstream tokens: %w", err)
	}

	// Convert stored ExpiresAt back to time.Time.
	// stored.ExpiresAt == 0 is a sentinel meaning "no expiry" — the write path
	// stores 0 for zero time. Skip the expiry check in this case since Redis TTL
	// handles the actual expiration.
	var expiresAt time.Time
	if stored.ExpiresAt != 0 {
		expiresAt = time.Unix(stored.ExpiresAt, 0)
		if time.Now().After(expiresAt) {
			return nil, ErrExpired
		}
	}

	return &UpstreamTokens{
		ProviderID:      stored.ProviderID,
		AccessToken:     stored.AccessToken,
		RefreshToken:    stored.RefreshToken,
		IDToken:         stored.IDToken,
		ExpiresAt:       expiresAt,
		UserID:          stored.UserID,
		UpstreamSubject: stored.UpstreamSubject,
		ClientID:        stored.ClientID,
	}, nil
}

// DeleteUpstreamTokens removes the upstream IDP tokens for a session.
func (s *RedisStorage) DeleteUpstreamTokens(ctx context.Context, sessionID string) error {
	key := redisKey(s.keyPrefix, KeyTypeUpstream, sessionID)

	// Get token first to find UserID for cleanup of user:upstream set
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// Delete the token
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete upstream tokens: %w", err)
	}

	// Best-effort secondary index cleanup (see warnOnCleanupErr).
	if string(data) != nullMarker {
		var stored storedUpstreamTokens
		if err := json.Unmarshal(data, &stored); err == nil && stored.UserID != "" {
			userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, stored.UserID)
			warnOnCleanupErr(s.client.SRem(ctx, userUpstreamSetKey, key).Err(), "SRem", userUpstreamSetKey)
		}
	}

	return nil
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
		CreatedAt:            pending.CreatedAt.Unix(),
	}

	data, err := json.Marshal(stored)
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

	data, err := json.Marshal(stored)
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

	data, err := json.Marshal(stored)
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
)
