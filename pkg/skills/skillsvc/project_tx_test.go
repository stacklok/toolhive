// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
)

// TestInstallGit_HoldsProjectTxThroughRegister proves a concurrent uninstall
// cannot observe mid-install state: the project transaction spans git
// resolve, extraction, DB, group, and lock-file bookkeeping.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallGit_HoldsProjectTxThroughRegister(t *testing.T) {
	ctrl := gomock.NewController(t)
	gr := gitmocks.NewMockResolver(ctrl)

	var resolveStarted, allowFinish sync.WaitGroup
	resolveStarted.Add(1)
	allowFinish.Add(1)

	ref, url := gitRef("locked-skill")
	content := gitSkill("locked-skill")
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, gitRef *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
			resolveStarted.Done()
			allowFinish.Wait()
			return &gitresolver.ResolveResult{
				SkillConfig: &skills.ParseResult{Name: "locked-skill"},
				Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: content, Mode: 0644}},
				CommitHash:  fixtureCommitHash(url, content),
			}, nil
		},
	)

	svc, projectRoot := newLockTestService(t, gr)

	installDone := make(chan error, 1)
	go func() {
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
		})
		installDone <- err
	}()

	resolveStarted.Wait()

	uninstallStarted := make(chan struct{})
	uninstallDone := make(chan error, 1)
	go func() {
		close(uninstallStarted)
		uninstallDone <- svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name: "locked-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		})
	}()

	select {
	case <-uninstallStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall goroutine did not start")
	}

	// Uninstall must block on the project tx until install finishes register.
	select {
	case err := <-uninstallDone:
		t.Fatalf("uninstall completed while install still held the project tx: %v", err)
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	allowFinish.Done()

	select {
	case err := <-installDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("install timed out")
	}

	select {
	case err := <-uninstallDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("uninstall timed out after install released the project tx")
	}

	_, err := svc.Info(t.Context(), skills.InfoOptions{
		Name: "locked-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "uninstall that ran after install must have removed the skill")
}

// TestSyncVsUninstall_SerializedOnProjectTx ensures sync and uninstall on the
// same project cannot interleave.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSyncVsUninstall_SerializedOnProjectTx(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	var inSync atomic.Bool
	var overlapped atomic.Bool
	syncer := svc.(*service) //nolint:forcetypeassert

	// Hold the project tx the way Sync does, with a barrier so uninstall
	// can attempt to enter while sync is "in progress".
	syncDone := make(chan struct{})
	go func() {
		unlock := syncer.projectTx.lock(projectRoot)
		inSync.Store(true)
		time.Sleep(150 * time.Millisecond)
		inSync.Store(false)
		unlock()
		close(syncDone)
	}()

	// Wait until sync holds the lock.
	require.Eventually(t, func() bool { return inSync.Load() }, 2*time.Second, 5*time.Millisecond)

	uninstallDone := make(chan error, 1)
	go func() {
		if inSync.Load() {
			// Still true at attempt start — uninstall must block, not overlap work.
		}
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		})
		if inSync.Load() {
			overlapped.Store(true)
		}
		uninstallDone <- err
	}()

	<-syncDone
	select {
	case err := <-uninstallDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("uninstall timed out")
	}
	assert.False(t, overlapped.Load(), "uninstall must not run while sync holds the project tx")
}

// TestUpgradeVsUninstall_SerializedOnProjectTx ensures upgrade and uninstall
// on the same project cannot interleave.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgradeVsUninstall_SerializedOnProjectTx(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	upgrader := svc.(*service) //nolint:forcetypeassert
	var inUpgrade atomic.Bool
	var overlapped atomic.Bool

	upgradeDone := make(chan struct{})
	go func() {
		unlock := upgrader.projectTx.lock(projectRoot)
		inUpgrade.Store(true)
		time.Sleep(150 * time.Millisecond)
		inUpgrade.Store(false)
		unlock()
		close(upgradeDone)
	}()

	require.Eventually(t, func() bool { return inUpgrade.Load() }, 2*time.Second, 5*time.Millisecond)

	uninstallDone := make(chan error, 1)
	go func() {
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		})
		if inUpgrade.Load() {
			overlapped.Store(true)
		}
		uninstallDone <- err
	}()

	<-upgradeDone
	select {
	case err := <-uninstallDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("uninstall timed out")
	}
	assert.False(t, overlapped.Load(), "uninstall must not run while upgrade holds the project tx")
}

