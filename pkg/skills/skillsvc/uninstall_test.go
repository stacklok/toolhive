// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestUninstall(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)

	t.Run("success with file cleanup", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		// Create a skill directory to be cleaned up
		skillDir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0750))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0600))

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"claude-code"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(skillDir, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)

		// Verify directory was removed
		_, statErr := os.Stat(skillDir)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("success without path resolver", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)
	})

	t.Run("respects explicit scope", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeProject,
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(existing, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(nil)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name:        "my-skill",
			Scope:       skills.ScopeProject,
			ProjectRoot: projectRoot,
		})
		require.NoError(t, err)
	})

	t.Run("project scope requires project root", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name:  "my-skill",
			Scope: skills.ScopeProject,
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("returns 404 when not found", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
	})

	t.Run("rejects invalid name", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "X"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("cleans up all clients", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		dir1 := filepath.Join(t.TempDir(), "client1", "my-skill")
		dir2 := filepath.Join(t.TempDir(), "client2", "my-skill")
		require.NoError(t, os.MkdirAll(dir1, 0750))
		require.NoError(t, os.MkdirAll(dir2, 0750))

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"client-a", "client-b"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("client-a", "my-skill", skills.ScopeUser, "").Return(dir1, nil)
		pr.EXPECT().GetSkillPath("client-b", "my-skill", skills.ScopeUser, "").Return(dir2, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)

		_, statErr1 := os.Stat(dir1)
		assert.True(t, os.IsNotExist(statErr1))
		_, statErr2 := os.Stat(dir2)
		assert.True(t, os.IsNotExist(statErr2))
	})

	t.Run("best-effort cleanup continues on remove error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"client-a", "client-b"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("client-a", "my-skill", skills.ScopeUser, "").Return("/some/dir-a", nil)
		pr.EXPECT().GetSkillPath("client-b", "my-skill", skills.ScopeUser, "").Return("/some/dir-b", nil)
		// First remove fails, but second should still be attempted
		inst.EXPECT().Remove("/some/dir-a").Return(fmt.Errorf("permission denied"))
		inst.EXPECT().Remove("/some/dir-b").Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		// Store deletion succeeds, but cleanup errors are returned
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})
}

func TestConcurrentInstallAndUninstall(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)
	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)
	pr := skillsmocks.NewMockPathResolver(ctrl)

	// Per-skill atomic counters verify that at most one goroutine is inside
	// a critical section for a given skill at any time.
	var inFlight sync.Map // skill name -> *int32

	assertExclusive := func(name string) {
		counter, _ := inFlight.LoadOrStore(name, new(int32))
		cnt := counter.(*int32)
		cur := atomic.AddInt32(cnt, 1)
		assert.Equal(t, int32(1), cur, "concurrent access detected for %s", name)
		// Sleep briefly to widen the window for detecting overlap.
		time.Sleep(time.Millisecond)
		atomic.AddInt32(cnt, -1)
	}

	// PathResolver returns unique temp directories per skill so extractions
	// don't collide. Use a temp base that outlives individual subtests.
	baseDir := tempDir(t)
	pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"}).AnyTimes()
	pr.EXPECT().GetSkillPath(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_, skillName string, _ skills.Scope, _ string) (string, error) {
			return filepath.Join(baseDir, skillName), nil
		}).AnyTimes()

	store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sk skills.InstalledSkill) error {
			assertExclusive(sk.Metadata.Name)
			return nil
		}).AnyTimes()
	store.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, name string, _ skills.Scope, _ string) (skills.InstalledSkill, error) {
			assertExclusive(name)
			return skills.InstalledSkill{}, storage.ErrNotFound
		}).AnyTimes()
	store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sk skills.InstalledSkill) error {
			assertExclusive(sk.Metadata.Name)
			return nil
		}).AnyTimes()
	store.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, name string, _ skills.Scope, _ string) error {
			assertExclusive(name)
			return nil
		}).AnyTimes()

	svc := New(store, WithPathResolver(pr))

	// Run concurrent install/uninstall pairs across multiple skill names.
	// Different skills proceed independently; the same skill name is
	// serialized by the per-skill lock. The atomic counters above detect
	// any overlap within a skill's critical section.
	skillNames := []string{"skill-a", "skill-b", "skill-c"}
	const goroutinesPerSkill = 5

	var wg sync.WaitGroup
	wg.Add(len(skillNames) * goroutinesPerSkill)

	for _, name := range skillNames {
		for range goroutinesPerSkill {
			go func() {
				defer wg.Done()
				// Provide LayerData so Install exercises installWithExtraction.
				_, _ = svc.Install(t.Context(), skills.InstallOptions{
					Name:      name,
					LayerData: layerData,
					Digest:    "sha256:concurrent-test",
				})
				// Uninstall may fail (not found) — that's fine for concurrency testing.
				_ = svc.Uninstall(t.Context(), skills.UninstallOptions{Name: name})
			}()
		}
	}

	wg.Wait()
}

// ---------- group-integration tests ----------

func TestUninstallRemovesSkillFromGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		opts           skills.UninstallOptions
		setupStoreMock func(*storemocks.MockSkillStore)
		setupGroupMock func(*groupmocks.MockManager)
		wantErr        string
	}{
		{
			name: "uninstall removes skill from all groups",
			opts: skills.UninstallOptions{Name: "my-skill"},
			setupStoreMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").
					Return(skills.InstalledSkill{
						Metadata: skills.SkillMetadata{Name: "my-skill"},
						Clients:  []string{},
					}, nil)
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
					{Name: "mygroup", Skills: []string{"my-skill"}},
				}, nil)
				gm.EXPECT().Update(gomock.Any(), &groups.Group{Name: "mygroup", Skills: []string{}}).
					Return(nil)
			},
		},
		{
			name: "uninstall with no group manager succeeds without group cleanup",
			opts: skills.UninstallOptions{Name: "my-skill"},
			setupStoreMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").
					Return(skills.InstalledSkill{
						Metadata: skills.SkillMetadata{Name: "my-skill"},
						Clients:  []string{},
					}, nil)
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)
			},
			setupGroupMock: nil, // no group mock needed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			tt.setupStoreMock(store)

			opts := []Option{}
			if tt.setupGroupMock != nil {
				gm := groupmocks.NewMockManager(ctrl)
				tt.setupGroupMock(gm)
				opts = append(opts, WithGroupManager(gm))
			}

			svc := New(store, opts...)

			err := svc.Uninstall(t.Context(), tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
