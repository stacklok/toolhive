// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"time"

	"github.com/ory/fosite"
)

// clientAssertionExpiryStore is the storage handed to fosite's provider and to
// the clock skew fallback. It is the one place that decides how long the replay
// marker of a client assertion is kept. SetClientAssertionJWT refuses an exp
// that ValidateClientAssertionExp refuses, and hands the backend the deadline
// ClientAssertionRetainUntil(exp) instead of the exp. A backend that keeps the
// jti until the deadline it is given, and whose ClientAssertionJWTValid or
// SetClientAssertionJWT is atomic for a jti, such as fosite's own MemoryStore,
// is therefore replay-safe for the whole time the assertion can be accepted.
//
// fosite accepts an exp of 0 as absent, and converts any other exp with
// time.Unix(exp, 0), which wraps for values near the int64 limit. A backend
// would otherwise record such a deadline as an already-expired marker, so the
// assertion could be replayed. fosite returns the error from
// SetClientAssertionJWT unchanged, so refusing here makes the client see
// invalid_client.
//
// fosite passes whole seconds, and its own check is now.Unix() <= exp. An
// assertion fosite accepts in the last second of its life has an exp at or
// just before now, and is accepted here too: the marker deadline is exp plus 65
// seconds, so the marker still outlives the assertion.
//
// The handler factories keep the raw store, so code that calls
// storage.Unwrap or asserts storage capabilities never sees the wrapper.
type clientAssertionExpiryStore struct {
	fosite.Storage
	now func() time.Time
}

// clientAssertionExpiryPARStore additionally forwards fosite.PARStorage, which
// fosite looks up with a type assertion on its store
// (f.Store.(PARStorage) in authorize_request_handler.go). Re-check that
// assertion, and any new capability assertions on f.Store, when bumping fosite.
type clientAssertionExpiryPARStore struct {
	clientAssertionExpiryStore
	fosite.PARStorage
}

// newClientAssertionExpiryStore wraps s. The result implements
// fosite.PARStorage exactly when s does.
func newClientAssertionExpiryStore(s fosite.Storage, now func() time.Time) fosite.Storage {
	base := clientAssertionExpiryStore{Storage: s, now: now}
	if par, ok := s.(fosite.PARStorage); ok {
		return clientAssertionExpiryPARStore{clientAssertionExpiryStore: base, PARStorage: par}
	}
	return base
}

func (s clientAssertionExpiryStore) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	if err := ValidateClientAssertionExp(exp, s.now()); err != nil {
		return err
	}
	return s.Storage.SetClientAssertionJWT(ctx, jti, ClientAssertionRetainUntil(exp))
}

// ValidateClientAssertionExp is the single place the acceptable-exp rule lives.
// The provider's store wrapper and the clock skew fallback call it; the
// storage backends check the resulting deadline with
// ValidateClientAssertionMarkerDeadline instead. It returns invalid_client unless
//   - the retention deadline ClientAssertionRetainUntil(exp) is after now, so
//     the replay marker outlives the last moment the assertion can be
//     accepted, and
//   - exp is at most ClientAssertionMaxExpiryAhead after now, which bounds how
//     long a marker is stored.
//
// An exp of 0 and a time.Unix value that wrapped into the far past fail the
// first condition.
func ValidateClientAssertionExp(exp, now time.Time) error {
	if !ClientAssertionRetainUntil(exp).After(now) {
		return fosite.ErrInvalidClient.WithHint("The 'client_assertion' has expired or has no 'exp'.")
	}
	if exp.Sub(now) > ClientAssertionMaxExpiryAhead {
		return fosite.ErrInvalidClient.WithHintf(
			"The 'client_assertion' 'exp' must be at most %s in the future.", ClientAssertionMaxExpiryAhead)
	}
	return nil
}

// ValidateClientAssertionMarkerDeadline is the rule a storage backend applies to
// the replay marker deadline it is given. It returns invalid_client unless
//   - deadline is after now, so the marker is still in force, and
//   - deadline is at most ClientAssertionMaxMarkerAhead after now, which bounds
//     how long a marker is stored.
//
// The provider hands backends ClientAssertionRetainUntil(exp). A provider built
// over a backend without the wrapper hands the raw exp, which is safe because
// such a provider accepts nothing after the second named by exp, and the
// backend refuses a deadline that has already passed, so a replay in that
// second fails closed.
func ValidateClientAssertionMarkerDeadline(deadline, now time.Time) error {
	if !deadline.After(now) {
		return fosite.ErrInvalidClient.WithHint("The 'client_assertion' has expired or has no 'exp'.")
	}
	if deadline.Sub(now) > ClientAssertionMaxMarkerAhead {
		return fosite.ErrInvalidClient.WithHintf(
			"The 'client_assertion' 'exp' must be at most %s in the future.", ClientAssertionMaxMarkerAhead)
	}
	return nil
}