// TestInstallProjectScope_AliasCycleRejected covers a cycle where requires
// edges use distinct git aliases that resolve to the same canonical names.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_AliasCycleRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	gr := gitmocks.NewMockResolver(ctrl)
	fx := &fakeGitFixtures{
		skills:  make(map[string]gitFixture),
		history: make(map[string]map[string]gitFixture),
	}

	aliasA := "git://github.com/test/alias-a"
	aliasB := "git://github.com/test/alias-b"
	_, urlA := gitRef("skill-a")
	_, urlB := gitRef("skill-b")

	// alias-a resolves to skill-a which requires alias-b; alias-b resolves
	// to skill-b which requires alias-a — a canonical a↔b cycle via aliases.
	fx.register("skill-a", gitSkill("skill-a", aliasB))
	fx.register("skill-b", gitSkill("skill-b", aliasA))
	// Also register under the alias URLs ParseGitReference will produce.
	fx.skills[mustGitURL(t, aliasA)] = fx.skills[urlA]
	fx.skills[mustGitURL(t, aliasB)] = fx.skills[urlB]
	fx.history[mustGitURL(t, aliasA)] = fx.history[urlA]
	fx.history[mustGitURL(t, aliasB)] = fx.history[urlB]

	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, ref *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
			fixture, ok := fx.skills[ref.URL]
			if !ok {
				return nil, fmt.Errorf("no fixture for %q", ref.URL)
			}
			return &gitresolver.ResolveResult{
				SkillConfig: &skills.ParseResult{Name: fixture.name},
				Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: fixture.content, Mode: 0644}},
				CommitHash:  fixtureCommitHash(ref.URL, fixture.content),
			}, nil
		})

	svc, projectRoot := newLockTestService(t, gr)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name: aliasA, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
		})
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency cycle")
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: alias cycle did not reject")
	}
}

func mustGitURL(t *testing.T, gitRefStr string) string {
	t.Helper()
	ref, err := gitresolver.ParseGitReference(gitRefStr)
	require.NoError(t, err)
	return ref.URL
}

// TestInstallProjectScope_DiamondDependencyMergesRequiredBy installs a root
// whose two children share one dependency within a single traversal.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_DiamondDependencyMergesRequiredBy(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("shared-dep")
	leftRef, _ := gitRef("left")
	rightRef, _ := gitRef("right")
	fx.register("shared-dep", gitSkill("shared-dep"))
	fx.register("left", gitSkill("left", depRef))
	fx.register("right", gitSkill("right", depRef))
	fx.register("root", gitSkill("root", leftRef, rightRef))
	svc, projectRoot := newLockTestService(t, gr)

	rootRef, _ := gitRef("root")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: rootRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	lf := readLockfile(t, projectRoot)
	dep, ok := lf.Get("shared-dep")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"left", "right"}, dep.RequiredBy,
		"diamond dependency must keep both parents from a single install traversal")
}

// TestSync_CanonicalNameMismatchRejected fails sync when the pinned artifact
// resolves to a different skill name than the lock entry.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_CanonicalNameMismatchRejected(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("lock-name", gitSkill("lock-name"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("lock-name")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	entry, ok := readLockfile(t, projectRoot).Get("lock-name")
	require.True(t, ok)

	// Republish the same source URL with a different manifest name.
	_, url := gitRef("lock-name")
	fx.skills[url] = gitFixture{name: "other-name", content: gitSkill("other-name")}
	fx.history[url][entry.Digest] = gitFixture{name: "other-name", content: gitSkill("other-name")}

	// Force drift so sync attempts a restore.
	skillMD := filepath.Join(projectRoot, ".claude", "skills", "lock-name", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("tampered"), 0o644))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.NotEmpty(t, result.Failed)
	assert.Contains(t, result.Failed[0].Error, "expected canonical name")

	// Lock entry and DB identity must be unchanged.
	after, ok := readLockfile(t, projectRoot).Get("lock-name")
	require.True(t, ok)
	assert.Equal(t, entry.Digest, after.Digest)
	info, err := svc.Info(t.Context(), skills.InfoOptions{
		Name: "lock-name", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	assert.Equal(t, "lock-name", info.InstalledSkill.Metadata.Name)
}
