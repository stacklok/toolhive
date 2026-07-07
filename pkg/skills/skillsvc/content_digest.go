// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"strings"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// contentDigestFromLayerData computes contentDigest from a tar.gz OCI layer.
func contentDigestFromLayerData(layerData []byte) (string, error) {
	tarData, err := ociskills.DecompressWithLimit(layerData, skills.MaxTotalExtractSize)
	if err != nil {
		return "", fmt.Errorf("decompressing layer: %w", err)
	}
	entries, err := ociskills.ExtractTarWithLimit(tarData, skills.MaxFileExtractSize)
	if err != nil {
		return "", fmt.Errorf("extracting tar: %w", err)
	}
	return contentDigestFromOCIEntries(entries)
}

func contentDigestFromOCIEntries(entries []ociskills.FileEntry) (string, error) {
	files := make([]lockfile.ContentFile, 0, len(entries))
	for _, e := range entries {
		files = append(files, lockfile.ContentFile{Path: e.Path, Content: e.Content})
	}
	return lockfile.ContentDigest(files)
}

func contentDigestFromGitFiles(files []gitresolver.FileEntry) (string, error) {
	contentFiles := make([]lockfile.ContentFile, 0, len(files))
	for _, f := range files {
		contentFiles = append(contentFiles, lockfile.ContentFile{Path: f.Path, Content: f.Content})
	}
	return lockfile.ContentDigest(contentFiles)
}

// requiresFromLayerData parses toolhive.requires from SKILL.md in a layer.
func requiresFromLayerData(layerData []byte) ([]skills.Dependency, error) {
	tarData, err := ociskills.DecompressWithLimit(layerData, skills.MaxTotalExtractSize)
	if err != nil {
		return nil, fmt.Errorf("decompressing layer: %w", err)
	}
	entries, err := ociskills.ExtractTarWithLimit(tarData, skills.MaxFileExtractSize)
	if err != nil {
		return nil, fmt.Errorf("extracting tar: %w", err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Path, "SKILL.md") || strings.HasSuffix(strings.ToLower(e.Path), "/skill.md") {
			parsed, parseErr := skills.ParseSkillMD(e.Content)
			if parseErr != nil {
				return nil, fmt.Errorf("parsing SKILL.md: %w", parseErr)
			}
			return parsed.Requires, nil
		}
	}
	return nil, nil
}
