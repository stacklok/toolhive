// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"

	"github.com/stacklok/toolhive/pkg/skills"
)

// NoopSkillStore is a no-op implementation of SkillStore for Kubernetes environments.
// Get always returns ErrNotFound, List returns empty, and write operations succeed silently.
type NoopSkillStore struct{}

var _ SkillStore = (*NoopSkillStore)(nil)

// Create is a no-op that always succeeds.
func (*NoopSkillStore) Create(_ context.Context, _ skills.InstalledSkill) error {
	return nil
}

// Get always returns ErrNotFound in the no-op implementation.
func (*NoopSkillStore) Get(_ context.Context, _ string, _ skills.Scope, _ string) (skills.InstalledSkill, error) {
	return skills.InstalledSkill{}, ErrNotFound
}

// List always returns an empty slice in the no-op implementation.
func (*NoopSkillStore) List(_ context.Context, _ ListFilter) ([]skills.InstalledSkill, error) {
	return []skills.InstalledSkill{}, nil
}

// Update is a no-op that always succeeds.
func (*NoopSkillStore) Update(_ context.Context, _ skills.InstalledSkill) error {
	return nil
}

// Delete is a no-op that always succeeds.
func (*NoopSkillStore) Delete(_ context.Context, _ string, _ skills.Scope, _ string) error {
	return nil
}

// Close is a no-op that always succeeds.
func (*NoopSkillStore) Close() error { return nil }
