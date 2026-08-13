// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hexDigest returns a valid lowercase hex string of exactly n characters. A
// distinct seed produces a distinct string, so tests needing multiple unique
// fixture digests can pass different seeds.
func hexDigest(n int, seed byte) string {
	const alphabet = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[(i+int(seed))%len(alphabet)]
	}
	return string(b)
}

// ociDigest returns a valid fixture value for an Entry.Digest/ContentDigest
// field ("sha256:" + 64 hex characters).
func ociDigest(seed byte) string {
	return ContentDigestPrefix + hexDigest(sha256HexLength, seed)
}

// testRoot creates a resolved temp dir with a .git directory (satisfying
// skills.ValidateProjectRoot) and returns its opened Root.
func testRoot(t *testing.T) Root {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	root, err := OpenRoot(dir)
	require.NoError(t, err)
	return root
}

func TestOpenRootRejectsInvalidProjectRoot(t *testing.T) {
	t.Parallel()

	_, err := OpenRoot(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestZeroRootPathFails(t *testing.T) {
	t.Parallel()

	var r Root
	_, err := r.Path()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uninitialized Root")
}

func TestLoadMissingFileReturnsEmptyLockfile(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	lf, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, CurrentVersion, lf.Version)
	assert.Empty(t, lf.Skills)
	assert.Empty(t, lf.Plugins)
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path, err := root.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("not: [valid: yaml"), 0o644))

	_, err = Load(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing lock file")
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path, err := root.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("version: 99\nskills: []\n"), 0o644))

	_, err = Load(root)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	lf := &Lockfile{Version: CurrentVersion}
	entry := Entry{
		Name:              "code-review",
		Version:           "1.0.0",
		Source:            "code-review",
		ResolvedReference: "ghcr.io/org/code-review:1.0.0",
		Digest:            ociDigest(1),
		ContentDigest:     ociDigest(2),
		Explicit:          true,
	}
	lf.Upsert(entry)
	require.NoError(t, lf.Save(root))

	loaded, err := Load(root)
	require.NoError(t, err)
	require.Len(t, loaded.Skills, 1)
	assert.Equal(t, entry, loaded.Skills[0])
	assert.Empty(t, loaded.Plugins)
}

// TestUnknownFieldsSurviveLoadModifySave freezes the version-skew contract:
// a lock file written by a newer binary (e.g. with Sigstore provenance
// fields added under schema version 1) must round-trip through this
// binary's Load -> modify -> Save without the unknown fields being
// stripped — otherwise an older thv silently removes a teammate's signing
// metadata on its next write.
func TestUnknownFieldsSurviveLoadModifySave(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path, err := root.Path()
	require.NoError(t, err)

	futureLock := "" +
		"version: 1\n" +
		"futureTopLevelField: keep-me\n" +
		"skills:\n" +
		"  - name: signed-skill\n" +
		"    source: ghcr.io/org/signed-skill:1.0.0\n" +
		"    digest: " + ociDigest(1) + "\n" +
		"    attestations:\n" +
		"      - predicateType: https://slsa.dev/provenance/v1\n" +
		"        digest: " + ociDigest(3) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(futureLock), 0o644))

	// Load, touch an unrelated entry, save — the classic older-binary write.
	require.NoError(t, UpsertEntry(root, Entry{
		Name:   "other-skill",
		Source: "other-skill",
		Digest: ociDigest(2),
	}))

	data, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)
	saved := string(data)
	assert.Contains(t, saved, "predicateType: https://slsa.dev/provenance/v1", "unknown entry fields must survive a Load->Save cycle")
	assert.Contains(t, saved, "futureTopLevelField: keep-me", "unknown top-level fields must survive a Load->Save cycle")

	loaded, err := Load(root)
	require.NoError(t, err)
	signed, ok := loaded.Get("signed-skill")
	require.True(t, ok)
	assert.Contains(t, signed.Extra, "attestations")
}

