// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package identity defines the authserver's credential-free canonical-user context contract.
package identity

import "context"

// CanonicalUserContextKey identifies the canonical-user value in a context.
// It is distinct from any authenticated-principal context key.
type CanonicalUserContextKey struct{}

// WithCanonicalUser stores a canonical user identifier in ctx.
// An empty userID leaves ctx unchanged.
func WithCanonicalUser(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, CanonicalUserContextKey{}, userID)
}

// CanonicalUserFromContext retrieves a canonical user identifier directly from
// the canonical-user context key. It does not infer a user from an identity.
// A directly stored empty string is present and returns ("", true).
func CanonicalUserFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(CanonicalUserContextKey{}).(string)
	return userID, ok
}
