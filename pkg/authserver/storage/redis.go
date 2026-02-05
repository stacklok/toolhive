// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
)

// Default timeouts for Redis operations.
const (
	DefaultDialTimeout  = 5 * time.Second
	DefaultReadTimeout  = 3 * time.Second
	DefaultWriteTimeout = 3 * time.Second
)

// nullMarker is used to store nil upstream tokens in Redis.
const nullMarker = "null"

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
	Password string
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
	ClientID           string              `json:"client_id"`
	RequestedAt        time.Time           `json:"requested_at"`
	RequestedScopes    []string            `json:"requested_scopes"`
	GrantedScopes      []string            `json:"granted_scopes"`
	RequestedAudience  []string            `json:"requested_audience"`
	GrantedAudience    []string            `json:"granted_audience"`
	Form               map[string][]string `json:"form"`
	RequestID          string              `json:"request_id"`
	Subject            string              `json:"subject"`
	ExpiresAt          map[string]int64    `json:"expires_at"`
	AccessTokenExpiry  int64               `json:"access_token_expiry,omitempty"`
	RefreshTokenExpiry int64               `json:"refresh_token_expiry,omitempty"`
	AuthCodeExpiry     int64               `json:"auth_code_expiry,omitempty"`
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
	if cfg.KeyPrefix == "" {
		return errors.New("key prefix is required")
	}
	return nil
}

// Close closes the Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

// Ping checks Redis connectivity (health check).
func (s *RedisStorage) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// -----------------------
// ClientRegistry
// -----------------------

// storedClient is a serializable wrapper for OAuth clients.
type storedClient struct {
	ID            string   `json:"id"`
	Secret        []byte   `json:"secret,omitempty"`
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
func (s *RedisStorage) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	key := redisKey(s.keyPrefix, KeyTypeJWT, jti)

	ttl := time.Until(exp)
	if ttl <= 0 {
		// Already expired, don't store
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
func (s *RedisStorage) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	// Check if invalidated first
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	invalidated, err := s.client.Exists(ctx, invalidatedKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check invalidation status: %w", err)
	}

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

	if invalidated > 0 {
		// Must return the request along with the error as per fosite documentation
		return request, fosite.ErrInvalidatedAuthorizeCode
	}

	return request, nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
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

	// Mark as invalidated
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	return s.client.Set(ctx, invalidatedKey, "1", DefaultInvalidatedCodeTTL).Err()
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

	// Store the token
	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}

	// Create secondary index for request ID -> signature mapping.
	// Set TTL on the index to prevent memory growth from orphaned indexes.
	// If index operations fail, delete the token to prevent orphaned tokens.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, request.GetID())
	if err := s.client.SAdd(ctx, reqIDKey, signature).Err(); err != nil {
		// Compensating transaction: delete the token we just stored
		_ = s.client.Del(ctx, key).Err()
		return err
	}
	// Use the token TTL for the index - when tokens expire, indexes can be cleaned up
	if err := s.client.Expire(ctx, reqIDKey, ttl).Err(); err != nil {
		// Compensating transaction: delete the token and remove from index
		_ = s.client.Del(ctx, key).Err()
		_ = s.client.SRem(ctx, reqIDKey, signature).Err()
		return err
	}

	return nil
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

	// Clean up the secondary index
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, stored.RequestID)
		// Ignore error - cleanup is best effort
		_ = s.client.SRem(ctx, reqIDKey, signature).Err()
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

	// Store the token
	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}

	// Create secondary index for request ID -> signature mapping.
	// Set TTL on the index to prevent memory growth from orphaned indexes.
	// If index operations fail, delete the token to prevent orphaned tokens.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, request.GetID())
	if err := s.client.SAdd(ctx, reqIDKey, signature).Err(); err != nil {
		// Compensating transaction: delete the token we just stored
		_ = s.client.Del(ctx, key).Err()
		return err
	}
	// Use the token TTL for the index - when tokens expire, indexes can be cleaned up
	if err := s.client.Expire(ctx, reqIDKey, ttl).Err(); err != nil {
		// Compensating transaction: delete the token and remove from index
		_ = s.client.Del(ctx, key).Err()
		_ = s.client.SRem(ctx, reqIDKey, signature).Err()
		return err
	}

	return nil
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

	// Clean up the secondary index
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, stored.RequestID)
		// Ignore error - cleanup is best effort
		_ = s.client.SRem(ctx, reqIDKey, signature).Err()
	}

	return nil
}

