// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"context"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
)

func TestContextWithSPIFFEID(t *testing.T) {
	t.Parallel()

	id := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	ctx := ContextWithSPIFFEID(context.Background(), id)

	got, ok := SPIFFEIDFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, id, got)
}

func TestSPIFFEIDFromContextWithoutID(t *testing.T) {
	t.Parallel()

	got, ok := SPIFFEIDFromContext(context.Background())
	assert.False(t, ok)
	assert.True(t, got.IsZero())
}
