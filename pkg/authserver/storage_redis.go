package authserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Redis key prefixes for different data types.
const (
	keyPrefixClients       = "clients:"
	keyPrefixAuthCodes     = "auth_codes:"
	keyPrefixAccessTokens  = "access_tokens:"
	keyPrefixRefreshTokens = "refresh_tokens:"
	keyPrefixPKCE          = "pkce:"
	keyPrefixIDPTokens     = "idp_tokens:"
	keyPrefixPendingAuth   = "pending_auth:"
	keyPrefixInvalidated   = "invalidated:"
	keyPrefixJTI           = "jti:"
)

// RedisStorage implements the Storage interface using Redis for persistence.
// This implementation is suitable for production deployments where
// persistence across restarts and multi-replica deployments is required.
type RedisStorage struct {
	client    *redis.Client
	keyPrefix string
}

// RedisStorageOption configures a RedisStorage instance.
type RedisStorageOption func(*RedisStorage)

// WithKeyPrefix sets a custom key prefix for all Redis keys.
func WithKeyPrefix(prefix string) RedisStorageOption {
	return func(s *RedisStorage) {
		s.keyPrefix = prefix
	}
}

// NewRedisStorage creates a new RedisStorage instance.
// It tests the connection before returning.
func NewRedisStorage(redisURL, password string, opts ...RedisStorageOption) (*RedisStorage, error) {
	// Parse Redis URL
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	// Override password if provided
	if password != "" {
		redisOpts.Password = password
	}

	client := redis.NewClient(redisOpts)

	storage := &RedisStorage{
		client:    client,
		keyPrefix: DefaultRedisKeyPrefix,
	}

	for _, opt := range opts {
		opt(storage)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	logger.Info("Connected to Redis storage backend")

	return storage, nil
}

// Close closes the Redis connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

// key builds a full Redis key with prefix.
func (s *RedisStorage) key(prefix, id string) string {
	return s.keyPrefix + prefix + id
}

// RegisterClient adds or updates a client in the storage.
func (s *RedisStorage) RegisterClient(client fosite.Client) {
	ctx := context.Background()
	serialized := serializeClient(client)
	data, err := json.Marshal(serialized)
	if err != nil {
		logger.Errorw("failed to marshal client", "client_id", client.GetID(), "error", err)
		return
	}

	key := s.key(keyPrefixClients, client.GetID())
	if err := s.client.Set(ctx, key, data, 0).Err(); err != nil {
		logger.Errorw("failed to store client", "client_id", client.GetID(), "error", err)
	}
}

// -----------------------
// fosite.ClientManager
// -----------------------

// GetClient loads the client by its ID or returns an error if the client does not exist.
func (s *RedisStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	key := s.key(keyPrefixClients, id)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("client not found", "client_id", id)
			return nil, fosite.ErrNotFound.WithHint("Client not found")
		}
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	var serialized serializedClient
	if err := json.Unmarshal(data, &serialized); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client: %w", err)
	}

	return deserializeClient(&serialized), nil
}

// ClientAssertionJWTValid returns an error if the JTI is known or the DB check failed,
// and nil if the JTI is not known (meaning it can be used).
func (s *RedisStorage) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	key := s.key(keyPrefixJTI, jti)
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check JTI: %w", err)
	}

	if exists > 0 {
		return fosite.ErrJTIKnown
	}
	return nil
}

// SetClientAssertionJWT marks a JTI as known for the given expiry time.
func (s *RedisStorage) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	key := s.key(keyPrefixJTI, jti)
	ttl := time.Until(exp)
	if ttl <= 0 {
		// Already expired, no need to store
		return nil
	}

	if err := s.client.Set(ctx, key, "1", ttl).Err(); err != nil {
		return fmt.Errorf("failed to store JTI: %w", err)
	}
	return nil
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

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	key := s.key(keyPrefixAuthCodes, code)
	ttl := time.Until(getExpirationFromRequester(request, fosite.AuthorizeCode, DefaultAuthCodeTTL))
	if ttl <= 0 {
		ttl = DefaultAuthCodeTTL
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store auth code: %w", err)
	}
	return nil
}

// GetAuthorizeCodeSession retrieves the authorization request for a given code.
func (s *RedisStorage) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	key := s.key(keyPrefixAuthCodes, code)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("authorization code not found", "code", code)
			return nil, fosite.ErrNotFound.WithHint("Authorization code not found")
		}
		return nil, fmt.Errorf("failed to get auth code: %w", err)
	}

	request, err := unmarshalRequester(data, s.getClientLookup(ctx))
	if err != nil {
		return nil, err
	}

	// Check if the code has been invalidated
	invalidatedKey := s.key(keyPrefixInvalidated, code)
	exists, err := s.client.Exists(ctx, invalidatedKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check invalidation: %w", err)
	}

	if exists > 0 {
		// Must return the request along with the error as per fosite documentation
		return request, fosite.ErrInvalidatedAuthorizeCode
	}

	return request, nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
