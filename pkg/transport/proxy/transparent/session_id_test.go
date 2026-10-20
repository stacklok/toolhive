// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestRewriteSessionQuery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ query, id, want string }{
		{"toolsets=core,alerting&session=abc", "", "toolsets=core,alerting&session=abc"},
		{"sessionId=old&toolsets=core,alerting", "", "toolsets=core,alerting"},
		{"toolsets=core,alerting&session%49d=old", "new/id", "toolsets=core,alerting&sessionId=new%2Fid"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			u := &url.URL{RawQuery: tc.query}
			rewriteSessionQuery(u, tc.id)
			assert.Equal(t, tc.want, u.RawQuery)
		})
	}
}

func TestNormalizeSessionID(t *testing.T) {
	t.Parallel()

	t.Run("valid UUID passes through unchanged", func(t *testing.T) {
		t.Parallel()
		id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		assert.Equal(t, id, normalizeSessionID(id))
	})

	t.Run("non-UUID is normalized to a valid UUID", func(t *testing.T) {
		t.Parallel()
		result := normalizeSessionID("some-opaque-session-token")
		_, err := uuid.Parse(result)
		assert.NoError(t, err, "normalized result should be a valid UUID")
	})

	t.Run("normalization is deterministic", func(t *testing.T) {
		t.Parallel()
		const externalID = "some-opaque-session-token"
		assert.Equal(t, normalizeSessionID(externalID), normalizeSessionID(externalID))
	})

	t.Run("different inputs produce different UUIDs", func(t *testing.T) {
		t.Parallel()
		a := normalizeSessionID("token-a")
		b := normalizeSessionID("token-b")
		assert.NotEqual(t, a, b)
	})
}
