package session

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds the configuration for Redis storage
type RedisConfig struct {
	// Connection settings
	Addresses []string `json:"addresses" yaml:"addresses"` // For cluster mode or single address
	Password  string   `json:"password" yaml:"password"`
	DB        int      `json:"db" yaml:"db"` // Database number (not used in cluster mode)

	// TLS Configuration
	TLSEnabled bool   `json:"tls_enabled" yaml:"tls_enabled"`
	TLSCert    string `json:"tls_cert" yaml:"tls_cert"`
	TLSKey     string `json:"tls_key" yaml:"tls_key"`
	TLSCA      string `json:"tls_ca" yaml:"tls_ca"`

	// Connection pool settings
	PoolSize     int `json:"pool_size" yaml:"pool_size"`
	MinIdleConns int `json:"min_idle_conns" yaml:"min_idle_conns"`

	// Options
	KeyPrefix   string        `json:"key_prefix" yaml:"key_prefix"`
	TTL         time.Duration `json:"ttl" yaml:"ttl"`
	ClusterMode bool          `json:"cluster_mode" yaml:"cluster_mode"`
}

// Validate checks if the Redis configuration is valid
func (c *RedisConfig) Validate() error {
	if len(c.Addresses) == 0 {
		return fmt.Errorf("at least one address is required")
	}

	if c.KeyPrefix == "" {
		c.KeyPrefix = "toolhive:sessions:"
	}

	if c.TTL == 0 {
		c.TTL = 30 * time.Minute
	}

	if c.PoolSize == 0 {
		c.PoolSize = 10
	}

	return nil
}

// RedisStorage implements the Storage interface using Redis
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisStorage creates a new Redis storage backend
func NewRedisStorage(config *RedisConfig) (*RedisStorage, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid redis config: %w", err)
	}

	var client redis.UniversalClient

	// Configure TLS if enabled
	var tlsConfig *tls.Config
	if config.TLSEnabled {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		// Additional TLS configuration would go here
		// (loading certs, CA, etc.)
	}

	if config.ClusterMode {
		// Create cluster client
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        config.Addresses,
			Password:     config.Password,
			PoolSize:     config.PoolSize,
			MinIdleConns: config.MinIdleConns,
			TLSConfig:    tlsConfig,
		})
	} else {
		// Create single node client
		client = redis.NewClient(&redis.Options{
			Addr:         config.Addresses[0],
			Password:     config.Password,
			DB:           config.DB,
			PoolSize:     config.PoolSize,
			MinIdleConns: config.MinIdleConns,
			TLSConfig:    tlsConfig,
		})
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisStorage{
		client:    client,
		keyPrefix: config.KeyPrefix,
		ttl:       config.TTL,
	}, nil
}

// Store saves a session to Redis with TTL
func (s *RedisStorage) Store(ctx context.Context, session Session) error {
	if session == nil {
		return fmt.Errorf("cannot store nil session")
	}
	if session.ID() == "" {
		return fmt.Errorf("cannot store session with empty ID")
	}

	// Serialize the session to JSON
	data, err := SerializeSession(session)
	if err != nil {
		return fmt.Errorf("failed to serialize session: %w", err)
	}

	// Store in Redis with TTL
	key := s.keyPrefix + session.ID()
	if err := s.client.Set(ctx, key, data, s.ttl).Err(); err != nil {
		return fmt.Errorf("failed to store session in Redis: %w", err)
	}

	// Also add to a sorted set for TTL management
	// Score is the expiration timestamp
	expiresAt := time.Now().Add(s.ttl).Unix()
	if err := s.client.ZAdd(ctx, s.keyPrefix+"expires", redis.Z{
		Score:  float64(expiresAt),
		Member: session.ID(),
	}).Err(); err != nil {
		// Non-critical error, log but don't fail
		// The key TTL will still work
		_ = err
	}

	return nil
}

// Load retrieves a session from Redis and touches it
func (s *RedisStorage) Load(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session with empty ID")
	}

	key := s.keyPrefix + id
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to load session from Redis: %w", err)
	}

	// Deserialize the session
	session, err := DeserializeSession(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize session: %w", err)
	}

	// Touch the session
	session.Touch()

	// Update the session in Redis with new timestamp
	// This is done asynchronously to not block the load
	go func() {
		storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Store(storeCtx, session)
	}()

	return session, nil
}

// Delete removes a session from Redis
func (s *RedisStorage) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session with empty ID")
	}

	key := s.keyPrefix + id

	// Delete from main storage
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete session from Redis: %w", err)
	}

	// Remove from expires sorted set
	if err := s.client.ZRem(ctx, s.keyPrefix+"expires", id).Err(); err != nil {
		// Non-critical error
		_ = err
	}

	return nil
}

// DeleteExpired removes all sessions that haven't been updated since the given time
func (s *RedisStorage) DeleteExpired(ctx context.Context, before time.Time) error {
	// Use the sorted set to find expired sessions
	maxScore := float64(before.Unix())

	// Get expired session IDs
	expiredIDs, err := s.client.ZRangeByScore(ctx, s.keyPrefix+"expires", &redis.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%f", maxScore),
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to get expired sessions: %w", err)
	}

	// Delete expired sessions
	for _, id := range expiredIDs {
		if err := s.Delete(ctx, id); err != nil {
			// Log error but continue with other deletions
			_ = err
		}
	}

	// Clean up the sorted set
	if err := s.client.ZRemRangeByScore(ctx, s.keyPrefix+"expires", "-inf", fmt.Sprintf("%f", maxScore)).Err(); err != nil {
		// Non-critical error
		_ = err
	}

	return nil
}

// Close closes the Redis connection
func (s *RedisStorage) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// Exists checks if a session exists in Redis (helper method, not part of interface)
func (s *RedisStorage) Exists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot check existence of session with empty ID")
	}

	key := s.keyPrefix + id
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check session existence: %w", err)
	}

	return exists > 0, nil
}

// Count returns the number of sessions in storage (helper method, not part of interface)
func (s *RedisStorage) Count(ctx context.Context) (int, error) {
	// Use the sorted set to count sessions
	count, err := s.client.ZCard(ctx, s.keyPrefix+"expires").Result()
	if err != nil {
		return 0, fmt.Errorf("failed to count sessions: %w", err)
	}

	return int(count), nil
}