func (s *RedisStorage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	key := s.key(keyPrefixAuthCodes, code)
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check auth code: %w", err)
	}

	if exists == 0 {
		logger.Debugw("authorization code not found for invalidation", "code", code)
		return fosite.ErrNotFound.WithHint("Authorization code not found")
	}

	invalidatedKey := s.key(keyPrefixInvalidated, code)
	if err := s.client.Set(ctx, invalidatedKey, "1", DefaultInvalidatedCodeTTL).Err(); err != nil {
		return fmt.Errorf("failed to store invalidation: %w", err)
	}
	return nil
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

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	key := s.key(keyPrefixAccessTokens, signature)
	ttl := time.Until(getExpirationFromRequester(request, fosite.AccessToken, DefaultAccessTokenTTL))
	if ttl <= 0 {
		ttl = DefaultAccessTokenTTL
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store access token: %w", err)
	}

	// Store request ID -> signature mapping for revocation
	requestIDKey := s.key(keyPrefixAccessTokens, "reqid:"+request.GetID()+":"+signature)
	if err := s.client.Set(ctx, requestIDKey, signature, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store access token index: %w", err)
	}

	return nil
}

// GetAccessTokenSession retrieves the access token session by its signature.
func (s *RedisStorage) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := s.key(keyPrefixAccessTokens, signature)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("access token not found", "signature", signature)
			return nil, fosite.ErrNotFound.WithHint("Access token not found")
		}
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	return unmarshalRequester(data, s.getClientLookup(ctx))
}

// DeleteAccessTokenSession removes the access token session.
func (s *RedisStorage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	key := s.key(keyPrefixAccessTokens, signature)
	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete access token: %w", err)
	}

	if result == 0 {
		return fosite.ErrNotFound.WithHint("Access token not found")
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

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	key := s.key(keyPrefixRefreshTokens, signature)
	ttl := time.Until(getExpirationFromRequester(request, fosite.RefreshToken, DefaultRefreshTokenTTL))
	if ttl <= 0 {
		ttl = DefaultRefreshTokenTTL
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store refresh token: %w", err)
	}

	// Store request ID -> signature mapping for revocation
	requestIDKey := s.key(keyPrefixRefreshTokens, "reqid:"+request.GetID()+":"+signature)
	if err := s.client.Set(ctx, requestIDKey, signature, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store refresh token index: %w", err)
	}

	return nil
}

// GetRefreshTokenSession retrieves the refresh token session by its signature.
func (s *RedisStorage) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := s.key(keyPrefixRefreshTokens, signature)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("refresh token not found", "signature", signature)
			return nil, fosite.ErrNotFound.WithHint("Refresh token not found")
		}
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	return unmarshalRequester(data, s.getClientLookup(ctx))
}

// DeleteRefreshTokenSession removes the refresh token session.
func (s *RedisStorage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	key := s.key(keyPrefixRefreshTokens, signature)
	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}

	if result == 0 {
		return fosite.ErrNotFound.WithHint("Refresh token not found")
	}
	return nil
}

// RotateRefreshToken invalidates a refresh token and all its related token data.
func (s *RedisStorage) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	// Delete the specific refresh token
	refreshKey := s.key(keyPrefixRefreshTokens, refreshTokenSignature)
	_ = s.client.Del(ctx, refreshKey).Err() // Ignore error

	// Find and delete access tokens by request ID using pattern matching
	pattern := s.key(keyPrefixAccessTokens, "reqid:"+requestID+":*")
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		indexKey := iter.Val()
		// Get the actual signature from the index
		sig, err := s.client.Get(ctx, indexKey).Result()
		if err == nil {
			// Delete the actual access token
			tokenKey := s.key(keyPrefixAccessTokens, sig)
			_ = s.client.Del(ctx, tokenKey).Err()
		}
		// Delete the index key
		_ = s.client.Del(ctx, indexKey).Err()
	}

	return nil
}

// -----------------------
// oauth2.TokenRevocationStorage
// -----------------------

// RevokeAccessToken marks an access token as revoked.
func (s *RedisStorage) RevokeAccessToken(ctx context.Context, requestID string) error {
	// Find and delete access tokens by request ID
	pattern := s.key(keyPrefixAccessTokens, "reqid:"+requestID+":*")
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		indexKey := iter.Val()
		sig, err := s.client.Get(ctx, indexKey).Result()
		if err == nil {
			tokenKey := s.key(keyPrefixAccessTokens, sig)
			_ = s.client.Del(ctx, tokenKey).Err()
		}
		_ = s.client.Del(ctx, indexKey).Err()
	}
	return nil
}

// RevokeRefreshToken marks a refresh token as revoked.
func (s *RedisStorage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	// Find and delete refresh tokens by request ID
	pattern := s.key(keyPrefixRefreshTokens, "reqid:"+requestID+":*")
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		indexKey := iter.Val()
		sig, err := s.client.Get(ctx, indexKey).Result()
		if err == nil {
			tokenKey := s.key(keyPrefixRefreshTokens, sig)
			_ = s.client.Del(ctx, tokenKey).Err()
		}
		_ = s.client.Del(ctx, indexKey).Err()
	}
	return nil
}

