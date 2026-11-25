// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/storage"
)

// NewDefaultSkillStore creates a SkillStore using OS environment for runtime
// detection. In Kubernetes it returns a NoopSkillStore; locally it opens a
// SQLite database at the default path. The caller owns the returned store.
func NewDefaultSkillStore() (storage.SkillStore, error) {
	return newSkillStoreWithDetector(&env.OSReader{})
}

// newSkillStoreWithDetector is the testable core of NewDefaultSkillStore.
func newSkillStoreWithDetector(envReader env.Reader) (storage.SkillStore, error) {
	if runtime.IsKubernetesRuntimeWithEnv(envReader) {
		return &storage.NoopSkillStore{}, nil
	}
	return newSkillStoreFromPath(context.Background(), DefaultDBPath())
}

// newSkillStoreFromPath opens a SQLite DB at the given path.
func newSkillStoreFromPath(ctx context.Context, dbPath string) (storage.SkillStore, error) {
	db, err := Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return NewSkillStore(db), nil
}

// NewDefaultPluginStore creates a PluginStore using OS environment for runtime
// detection. In Kubernetes it returns a NoopPluginStore; locally it opens the
// shared SQLite database at the default path (the same file as skills — Open
// runs all migrations including 002_create_plugins). The caller owns the
// returned store. Mirrors NewDefaultSkillStore.
func NewDefaultPluginStore() (storage.PluginStore, error) {
	return newPluginStoreWithDetector(&env.OSReader{})
}

// newPluginStoreWithDetector is the testable core of NewDefaultPluginStore.
func newPluginStoreWithDetector(envReader env.Reader) (storage.PluginStore, error) {
	if runtime.IsKubernetesRuntimeWithEnv(envReader) {
		return &storage.NoopPluginStore{}, nil
	}
	return newPluginStoreFromPath(context.Background(), DefaultDBPath())
}

// newPluginStoreFromPath opens a SQLite DB at the given path.
func newPluginStoreFromPath(ctx context.Context, dbPath string) (storage.PluginStore, error) {
	db, err := Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return NewPluginStore(db), nil
}
