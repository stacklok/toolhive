// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"os"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// skillMDFileName is the well-known skill definition file every installed
// skill directory contains.
const skillMDFileName = "SKILL.md"

// computeContentDigest hashes the on-disk file set for an installed skill,
// for lock file integrity verification. Every client directory a skill is
// installed into is written from the same source, so any one of them is
// representative; the first client in the skill's Clients list is used.
func computeContentDigest(pathResolver skills.PathResolver, sk skills.InstalledSkill) (string, error) {
	dir, err := installedSkillDir(pathResolver, sk)
	if err != nil {
		return "", err
	}
	digest, err := lockfile.ContentDigestFromDir(dir)
	if err != nil {
		return "", fmt.Errorf("computing content digest: %w", err)
	}
	return digest, nil
}

// readSkillMD reads and parses SKILL.md from an installed skill's directory,
// for discovering toolhive.requires dependencies at install time. The
// artifact source (OCI labels or a git checkout) is not normalized to expose
// requires before extraction, but every installed skill has SKILL.md on disk
// once extraction succeeds, so reading it back is the uniform way to recover
// dependency declarations regardless of source type.
func readSkillMD(pathResolver skills.PathResolver, sk skills.InstalledSkill) (*skills.ParseResult, error) {
	dir, err := installedSkillDir(pathResolver, sk)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening skill directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	content, err := root.ReadFile(skillMDFileName)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", skillMDFileName, err)
	}
	parsed, err := skills.ParseSkillMD(content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", skillMDFileName, err)
	}
	return parsed, nil
}

// installedSkillDir resolves the filesystem path for one of sk's installed
// clients, used as a representative directory for reading back on-disk state.
func installedSkillDir(pathResolver skills.PathResolver, sk skills.InstalledSkill) (string, error) {
	if len(sk.Clients) == 0 {
		return "", fmt.Errorf("skill %q has no installed clients", sk.Metadata.Name)
	}
	dir, err := pathResolver.GetSkillPath(sk.Clients[0], sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	if err != nil {
		return "", fmt.Errorf("resolving skill path: %w", err)
	}
	return dir, nil
}
