// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/skills"
)

// ErrUnsupportedVersion indicates the lock file version is newer than this build supports.
var ErrUnsupportedVersion = errors.New("unsupported lock file version")

// validateLockfile checks every entry and cross-references requiredBy links.
func validateLockfile(lf *Lockfile) error {
	if lf.Version != CurrentVersion {
		return fmt.Errorf("%w: version %d (upgrade thv to read this lock file)", ErrUnsupportedVersion, lf.Version)
	}

	names := make(map[string]struct{}, len(lf.Skills))
	for _, entry := range lf.Skills {
		if err := validateEntry(entry); err != nil {
			return err
		}
		names[entry.Name] = struct{}{}
	}

	for _, entry := range lf.Skills {
		for _, parent := range entry.RequiredBy {
			if err := skills.ValidateSkillName(parent); err != nil {
				return fmt.Errorf("entry %q: requiredBy parent %q: %w", entry.Name, parent, err)
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
	if entry.Unsigned && entry.Provenance != nil {
		return fmt.Errorf("entry %q: cannot set both unsigned and provenance", entry.Name)
	}
	if entry.Provenance != nil {
		if err := validateProvenance(entry.Name, entry.Provenance); err != nil {
			return err
		}
	}
	return nil
}

func validateProvenance(entryName string, p *Provenance) error {
	if p.SignerIdentity == "" {
		return fmt.Errorf("entry %q: provenance.signerIdentity is required", entryName)
	}
	if p.CertIssuer == "" {
		return fmt.Errorf("entry %q: provenance.certIssuer is required", entryName)
	}
	return nil
}

func validateDigest(d string) error {
	if strings.HasPrefix(d, "sha256:") {
		hexPart := strings.TrimPrefix(d, "sha256:")
		if len(hexPart) < 12 {
			return fmt.Errorf("sha256 digest too short")
		}
		return nil
	}
	// Git commit hash (40 hex chars) or abbreviated.
	if len(d) >= 7 && len(d) <= 64 {
		for _, c := range d {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return fmt.Errorf("invalid digest format %q", d)
			}
		}
		return nil
	}
	return fmt.Errorf("invalid digest format %q", d)
}

func validateContentDigest(d string) error {
	if !strings.HasPrefix(d, ContentDigestPrefix) {
		return fmt.Errorf("must start with %q", ContentDigestPrefix)
	}
	hexPart := strings.TrimPrefix(d, ContentDigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("expected 64 hex characters, got %d", len(hexPart))
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("invalid hex character %q", c)
		}
	}
	return nil
}
