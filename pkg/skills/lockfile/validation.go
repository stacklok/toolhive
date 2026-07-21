// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/skills"
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

// validateLockfile checks the schema version, every entry, and
// cross-references requiredBy links against the set of known entries.
func validateLockfile(lf *Lockfile) error {
	if lf.Version != CurrentVersion {
		return fmt.Errorf("%w: version %d (upgrade thv to read this lock file)", ErrUnsupportedVersion, lf.Version)
	}

	names := make(map[string]struct{}, len(lf.Skills))
	for _, entry := range lf.Skills {
		if err := validateEntry(entry); err != nil {
			return err
		}
		if _, dup := names[entry.Name]; dup {
			return fmt.Errorf("duplicate entry %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
	}

	for _, entry := range lf.Skills {
		for _, parent := range entry.RequiredBy {
			if parent == entry.Name {
				return fmt.Errorf("entry %q: requiredBy references itself", entry.Name)
			}
			if _, ok := names[parent]; !ok {
				return fmt.Errorf("entry %q: requiredBy references unknown parent %q", entry.Name, parent)
			}
		}
	}
	return nil
}

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
