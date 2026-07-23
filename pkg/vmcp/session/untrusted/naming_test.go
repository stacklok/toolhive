// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
)

var dns1123Name = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestPodNameFor(t *testing.T) {
	t.Parallel()

	userKey, err := binding.Format("https://issuer.example.com", "user-123")
	require.NoError(t, err)

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		a := PodNameFor("uid-abc", userKey, "session-1")
		b := PodNameFor("uid-abc", userKey, "session-1")
		assert.Equal(t, a, b)
	})

	t.Run("63 chars and DNS-1123 safe", func(t *testing.T) {
		t.Parallel()
		name := PodNameFor("a-very-long-mcpserver-uid-that-exceeds-thirteen-chars", userKey, "session-1")
		assert.LessOrEqual(t, len(name), 63)
		assert.Regexp(t, dns1123Name, name)
	})

	t.Run("distinct per session", func(t *testing.T) {
		t.Parallel()
		a := PodNameFor("uid-abc", userKey, "session-1")
		b := PodNameFor("uid-abc", userKey, "session-2")
		assert.NotEqual(t, a, b)
	})

	t.Run("distinct per user", func(t *testing.T) {
		t.Parallel()
		otherKey, err := binding.Format("https://issuer.example.com", "user-456")
		require.NoError(t, err)
		a := PodNameFor("uid-abc", userKey, "session-1")
		b := PodNameFor("uid-abc", otherKey, "session-1")
		assert.NotEqual(t, a, b)
	})

	t.Run("distinct per mcpserver uid prefix", func(t *testing.T) {
		t.Parallel()
		a := PodNameFor("uid-abc", userKey, "session-1")
		b := PodNameFor("uid-xyz", userKey, "session-1")
		assert.NotEqual(t, a, b)
	})
}

func TestSessionRefUserKey(t *testing.T) {
	t.Parallel()

	t.Run("equals binding.Format output", func(t *testing.T) {
		t.Parallel()
		ref := SessionRef{Issuer: "https://iss", Subject: "sub"}
		want, err := binding.Format("https://iss", "sub")
		require.NoError(t, err)
		assert.Equal(t, want, ref.UserKey())
	})

	t.Run("empty halves yield empty key", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, SessionRef{}.UserKey())
		assert.Empty(t, SessionRef{Issuer: "iss"}.UserKey())
		assert.Empty(t, SessionRef{Subject: "sub"}.UserKey())
	})
}

func TestHashes(t *testing.T) {
	t.Parallel()

	t.Run("hashes never contain raw identifiers", func(t *testing.T) {
		t.Parallel()
		userKey, err := binding.Format("https://issuer.example.com", "sensitive-sub")
		require.NoError(t, err)
		assert.NotContains(t, userHash(userKey), "sensitive-sub")
		assert.NotContains(t, subjectHash("sensitive-sub"), "sensitive-sub")
		assert.NotContains(t, sessionHash("session-id-value"), "session-id-value")
	})

	t.Run("hash lengths", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, userHash("k"), 40)
		assert.Len(t, subjectHash("s"), 40)
		assert.Len(t, sessionHash("s"), 40)
	})
}
