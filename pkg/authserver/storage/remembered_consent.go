// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"slices"
	"time"

	"github.com/ory/fosite"
)

var _ RememberedConsentStorage = (*MemoryStorage)(nil)

// StoreConsentSession stores only the digest of a browser's session secret.
func (s *MemoryStorage) StoreConsentSession(_ context.Context, digest string, session *ConsentSession) error {
	if digest == "" {
		return fosite.ErrInvalidRequest.WithHint("consent session digest cannot be empty")
	}
	if session == nil {
		return fosite.ErrInvalidRequest.WithHint("consent session cannot be nil")
	}
	if !time.Now().Before(session.ExpiresAt) {
		return ErrExpired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *session
	s.consentSessions[digest] = &timedEntry[*ConsentSession]{value: &clone, expiresAt: clone.ExpiresAt}
	return nil
}

// GetConsentSession loads a session without extending its fixed expiry.
func (s *MemoryStorage) GetConsentSession(_ context.Context, digest string) (*ConsentSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.consentSessions[digest]
	if !ok {
		return nil, ErrNotFound
	}
	if !time.Now().Before(entry.expiresAt) {
		return nil, ErrExpired
	}
	clone := *entry.value
	return &clone, nil
}

// DeleteConsentSession revokes a browser session; absence is harmless.
func (s *MemoryStorage) DeleteConsentSession(_ context.Context, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.consentSessions, digest)
	return nil
}

// StoreClientApproval replaces the approval for this user and client.
func (s *MemoryStorage) StoreClientApproval(_ context.Context, userID, clientID string, approval *ClientApproval) error {
	if userID == "" {
		return fosite.ErrInvalidRequest.WithHint("client approval user ID cannot be empty")
	}
	if clientID == "" {
		return fosite.ErrInvalidRequest.WithHint("client approval client ID cannot be empty")
	}
	if approval == nil {
		return fosite.ErrInvalidRequest.WithHint("client approval cannot be nil")
	}
	if !time.Now().Before(approval.ExpiresAt) {
		return ErrExpired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *approval
	clone.Scopes = slices.Clone(approval.Scopes)
	s.clientApprovals[[2]string{userID, clientID}] = &timedEntry[*ClientApproval]{value: &clone, expiresAt: clone.ExpiresAt}
	return nil
}

// GetClientApproval loads a copy without extending the approval's fixed expiry.
func (s *MemoryStorage) GetClientApproval(_ context.Context, userID, clientID string) (*ClientApproval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.clientApprovals[[2]string{userID, clientID}]
	if !ok {
		return nil, ErrNotFound
	}
	if !time.Now().Before(entry.expiresAt) {
		return nil, ErrExpired
	}
	clone := *entry.value
	clone.Scopes = slices.Clone(clone.Scopes)
	return &clone, nil
}
