// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
)

var _ RememberedConsentStorage = (*RedisStorage)(nil)

func approvalKey(prefix, userID, clientID string) (string, error) {
	pair, err := json.Marshal([2]string{userID, clientID})
	if err != nil {
		return "", err
	}
	return redisKey(prefix, KeyTypeClientApproval, fmt.Sprintf("%x", sha256.Sum256(pair))), nil
}

// StoreConsentSession stores a digest with its remaining fixed lifetime.
func (s *RedisStorage) StoreConsentSession(ctx context.Context, digest string, session *ConsentSession) error {
	if digest == "" {
		return fosite.ErrInvalidRequest.WithHint("consent session digest cannot be empty")
	}
	if session == nil {
		return fosite.ErrInvalidRequest.WithHint("consent session cannot be nil")
	}
	ttl := time.Until(session.ExpiresAt)
	if ttl <= 0 {
		return ErrExpired
	}
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal consent session: %w", err)
	}
	if err := s.client.Set(ctx, redisKey(s.keyPrefix, KeyTypeConsentSession, digest), data, ttl).Err(); err != nil {
		return fmt.Errorf("store consent session: %w", err)
	}
	return nil
}

// GetConsentSession returns a session only until its stored absolute expiry.
func (s *RedisStorage) GetConsentSession(ctx context.Context, digest string) (*ConsentSession, error) {
	data, err := s.client.Get(ctx, redisKey(s.keyPrefix, KeyTypeConsentSession, digest)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load consent session: %w", err)
	}
	var session ConsentSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("decode consent session: %w", err)
	}
	if !time.Now().Before(session.ExpiresAt) {
		return nil, ErrExpired
	}
	return &session, nil
}

// DeleteConsentSession revokes the originating browser session.
func (s *RedisStorage) DeleteConsentSession(ctx context.Context, digest string) error {
	if err := s.client.Del(ctx, redisKey(s.keyPrefix, KeyTypeConsentSession, digest)).Err(); err != nil {
		return fmt.Errorf("delete consent session: %w", err)
	}
	return nil
}

// StoreClientApproval overwrites one user's approval for one client.
func (s *RedisStorage) StoreClientApproval(ctx context.Context, userID, clientID string, approval *ClientApproval) error {
	if userID == "" {
		return fosite.ErrInvalidRequest.WithHint("client approval user ID cannot be empty")
	}
	if clientID == "" {
		return fosite.ErrInvalidRequest.WithHint("client approval client ID cannot be empty")
	}
	if approval == nil {
		return fosite.ErrInvalidRequest.WithHint("client approval cannot be nil")
	}
	ttl := time.Until(approval.ExpiresAt)
	if ttl <= 0 {
		return ErrExpired
	}
	key, err := approvalKey(s.keyPrefix, userID, clientID)
	if err != nil {
		return err
	}
	clone := *approval
	clone.Scopes = slices.Clone(approval.Scopes)
	data, err := json.Marshal(&clone)
	if err != nil {
		return fmt.Errorf("marshal client approval: %w", err)
	}
	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("store client approval: %w", err)
	}
	return nil
}

// GetClientApproval returns a copy only until its stored absolute expiry.
func (s *RedisStorage) GetClientApproval(ctx context.Context, userID, clientID string) (*ClientApproval, error) {
	key, err := approvalKey(s.keyPrefix, userID, clientID)
	if err != nil {
		return nil, err
	}
	data, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load client approval: %w", err)
	}
	var approval ClientApproval
	if err := json.Unmarshal(data, &approval); err != nil {
		return nil, fmt.Errorf("decode client approval: %w", err)
	}
	if !time.Now().Before(approval.ExpiresAt) {
		return nil, ErrExpired
	}
	return &approval, nil
}
