// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"path/filepath"
	"testing"

	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
)

func TestFactory_Kubernetes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("kubernetes")

	store, err := newSkillStoreWithDetector(mockEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.(*storage.NoopSkillStore); !ok {
		t.Fatalf("expected *storage.NoopSkillStore for Kubernetes, got %T", store)
	}
}

func TestFactory_KubernetesServiceHost(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")
	mockEnv.EXPECT().Getenv("KUBERNETES_SERVICE_HOST").Return("10.0.0.1")

	store, err := newSkillStoreWithDetector(mockEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.(*storage.NoopSkillStore); !ok {
		t.Fatalf("expected *storage.NoopSkillStore for Kubernetes (service host), got %T", store)
	}
}

func TestFactory_Local(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test-factory.db")
	store, err := newSkillStoreFromPath(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, ok := store.(*SkillStore); !ok {
		t.Fatalf("expected *SkillStore for local, got %T", store)
	}
}

func TestFactory_FromPath(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := newSkillStoreFromPath(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, ok := store.(*SkillStore); !ok {
		t.Fatalf("expected *SkillStore, got %T", store)
	}
}