func TestSaveRejectsInvalidLockfile(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	lf := &Lockfile{Version: CurrentVersion}
	lf.Upsert(Entry{Name: "code-review", Source: "code-review"}) // missing required Digest

	err := lf.Save(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid lock file")

	_, statErr := os.Stat(filepath.Join(root.Dir(), FileName))
	require.True(t, os.IsNotExist(statErr), "Save must not write an invalid lock file to disk")
}

func TestUpdateRejectsInvalidLockfile(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	err := Update(root, func(lf *Lockfile) error {
		lf.Upsert(Entry{Name: "code-review", Source: "code-review"}) // missing required Digest
		return nil
	})
	require.Error(t, err)

	// A caller bug that upserts an invalid entry must not leave every
	// subsequent Load/Update permanently broken.
	loaded, loadErr := Load(root)
	require.NoError(t, loadErr)
	assert.Empty(t, loaded.Skills)
}

func TestLockfileGetUpsertRemove(t *testing.T) {
	t.Parallel()

	lf := &Lockfile{Version: CurrentVersion}

	_, ok := lf.Get("missing")
	assert.False(t, ok)

	a := Entry{Name: "b-skill", Source: "b-skill", Digest: ociDigest(3)}
	c := Entry{Name: "a-skill", Source: "a-skill", Digest: ociDigest(4)}
	lf.Upsert(a)
	lf.Upsert(c)

	// Entries are kept sorted by name.
	require.Len(t, lf.Skills, 2)
	assert.Equal(t, "a-skill", lf.Skills[0].Name)
	assert.Equal(t, "b-skill", lf.Skills[1].Name)

	got, ok := lf.Get("b-skill")
	require.True(t, ok)
	assert.Equal(t, a.Digest, got.Digest)

	// Upsert replaces an existing entry rather than duplicating it.
	updated := Entry{Name: "b-skill", Source: "b-skill", Digest: ociDigest(5)}
	lf.Upsert(updated)
	require.Len(t, lf.Skills, 2)
	got, ok = lf.Get("b-skill")
	require.True(t, ok)
	assert.Equal(t, updated.Digest, got.Digest)

	removed := lf.Remove("b-skill")
	assert.True(t, removed)
	require.Len(t, lf.Skills, 1)

	removedAgain := lf.Remove("b-skill")
	assert.False(t, removedAgain)
}

func TestPluginSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	lf := &Lockfile{Version: CurrentVersion}
	skill := Entry{
		Name:              "code-review",
		Source:            "code-review",
		ResolvedReference: "ghcr.io/org/code-review:1.0.0",
		Digest:            ociDigest(1),
		Explicit:          true,
	}
	plugin := Entry{
		Name:              "code-review", // same name, different key — allowed
		Version:           "2.0.0",
		Source:            "code-review",
		ResolvedReference: "ghcr.io/org/code-review-plugin:2.0.0",
		Digest:            ociDigest(3),
		ContentDigest:     ociDigest(4),
		Explicit:          true,
	}
	lf.Upsert(skill)
	lf.UpsertPlugin(plugin)
	require.NoError(t, lf.Save(root))

	loaded, err := Load(root)
	require.NoError(t, err)
	require.Len(t, loaded.Skills, 1)
	assert.Equal(t, skill, loaded.Skills[0])
	require.Len(t, loaded.Plugins, 1)
	assert.Equal(t, plugin, loaded.Plugins[0])
}

