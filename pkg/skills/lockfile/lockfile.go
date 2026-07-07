// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package lockfile manages the project-level skills lock file
// (toolhive.lock.yaml). The lock file pins the exact name, version, and
// digest of every project-scoped skill install so a team can restore
// ("thv skill sync") or refresh ("thv skill upgrade") the pinned state.
package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/fileutils"
)

// FileName is the name of the project-level skills lock file, written to the
// project root alongside .git.
const FileName = "toolhive.lock.yaml"

// CurrentVersion is the schema version written to new lock files.
const CurrentVersion = 1

// Entry represents a single pinned skill installation in the lock file.
type Entry struct {
	// Name is the skill's unique name.
	Name string `yaml:"name"`
	// Version is the skill's declared version, if any (from SKILL.md frontmatter).
	Version string `yaml:"version,omitempty"`
	// Source is exactly what the user (or the registry resolver) originally
	// requested — a plain registry name, an OCI reference, or a git://
	// reference. Upgrade re-resolves this value to check for newer content.
	Source string `yaml:"source"`
	// ResolvedReference is the concrete OCI reference or git:// URL that
	// Source resolved to at install time.
	ResolvedReference string `yaml:"resolvedReference,omitempty"`
	// Digest pins the exact content installed: an OCI "sha256:..." digest or
	// a git commit hash.
	Digest string `yaml:"digest"`
	// ContentDigest is a deterministic SHA-256 dirhash of the materialized
	// skill file set, used for on-disk integrity verification.
	ContentDigest string `yaml:"contentDigest,omitempty"`
	// RequiredBy lists parent skill names for transitively materialized deps.
	RequiredBy []string `yaml:"requiredBy,omitempty"`
	// Explicit is true when the user directly installed this skill (exempt from
	// cascade removal when requiredBy becomes empty).
	Explicit bool `yaml:"explicit,omitempty"`
}

// Lockfile is the parsed contents of a project's toolhive.lock.yaml.
type Lockfile struct {
	// Version is the lock file schema version.
	Version int `yaml:"version"`
	// Skills is the set of pinned skill installations, sorted by name.
	Skills []Entry `yaml:"skills,omitempty"`
}

// Path returns the absolute path to the lock file for the given project root.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, FileName)
}

// Load reads and parses the lock file for projectRoot. A missing lock file
// is not an error — it returns an empty Lockfile ready to be populated.
func Load(projectRoot string) (*Lockfile, error) {
	data, err := os.ReadFile(Path(projectRoot)) // #nosec G304 -- projectRoot is validated by callers before reaching this package
	if errors.Is(err, fs.ErrNotExist) {
		return &Lockfile{Version: CurrentVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var lf Lockfile
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if lf.Version == 0 {
		lf.Version = CurrentVersion
	}
	sortEntries(lf.Skills)
	if err := validateLockfile(&lf); err != nil {
		return nil, err
	}
	return &lf, nil
}

// Get returns the entry for name, if present.
func (l *Lockfile) Get(name string) (Entry, bool) {
	for _, e := range l.Skills {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Upsert inserts or replaces the entry with a matching name, keeping the
// slice sorted by name for stable diffs.
func (l *Lockfile) Upsert(entry Entry) {
	for i := range l.Skills {
		if l.Skills[i].Name == entry.Name {
			l.Skills[i] = entry
			return
		}
	}
	l.Skills = append(l.Skills, entry)
	sortEntries(l.Skills)
}

// Remove deletes the entry with the given name, if present. Reports whether
// an entry was removed.
func (l *Lockfile) Remove(name string) bool {
	for i, e := range l.Skills {
		if e.Name == name {
			l.Skills = append(l.Skills[:i], l.Skills[i+1:]...)
			return true
		}
	}
	return false
}

// Save writes the lock file to projectRoot, creating it if necessary.
// Callers that need read-modify-write atomicity across processes should use
// [UpsertEntry] or [RemoveEntry] instead of calling Load+Save directly.
func (l *Lockfile) Save(projectRoot string) error {
	if l.Version == 0 {
		l.Version = CurrentVersion
	}
	sortEntries(l.Skills)

	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("marshaling lock file: %w", err)
	}

	path := Path(projectRoot)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { // #nosec G306 -- lock file is committed to git, not sensitive
		return fmt.Errorf("writing lock file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("saving lock file: %w", err)
	}
	return nil
}

// UpsertEntry loads the lock file, upserts entry, and saves it back, all
// under a single file lock so concurrent installs cannot race on
// read-modify-write.
func UpsertEntry(projectRoot string, entry Entry) error {
	return fileutils.WithFileLock(Path(projectRoot), func() error {
		lf, err := Load(projectRoot)
		if err != nil {
			return err
		}
		lf.Upsert(entry)
		return lf.Save(projectRoot)
	})
}

// RemoveEntry loads the lock file, removes the named entry if present, and
// saves it back, all under a single file lock. Removing an entry that does
// not exist is a no-op, not an error.
func RemoveEntry(projectRoot string, name string) error {
	return fileutils.WithFileLock(Path(projectRoot), func() error {
		lf, err := Load(projectRoot)
		if err != nil {
			return err
		}
		if !lf.Remove(name) {
			return nil
		}
		return lf.Save(projectRoot)
	})
}

// UpdateEntry loads the lock file, applies fn to it, and saves under a file lock.
func UpdateEntry(projectRoot string, fn func(*Lockfile) error) error {
	return fileutils.WithFileLock(Path(projectRoot), func() error {
		lf, err := Load(projectRoot)
		if err != nil {
			return err
		}
		if err := fn(lf); err != nil {
			return err
		}
		return lf.Save(projectRoot)
	})
}

// RemoveParentFromRequiredBy removes parent from each entry's requiredBy list.
// Returns names of entries that lost their last requiredBy parent and are not explicit.
func (l *Lockfile) RemoveParentFromRequiredBy(parent string) (cascadeCandidates []string) {
	for i := range l.Skills {
		hadParent := containsString(l.Skills[i].RequiredBy, parent)
		l.Skills[i].RequiredBy = removeString(l.Skills[i].RequiredBy, parent)
		if hadParent && len(l.Skills[i].RequiredBy) == 0 && !l.Skills[i].Explicit {
			cascadeCandidates = append(cascadeCandidates, l.Skills[i].Name)
		}
	}
	return cascadeCandidates
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func removeString(slice []string, target string) []string {
	out := slice[:0]
	for _, s := range slice {
		if s != target {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}
