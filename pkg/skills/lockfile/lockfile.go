// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package lockfile manages the project-level lock file (toolhive.lock.yaml).
// The lock file pins the exact name, version, source, and digests of every
// project-scoped skill and plugin install so a team can restore
// ("thv skill sync" / "thv ai-plugin sync") or refresh
// ("thv skill upgrade" / "thv ai-plugin upgrade") the pinned state on any
// machine. RFC THV-0080 owns the version and skills: keys; plugins: is the
// sibling key ratified by the AI-plugin lock track.
//
// All filesystem access goes through [Root], a capability type that can only
// be constructed from a validated project root. Root confines every read and
// write to that directory using [os.Root] (OS-enforced containment, not just
// string validation), so no function in this package can be made to open a
// path outside the project root regardless of what name is requested.
package lockfile

import (
	"crypto/rand"
	"encoding/hex"
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

// FileName is the name of the project-level lock file, written to the
// project root alongside .git.
const FileName = "toolhive.lock.yaml"

// CurrentVersion is the schema version written to new lock files. Loading a
// lock file with a different version is a hard error, never a silent partial
// parse.
const CurrentVersion = 1

// Entry represents a single pinned skill or plugin installation in the lock file.
type Entry struct {
	// Name is the artifact's unique name within its key (skills: or plugins:).
	// The same name may appear once under each key.
	Name string `yaml:"name"`
	// Version is the artifact's declared version, if any (SKILL.md frontmatter
	// or .claude-plugin/plugin.json).
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
	// file set (skill directory, or the canonical plugin tree ExtractPlugin
	// writes), used for on-disk integrity verification.
	ContentDigest string `yaml:"contentDigest,omitempty"`
	// Provenance records the verified signer identity of the installed
	// artifact — the trust-on-first-use anchor later installs, syncs, and
	// upgrades are checked against. Nil for entries recorded before
	// verification existed and for unsigned entries (see Unsigned); an
	// entry never carries both.
	Provenance *Provenance `yaml:"provenance,omitempty"`
	// Unsigned is true when the artifact carried no signature and the user
	// explicitly accepted that with --allow-unsigned. It is a recorded
	// trust decision, not an omission: replacing an unsigned entry with a
	// signed one clears it, and a signed entry never becomes unsigned
	// without the same explicit flag.
	Unsigned bool `yaml:"unsigned,omitempty"`
	// RequiredBy lists parent names in the same key (skills: or plugins:)
	// for transitively materialized dependencies. Plugin v1 does not
	// populate this; the field is reserved for when plugin requires are
	// materialized.
	RequiredBy []string `yaml:"requiredBy,omitempty"`
	// Explicit is true when the user directly installed this artifact;
	// explicit entries are exempt from cascade removal when RequiredBy
	// becomes empty.
	Explicit bool `yaml:"explicit,omitempty"`
	// Extra round-trips fields this binary does not know about, so a
	// Load→modify→Save cycle by an older binary preserves rather than strips
	// them. It applies per entry: an entry this binary rewrites (reinstall,
	// upgrade) is built fresh, so its unknown fields — which described the
	// previous install — are intentionally dropped along with it.
	Extra map[string]any `yaml:",inline"`
}

// Provenance is the Sigstore signer identity recorded for a verified entry.
type Provenance struct {
	// SignerIdentity is the certificate subject identity: for GitHub
	// Actions certificates, the workflow path relative to the repository;
	// otherwise the certificate SAN verbatim (a URI, email, or SPIFFE ID).
	SignerIdentity string `yaml:"signerIdentity"`
	// CertIssuer is the OIDC issuer that authenticated the signer.
	CertIssuer string `yaml:"certIssuer"`
	// RepositoryURI is the source repository from the Fulcio certificate
	// extensions, when present.
	RepositoryURI string `yaml:"repositoryUri,omitempty"`
	// SigstoreURL is the Sigstore instance the signature chains to.
	SigstoreURL string `yaml:"sigstoreUrl,omitempty"`
	// Provisional marks provenance whose verification has a documented
	// gap — currently git-commit signatures, verified for signature and
	// certificate chain but without transparency-log proof of signing
	// time. Visible in lock diffs so reduced assurance is never silent;
	// removed when the gap closes.
	Provisional bool `yaml:"provisional,omitempty"`
}

// Lockfile is the parsed contents of a project's toolhive.lock.yaml.
type Lockfile struct {
	// Version is the lock file schema version.
	Version int `yaml:"version"`
	// Skills is the set of pinned skill installations, sorted by name for
	// stable diffs.
	Skills []Entry `yaml:"skills,omitempty"`
	// Plugins is the set of pinned plugin installations, sorted by name
	// for stable diffs. Validated as its own name/requiredBy graph, so a
	// skill and a plugin may share a name.
	Plugins []Entry `yaml:"plugins,omitempty"`
	// Extra round-trips unknown top-level fields, mirroring Entry.Extra.
	Extra map[string]any `yaml:",inline"`
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
	sortEntries(lf.Plugins)
	if err := validateLockfile(&lf); err != nil {
		return nil, err
	}
	return &lf, nil
}

// Get returns the skills: entry for name, if present.
func (l *Lockfile) Get(name string) (Entry, bool) {
	return getNamed(l.Skills, name)
}

// GetPlugin returns the plugins: entry for name, if present.
func (l *Lockfile) GetPlugin(name string) (Entry, bool) {
	return getNamed(l.Plugins, name)
}

// Upsert inserts or replaces the skills: entry with a matching name, keeping
// the slice sorted by name for stable diffs. It does not touch plugins:.
func (l *Lockfile) Upsert(entry Entry) {
	upsertNamed(&l.Skills, entry)
}

// UpsertPlugin inserts or replaces the plugins: entry with a matching name,
// keeping the slice sorted by name for stable diffs. It does not touch skills:.
func (l *Lockfile) UpsertPlugin(entry Entry) {
	upsertNamed(&l.Plugins, entry)
}

// Remove deletes the skills: entry with the given name, if present. Reports
// whether an entry was removed.
func (l *Lockfile) Remove(name string) bool {
	return removeNamed(&l.Skills, name)
}

// RemovePlugin deletes the plugins: entry with the given name, if present.
// Reports whether an entry was removed.
func (l *Lockfile) RemovePlugin(name string) bool {
	return removeNamed(&l.Plugins, name)
}

// RemoveParentFromRequiredBy removes parent from every skills: entry's
// RequiredBy list and returns the names of entries that lost their last
// parent and are not explicit — the candidates for cascade removal. It
// does not touch plugins:.
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
// tmpFileName returns a per-call temporary file name for Save's atomic
// write. The random suffix means two concurrent direct Save calls (which,
// unlike Update/UpsertEntry/RemoveEntry, hold no file lock) cannot write
// to, rename, or delete each other's temp file — last rename still wins,
// but neither can promote or destroy a half-written file.
func tmpFileName() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating temp file suffix: %w", err)
	}
	return ".toolhive.lock." + hex.EncodeToString(buf[:]) + ".tmp", nil
}

