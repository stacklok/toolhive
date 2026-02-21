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

// ValidateProjectRoot validates a project root path and returns its cleaned
// form. Symlinks are rejected to avoid unexpected filesystem traversal.
func ValidateProjectRoot(projectRoot string) (string, error) {
	if err := validateProjectRootInput(projectRoot); err != nil {
		return "", err
	}

	projectRoot = filepath.Clean(projectRoot)
	resolvedRoot, err := resolveProjectRoot(projectRoot)
	if err != nil {
		return "", err
	}
	if err := validateProjectRootDir(resolvedRoot); err != nil {
		return "", err
	}
	if err := validateProjectRootGitDir(resolvedRoot); err != nil {
		return "", err
	}

	return resolvedRoot, nil
}

// NormalizeScopeAndProjectRoot validates scope and project_root and returns
// normalized values.
func NormalizeScopeAndProjectRoot(scope Scope, projectRoot string) (Scope, string, error) {
	if projectRoot != "" && scope == "" {
		scope = ScopeProject
	}
	if err := ValidateScope(scope); err != nil {
		return scope, projectRoot, err
	}
	if projectRoot != "" && scope != ScopeProject {
		return scope, projectRoot, errors.New("project_root is only valid with project scope")
	}
	if scope == ScopeProject {
		cleaned, err := ValidateProjectRoot(projectRoot)
		if err != nil {
			return scope, projectRoot, err
		}
		return scope, cleaned, nil
	}
	return scope, projectRoot, nil
}

func validateProjectRootInput(projectRoot string) error {
	if projectRoot == "" {
		return errors.New("project_root is required for project scope")
	}
	if strings.ContainsRune(projectRoot, 0) {
		return errors.New("project_root contains null bytes")
	}
	if !filepath.IsAbs(projectRoot) {
		return fmt.Errorf("project_root must be absolute, got %q", projectRoot)
	}
	return validateNoTraversal(projectRoot)
}

func resolveProjectRoot(projectRoot string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("project_root does not exist")
		}
		return "", fmt.Errorf("resolving project_root: %w", err)
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	cleanedRoot := filepath.Clean(projectRoot)
	if resolvedRoot != cleanedRoot {
		return "", errors.New("project_root must not contain symlinks")
	}
	if !filepath.IsAbs(resolvedRoot) {
		return "", fmt.Errorf("project_root must be absolute, got %q", resolvedRoot)
	}
	if err := validateNoTraversal(resolvedRoot); err != nil {
		return "", err
	}
	return resolvedRoot, nil
}

func validateProjectRootDir(projectRoot string) error {
	// project_root is user-provided, but already validated for absolute paths,
	// traversal, and symlink usage before any filesystem access.
	info, err := os.Stat(projectRoot) // #nosec G304 -- path is validated and resolved above
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("project_root does not exist")
		}
		return fmt.Errorf("checking project_root: %w", err)
	}
	if !info.IsDir() {
		return errors.New("project_root must be a directory")
	}
	return nil
}

func validateProjectRootGitDir(projectRoot string) error {
	gitPath := filepath.Join(projectRoot, ".git")
	gitInfo, err := os.Stat(gitPath) // #nosec G304 -- path is validated and resolved above
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("project_root must be a git repository")
		}
		return fmt.Errorf("checking project_root .git: %w", err)
	}
	if !gitInfo.IsDir() && !gitInfo.Mode().IsRegular() {
		return errors.New("project_root must contain a .git directory or file")
	}
	return nil
}

func validateNoTraversal(path string) error {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return errors.New("project_root must not contain '..' traversal segments")
		}
	}
	return nil
}
