// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spiffeauth provides shared SPIFFE authentication primitives for the authorization server.
package spiffeauth

import (
	"context"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// SPIFFEJWTAssertionType identifies SPIFFE JWT-SVID client assertions.
const SPIFFEJWTAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-spiffe"

type contextKey struct{}

// ContextWithSPIFFEID returns a new context with the SPIFFE ID stored.
func ContextWithSPIFFEID(ctx context.Context, id spiffeid.ID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// SPIFFEIDFromContext retrieves the SPIFFE ID from the context.
// It returns the zero value and false if no SPIFFE ID is present.
func SPIFFEIDFromContext(ctx context.Context) (spiffeid.ID, bool) {
	id, ok := ctx.Value(contextKey{}).(spiffeid.ID)
	return id, ok
}
