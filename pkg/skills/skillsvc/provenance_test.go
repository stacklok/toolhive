// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProvenance_ExistsStartsFalse(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	exists, err := p.Exists()
	require.NoError(t, err)
	assert.False(t, exists, "provenance file should not exist before any write")
}

func TestBuildProvenance_RecordCreatesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newBuildProvenance(dir)

	require.NoError(t, p.Record("my-skill", "sha256:abc"))

	exists, err := p.Exists()
	require.NoError(t, err)
	assert.True(t, exists)

	// File lives next to the OCI layout at <storeRoot>/builds.json.
	info, err := os.Stat(filepath.Join(dir, provenanceFileName))
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestBuildProvenance_RecordListForgetRoundTrip(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	require.NoError(t, p.Record("skill-a", "sha256:aaa"))
	require.NoError(t, p.Record("skill-b", "sha256:bbb"))

	entries, err := p.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)

	byTag := map[string]string{}
	for _, e := range entries {
		byTag[e.Tag] = e.Digest
		assert.False(t, e.CreatedAt.IsZero(), "createdAt should be populated")
	}
	assert.Equal(t, "sha256:aaa", byTag["skill-a"])
	assert.Equal(t, "sha256:bbb", byTag["skill-b"])

	has, err := p.Has("skill-a")
	require.NoError(t, err)
	assert.True(t, has)

	require.NoError(t, p.Forget("skill-a"))

	has, err = p.Has("skill-a")
	require.NoError(t, err)
	assert.False(t, has)

	entries, err = p.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "skill-b", entries[0].Tag)
}

func TestBuildProvenance_RecordUpdatesExistingEntry(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	require.NoError(t, p.Record("skill-a", "sha256:old"))
	require.NoError(t, p.Record("skill-a", "sha256:new"))

	entries, err := p.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "sha256:new", entries[0].Digest)
}

func TestBuildProvenance_ForgetMissingTagIsNoop(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	require.NoError(t, p.Forget("nonexistent"))
	// Forget on a missing provenance file should leave it untouched.
	exists, err := p.Exists()
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestBuildProvenance_SeedReplacesState(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	require.NoError(t, p.Record("pre-existing", "sha256:zzz"))

	require.NoError(t, p.Seed([]provenanceEntry{
		{Tag: "seeded-a", Digest: "sha256:aaa"},
		{Tag: "seeded-b", Digest: "sha256:bbb"},
	}))

	entries, err := p.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Tag] = true
	}
	assert.True(t, seen["seeded-a"])
	assert.True(t, seen["seeded-b"])
	assert.False(t, seen["pre-existing"], "seed should replace prior state")
}

func TestBuildProvenance_SeedEmptyMarksExists(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	require.NoError(t, p.Seed(nil))

	exists, err := p.Exists()
	require.NoError(t, err)
	assert.True(t, exists, "seeding an empty set must still create the file to block re-migration")

	entries, err := p.List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestBuildProvenance_LoadHandlesEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newBuildProvenance(dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, provenanceFileName), []byte{}, 0o600))

	entries, err := p.List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestBuildProvenance_LoadRejectsCorruptJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newBuildProvenance(dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, provenanceFileName), []byte("not-json"), 0o600))

	_, err := p.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing provenance file")
}

func TestBuildProvenance_AtomicWriteLeavesNoStragglers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newBuildProvenance(dir)

	require.NoError(t, p.Record("skill-a", "sha256:aaa"))
	require.NoError(t, p.Record("skill-b", "sha256:bbb"))
	require.NoError(t, p.Forget("skill-a"))

	// After several writes, only the final builds.json should remain —
	// the atomic temp-file + rename path must not leak "*.tmp" siblings.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".tmp", "temp file left behind: %s", entry.Name())
	}
}

func TestBuildProvenance_ConcurrentWritesAreSerialized(t *testing.T) {
	t.Parallel()
	p := newBuildProvenance(t.TempDir())

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			tag := "skill-" + string(rune('a'+i%26))
			_ = p.Record(tag, "sha256:digest")
		}(i)
	}
	wg.Wait()

	// The final file must still be valid JSON regardless of interleaving.
	data, err := os.ReadFile(filepath.Join(p.path))
	require.NoError(t, err)
	var doc provenanceFile
	require.NoError(t, json.Unmarshal(data, &doc))
}

func TestBuildProvenance_NilReceiverSafeMethods(t *testing.T) {
	t.Parallel()
	var p *buildProvenance

	exists, err := p.Exists()
	require.NoError(t, err)
	assert.False(t, exists)

	entries, err := p.List()
	require.NoError(t, err)
	assert.Nil(t, entries)

	has, err := p.Has("anything")
	require.NoError(t, err)
	assert.False(t, has)

	// Forget is a no-op for nil receivers (no store configured).
	require.NoError(t, p.Forget("anything"))

	// Record and Seed require configuration; they must error explicitly.
	require.Error(t, p.Record("tag", "sha256:abc"))
	require.Error(t, p.Seed(nil))
}

func TestLooksLikePulledRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tag    string
		pulled bool
	}{
		{"my-skill", false},
		{"v1.0.0", false},
		{"latest", false},
		{"ghcr.io/org/skill:v1", true},
		{"localhost:5000/skill:v1", true},
		{"ghcr.io/org/skill@sha256:abc", true},
		{"org/skill", true},
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.pulled, looksLikePulledRef(tc.tag))
		})
	}
}
