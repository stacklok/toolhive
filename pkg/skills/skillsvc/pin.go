// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"regexp"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// fullCommitHashRe matches a full 40-character hex git commit hash.
var fullCommitHashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// buildPinnedReference returns an install Name that installs exactly the
// digest recorded in entry, regardless of what its resolved reference's tag
// or branch currently points to. Used by Sync to restore drifted skills.
func buildPinnedReference(entry lockfile.Entry) (string, error) {
	if gitresolver.IsGitReference(entry.ResolvedReference) {
		return pinGitReference(entry.ResolvedReference, entry.Digest)
	}
	return pinOCIReference(entry.ResolvedReference, entry.Digest)
}

// pinOCIReference rewrites ref to reference digest directly, dropping any tag.
func pinOCIReference(ref, digest string) (string, error) {
	parsed, err := nameref.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing pinned OCI reference %q: %w", ref, err)
	}
	return parsed.Context().Digest(digest).String(), nil
}

// pinGitReference rewrites a git:// reference to pin its ref (branch/tag) to
// the exact commit hash, preserving the host/repo and any skill subfolder.
func pinGitReference(resolvedRef, commitHash string) (string, error) {
	parsed, err := gitresolver.ParseGitReference(resolvedRef)
	if err != nil {
		return "", fmt.Errorf("parsing pinned git reference %q: %w", resolvedRef, err)
	}
	hostPath := strings.TrimPrefix(strings.TrimPrefix(parsed.URL, "https://"), "http://")
	pinned := "git://" + hostPath + "@" + commitHash
	if parsed.Path != "" {
		pinned += "#" + parsed.Path
	}
	return pinned, nil
}

// isImmutableSource reports whether source already pins an exact, unambiguous
// artifact — an OCI digest reference or a git reference at a full commit
// hash — such that re-resolving it can never surface newer content.
func isImmutableSource(source string) bool {
	if gitresolver.IsGitReference(source) {
		gitRef, err := gitresolver.ParseGitReference(source)
		if err != nil {
			return false
		}
		return fullCommitHashRe.MatchString(gitRef.Ref)
	}

	ref, isOCI, err := parseOCIReference(source)
	if err != nil || !isOCI {
		return false
	}
	_, isDigest := ref.(nameref.Digest)
	return isDigest
}
