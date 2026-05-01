// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

// Shared constants used across session package test files.
const (
	// testRedisAddr is the default Redis address used in tests that require a
	// literal address (e.g., validation tests that expect connection failures).
	testRedisAddr = "localhost:6379"

	// testMgrKeyPrefix is the key prefix used in manager Redis tests.
	testMgrKeyPrefix = "test:mgr:"

	// testMetaKey is a generic metadata key used across serialization and
	// data-storage tests.
	testMetaKey = "key"

	// testMetaValue is a generic metadata value used in serialization tests.
	testMetaValue = "value"

	// testOriginalValue is used in data-storage tests to assert that an
	// existing entry is not overwritten by a second Create call.
	testOriginalValue = "original"

	// testSentinelAddr is a sentinel address used in Redis config validation tests.
	testSentinelAddr = "s:26379"
)
