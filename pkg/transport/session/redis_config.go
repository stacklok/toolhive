// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"log/slog"

	"github.com/stacklok/toolhive-core/redisconn"
)

// RedisPasswordEnvVar is the environment variable name for the Redis session storage password.
// The operator injects this as a SecretKeyRef when sessionStorage.provider is "redis"
// and passwordRef is set.
// #nosec G101 -- This is an environment variable name, not a hardcoded credential
const RedisPasswordEnvVar = "THV_SESSION_REDIS_PASSWORD"

// warnInsecureRedisTransport emits one startup WARN when the session store
// connection is not protected by verified TLS: either TLS is not configured
// (session data, and the password if one is set, cross the network in
// plaintext) or server certificate verification is disabled. Both remain
// allowed so existing deployments keep starting (for example, ones relying on
// a service mesh for encryption), but the configuration is made visible in
// logs. The password itself is never logged. It is called by the Redis session
// store constructors so every caller gets the warning.
func warnInsecureRedisTransport(cfg *redisconn.Config) {
	if cfg == nil {
		return
	}
	switch {
	case cfg.TLS == nil && cfg.Password != "":
		slog.Warn("Redis session storage sends its password and session data without TLS; "+
			"configure TLS for the session storage connection to encrypt it",
			"store", cfg.Addr)
	case cfg.TLS == nil:
		slog.Warn("Redis session storage sends session data without TLS; "+
			"configure TLS for the session storage connection to encrypt it",
			"store", cfg.Addr)
	case cfg.TLS.InsecureSkipVerify:
		slog.Warn("Redis session storage uses TLS without server certificate verification; "+
			"the connection is encrypted but the server is not authenticated",
			"store", cfg.Addr)
	}
}
