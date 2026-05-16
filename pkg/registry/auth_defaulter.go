// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"log/slog"
	"sync"
)

// AuthDefaulter resolves OIDC issuer and client ID for a registry update
// when the caller did not supply them explicitly. Enterprise builds register
// a defaulter that queries the platform's well-known configuration endpoint,
// so admins enforcing a registry no longer need to distribute issuer and
// client_id out of band.
type AuthDefaulter func(ctx context.Context) (issuer, clientID string, err error)

var (
	authDefaulterMu sync.RWMutex
	authDefaulter   AuthDefaulter
)

// RegisterAuthDefaulter sets the active registry auth defaulter. Safe for
// concurrent use, though it is intended to be called once at startup.
// Passing nil clears any previously registered defaulter.
func RegisterAuthDefaulter(d AuthDefaulter) {
	authDefaulterMu.Lock()
	defer authDefaulterMu.Unlock()
	authDefaulter = d
}

// ActiveAuthDefaulter returns the currently registered auth defaulter, or
// nil if none has been registered.
func ActiveAuthDefaulter() AuthDefaulter {
	authDefaulterMu.RLock()
	defer authDefaulterMu.RUnlock()
	return authDefaulter
}

// ResolveAuthDefaults returns the OIDC issuer and client ID for a registry
// update. Explicit values always win; when both are empty and a defaulter
// is registered, the defaulter is consulted. A defaulter error falls back
// to empty so callers preserve the legacy "no auth" behaviour.
func ResolveAuthDefaults(ctx context.Context, issuer, clientID string, defaulter AuthDefaulter) (string, string) {
	if issuer != "" || clientID != "" {
		return issuer, clientID
	}
	if defaulter == nil {
		return "", ""
	}
	resolvedIssuer, resolvedClientID, err := defaulter(ctx)
	if err != nil {
		slog.Debug("registry auth discovery failed, proceeding without auth", "error", err)
		return "", ""
	}
	return resolvedIssuer, resolvedClientID
}
