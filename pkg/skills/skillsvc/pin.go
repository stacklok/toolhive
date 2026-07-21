// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// isImmutableSource reports whether a lock entry's source can never produce
// newer content: an OCI digest reference, or a git reference already pinned
// to a full commit hash. Upgrade reports these as not-upgradable rather than
// attempting to re-resolve them.
func isImmutableSource(entry lockfile.Entry) bool {
	if gitresolver.IsGitReference(entry.Source) {
		ref, err := gitresolver.ParseGitReference(entry.Source)
		return err == nil && isFullCommitHash(ref.Ref)
	}
	ref, err := nameref.ParseReference(entry.Source)
	if err != nil {
		return false
	}
	_, isDigest := ref.(nameref.Digest)
	return isDigest
}

func isFullCommitHash(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// buildPinnedReference returns the exact reference sync must install: entry's
// resolvedReference re-pointed at its pinned digest, never re-resolved from
// source. This is what makes sync a restore operation rather than an upgrade
// — installing this reference always yields entry's exact pinned content.
func buildPinnedReference(entry lockfile.Entry) (string, error) {
	if gitresolver.IsGitReference(entry.ResolvedReference) {
		return pinGitReference(entry)
	}
	return pinOCIReference(entry)
}

func pinOCIReference(entry lockfile.Entry) (string, error) {
	ref, err := nameref.ParseReference(entry.ResolvedReference)
	if err != nil {
		return "", fmt.Errorf("parsing resolvedReference %q: %w", entry.ResolvedReference, err)
	}
	return ref.Context().String() + "@" + entry.Digest, nil
}

func pinGitReference(entry lockfile.Entry) (string, error) {
	gitRef, err := gitresolver.ParseGitReference(entry.ResolvedReference)
	if err != nil {
		return "", fmt.Errorf("parsing resolvedReference %q: %w", entry.ResolvedReference, err)
	}
	hostAndPath := strings.TrimPrefix(strings.TrimPrefix(gitRef.URL, "https://"), "http://")
	pinned := "git://" + hostAndPath + "@" + entry.Digest
	if gitRef.Path != "" {
		pinned += "#" + gitRef.Path
	}
	return pinned, nil
}