// RevokeRefreshTokenMaybeGracePeriod marks a refresh token as revoked.
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

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	key := s.key(keyPrefixPKCE, signature)
	ttl := time.Until(getExpirationFromRequester(request, fosite.AuthorizeCode, DefaultPKCETTL))
	if ttl <= 0 {
		ttl = DefaultPKCETTL
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store PKCE request: %w", err)
	}
	return nil
}

// GetPKCERequestSession retrieves the PKCE request session by its signature.
func (s *RedisStorage) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := s.key(keyPrefixPKCE, signature)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("PKCE request not found", "signature", signature)
			return nil, fosite.ErrNotFound.WithHint("PKCE request not found")
		}
		return nil, fmt.Errorf("failed to get PKCE request: %w", err)
	}

	return unmarshalRequester(data, s.getClientLookup(ctx))
}

// DeletePKCERequestSession removes the PKCE request session.
func (s *RedisStorage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	key := s.key(keyPrefixPKCE, signature)
	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete PKCE request: %w", err)
	}

	if result == 0 {
		return fosite.ErrNotFound.WithHint("PKCE request not found")
	}
	return nil
}

// -----------------------
// IDP Token Storage
// -----------------------

// StoreIDPTokens stores the upstream IDP tokens for a session.
func (s *RedisStorage) StoreIDPTokens(ctx context.Context, sessionID string, tokens *IDPTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}

	data, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("failed to marshal IDP tokens: %w", err)
	}

	key := s.key(keyPrefixIDPTokens, sessionID)
	var ttl time.Duration
	if tokens != nil && !tokens.ExpiresAt.IsZero() {
		ttl = time.Until(tokens.ExpiresAt)
	} else {
		ttl = DefaultAccessTokenTTL
	}

	if ttl <= 0 {
		ttl = DefaultAccessTokenTTL
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to store IDP tokens: %w", err)
	}
	return nil
}

// GetIDPTokens retrieves the upstream IDP tokens for a session.
func (s *RedisStorage) GetIDPTokens(ctx context.Context, sessionID string) (*IDPTokens, error) {
	key := s.key(keyPrefixIDPTokens, sessionID)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("IDP tokens not found", "session_id", sessionID)
			return nil, fosite.ErrNotFound.WithHint("IDP tokens not found")
		}
		return nil, fmt.Errorf("failed to get IDP tokens: %w", err)
	}

	var tokens IDPTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("failed to unmarshal IDP tokens: %w", err)
	}

	return &tokens, nil
}

// DeleteIDPTokens removes the upstream IDP tokens for a session.
func (s *RedisStorage) DeleteIDPTokens(ctx context.Context, sessionID string) error {
	key := s.key(keyPrefixIDPTokens, sessionID)
	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete IDP tokens: %w", err)
	}

	if result == 0 {
		return fosite.ErrNotFound.WithHint("IDP tokens not found")
	}
	return nil
}

// -----------------------
// Pending Authorization Storage
// -----------------------

// StorePendingAuthorization stores a pending authorization request.
func (s *RedisStorage) StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error {
	if state == "" {
		return fosite.ErrInvalidRequest.WithHint("state cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending authorization cannot be nil")
	}

	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("failed to marshal pending authorization: %w", err)
	}

	key := s.key(keyPrefixPendingAuth, state)
	if err := s.client.Set(ctx, key, data, DefaultPendingAuthorizationTTL).Err(); err != nil {
		return fmt.Errorf("failed to store pending authorization: %w", err)
	}
	return nil
}

// LoadPendingAuthorization retrieves a pending authorization by internal state.
func (s *RedisStorage) LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error) {
	key := s.key(keyPrefixPendingAuth, state)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			logger.Debugw("pending authorization not found", "state", state)
			return nil, fosite.ErrNotFound.WithHint("Pending authorization not found")
		}
		return nil, fmt.Errorf("failed to get pending authorization: %w", err)
	}

	var pending PendingAuthorization
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending authorization: %w", err)
	}

	return &pending, nil
}

// DeletePendingAuthorization removes a pending authorization.
func (s *RedisStorage) DeletePendingAuthorization(ctx context.Context, state string) error {
	key := s.key(keyPrefixPendingAuth, state)
	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete pending authorization: %w", err)
	}

	if result == 0 {
		return fosite.ErrNotFound.WithHint("Pending authorization not found")
	}
	return nil
}

// -----------------------
// Helper functions
// -----------------------

// getClientLookup returns a function that looks up clients by ID.
func (s *RedisStorage) getClientLookup(ctx context.Context) func(string) fosite.Client {
	return func(id string) fosite.Client {
		client, err := s.GetClient(ctx, id)
		if err != nil {
			return nil
		}
		return client
	}
}

// Compile-time interface compliance check
var _ Storage = (*RedisStorage)(nil)
