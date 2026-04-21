// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/memory"
)

func TestMemoryTypeConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, memory.MemoryType("semantic"), memory.MemoryTypeSemantic)
	require.Equal(t, memory.MemoryType("procedural"), memory.MemoryTypeProcedural)
	require.Equal(t, memory.AuthorType("human"), memory.AuthorHuman)
	require.Equal(t, memory.AuthorType("agent"), memory.AuthorAgent)
	require.Equal(t, memory.EntryStatus("active"), memory.EntryStatusActive)
	require.Equal(t, memory.EntryStatus("flagged"), memory.EntryStatusFlagged)
	require.Equal(t, memory.EntryStatus("expired"), memory.EntryStatusExpired)
	require.Equal(t, memory.EntryStatus("archived"), memory.EntryStatusArchived)
}
