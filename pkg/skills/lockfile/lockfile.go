// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package lockfile manages the project-level skills lock file
// (toolhive.lock.yaml). The lock file pins the exact name, version, source,
// and digests of every project-scoped skill install so a team can restore
// ("thv skill sync") or refresh ("thv skill upgrade") the pinned state on any
// machine. See RFC THV-0080.
//
// All filesystem access goes through [Root], a capability type that can only
// be constructed from a validated project root. Root confines every read and
// write to that directory using [os.Root] (OS-enforced containment, not just
// string validation), so no function in this package can be made to open a
// path outside the project root regardless of what name is requested.
package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/skills"
)

// FileName is the name of the project-level skills lock file, written to the
// project root alongside .git.
const FileName = "toolhive.lock.yaml"

// CurrentVersion is the schema version written to new lock files. Loading a
// lock file with a different version is a hard error, never a silent partial
// parse.
const CurrentVersion = 1

// Entry represents a single pinned skill installation in the lock file.
type Entry struct {
	// Name is the skill's unique name.
	Name string `yaml:"name"`
	// Version is the skill's declared version, if any (from SKILL.md frontmatter).
	Version string `yaml:"version,omitempty"`
	// Source is exactly what the user (or the registry resolver) originally
	// requested — a plain registry name, an OCI reference, or a git://
	// reference. It is never rewritten; upgrade re-resolves this value to
	// check for newer content.
	Source string `yaml:"source"`
	// ResolvedReference is the concrete OCI reference or git:// URL that
	// Source resolved to at install time.
	ResolvedReference string `yaml:"resolvedReference,omitempty"`
	// Digest pins the exact content installed: an OCI "sha256:..." manifest
	// digest or a full git commit hash.
	Digest string `yaml:"digest"`
	// ContentDigest is a deterministic SHA-256 dirhash of the materialized
	// skill file set, used for on-disk integrity verification.
	ContentDigest string `yaml:"contentDigest,omitempty"`
	// RequiredBy lists parent skill names for transitively materialized
	// dependencies (skills declared via toolhive.requires).
	RequiredBy []string `yaml:"requiredBy,omitempty"`
	// Explicit is true when the user directly installed this skill; explicit
	// entries are exempt from cascade removal when RequiredBy becomes empty.
	Explicit bool `yaml:"explicit,omitempty"`
}

// Lockfile is the parsed contents of a project's toolhive.lock.yaml.
type Lockfile struct {
	// Version is the lock file schema version.
	Version int `yaml:"version"`
	// Skills is the set of pinned skill installations, sorted by name for
	// stable diffs.
	Skills []Entry `yaml:"skills,omitempty"`
}

// Root is a validated project root directory. It is the only way to address
// a lock file on disk: constructing one runs full project-root validation
// (absolute, NUL-free, no traversal segments, symlink-canonical, git-rooted).
// Every read and write additionally goes through a freshly opened [os.Root]
// scoped to the validated directory, so access is confined at the OS level.
type Root struct {
	dir string
}

// OpenRoot validates projectRoot and returns a Root for it. The zero Root is
// unusable; all lock file operations require a Root produced here.
func OpenRoot(projectRoot string) (Root, error) {
	dir, err := skills.ValidateProjectRoot(projectRoot)
	if err != nil {
		return Root{}, err
	}
	return Root{dir: dir}, nil
}

// Dir returns the validated project root directory.
func (r Root) Dir() string {
	return r.dir
}

// Path returns the absolute path of the lock file inside the project root,
// for display purposes (e.g. error messages, CLI output). It is not used by
// this package to open the file; use [Load] and [Save] for that.
func (r Root) Path() (string, error) {
	if r.dir == "" {
		return "", errors.New("lockfile: uninitialized Root, use OpenRoot")
	}
	return filepath.Join(r.dir, FileName), nil
}

// osRoot opens an OS-level containment handle for r. Every name passed to
// its methods is resolved beneath r.dir; a name that would escape it (via
// "..", an absolute path, or a symlink) is rejected by the OS, not by string
// inspection.
func (r Root) osRoot() (*os.Root, error) {
	if r.dir == "" {
		return nil, errors.New("lockfile: uninitialized Root, use OpenRoot")
	}
	return os.OpenRoot(r.dir)
}

