// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

// RedisPasswordEnvVar is the environment variable name for the Redis session storage password.
// The operator injects this as a SecretKeyRef when sessionStorage.provider is "redis"
// and passwordRef is set.
// #nosec G101 -- This is an environment variable name, not a hardcoded credential
const RedisPasswordEnvVar = "THV_SESSION_REDIS_PASSWORD"
