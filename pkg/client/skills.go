// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	// ErrSkillsNotSupported is returned when a client does not support skills.
	ErrSkillsNotSupported = errors.New("client does not support skills")
	// ErrNoSkillPath is returned when a client has no skill path for the requested scope.
	ErrNoSkillPath = errors.New("client has no skill path for the requested scope")
	// ErrProjectRootRequired is returned when project root is empty for project-scoped skills.
	ErrProjectRootRequired = errors.New("project root must be provided for project-scoped skills")
	// ErrProjectRootNotFound is returned when no project root can be detected.
	ErrProjectRootNotFound = errors.New("could not detect project root (no .git found)")
	// ErrUnknownScope is returned when an unrecognized skill scope is provided.
	ErrUnknownScope = errors.New("unknown skill scope")
)

// SupportsSkills returns whether the given client supports skills.
func (cm *ClientManager) SupportsSkills(clientType ClientApp) bool {
	cfg := cm.lookupClientAppConfig(clientType)
	return cfg != nil && cfg.SupportsSkills
}

// ListSkillSupportingClients returns a sorted slice of all clients that support skills.
func (cm *ClientManager) ListSkillSupportingClients() []ClientApp {
	var clients []ClientApp
	for _, cfg := range cm.clientIntegrations {
		if cfg.SupportsSkills {
			clients = append(clients, cfg.ClientType)
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		return clients[i] < clients[j]
	})
	return clients
}

// GetSkillPath resolves the filesystem path for a skill installation.
//
// For [skills.ScopeUser], it returns ~/<SkillsGlobalPath>/<skillName>.
// For [skills.ScopeProject], it returns <projectRoot>/<SkillsProjectPath>/<skillName>.
//
// Returns an error if the client doesn't support skills, the scope has no
// configured path, the project root is empty, or the skill name would result
// in path traversal outside the skills directory.
func (cm *ClientManager) GetSkillPath(
	clientType ClientApp, skillName string, scope skills.Scope, projectRoot string,
) (string, error) {
	if err := skills.ValidateSkillName(skillName); err != nil {
		return "", err
	}

	cfg := cm.lookupClientAppConfig(clientType)
	if cfg == nil {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}
	if !cfg.SupportsSkills {
		return "", fmt.Errorf("%w: %s", ErrSkillsNotSupported, clientType)
	}

	switch scope {
	case skills.ScopeUser:
		return cm.buildSkillsGlobalPath(cfg, skillName)
	case skills.ScopeProject:
		return buildSkillsProjectPath(cfg, skillName, projectRoot)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownScope, scope)
	}
}

// DetectProjectRoot walks up from startDir looking for a .git directory or file
// (which indicates a git worktree). If startDir is empty, it uses the current
// working directory.
func DetectProjectRoot(startDir string) (string, error) {
	dir := startDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Lstat(gitPath); err == nil {
			// Accept both directories (regular repos) and files (worktrees/submodules)
			if info.IsDir() || info.Mode().IsRegular() {
				return dir, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding .git
			return "", ErrProjectRootNotFound
		}
		dir = parent
	}
}

func (cm *ClientManager) lookupClientAppConfig(clientType ClientApp) *clientAppConfig {
	for i := range cm.clientIntegrations {
		if cm.clientIntegrations[i].ClientType == clientType {
			return &cm.clientIntegrations[i]
		}
	}
	return nil
}

func (cm *ClientManager) buildSkillsGlobalPath(cfg *clientAppConfig, skillName string) (string, error) {
	if len(cfg.SkillsGlobalPath) == 0 {
		return "", fmt.Errorf("%w: %s has no global skill path", ErrNoSkillPath, cfg.ClientType)
	}

	parts := []string{cm.homeDir}
	if prefix, ok := cfg.SkillsPlatformPrefix[Platform(runtime.GOOS)]; ok {
		parts = append(parts, prefix...)
	}
	parts = append(parts, cfg.SkillsGlobalPath...)
	parts = append(parts, skillName)
	return filepath.Join(parts...), nil
}

func buildSkillsProjectPath(cfg *clientAppConfig, skillName string, projectRoot string) (string, error) {
	if len(cfg.SkillsProjectPath) == 0 {
		return "", fmt.Errorf("%w: %s has no project skill path", ErrNoSkillPath, cfg.ClientType)
	}

	if projectRoot == "" {
		return "", ErrProjectRootRequired
	}

	parts := []string{projectRoot}
	parts = append(parts, cfg.SkillsProjectPath...)
	parts = append(parts, skillName)
	return filepath.Join(parts...), nil
}