// Load reads and parses the lock file for root. A missing lock file is not
// an error — it returns an empty Lockfile ready to be populated. A lock file
// with an unsupported schema version is a hard error.
func Load(root Root) (*Lockfile, error) {
	osRoot, err := root.osRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = osRoot.Close() }()

	data, err := osRoot.ReadFile(FileName)
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
		// Tolerate a hand-written lock file that omits the version key.
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
			l.Skills = slices.Delete(l.Skills, i, i+1)
			return true
		}
	}
	return false
}

// RemoveParentFromRequiredBy removes parent from every entry's RequiredBy
// list and returns the names of entries that lost their last parent and are
// not explicit — the candidates for cascade removal.
func (l *Lockfile) RemoveParentFromRequiredBy(parent string) []string {
	var cascadeCandidates []string
	for i := range l.Skills {
		entry := &l.Skills[i]
		if !slices.Contains(entry.RequiredBy, parent) {
			continue
		}
		entry.RequiredBy = slices.DeleteFunc(entry.RequiredBy, func(s string) bool { return s == parent })
		if len(entry.RequiredBy) == 0 {
			entry.RequiredBy = nil
			if !entry.Explicit {
				cascadeCandidates = append(cascadeCandidates, entry.Name)
			}
		}
	}
	return cascadeCandidates
}

// tmpFileName is the temporary file Save writes before renaming it into
// place. It is a fixed name (not a random suffix) because writes are already
// serialized by the caller's file lock; a fixed name also keeps Save's
// containment check on osRoot trivial.
const tmpFileName = ".toolhive.lock.tmp"

// Save writes the lock file into root atomically (temp file + rename), after
// validating it with the same rules [Load] enforces on read. This prevents a
// caller bug from writing a lock file that every subsequent Load/Update call
// would then hard-fail on, with no recovery path through this package.
// Callers that need read-modify-write atomicity across processes must use
// [UpsertEntry], [RemoveEntry], or [Update] instead of Load+Save.
func (l *Lockfile) Save(root Root) error {
	osRoot, err := root.osRoot()
	if err != nil {
		return err
	}
	defer func() { _ = osRoot.Close() }()

	if l.Version == 0 {
		l.Version = CurrentVersion
	}
	sortEntries(l.Skills)

	if err := validateLockfile(l); err != nil {
		return fmt.Errorf("refusing to save invalid lock file: %w", err)
	}

	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("marshaling lock file: %w", err)
	}

	// The lock file is committed to git and not sensitive; 0o644 matches any
	// other source file.
	if err := osRoot.WriteFile(tmpFileName, data, 0o644); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}
	if err := osRoot.Rename(tmpFileName, FileName); err != nil {
		_ = osRoot.Remove(tmpFileName)
		return fmt.Errorf("saving lock file: %w", err)
	}
	return nil
}

// UpsertEntry loads the lock file, upserts entry, and saves it back, all
// under a single file lock so concurrent installs cannot race on
// read-modify-write.
func UpsertEntry(root Root, entry Entry) error {
	return Update(root, func(lf *Lockfile) error {
		lf.Upsert(entry)
		return nil
	})
}

// RemoveEntry loads the lock file, removes the named entry if present, and
// saves it back, all under a single file lock. Removing an entry that does
// not exist is a no-op, not an error.
func RemoveEntry(root Root, name string) error {
	return Update(root, func(lf *Lockfile) error {
		if !lf.Remove(name) {
			return errSkipSave
		}
		return nil
	})
}

// Update loads the lock file, applies fn, and saves the result, all under a
// single file lock. If fn returns an error the lock file is left unchanged.
func Update(root Root, fn func(*Lockfile) error) error {
	path, err := root.Path()
	if err != nil {
		return err
	}
	return fileutils.WithFileLock(path, func() error {
		lf, err := Load(root)
		if err != nil {
			return err
		}
		if err := fn(lf); err != nil {
			if errors.Is(err, errSkipSave) {
				return nil
			}
			return err
		}
		return lf.Save(root)
	})
}

// errSkipSave signals from an Update callback that nothing changed and the
// save should be skipped without reporting an error.
var errSkipSave = errors.New("lockfile: no changes to save")

func sortEntries(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
}
