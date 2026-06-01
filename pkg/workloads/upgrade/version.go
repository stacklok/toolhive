// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"golang.org/x/mod/semver"
)

// tagComparison is the result of comparing a workload's current image tag
// against a candidate registry image tag.
type tagComparison int

const (
	// comparisonSameOrOlder means the candidate is the same as, or older than,
	// the current image (no upgrade available).
	comparisonSameOrOlder tagComparison = iota

	// comparisonNewer means the candidate is strictly newer than the current
	// image (an upgrade is available).
	comparisonNewer

	// comparisonUndecidable means the two references cannot be meaningfully
	// compared (different repositories, a "latest" tag, or non-semver tags that
	// are not equal). The accompanying reason explains why.
	comparisonUndecidable
)

// latestTag is the floating tag that defers to the registry and therefore
// cannot be compared for ordering.
const latestTag = "latest"

// compareImageTags compares a workload's current image against a candidate
// registry image and reports whether the candidate represents an upgrade.
//
// Both references are parsed and their repositories are compared using the
// host-normalized fully-qualified name. The comparison is intentionally
// conservative: any situation it cannot reason about confidently (parse
// failure, differing repositories, a "latest" tag on either side, or
// non-equal non-semver tags) yields comparisonUndecidable with a reason rather
// than a guess. Equal semver or equal literal tags yield comparisonSameOrOlder.
func compareImageTags(current, candidate string) (tagComparison, string) {
	curRepo, curTag, err := splitRepoTag(current)
	if err != nil {
		return comparisonUndecidable, fmt.Sprintf("cannot parse current image %q: %v", current, err)
	}
	candRepo, candTag, err := splitRepoTag(candidate)
	if err != nil {
		return comparisonUndecidable, fmt.Sprintf("cannot parse candidate image %q: %v", candidate, err)
	}

	if curRepo != candRepo {
		return comparisonUndecidable,
			fmt.Sprintf("repository differs: current %q vs candidate %q", curRepo, candRepo)
	}

	if curTag == latestTag || candTag == latestTag {
		return comparisonUndecidable, "image uses the \"latest\" tag, which defers to the registry and cannot be ordered"
	}

	curSemver := normalizeSemver(curTag)
	candSemver := normalizeSemver(candTag)
	if semver.IsValid(curSemver) && semver.IsValid(candSemver) {
		switch semver.Compare(semver.Canonical(curSemver), semver.Canonical(candSemver)) {
		case -1:
			return comparisonNewer, ""
		default:
			// Equal or current is newer: nothing to upgrade to.
			return comparisonSameOrOlder, ""
		}
	}

	// Fallback for non-semver tags: only an exact match is decidable.
	if curTag == candTag {
		return comparisonSameOrOlder, ""
	}
	return comparisonUndecidable,
		fmt.Sprintf("tags are not comparable: current %q vs candidate %q", curTag, candTag)
}

// splitRepoTag parses an image reference and returns its host-normalized
// fully-qualified repository name and tag string.
//
// It uses name.ParseReference and asserts the result to name.Tag, then reads
// the repository via Context().Name() and the tag via TagStr() (not
// Identifier(), which returns a digest for digest references). A reference that
// is not a tag (e.g. a bare digest) is rejected so the caller can treat it as
// undecidable rather than mis-compare a digest against a tag.
func splitRepoTag(ref string) (repo, tag string, err error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", "", err
	}
	tagged, ok := parsed.(name.Tag)
	if !ok {
		return "", "", fmt.Errorf("reference %q is not a tag", ref)
	}
	return tagged.Context().Name(), tagged.TagStr(), nil
}

// normalizeSemver prepends a "v" to a tag that looks like a bare semantic
// version (e.g. "1.2.3" -> "v1.2.3") so it can be validated and compared by
// golang.org/x/mod/semver, which requires the "v" prefix. Tags that already
// start with "v", or that are not semver-shaped, are returned unchanged.
func normalizeSemver(tag string) string {
	if tag == "" {
		return tag
	}
	if tag[0] == 'v' || tag[0] == 'V' {
		return tag
	}
	if tag[0] >= '0' && tag[0] <= '9' {
		return "v" + tag
	}
	return tag
}
