// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResourceMetadataContext(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		want := ResourceMetadata{BackendID: "github-mcp"}
		ctx := WithResourceMetadata(t.Context(), want)

		got, ok := ResourceMetadataFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, want, got)
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()

		got, ok := ResourceMetadataFromContext(t.Context())
		assert.False(t, ok)
		assert.Empty(t, got)
	})
}
