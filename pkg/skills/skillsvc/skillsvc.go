// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillsvc provides the default implementation of skills.SkillService.
package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

// service is the default implementation of skills.SkillService.
type service struct {
	store storage.SkillStore
}

// New creates a new SkillService backed by the given store.
func New(store storage.SkillStore) skills.SkillService {
	return &service{store: store}
}

// List returns all installed skills matching the given options.
func (s *service) List(ctx context.Context, opts skills.ListOptions) ([]skills.InstalledSkill, error) {
	filter := storage.ListFilter{
		Scope: opts.Scope,
	}
	return s.store.List(ctx, filter)
}

// Install creates a pending skill record. OCI pull is not yet implemented.
func (s *service) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	skill := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:       defaultScope(opts.Scope),
		Status:      skills.InstallStatusPending,
		InstalledAt: time.Now().UTC(),
	}

	if err := s.store.Create(ctx, skill); err != nil {
		return nil, err
	}

	return &skills.InstallResult{Skill: skill}, nil
}

// Uninstall removes an installed skill.
func (s *service) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	return s.store.Delete(ctx, opts.Name, defaultScope(opts.Scope), "")
}

// Info returns detailed information about a skill.
// Info always queries user-scoped skills; project-scoped lookup is not yet
// supported and will be added when InfoOptions gains a Scope field.
func (s *service) Info(ctx context.Context, opts skills.InfoOptions) (*skills.SkillInfo, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	skill, err := s.store.Get(ctx, opts.Name, skills.ScopeUser, "")
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return &skills.SkillInfo{Installed: false}, nil
		}
		return nil, fmt.Errorf("failed to get skill info: %w", err)
	}

	return &skills.SkillInfo{
		Metadata:       skill.Metadata,
		Installed:      true,
		InstalledSkill: &skill,
	}, nil
}

// Validate checks whether a skill definition is valid.
func (*service) Validate(_ context.Context, path string) (*skills.ValidationResult, error) {
	return skills.ValidateSkillDir(path)
}

// Build is not yet implemented.
func (*service) Build(_ context.Context, _ skills.BuildOptions) (*skills.BuildResult, error) {
	return nil, httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// Push is not yet implemented.
func (*service) Push(_ context.Context, _ skills.PushOptions) error {
	return httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// defaultScope returns ScopeUser when s is empty, otherwise returns s unchanged.
func defaultScope(s skills.Scope) skills.Scope {
	if s == "" {
		return skills.ScopeUser
	}
	return s
}