// RotateRefreshToken invalidates a refresh token and all its related token data.
func (s *RedisStorage) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	// Delete the specific refresh token
	refreshKey := redisKey(s.keyPrefix, KeyTypeRefresh, refreshTokenSignature)
	_ = s.client.Del(ctx, refreshKey).Err()

	// Remove from the request ID index
	reqIDRefreshKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, requestID)
	_ = s.client.SRem(ctx, reqIDRefreshKey, refreshTokenSignature).Err()

	// Delete all access tokens associated with this request ID
	reqIDAccessKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDAccessKey).Result()
	if err == nil {
		for _, sig := range signatures {
			accessKey := redisKey(s.keyPrefix, KeyTypeAccess, sig)
			_ = s.client.Del(ctx, accessKey).Err()
		}
		_ = s.client.Del(ctx, reqIDAccessKey).Err()
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
		_ = s.client.Del(ctx, accessKey).Err()
	}

	// Clean up the index
	_ = s.client.Del(ctx, reqIDKey).Err()

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
		_ = s.client.Del(ctx, refreshKey).Err()
	}

	// Clean up the index
	_ = s.client.Del(ctx, reqIDKey).Err()

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
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	IDToken         string `json:"id_token"`
	ExpiresAt       int64  `json:"expires_at"`
	UserID          string `json:"user_id"`
	UpstreamSubject string `json:"upstream_subject"`
	ClientID        string `json:"client_id"`
}

// getExistingUpstreamUserID retrieves the UserID from existing upstream tokens, if any.
func (s *RedisStorage) getExistingUpstreamUserID(ctx context.Context, key string) string {
	existingData, err := s.client.Get(ctx, key).Bytes()
	if err != nil || string(existingData) == nullMarker {
		return ""
	}
	var existingStored storedUpstreamTokens
	if json.Unmarshal(existingData, &existingStored) != nil {
		return ""
	}
	return existingStored.UserID
}