func TestLockfileGetUpsertRemovePlugin(t *testing.T) {
	t.Parallel()

	lf := &Lockfile{Version: CurrentVersion}

	_, ok := lf.GetPlugin("missing")
	assert.False(t, ok)

	a := Entry{Name: "b-plugin", Source: "b-plugin", Digest: ociDigest(3)}
	c := Entry{Name: "a-plugin", Source: "a-plugin", Digest: ociDigest(4)}
	lf.UpsertPlugin(a)
	lf.UpsertPlugin(c)

	require.Len(t, lf.Plugins, 2)
	assert.Equal(t, "a-plugin", lf.Plugins[0].Name)
	assert.Equal(t, "b-plugin", lf.Plugins[1].Name)
	assert.Empty(t, lf.Skills, "UpsertPlugin must not write skills:")

	got, ok := lf.GetPlugin("b-plugin")
	require.True(t, ok)
	assert.Equal(t, a.Digest, got.Digest)

	updated := Entry{Name: "b-plugin", Source: "b-plugin", Digest: ociDigest(5)}
	lf.UpsertPlugin(updated)
	require.Len(t, lf.Plugins, 2)
	got, ok = lf.GetPlugin("b-plugin")
	require.True(t, ok)
	assert.Equal(t, updated.Digest, got.Digest)

	removed := lf.RemovePlugin("b-plugin")
	assert.True(t, removed)
	require.Len(t, lf.Plugins, 1)

	removedAgain := lf.RemovePlugin("b-plugin")
	assert.False(t, removedAgain)
}

func TestUpsertPluginEntryAndRemovePluginEntry(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	skill := Entry{Name: "shared-name", Source: "shared-name", Digest: ociDigest(1)}
	require.NoError(t, UpsertEntry(root, skill))

	plugin := Entry{Name: "shared-name", Source: "shared-name", Digest: ociDigest(3)}
	require.NoError(t, UpsertPluginEntry(root, plugin))

	lf, err := Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	require.Len(t, lf.Plugins, 1)
	assert.Equal(t, skill.Digest, lf.Skills[0].Digest)
	assert.Equal(t, plugin.Digest, lf.Plugins[0].Digest)

	other := Entry{Name: "other-plugin", Source: "other-plugin", Digest: ociDigest(6)}
	require.NoError(t, UpsertPluginEntry(root, other))
	lf, err = Load(root)
	require.NoError(t, err)
	assert.Len(t, lf.Plugins, 2)
	assert.Len(t, lf.Skills, 1, "plugin upsert must not drop skills:")

	require.NoError(t, RemovePluginEntry(root, "shared-name"))
	lf, err = Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Plugins, 1)
	assert.Equal(t, "other-plugin", lf.Plugins[0].Name)
	require.Len(t, lf.Skills, 1, "removing a plugin must not remove the skill of the same name")

	require.NoError(t, RemovePluginEntry(root, "does-not-exist"))
}

func TestRemoveParentFromRequiredBy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		entries               []Entry
		parent                string
		wantCascadeCandidates []string
		wantRemainingParents  map[string][]string
	}{
		{
			name: "last parent removed and not explicit becomes cascade candidate",
			entries: []Entry{
				{Name: "dep", RequiredBy: []string{"parent"}},
			},
			parent:                "parent",
			wantCascadeCandidates: []string{"dep"},
			wantRemainingParents:  map[string][]string{"dep": nil},
		},
		{
			name: "explicit entry is never a cascade candidate",
			entries: []Entry{
				{Name: "dep", RequiredBy: []string{"parent"}, Explicit: true},
			},
			parent:                "parent",
			wantCascadeCandidates: nil,
			wantRemainingParents:  map[string][]string{"dep": nil},
		},
		{
			name: "dep with another parent survives",
			entries: []Entry{
				{Name: "dep", RequiredBy: []string{"parent", "other-parent"}},
			},
			parent:                "parent",
			wantCascadeCandidates: nil,
			wantRemainingParents:  map[string][]string{"dep": {"other-parent"}},
		},
		{
			name: "entry without the parent is untouched",
			entries: []Entry{
				{Name: "unrelated", RequiredBy: []string{"someone-else"}},
			},
			parent:                "parent",
			wantCascadeCandidates: nil,
			wantRemainingParents:  map[string][]string{"unrelated": {"someone-else"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lf := &Lockfile{Version: CurrentVersion, Skills: tt.entries}
			got := lf.RemoveParentFromRequiredBy(tt.parent)
			assert.Equal(t, tt.wantCascadeCandidates, got)
			for name, want := range tt.wantRemainingParents {
				entry, ok := lf.Get(name)
				require.True(t, ok)
				assert.Equal(t, want, entry.RequiredBy)
			}
		})
	}
}

