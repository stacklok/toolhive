// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// strictReader wraps a TokenReader and forces ExpectedBinding.Strict=true on every
// GetAllUpstreamCredentials call. Used for untrusted workloads (ADR-0001): a legacy row
// with no recorded owner must never be released to an untrusted backend's credential path.
// This is the untrusted-mode caller the Wave-0 Strict field was built for.
type strictReader struct{ inner TokenReader }

// NewStrictTokenReader wraps inner so all reads fail closed on legacy/unowned rows:
// the wrapped reader asserts ExpectedBinding.Strict=true on every call, so a stored
// upstream-token row with an empty UserID yields storage.ErrInvalidBinding instead of
// being released under the permissive legacy rule. It does NOT relax any other binding
// dimension — caller-supplied UserID/ClientID/UpstreamSubject are preserved untouched.
// Callers (the untrusted-mode credential broker, Wave 3) are responsible for wrapping
// the reader at construction time; the per-JWT load path in TokenValidator must NOT be
// wrapped, since trusted workloads legitimately read legacy rows.
func NewStrictTokenReader(inner TokenReader) TokenReader { return &strictReader{inner: inner} }

// GetAllUpstreamCredentials forces Strict=true on the expected binding, copying the
// caller's binding before mutating (copy-before-mutate: the caller's struct is never
// modified), and delegates to the inner reader.
func (s *strictReader) GetAllUpstreamCredentials(
	ctx context.Context, sessionID string, expected *storage.ExpectedBinding,
) (map[string]UpstreamCredential, []string, error) {
	e := &storage.ExpectedBinding{Strict: true}
	if expected != nil {
		*e = *expected
		e.Strict = true
	}
	return s.inner.GetAllUpstreamCredentials(ctx, sessionID, e)
}
