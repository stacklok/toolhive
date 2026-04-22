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
	require.Equal(t, memory.Type("semantic"), memory.TypeSemantic)
	require.Equal(t, memory.Type("procedural"), memory.TypeProcedural)
	require.Equal(t, memory.Type("episodic"), memory.TypeEpisodic)
	require.Equal(t, memory.AuthorType("human"), memory.AuthorHuman)
	require.Equal(t, memory.AuthorType("agent"), memory.AuthorAgent)
	require.Equal(t, memory.EntryStatus("active"), memory.EntryStatusActive)
	require.Equal(t, memory.EntryStatus("flagged"), memory.EntryStatusFlagged)
	require.Equal(t, memory.EntryStatus("expired"), memory.EntryStatusExpired)
	require.Equal(t, memory.EntryStatus("archived"), memory.EntryStatusArchived)
	require.Equal(t, memory.SourceType("memory"), memory.SourceMemory)
	require.Equal(t, memory.SourceType("skill"), memory.SourceSkill)
	require.Equal(t, memory.ArchiveReason("consolidated"), memory.ArchiveReasonConsolidated)
	require.Equal(t, memory.ArchiveReason("crystallized"), memory.ArchiveReasonCrystallized)
	require.Equal(t, memory.ArchiveReason("manual"), memory.ArchiveReasonManual)
	require.Equal(t, memory.ArchiveReason("expired"), memory.ArchiveReasonExpired)
}