func TestUpsertEntryAndRemoveEntry(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	entry := Entry{Name: "my-skill", Source: "my-skill", Digest: ociDigest(3)}
	require.NoError(t, UpsertEntry(root, entry))

	lf, err := Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "my-skill", lf.Skills[0].Name)

	// Upserting a second entry preserves the first.
	other := Entry{Name: "other-skill", Source: "other-skill", Digest: ociDigest(6)}
	require.NoError(t, UpsertEntry(root, other))
	lf, err = Load(root)
	require.NoError(t, err)
	assert.Len(t, lf.Skills, 2)

	require.NoError(t, RemoveEntry(root, "my-skill"))
	lf, err = Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "other-skill", lf.Skills[0].Name)

	// Removing a name that isn't present is a no-op, not an error.
	require.NoError(t, RemoveEntry(root, "does-not-exist"))
}

func TestUpdateLeavesLockfileUnchangedOnError(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	require.NoError(t, UpsertEntry(root, Entry{Name: "my-skill", Source: "my-skill", Digest: ociDigest(3)}))

	wantErr := errors.New("boom")
	err := Update(root, func(lf *Lockfile) error {
		lf.Upsert(Entry{Name: "should-not-persist", Source: "x", Digest: ociDigest(4)})
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	lf, err := Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "my-skill", lf.Skills[0].Name)
}

// TestConcurrentUpsertEntryDoesNotLoseUpdates hammers UpsertEntry from many
// goroutines to confirm the read-modify-write cycle is fully serialized by
// the file lock, not just protected at the Save call.
func TestConcurrentUpsertEntryDoesNotLoseUpdates(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := skillNameForIndex(i)
			errCh <- UpsertEntry(root, Entry{
				Name:   name,
				Source: name,
				Digest: ociDigest(3),
			})
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent upserts to finish")
	}
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	lf, err := Load(root)
	require.NoError(t, err)
	require.Len(t, lf.Skills, n, "a concurrent upsert lost an update")
}

func skillNameForIndex(i int) string {
	return "skill-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
}

// TestProvenanceRoundTrip covers the Stack-2 schema: a verified entry's
// provenance block and an unsigned entry's exception flag both survive
// Save -> Load intact under schema version 1.
func TestProvenanceRoundTrip(t *testing.T) {
	t.Parallel()
	root := testRoot(t)

	lf := &Lockfile{Version: CurrentVersion}
	signed := Entry{
		Name:              "signed-skill",
		Source:            "ghcr.io/org/signed-skill",
		ResolvedReference: "ghcr.io/org/signed-skill:latest",
		Digest:            ociDigest(1),
		Provenance: &Provenance{
			SignerIdentity: "/.github/workflows/release.yml",
			CertIssuer:     "https://token.actions.githubusercontent.com",
			RepositoryURI:  "https://github.com/org/signed-skill",
			SigstoreURL:    "https://rekor.sigstore.dev",
		},
		Explicit: true,
	}
	unsigned := Entry{
		Name:     "unsigned-skill",
		Source:   "unsigned-skill",
		Digest:   ociDigest(2),
		Unsigned: true,
		Explicit: true,
	}
	lf.Upsert(signed)
	lf.Upsert(unsigned)
	require.NoError(t, lf.Save(root))

	loaded, err := Load(root)
	require.NoError(t, err)

	gotSigned, ok := loaded.Get("signed-skill")
	require.True(t, ok)
	assert.Equal(t, signed.Provenance, gotSigned.Provenance)
	assert.False(t, gotSigned.Unsigned)
	assert.Empty(t, gotSigned.Extra, "a typed provenance block must not leak into the inline Extra map")

	gotUnsigned, ok := loaded.Get("unsigned-skill")
	require.True(t, ok)
	assert.True(t, gotUnsigned.Unsigned)
	assert.Nil(t, gotUnsigned.Provenance)
}

