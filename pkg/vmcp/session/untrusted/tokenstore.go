// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
)

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
	// KEKSecretRef, when non-nil, names the Secret + key holding the base64
	// token-encryption KEK (32 bytes decoded). The sidecar mounts it as an env
	// SecretKeyRef — the KEK value is never placed in a ConfigMap or pod env
	// literal. Nil means token rows are read unencrypted (legacy plaintext).
	KEKSecretRef *SecretKeyRef
}

// SecretKeyRef is a minimal (name, key) reference into a Secret, mirroring the
// subset of corev1.SecretKeySelector the wiring needs without coupling the
// config to a specific source.
type SecretKeyRef struct {
	// Name is the Secret name. Required.
	Name string
	// Key is the data key within the Secret. Required.
	Key string
}

// validate enforces the fail-closed contract: the sidecar must never be given
// partial token-store coordinates (it would crash-loop on a malformed prefix
// or dial a default address). An entirely-empty config is valid and means
// "no token store wired" (the broker is then expected to be absent).
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
	if c.KEKSecretRef != nil {
		if c.KEKSecretRef.Name == "" || c.KEKSecretRef.Key == "" {
			return fmt.Errorf("untrusted token store: KEKSecretRef requires both Name and Key")
		}
	}
	return nil
}