// Save writes the lock file into root atomically (temp file + rename), after
// validating it with the same rules [Load] enforces on read. This prevents a
// caller bug from writing a lock file that every subsequent Load/Update call
// would then hard-fail on, with no recovery path through this package.
// Callers that need read-modify-write atomicity across processes must use
// [UpsertEntry], [RemoveEntry], or [Update] instead of Load+Save — Save alone
// is write-atomic but does nothing to serialize concurrent read-modify-write
// cycles.
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
	sortEntries(l.Plugins)

	if err := validateLockfile(l); err != nil {
		return fmt.Errorf("refusing to save invalid lock file: %w", err)
	}

	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("marshaling lock file: %w", err)
	}

	tmpName, err := tmpFileName()
	if err != nil {
		return err
	}
	// The lock file is committed to git and not sensitive; 0o644 matches any
	// other source file.
	if err := osRoot.WriteFile(tmpName, data, 0o644); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}
	if err := osRoot.Rename(tmpName, FileName); err != nil {
		_ = osRoot.Remove(tmpName)
		return fmt.Errorf("saving lock file: %w", err)
	}
	return nil
}

// UpsertEntry loads the lock file, upserts a skills: entry, and saves it
// back, all under a single file lock so concurrent installs cannot race on
// read-modify-write.
func UpsertEntry(root Root, entry Entry) error {
	return Update(root, func(lf *Lockfile) error {
		lf.Upsert(entry)
		return nil
	})
}

// UpsertPluginEntry loads the lock file, upserts a plugins: entry, and
// saves it back, all under a single file lock so concurrent installs cannot
// race on read-modify-write.
func UpsertPluginEntry(root Root, entry Entry) error {
	return Update(root, func(lf *Lockfile) error {
		lf.UpsertPlugin(entry)
		return nil
	})
}

// RemoveEntry loads the lock file, removes the named skills: entry if
// present, and saves it back, all under a single file lock. Removing an
// entry that does not exist is a no-op, not an error.
func RemoveEntry(root Root, name string) error {
	return Update(root, func(lf *Lockfile) error {
		if !lf.Remove(name) {
			return errSkipSave
		}
		return nil
	})
}

// RemovePluginEntry loads the lock file, removes the named plugins: entry
// if present, and saves it back, all under a single file lock. Removing an
// entry that does not exist is a no-op, not an error.
func RemovePluginEntry(root Root, name string) error {
	return Update(root, func(lf *Lockfile) error {
		if !lf.RemovePlugin(name) {
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

func getNamed(entries []Entry, name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

func upsertNamed(entries *[]Entry, entry Entry) {
	for i := range *entries {
		if (*entries)[i].Name == entry.Name {
			(*entries)[i] = entry
			return
		}
	}
	*entries = append(*entries, entry)
	sortEntries(*entries)
}

func removeNamed(entries *[]Entry, name string) bool {
	for i, e := range *entries {
		if e.Name == name {
			*entries = slices.Delete(*entries, i, i+1)
			return true
		}
	}
	return false
}
