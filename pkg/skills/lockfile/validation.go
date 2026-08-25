// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// ErrUnsupportedVersion indicates the lock file schema version is not
// supported by this build.
var ErrUnsupportedVersion = errors.New("unsupported lock file version")

const (
	// ContentDigestPrefix is the prefix for contentDigest values in the lock file.
	ContentDigestPrefix = "sha256:"

	sha256HexLength = 64
	sha1HexLength   = 40
)

// validateLockfile checks the schema version and every entry. skills: and
// plugins: are validated as independent name/requiredBy graphs, so a skill
// and a plugin may share a name, and a requiredBy parent must live in the
// same key as the entry that names it.
func validateLockfile(lf *Lockfile) error {
	if lf.Version != CurrentVersion {
		return fmt.Errorf("%w: version %d (upgrade thv to read this lock file)", ErrUnsupportedVersion, lf.Version)
	}
	if err := validateEntryGraph(lf.Skills); err != nil {
		return err
	}
	if err := validateEntryGraph(lf.Plugins); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	return nil
}

// validateEntryGraph checks uniqueness, per-entry fields, requiredBy
// cross-references, and cycles within one lock-file key.
func validateEntryGraph(entries []Entry) error {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateEntry(entry); err != nil {
			return err
		}
		if _, dup := names[entry.Name]; dup {
			return fmt.Errorf("duplicate entry %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
	}

	for _, entry := range entries {
		for _, parent := range entry.RequiredBy {
			if parent == entry.Name {
				return fmt.Errorf("entry %q: requiredBy references itself", entry.Name)
			}
			if _, ok := names[parent]; !ok {
				return fmt.Errorf("entry %q: requiredBy references unknown parent %q", entry.Name, parent)
			}
		}
	}

	if cycle := findRequiredByCycle(entries); len(cycle) > 0 {
		return fmt.Errorf("requiredBy cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// findRequiredByCycle detects a cycle in the requiredBy graph and returns
// one such cycle path, or nil. Normal installs can never produce a cycle
// (the install-time depState active stack rejects them), but a hand-edited or badly
// merge-resolved lock file can — and a ring of mutually-required,
// non-explicit entries would then be impossible to ever cascade-remove, so
// it is rejected at validation instead of persisting silently.
func findRequiredByCycle(entries []Entry) []string {
	requiredBy := make(map[string][]string, len(entries))
	for _, e := range entries {
		requiredBy[e.Name] = e.RequiredBy
	}

	const (
		unvisited = 0
		inStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(entries))

	var visit func(name string, path []string) []string
	visit = func(name string, path []string) []string {
		state[name] = inStack
		path = append(path, name)
		for _, parent := range requiredBy[name] {
			switch state[parent] {
			case inStack:
				// Trim the path to start at the cycle entry point.
				for i, n := range path {
					if n == parent {
						return append(path[i:], parent)
					}
				}
			case unvisited:
				if cycle := visit(parent, path); cycle != nil {
					return cycle
				}
			}
		}
		state[name] = done
		return nil
	}

	for _, e := range entries {
		if state[e.Name] == unvisited {
			if cycle := visit(e.Name, nil); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// maxReferenceLength bounds ResolvedReference: longer than any legitimate
// OCI reference or git URL, short enough to stop megabyte-scale garbage
// from a corrupted or hostile lock file reaching the fetch path.
const maxReferenceLength = 512

func validateEntry(entry Entry) error {
	if err := skills.ValidateSkillName(entry.Name); err != nil {
		return fmt.Errorf("entry name: %w", err)
	}
	if entry.Source == "" {
		return fmt.Errorf("entry %q: source is required", entry.Name)
	}
	if entry.Digest == "" {
		return fmt.Errorf("entry %q: digest is required", entry.Name)
	}
	if err := validateDigest(entry.Digest); err != nil {
		return fmt.Errorf("entry %q: digest: %w", entry.Name, err)
	}
	if entry.ContentDigest != "" {
		if err := validateContentDigest(entry.ContentDigest); err != nil {
			return fmt.Errorf("entry %q: contentDigest: %w", entry.Name, err)
		}
	}
	if entry.ResolvedReference != "" {
		if err := validateResolvedReference(entry.ResolvedReference); err != nil {
			return fmt.Errorf("entry %q: resolvedReference: %w", entry.Name, err)
		}
	}
	if entry.Provenance != nil && entry.Unsigned {
		return fmt.Errorf("entry %q: provenance and unsigned are mutually exclusive"+
			" — an entry is either a verified signature or a recorded unsigned exception", entry.Name)
	}
	if entry.Provenance != nil {
		if err := validateProvenance(entry.Provenance); err != nil {
			return fmt.Errorf("entry %q: provenance: %w", entry.Name, err)
		}
	}
	return nil
}

// validateProvenance syntactically constrains a provenance block. Like
// resolvedReference, these values are hand-editable and feed the identity
// policy that future verifications are checked against, so they must be
// well-formed graphic strings of bounded length. Validation is purely
// syntactic — whether the identity is trustworthy is the verifier's job.
func validateProvenance(p *Provenance) error {
	if p.SignerIdentity == "" {
		return errors.New("signerIdentity is required")
	}
	if p.CertIssuer == "" {
		return errors.New("certIssuer is required")
	}
	fields := map[string]string{
		"signerIdentity":    p.SignerIdentity,
		"certIssuer":        p.CertIssuer,
		"repositoryUri":     p.RepositoryURI,
		"repositoryRef":     p.RepositoryRef,
		"runnerEnvironment": p.RunnerEnvironment,
		"sigstoreUrl":       p.SigstoreURL,
	}
	for name, value := range fields {
		if value == "" {
			continue
		}
		if len(value) > maxReferenceLength {
			return fmt.Errorf("%s exceeds %d characters", name, maxReferenceLength)
		}
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("%s has leading or trailing whitespace", name)
		}
		for _, r := range value {
			if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
				return fmt.Errorf("%s contains non-graphic or whitespace character %q", name, r)
			}
		}
	}
	return nil
}

// validateResolvedReference syntactically constrains the resolvedReference
// field. Sync fetches from this value without re-resolving Source, and the
// lock file is hand-editable, so a value that is not a plausible git:// or
// OCI reference must never reach the fetch path. Validation is purely
// syntactic — no network access, no allow-list policy.
func validateResolvedReference(ref string) error {
	if len(ref) > maxReferenceLength {
		return fmt.Errorf("exceeds %d characters", maxReferenceLength)
	}
	if strings.TrimSpace(ref) != ref {
		return errors.New("has leading or trailing whitespace")
	}
	for _, r := range ref {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return fmt.Errorf("contains non-graphic or whitespace character %q", r)
		}
	}
	if gitresolver.IsGitReference(ref) {
		if _, err := gitresolver.ParseGitReference(ref); err != nil {
			return fmt.Errorf("invalid git reference: %w", err)
		}
		return nil
	}
	// StrictValidation requires an explicit tag or digest, matching what the
	// install path records (qualifiedOCIRef always includes one) — and,
	// unlike weak validation, rejects URL-shaped strings such as
	// "http://169.254.169.254/…" that must never reach the fetch path.
	if _, err := nameref.ParseReference(ref, nameref.StrictValidation); err != nil {
		return fmt.Errorf("not a valid git:// or OCI reference: %w", err)
	}
	return nil
}

// validateDigest accepts the two pin formats the lock file records: an OCI
// manifest digest ("sha256:" + 64 hex chars) or a full git commit hash
// (40 hex chars for SHA-1 repositories, 64 for SHA-256 repositories).
// Abbreviated digests are rejected: a truncated pin weakens the guarantee
// the lock file exists to provide.
func validateDigest(d string) error {
	if hexPart, ok := strings.CutPrefix(d, ContentDigestPrefix); ok {
		if err := validateHex(hexPart, sha256HexLength); err != nil {
			return fmt.Errorf("OCI digest: %w", err)
		}
		return nil
	}
	if len(d) != sha1HexLength && len(d) != sha256HexLength {
		return fmt.Errorf("expected %q + 64 hex chars or a full git commit hash, got %q", ContentDigestPrefix, d)
	}
	if err := validateHex(d, len(d)); err != nil {
		return fmt.Errorf("git commit hash: %w", err)
	}
	return nil
}

func validateContentDigest(d string) error {
	hexPart, ok := strings.CutPrefix(d, ContentDigestPrefix)
	if !ok {
		return fmt.Errorf("must start with %q", ContentDigestPrefix)
	}
	return validateHex(hexPart, sha256HexLength)
}

func validateHex(s string, wantLen int) error {
	if len(s) != wantLen {
		return fmt.Errorf("expected %d hex characters, got %d", wantLen, len(s))
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("invalid hex character %q", c)
		}
	}
	return nil
}
