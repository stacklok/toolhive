// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"time"

	"github.com/ory/fosite"
)

// clientAssertionExpiryStore is the storage handed to fosite's provider. It
// refuses to record the replay marker of a client assertion whose deadline is
// not in the future.
//
// fosite accepts an exp of 0 as absent, and converts any other exp with
// time.Unix(exp, 0), which wraps for values near the int64 limit. A backend
// would otherwise record such a deadline as an already-expired marker, so the
// assertion could be replayed. fosite returns the error from
// SetClientAssertionJWT unchanged, so refusing here makes the client see
// invalid_client.
//
// fosite passes whole seconds, and its own check is now.Unix() <= exp. An
// assertion presented in [exp, exp+1s) therefore passes fosite but is refused
// here, because exp.After(now) is false once now is past the whole second.
// That is deliberate: a marker that is already expired is the replay hole, and
// refusing a request in the last second of its life is the safe direction.
//
// Only the provider gets this wrapper. The wrapper is visible only to fosite:
// never hand it to code that calls storage.Unwrap or asserts storage
// capabilities. The handler factories keep the raw store for that reason.
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
	return s.Storage.SetClientAssertionJWT(ctx, jti, exp)
}

// ValidateClientAssertionExp refuses a client assertion whose exp is not after
// now, with invalid_client. It is the single place the acceptable-exp rule
// lives: the provider's store wrapper and both storage backends call it, and
// the clock-skew change will alter only this body, to judge the retention
// deadline of the replay marker.
func ValidateClientAssertionExp(exp, now time.Time) error {
	if !exp.After(now) {
		return fosite.ErrInvalidClient.WithHint("The 'client_assertion' must have an 'exp' in the future.")
	}
	return nil
}
