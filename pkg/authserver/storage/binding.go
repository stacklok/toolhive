// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/subtle"
	"errors"

	"go.uber.org/mock/gomock"
)

// bindingCtxKey is the context key carrying the canonical platform user for
// read-side binding validation. It is unexported: the value is placed by
// ContextWithBindingUser and consumed only by checkUpstreamBinding, keeping the
// storage package free of any dependency on the platform auth package (import
// cycle) while still letting storage resolve the caller's user on
// request-serving paths.
type bindingCtxKey struct{}

// ContextWithBindingUser returns a context carrying the canonical platform
// user that read-side binding validation must enforce when the caller passes
// a nil *ExpectedBinding (or an empty ExpectedBinding.UserID). Callers obtain
// the user from their identity layer (e.g. auth.CanonicalUserFromContext) and
// attach it with this helper; an empty userID returns the context unchanged.
func ContextWithBindingUser(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, bindingCtxKey{}, userID)
}

// checkUpstreamBinding compares a stored upstream-token row against the
// identity the caller expects it to be bound to. A nil stored row passes (the
// caller's own not-found / tombstone handling applies). A nil expected means
// "resolve the user from ctx only" (placed by ContextWithBindingUser); the
// client/subject checks are skipped in that case.
//
// A dimension is only enforced when BOTH the expected value and the stored
// value are non-empty; a stored row with an empty field (a legacy row predating
// binding fields) is released. Returns ErrInvalidBinding on mismatch.
//
// Strict mode (ExpectedBinding.Strict) fails closed on the user dimension: a
// row with an empty stored UserID is rejected outright, so a legacy row that
// cannot prove its owner is never released to a strict caller.
//
// Comparisons use subtle.ConstantTimeCompare (matching the session-binding
// convention in pkg/vmcp/session/internal/security) so a stolen session ID
// cannot be used to probe stored binding values via timing.
func checkUpstreamBinding(ctx context.Context, stored *UpstreamTokens, expected *ExpectedBinding) error {
	if stored == nil {
		return nil
	}

	if expected != nil && expected.Strict && stored.UserID == "" {
		// Fail closed: the strict caller demands an owned row; a legacy row
		// with no recorded owner is indistinguishable from a stolen one.
		return &bindingMismatchError{dimension: "user_id"}
	}

	expectedUser := ""
	if expected != nil {
		expectedUser = expected.UserID
	}
	if expectedUser == "" {
		expectedUser, _ = ctx.Value(bindingCtxKey{}).(string)
	}
	if err := compareBindingDimension(expectedUser, stored.UserID, "user_id"); err != nil {
		return err
	}

	if expected == nil {
		return nil
	}
	if err := compareBindingDimension(expected.ClientID, stored.ClientID, "client_id"); err != nil {
		return err
	}
	return compareBindingDimension(expected.UpstreamSubject, stored.UpstreamSubject, "upstream_subject")
}

// compareBindingDimension enforces one binding dimension. A mismatch returns an
// error whose message carries only the dimension name — never the compared
// values — while errors.Is finds the ErrInvalidBinding sentinel. Both empty
// and one-side-empty comparisons pass per the legacy-row rule.
func compareBindingDimension(expected, stored, dimension string) error {
	if expected == "" || stored == "" {
		return nil
	}
	// ConstantTimeCompare short-circuits on length mismatch; leaking binding
	// length is acceptable (user/client IDs are non-secret identifiers).
	if subtle.ConstantTimeCompare([]byte(expected), []byte(stored)) != 1 {
		return &bindingMismatchError{dimension: dimension}
	}
	return nil
}

// bindingMismatchError carries the binding dimension that failed validation so
// logs can name it (metadata only — never token material) while error identity
// stays the ErrInvalidBinding sentinel. The gomock matcher below treats any
// ErrInvalidBinding as satisfying the sentinel, which both keeps errors.Is
// working for callers and lets gomock EXPECT(...).Return(storage.ErrInvalidBinding)
// match the wrapped error produced here.
type bindingMismatchError struct{ dimension string }

func (e *bindingMismatchError) Error() string {
	return ErrInvalidBinding.Error() + ": " + e.dimension
}

// Is reports whether target is the ErrInvalidBinding sentinel. It also lets
// gomock's Eq matcher (which prefers gomock.Matcher implementations on the
// actual value) match the sentinel against this error.
func (*bindingMismatchError) Is(target error) bool {
	return target == ErrInvalidBinding
}

var _ gomock.Matcher = (*bindingMismatchError)(nil)

// Matches implements gomock.Matcher: an expected ErrInvalidBinding matches any
// binding mismatch, regardless of dimension.
func (*bindingMismatchError) Matches(x any) bool {
	target, ok := x.(error)
	return ok && target == ErrInvalidBinding
}

func (*bindingMismatchError) String() string {
	return "is " + ErrInvalidBinding.Error()
}

// bindingMismatchDimension extracts the failed dimension from a binding error
// for log metadata; "unknown" when the error is not a binding mismatch.
func bindingMismatchDimension(err error) string {
	var mismatch *bindingMismatchError
	if errors.As(err, &mismatch) {
		return mismatch.dimension
	}
	return "unknown"
}
