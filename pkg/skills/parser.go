// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// frontmatterDelimiter is the YAML frontmatter delimiter.
var frontmatterDelimiter = []byte("---")

// MaxFrontmatterSize is the maximum size of frontmatter content in bytes.
// This prevents YAML parsing attacks (e.g., billion laughs).
const MaxFrontmatterSize = 64 * 1024 // 64KB

// MaxDependencies is the maximum number of dependencies allowed per skill.
// This prevents resource exhaustion from malicious or misconfigured skills.
const MaxDependencies = 100

// ErrInvalidFrontmatter indicates that the SKILL.md frontmatter is malformed.
var ErrInvalidFrontmatter = errors.New("invalid frontmatter")

// ParseSkillMD parses a SKILL.md file and extracts frontmatter and body.
func ParseSkillMD(content []byte) (*ParseResult, error) {
	fm, body, err := extractFrontmatter(content)
	if err != nil {
		return nil, err
	}

	requires, err := toDependencies(fm.Requires)
	if err != nil {
		return nil, fmt.Errorf("parsing requires: %w", err)
	}

	return &ParseResult{
		Name:          fm.Name,
		Description:   fm.Description,
		Version:       fm.Version,
		AllowedTools:  fm.AllowedTools,
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		Requires:      requires,
		Body:          body,
	}, nil
}

// extractFrontmatter extracts YAML frontmatter from content.
// Returns the parsed frontmatter and the remaining body content.
func extractFrontmatter(content []byte) (*SkillFrontmatter, []byte, error) {
	content = bytes.TrimSpace(content)

	if !bytes.HasPrefix(content, frontmatterDelimiter) {
		return nil, nil, ErrInvalidFrontmatter
	}

	// Skip opening delimiter and optional newline
	rest := content[len(frontmatterDelimiter):]
	rest = bytes.TrimPrefix(rest, []byte("\n"))

	// Limit the search scope for the closing delimiter to avoid scanning
	// arbitrarily large inputs. Search within MaxFrontmatterSize + delimiter
	// bytes for the closing "---"; if not found, the frontmatter is too large.
	searchLimit := rest
	maxSearch := MaxFrontmatterSize + len(frontmatterDelimiter)
	if len(searchLimit) > maxSearch {
		searchLimit = searchLimit[:maxSearch]
	}

	endIdx := bytes.Index(searchLimit, frontmatterDelimiter)
	if endIdx == -1 {
		if len(rest) > MaxFrontmatterSize {
			return nil, nil, fmt.Errorf("%w: frontmatter exceeds maximum size of %d bytes",
				ErrInvalidFrontmatter, MaxFrontmatterSize)
		}
		return nil, nil, ErrInvalidFrontmatter
	}

	frontmatterBytes := rest[:endIdx]
	body := rest[endIdx+len(frontmatterDelimiter):]
	body = bytes.TrimPrefix(body, []byte("\n"))

	var fm SkillFrontmatter
	if err := yaml.Unmarshal(frontmatterBytes, &fm); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidFrontmatter, err)
	}

	return &fm, body, nil
}

// toDependencies converts a list of OCI reference strings to Dependencies.
func toDependencies(refs []string) ([]Dependency, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	if len(refs) > MaxDependencies {
		return nil, fmt.Errorf("too many dependencies: %d (max %d)", len(refs), MaxDependencies)
	}

	deps := make([]Dependency, 0, len(refs))
	for _, ref := range refs {
		if ref != "" {
			deps = append(deps, Dependency{Reference: ref})
		}
	}

	return deps, nil
}
