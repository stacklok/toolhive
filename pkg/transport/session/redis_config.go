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

// WarnInsecureRedisTransport emits one startup WARN when cfg sends a static
// Redis password over a connection that is not protected by verified TLS:
// either TLS is not configured (cfg.TLS is nil) or server certificate
// verification is disabled. Both remain allowed so existing deployments keep
// starting (for example, ones relying on a service mesh for encryption), but
// the configuration is made visible in logs. The password itself is never
// logged. The "store" attribute matches the other Redis startup logs so a
// single log-based alert can match every Redis consumer.
func WarnInsecureRedisTransport(cfg *redisconn.Config, store string) {
	if cfg == nil || cfg.Password == "" {
		return
	}
	switch {
	case cfg.TLS == nil:
		slog.Warn("Redis session storage sends its password and session data without TLS; "+
			"set sessionStorage.tls to encrypt the connection (TLS is not yet supported "+
			"for the operator-wide default Redis configured via TOOLHIVE_DEFAULT_REDIS_ADDR)",
			"store", store)
	case cfg.TLS.InsecureSkipVerify:
		slog.Warn("Redis session storage uses TLS without server certificate verification; "+
			"the connection is encrypted but the server is not authenticated",
			"store", store)
	}
}
