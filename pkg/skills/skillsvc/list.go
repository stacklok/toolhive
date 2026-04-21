// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

// List returns all installed skills matching the given options.
func (s *service) List(ctx context.Context, opts skills.ListOptions) ([]skills.InstalledSkill, error) {
	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	filter := storage.ListFilter{
		Scope:       scope,
		ClientApp:   opts.ClientApp,
		ProjectRoot: projectRoot,
	}
	all, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	if opts.Group == "" {
		return all, nil
	}

	if s.groupManager == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("group filtering is not available: group manager is not configured"),
			http.StatusInternalServerError,
		)
	}

	group, err := s.groupManager.Get(ctx, opts.Group)
	if err != nil {
		return nil, fmt.Errorf("getting group %q: %w", opts.Group, err)
	}

	// Build a lookup set of skill names in the group.
	groupSkills := make(map[string]struct{}, len(group.Skills))
	for _, name := range group.Skills {
		groupSkills[name] = struct{}{}
	}

	filtered := make([]skills.InstalledSkill, 0, len(all))
	for _, sk := range all {
		if _, ok := groupSkills[sk.Metadata.Name]; ok {
			filtered = append(filtered, sk)
		}
	}
	return filtered, nil
}

// Info returns detailed information about a skill.
func (s *service) Info(ctx context.Context, opts skills.InfoOptions) (*skills.SkillInfo, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)

	skill, err := s.store.Get(ctx, opts.Name, scope, projectRoot)
	if err != nil {
		return nil, err
	}

	return &skills.SkillInfo{
		Metadata:       skill.Metadata,
		InstalledSkill: &skill,
	}, nil
}
