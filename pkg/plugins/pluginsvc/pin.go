// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

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

// repositoryMoved reports whether two resolved references point at different
// repositories, as opposed to differing only by tag. A tag move within one
// repository is the normal way a mutable source advances; a repository move
// means the artifact now comes from somewhere else, which is the case the
// ref-change guard exists to catch.
//
// Both the registry and the repository path are compared, so ghcr.io ->
// another registry, or a different org or path on the same registry, all
// count as a move.
func repositoryMoved(oldRef, newRef string) bool {
	if oldRef == newRef {
		return false
	}
	oldRepo, ok := referenceRepository(oldRef)
	if !ok {
		return true
	}
	newRepo, ok := referenceRepository(newRef)
	if !ok {
		return true
	}
	return oldRepo != newRepo
}

// referenceRepository returns the registry and repository portion of an OCI
// reference, dropping any tag or digest. It reports false for git references
// and for anything it cannot parse, so callers fall back to treating the
// reference as moved rather than silently equating two unlike sources.
func referenceRepository(ref string) (string, bool) {
	if gitresolver.IsGitReference(ref) {
		return "", false
	}
	parsed, err := nameref.ParseReference(ref)
	if err != nil {
		return "", false
	}
	return parsed.Context().String(), true
}

// isFullCommitHash accepts both hex cases: the sibling git resolver does
// too, and classifying an uppercase-pinned source as mutable would
// needlessly re-clone it on every upgrade despite the pin being immutable.
func isFullCommitHash(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isLocalStorePin reports whether entry pins a local OCI-store artifact by
// digest: a plain plugin Source, no restorable ResolvedReference, and an OCI
// digest. Sync restores these by loading the exact digest from the local
// store rather than reinterpreting Source as a Docker Hub reference.
func isLocalStorePin(entry lockfile.Entry) bool {
	if entry.ResolvedReference != "" || entry.Digest == "" {
		return false
	}
	if gitresolver.IsGitReference(entry.Source) {
		return false
	}
	_, isOCI, err := parseOCIReference(entry.Source)
	if err != nil || isOCI {
		return false
	}
	return true
}

// buildPinnedReference returns the exact reference sync must install: entry's
// resolvedReference re-pointed at its pinned digest, never re-resolved from
// source. This is what makes sync a restore operation rather than an upgrade
// — installing this reference always yields entry's exact pinned content.
// Local-store pins (empty ResolvedReference) are handled by
// reinstallLocalStorePin instead of this helper.
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
