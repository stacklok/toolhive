// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateProjectRoot validates a project root path and returns its cleaned form.
func ValidateProjectRoot(projectRoot string) (string, error) {
	if projectRoot == "" {
		return "", errors.New("project_root is required for project scope")
	}
	if strings.ContainsRune(projectRoot, 0) {
		return "", errors.New("project_root contains null bytes")
	}
	if !filepath.IsAbs(projectRoot) {
		return "", fmt.Errorf("project_root must be absolute, got %q", projectRoot)
	}
	for _, segment := range strings.Split(filepath.ToSlash(projectRoot), "/") {
		if segment == ".." {
			return "", errors.New("project_root must not contain '..' traversal segments")
		}
	}

	projectRoot = filepath.Clean(projectRoot)

	info, err := os.Stat(projectRoot) // #nosec G304 -- path is validated as absolute above
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("project_root does not exist")
		}
		return "", fmt.Errorf("checking project_root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("project_root must be a directory")
	}

	gitPath := filepath.Join(projectRoot, ".git")
	gitInfo, err := os.Stat(gitPath) // #nosec G304 -- path is validated as absolute above
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("project_root must be a git repository")
		}
		return "", fmt.Errorf("checking project_root .git: %w", err)
	}
	if !gitInfo.IsDir() && !gitInfo.Mode().IsRegular() {
		return "", errors.New("project_root must contain a .git directory or file")
	}

	return projectRoot, nil
}
