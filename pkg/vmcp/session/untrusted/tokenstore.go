// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"regexp"
)

// kekIDPattern bounds key IDs to the charset that is safe as a broker env-var
// suffix (THV_EGRESSBROKER_KEK_<ID>) — the sidecar never sanitizes IDs, so a
// malformed ID must be rejected here at clone time.
var kekIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TokenStoreConfig carries the coordinates the egress-broker sidecar needs to
// reach the auth-server's upstream token store, injected into the cloned pod at
// clone time (pkg/vmcp/session/untrusted/egress.go). Only non-secret
// coordinates live here — credentials are never carried on this struct.
//
// The KeyPrefix is derived by the vMCP composition root from its own identity
// (the auth server it embeds owns the token rows) via
// storage.DeriveKeyPrefix(vmcpNamespace, vmcpName), so the sidecar reads the
// exact same per-tenant prefix the auth server writes under. RedisAddr is the
// one coordinate the vMCP cannot compute and is supplied explicitly by the
// operator / deployment.
type TokenStoreConfig struct {
	// RedisAddr is the auth-server Redis address (host:port). Required.
	RedisAddr string
	// KeyPrefix is the auth-server per-tenant key prefix (e.g.
	// "thv:auth:{ns:name}:"). Required; must end with ':'.
	KeyPrefix string
	// KEKSecret, when non-empty, names the Secret holding the base64
	// token-encryption KEKs (one data entry per key ID, 32 bytes decoded).
	// Every ID in KEKIDs is mounted as a per-ID SecretKeyRef env on the
	// sidecar — the KEK values are never placed in a ConfigMap or pod env
	// literal. Empty means token rows are read unencrypted (legacy
	// plaintext).
	KEKSecret string
	// KEKActiveID is the key ID the auth server encrypts NEW writes under.
	// It must be one of KEKIDs — if it drifts (e.g. the operator rotated
	// activeKeyId) and the sidecar keyring only knew a hardcoded ID, every
	// injection would deny.
	KEKActiveID string
	// KEKIDs is the full set of key IDs the sidecar keyring must know: the
	// active ID plus every retired ID rows may still be sealed under. A
	// single-ID set (no rotation history) is the common case.
	KEKIDs []string
}

// validate enforces the fail-closed contract: the sidecar must never be given
// partial token-store coordinates (it would crash-loop on a malformed prefix
// or dial a default address). An entirely-empty config is valid and means
// "no token store wired" (the broker is then expected to be absent).
//
//nolint:gocyclo // sequential fail-loud coordinate validation is clearest linear.
func (c *TokenStoreConfig) validate() error {
	if c == nil {
		return fmt.Errorf("untrusted token store: config must not be nil")
	}
	if c.RedisAddr == "" {
		return fmt.Errorf("untrusted token store: RedisAddr must not be empty")
	}
	if c.KeyPrefix == "" || c.KeyPrefix[len(c.KeyPrefix)-1] != ':' {
		return fmt.Errorf("untrusted token store: KeyPrefix must be non-empty and end with ':'")
	}
	// KEK coordinates are all-or-nothing: secret name + active ID + at least
	// one key ID, with the active ID a member of the set.
	if c.KEKSecret == "" && c.KEKActiveID == "" && len(c.KEKIDs) == 0 {
		return nil
	}
	if c.KEKSecret == "" || c.KEKActiveID == "" || len(c.KEKIDs) == 0 {
		return fmt.Errorf("untrusted token store: KEK coordinates are all-or-nothing " +
			"(KEKSecret, KEKActiveID, and at least one KEKIDs entry are required together)")
	}
	seen := make(map[string]struct{}, len(c.KEKIDs))
	activeInSet := false
	for _, id := range c.KEKIDs {
		if !kekIDPattern.MatchString(id) {
			return fmt.Errorf("untrusted token store: KEK key ID %q must match [A-Za-z0-9_-]+ "+
				"(it becomes a broker env-var suffix)", id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("untrusted token store: duplicate KEK key ID %q", id)
		}
		seen[id] = struct{}{}
		if id == c.KEKActiveID {
			activeInSet = true
		}
	}
	if !activeInSet {
		return fmt.Errorf("untrusted token store: KEKActiveID %q must be one of KEKIDs", c.KEKActiveID)
	}
	return nil
}
