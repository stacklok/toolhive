// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// provenanceFileName is the name of the JSON sidecar file, stored at the root
// of the OCI skills store, that records which tags were produced by
// `thv skill build`. Tags created by OCI pulls (install, content preview) are
// intentionally absent so they remain invisible to ListBuilds while their
// blobs stay in the OCI store as a cache.
const provenanceFileName = "builds.json"

// provenanceEntry records a single locally-built skill tag.
type provenanceEntry struct {
	Tag       string    `json:"tag"`
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"createdAt"`
}

// provenanceFile is the on-disk JSON document layout.
type provenanceFile struct {
	Entries []provenanceEntry `json:"entries"`
}

// buildProvenance tracks which OCI-store tags were produced by a local build.
// It is an in-process, file-backed index: callers hold a mutex while reading
// or writing the JSON file. Single-process semantics are sufficient since the
// skills service is not shared across processes.
type buildProvenance struct {
	mu   sync.Mutex
	path string
}

// newBuildProvenance constructs a provenance index rooted at the given OCI
// store root. The file itself is created lazily on first write.
func newBuildProvenance(storeRoot string) *buildProvenance {
	return &buildProvenance{
		path: filepath.Join(storeRoot, provenanceFileName),
	}
}

// Exists reports whether the provenance file has been created on disk. Used
// by the migration path to decide whether to grandfather existing tags.
func (p *buildProvenance) Exists() (bool, error) {
	if p == nil {
		return false, nil
	}
	_, err := os.Stat(p.path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat provenance file: %w", err)
}

// Record adds or updates the entry for tag. The timestamp on updates is
// refreshed so "last built" reflects the most recent build.
func (p *buildProvenance) Record(tag, digest string) error {
	if p == nil {
		return errors.New("build provenance is not configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	doc, err := p.loadLocked()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	found := false
	for i := range doc.Entries {
		if doc.Entries[i].Tag == tag {
			doc.Entries[i].Digest = digest
			doc.Entries[i].CreatedAt = now
			found = true
			break
		}
	}
	if !found {
		doc.Entries = append(doc.Entries, provenanceEntry{
			Tag:       tag,
			Digest:    digest,
			CreatedAt: now,
		})
	}

	return p.saveLocked(doc)
}

// Forget removes the entry for tag, if present. A missing tag is not an error.
func (p *buildProvenance) Forget(tag string) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	doc, err := p.loadLocked()
	if err != nil {
		return err
	}

	kept := doc.Entries[:0]
	removed := false
	for _, e := range doc.Entries {
		if e.Tag == tag {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	doc.Entries = kept
	return p.saveLocked(doc)
}

// List returns a copy of all recorded entries.
func (p *buildProvenance) List() ([]provenanceEntry, error) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	doc, err := p.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]provenanceEntry, len(doc.Entries))
	copy(out, doc.Entries)
	return out, nil
}

// Has reports whether tag has a recorded entry.
func (p *buildProvenance) Has(tag string) (bool, error) {
	if p == nil {
		return false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	doc, err := p.loadLocked()
	if err != nil {
		return false, err
	}
	for _, e := range doc.Entries {
		if e.Tag == tag {
			return true, nil
		}
	}
	return false, nil
}

// Seed replaces the on-disk state with the given entries. Used by the one-shot
// migration path to grandfather pre-existing tags into the provenance index.
// The provenance file is always (re)written, even when entries is empty, so
// subsequent calls to Exists report true and the migration does not re-run.
func (p *buildProvenance) Seed(entries []provenanceEntry) error {
	if p == nil {
		return errors.New("build provenance is not configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	doc := &provenanceFile{Entries: make([]provenanceEntry, 0, len(entries))}
	doc.Entries = append(doc.Entries, entries...)
	return p.saveLocked(doc)
}

// loadLocked reads and parses the provenance file. Missing files yield an
// empty document. Callers must hold p.mu.
func (p *buildProvenance) loadLocked() (*provenanceFile, error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &provenanceFile{}, nil
		}
		return nil, fmt.Errorf("reading provenance file: %w", err)
	}
	if len(data) == 0 {
		return &provenanceFile{}, nil
	}
	var doc provenanceFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing provenance file: %w", err)
	}
	return &doc, nil
}

// saveLocked atomically writes the provenance file via temp-file + rename.
// Callers must hold p.mu.
func (p *buildProvenance) saveLocked(doc *provenanceFile) error {
	if doc.Entries == nil {
		doc.Entries = []provenanceEntry{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling provenance file: %w", err)
	}

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating provenance directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, provenanceFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp provenance file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("writing temp provenance file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("syncing temp provenance file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp provenance file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp provenance file: %w", err)
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming provenance file: %w", err)
	}
	return nil
}