// marshalUpstreamTokensWithTTL marshals tokens and calculates TTL.
func marshalUpstreamTokensWithTTL(tokens *UpstreamTokens) ([]byte, time.Duration, error) {
	if tokens == nil {
		return []byte(nullMarker), DefaultAccessTokenTTL, nil
	}

	stored := storedUpstreamTokens{
		ProviderID:      tokens.ProviderID,
		AccessToken:     tokens.AccessToken,
		RefreshToken:    tokens.RefreshToken,
		IDToken:         tokens.IDToken,
		ExpiresAt:       tokens.ExpiresAt.Unix(),
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
func (s *RedisStorage) StoreUpstreamTokens(ctx context.Context, sessionID string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}

	key := redisKey(s.keyPrefix, KeyTypeUpstream, sessionID)

	// Check for existing token to handle UserID changes on overwrite.
	oldUserID := s.getExistingUpstreamUserID(ctx, key)

	// Marshal tokens and calculate TTL
	data, ttl, err := marshalUpstreamTokensWithTTL(tokens)
	if err != nil {
		return err
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}

	// Handle user set operations for reverse lookup
	return s.updateUpstreamUserSets(ctx, key, oldUserID, tokens)
}

// updateUpstreamUserSets handles adding/removing upstream token references in user sets.
func (s *RedisStorage) updateUpstreamUserSets(ctx context.Context, key, oldUserID string, tokens *UpstreamTokens) error {
	newUserID := ""
	if tokens != nil {
		newUserID = tokens.UserID
	}

	// Clean up old user's set if UserID changed
	if oldUserID != "" && oldUserID != newUserID {
		oldUserUpstreamSetKey := redisSetKey(s.keyPrefix, "user:upstream", oldUserID)
		_ = s.client.SRem(ctx, oldUserUpstreamSetKey, key).Err()
	}

	// Add to user's upstream token set for reverse lookup during DeleteUser.
	// Only add if tokens have a UserID - allows for cleanup when user is deleted.
	if newUserID != "" {
		userUpstreamSetKey := redisSetKey(s.keyPrefix, "user:upstream", newUserID)
		if err := s.client.SAdd(ctx, userUpstreamSetKey, key).Err(); err != nil {
			return err
		}
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

	expiresAt := time.Unix(stored.ExpiresAt, 0)

	// Check if expired
	if time.Now().After(expiresAt) {
		return nil, ErrExpired
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

	// Clean up the user:upstream set (best effort - ignore errors)
	// Handle null marker case
	if string(data) != nullMarker {
		var stored storedUpstreamTokens
		if err := json.Unmarshal(data, &stored); err == nil && stored.UserID != "" {
			userUpstreamSetKey := redisSetKey(s.keyPrefix, "user:upstream", stored.UserID)
			_ = s.client.SRem(ctx, userUpstreamSetKey, key).Err()
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

	// Delete associated provider identities and upstream tokens
	// This requires scanning for keys with the user ID, which is done via pattern matching
	// For efficiency, we store a set of user's provider identity keys
	userProviderSetKey := redisSetKey(s.keyPrefix, "user:providers", id)
	providerKeys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err == nil {
		for _, providerKey := range providerKeys {
			_ = s.client.Del(ctx, providerKey).Err()
		}
		_ = s.client.Del(ctx, userProviderSetKey).Err()
	}

	// Delete associated upstream tokens
	userUpstreamSetKey := redisSetKey(s.keyPrefix, "user:upstream", id)
	upstreamKeys, err := s.client.SMembers(ctx, userUpstreamSetKey).Result()
	if err == nil {
		for _, upstreamKey := range upstreamKeys {
			_ = s.client.Del(ctx, upstreamKey).Err()
		}
		_ = s.client.Del(ctx, userUpstreamSetKey).Err()
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
	userProviderSetKey := redisSetKey(s.keyPrefix, "user:providers", identity.UserID)
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
	userProviderSetKey := redisSetKey(s.keyPrefix, "user:providers", userID)
	keys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get provider identity keys: %w", err)
	}

	var identities []*ProviderIdentity
	for _, key := range keys {
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				// Identity was deleted, clean up the set
				_ = s.client.SRem(ctx, userProviderSetKey, key).Err()
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
func marshalRequester(request fosite.Requester) ([]byte, error) {
	// Preserve all form values (url.Values is map[string][]string)
	formMap := make(map[string][]string)
	for key, values := range request.GetRequestForm() {
		formMap[key] = values
	}

	// Extract expiration times from session
	expiresAt := make(map[string]int64)
	session := request.GetSession()
	if session != nil {
		for _, tokenType := range []fosite.TokenType{fosite.AccessToken, fosite.RefreshToken, fosite.AuthorizeCode} {
			if exp := session.GetExpiresAt(tokenType); !exp.IsZero() {
				expiresAt[string(tokenType)] = exp.Unix()
			}
		}
	}

	subject := ""
	if session != nil {
		subject = session.GetSubject()
	}

	stored := storedSession{
		ClientID:          request.GetClient().GetID(),
		RequestedAt:       request.GetRequestedAt(),
		RequestedScopes:   request.GetRequestedScopes(),
		GrantedScopes:     request.GetGrantedScopes(),
		RequestedAudience: request.GetRequestedAudience(),
		GrantedAudience:   request.GetGrantedAudience(),
		Form:              formMap,
		RequestID:         request.GetID(),
		Subject:           subject,
		ExpiresAt:         expiresAt,
	}

	return json.Marshal(stored)
}

// unmarshalRequester deserializes a fosite.Requester from JSON.
// It requires storage access to look up the client.
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

	// Convert form values back (stored.Form is map[string][]string, same as url.Values)
	form := url.Values(stored.Form)

	// Create session with expiration times
	session := &redisSession{
		subject:   stored.Subject,
		expiresAt: make(map[fosite.TokenType]time.Time),
	}
	for tokenTypeStr, unix := range stored.ExpiresAt {
		session.expiresAt[fosite.TokenType(tokenTypeStr)] = time.Unix(unix, 0)
	}

	return &redisRequester{
		id:                stored.RequestID,
		requestedAt:       stored.RequestedAt,
		client:            client,
		requestedScopes:   stored.RequestedScopes,
		grantedScopes:     stored.GrantedScopes,
		requestedAudience: stored.RequestedAudience,
		grantedAudience:   stored.GrantedAudience,
		form:              form,
		session:           session,
	}, nil
}

// getTTLFromRequester extracts the TTL from a fosite.Requester session.
func getTTLFromRequester(request fosite.Requester, tokenType fosite.TokenType, defaultTTL time.Duration) time.Duration {
	if request == nil {
		return defaultTTL
	}

	session := request.GetSession()
	if session == nil {
		return defaultTTL
	}

	expTime := session.GetExpiresAt(tokenType)
	if expTime.IsZero() {
		return defaultTTL
	}

	ttl := time.Until(expTime)
	if ttl <= 0 {
		return defaultTTL
	}

	return ttl
}

// -----------------------
// Redis Session/Requester Types
// -----------------------

// redisSession implements fosite.Session for deserialization.
type redisSession struct {
	subject   string
	expiresAt map[fosite.TokenType]time.Time
}

func (s *redisSession) SetExpiresAt(key fosite.TokenType, exp time.Time) { s.expiresAt[key] = exp }
func (s *redisSession) GetExpiresAt(key fosite.TokenType) time.Time      { return s.expiresAt[key] }
func (*redisSession) GetUsername() string                                { return "" }
func (s *redisSession) GetSubject() string                               { return s.subject }
func (s *redisSession) Clone() fosite.Session {
	clone := &redisSession{subject: s.subject, expiresAt: make(map[fosite.TokenType]time.Time)}
	for k, v := range s.expiresAt {
		clone.expiresAt[k] = v
	}
	return clone
}

// redisRequester implements fosite.Requester for deserialization.
type redisRequester struct {
	id                string
	requestedAt       time.Time
	client            fosite.Client
	requestedScopes   fosite.Arguments
	requestedAudience fosite.Arguments
	grantedScopes     fosite.Arguments
	grantedAudience   fosite.Arguments
	form              url.Values
	session           fosite.Session
}

func (r *redisRequester) SetID(id string)                           { r.id = id }
func (r *redisRequester) GetID() string                             { return r.id }
func (r *redisRequester) GetRequestedAt() time.Time                 { return r.requestedAt }
func (r *redisRequester) GetClient() fosite.Client                  { return r.client }
func (r *redisRequester) GetRequestedScopes() fosite.Arguments      { return r.requestedScopes }
func (r *redisRequester) GetRequestedAudience() fosite.Arguments    { return r.requestedAudience }
func (r *redisRequester) SetRequestedScopes(s fosite.Arguments)     { r.requestedScopes = s }
func (r *redisRequester) SetRequestedAudience(aud fosite.Arguments) { r.requestedAudience = aud }
func (r *redisRequester) AppendRequestedScope(scope string) {
	r.requestedScopes = append(r.requestedScopes, scope)
}
func (r *redisRequester) GetGrantedScopes() fosite.Arguments   { return r.grantedScopes }
func (r *redisRequester) GetGrantedAudience() fosite.Arguments { return r.grantedAudience }
func (r *redisRequester) GrantScope(scope string)              { r.grantedScopes = append(r.grantedScopes, scope) }
func (r *redisRequester) GrantAudience(aud string) {
	r.grantedAudience = append(r.grantedAudience, aud)
}
func (r *redisRequester) GetSession() fosite.Session           { return r.session }
func (r *redisRequester) SetSession(s fosite.Session)          { r.session = s }
func (r *redisRequester) GetRequestForm() url.Values           { return r.form }
func (*redisRequester) Merge(_ fosite.Requester)               {}
func (r *redisRequester) Sanitize(_ []string) fosite.Requester { return r }

// Compile-time interface compliance checks
var (
	_ Storage                     = (*RedisStorage)(nil)
	_ PendingAuthorizationStorage = (*RedisStorage)(nil)
	_ ClientRegistry              = (*RedisStorage)(nil)
	_ UpstreamTokenStorage        = (*RedisStorage)(nil)
	_ UserStorage                 = (*RedisStorage)(nil)
)
