// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
)

func TestNewSkillStoreWithDetector_Kubernetes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("kubernetes")

	store, err := NewSkillStoreWithDetector(mockEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.(*NoopSkillStore); !ok {
		t.Fatalf("expected *NoopSkillStore for Kubernetes, got %T", store)
	}
}

func TestNewSkillStoreWithDetector_Local(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")
	mockEnv.EXPECT().Getenv("KUBERNETES_SERVICE_HOST").Return("")

	store, err := NewSkillStoreWithDetector(mockEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Until PR 2 wires SQLite, local also returns NoopSkillStore
	if _, ok := store.(*NoopSkillStore); !ok {
		t.Fatalf("expected *NoopSkillStore for local (temporary), got %T", store)
	}
}

func TestNewSkillStoreWithDetector_KubernetesServiceHost(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")
	mockEnv.EXPECT().Getenv("KUBERNETES_SERVICE_HOST").Return("10.0.0.1")

	store, err := NewSkillStoreWithDetector(mockEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.(*NoopSkillStore); !ok {
		t.Fatalf("expected *NoopSkillStore for Kubernetes (service host), got %T", store)
	}
}