// TestProvenanceGraduatesFromExtraMap: before this schema change, a
// provenance block written by a newer binary round-tripped through the
// Extra inline map. Now that the field is typed, loading such a file must
// parse it into Entry.Provenance — not duplicate it in Extra, which would
// make Save produce a colliding key.
func TestProvenanceGraduatesFromExtraMap(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path, err := root.Path()
	require.NoError(t, err)

	handWritten := "" +
		"version: 1\n" +
		"skills:\n" +
		"  - name: signed-skill\n" +
		"    source: ghcr.io/org/signed-skill\n" +
		"    digest: " + ociDigest(1) + "\n" +
		"    provenance:\n" +
		"      signerIdentity: dev@example.com\n" +
		"      certIssuer: https://accounts.example.com\n" +
		"    explicit: true\n"
	require.NoError(t, os.WriteFile(path, []byte(handWritten), 0o644))

	loaded, err := Load(root)
	require.NoError(t, err)
	entry, ok := loaded.Get("signed-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance, "provenance must parse into the typed field")
	assert.Equal(t, "dev@example.com", entry.Provenance.SignerIdentity)
	assert.NotContains(t, entry.Extra, "provenance", "the typed field must not also appear in Extra")

	// A Save after modification must not produce duplicate keys.
	require.NoError(t, UpsertEntry(root, Entry{
		Name: "other-skill", Source: "other-skill", Digest: ociDigest(2),
	}))
	reloaded, err := Load(root)
	require.NoError(t, err)
	entry, ok = reloaded.Get("signed-skill")
	require.True(t, ok)
	assert.Equal(t, "dev@example.com", entry.Provenance.SignerIdentity)
}

// TestPluginsGraduateFromExtraMap: before this schema change, a plugins:
// key written by a newer binary round-tripped through Lockfile.Extra. Now
// that the field is typed, loading such a file must parse it into
// Lockfile.Plugins — not duplicate it in Extra, which would make Save
// produce a colliding key. A skills-only lock (no plugins: key) must still
// load, and unknown top-level fields must still round-trip.
func TestPluginsGraduateFromExtraMap(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	path, err := root.Path()
	require.NoError(t, err)

	handWritten := "" +
		"version: 1\n" +
		"futureTopLevelField: keep-me\n" +
		"skills:\n" +
		"  - name: my-skill\n" +
		"    source: my-skill\n" +
		"    digest: " + ociDigest(1) + "\n" +
		"plugins:\n" +
		"  - name: my-plugin\n" +
		"    source: my-plugin\n" +
		"    digest: " + ociDigest(2) + "\n" +
		"    explicit: true\n"
	require.NoError(t, os.WriteFile(path, []byte(handWritten), 0o644))

	loaded, err := Load(root)
	require.NoError(t, err)
	assert.NotContains(t, loaded.Extra, "plugins", "the typed field must not also appear in Extra")
	plugin, ok := loaded.GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, ociDigest(2), plugin.Digest)
	assert.True(t, plugin.Explicit)
	_, skillOK := loaded.Get("my-skill")
	require.True(t, skillOK)

	require.NoError(t, UpsertEntry(root, Entry{
		Name: "other-skill", Source: "other-skill", Digest: ociDigest(3),
	}))
	data, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)
	saved := string(data)
	assert.Contains(t, saved, "futureTopLevelField: keep-me")
	assert.Contains(t, saved, "name: my-plugin")

	reloaded, err := Load(root)
	require.NoError(t, err)
	_, ok = reloaded.GetPlugin("my-plugin")
	require.True(t, ok, "a skills: upsert must not strip plugins:")
}
