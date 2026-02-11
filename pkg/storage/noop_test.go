// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/toolhive/pkg/skills"
)

func TestNoopSkillStore_Create(t *testing.T) {
	t.Parallel()
	store := &NoopSkillStore{}
	err := store.Create(context.Background(), skills.InstalledSkill{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestNoopSkillStore_Get(t *testing.T) {
	t.Parallel()
	store := &NoopSkillStore{}
	_, err := store.Get(context.Background(), "test", skills.ScopeUser, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNoopSkillStore_List(t *testing.T) {
	t.Parallel()
	store := &NoopSkillStore{}
	result, err := store.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %d items", len(result))
	}
}

func TestNoopSkillStore_Update(t *testing.T) {
	t.Parallel()
	store := &NoopSkillStore{}
	err := store.Update(context.Background(), skills.InstalledSkill{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestNoopSkillStore_Delete(t *testing.T) {
	t.Parallel()
	store := &NoopSkillStore{}
	err := store.Delete(context.Background(), "test", skills.ScopeUser, "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
