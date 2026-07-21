// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"

	"github.com/stacklok/toolhive/pkg/plugins"
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

// NoopPluginStore is a no-op implementation of PluginStore for Kubernetes
// environments. Mirrors NoopSkillStore: Get returns ErrNotFound, List returns
// empty, and write operations succeed silently.
type NoopPluginStore struct{}

var _ PluginStore = (*NoopPluginStore)(nil)

// Create is a no-op that always succeeds.
func (*NoopPluginStore) Create(_ context.Context, _ plugins.InstalledPlugin) error {
	return nil
}

// Get always returns ErrNotFound in the no-op implementation.
func (*NoopPluginStore) Get(_ context.Context, _ string, _ plugins.Scope, _ string) (plugins.InstalledPlugin, error) {
	return plugins.InstalledPlugin{}, ErrNotFound
}

// List always returns an empty slice in the no-op implementation.
func (*NoopPluginStore) List(_ context.Context, _ ListFilter) ([]plugins.InstalledPlugin, error) {
	return []plugins.InstalledPlugin{}, nil
}

// Update is a no-op that always succeeds.
func (*NoopPluginStore) Update(_ context.Context, _ plugins.InstalledPlugin) error {
	return nil
}

// Delete is a no-op that always succeeds.
func (*NoopPluginStore) Delete(_ context.Context, _ string, _ plugins.Scope, _ string) error {
	return nil
}

// Close is a no-op that always succeeds.
func (*NoopPluginStore) Close() error { return nil }
