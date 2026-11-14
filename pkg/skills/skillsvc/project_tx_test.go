// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/plugins/pluginsvc"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
)

const (
	projectTxHelperEnv       = "TOOLHIVE_PROJECT_TX_HELPER"
	projectTxHelperRootEnv   = "TOOLHIVE_PROJECT_TX_ROOT"
	projectTxHelperReadyEnv  = "TOOLHIVE_PROJECT_TX_READY"
	projectTxHelperMarkerEnv = "TOOLHIVE_PROJECT_TX_MARKER"
)

//nolint:paralleltest // t.Setenv and xdg.Reload mutate process-wide state
func TestProjectTxRun_SerializesAcrossProcesses(t *testing.T) {
	t.Cleanup(xdg.Reload)
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	xdg.Reload()

	projectRoot := t.TempDir()
	readyPath := filepath.Join(t.TempDir(), "child-ready")
	markerPath := filepath.Join(t.TempDir(), "child-entered")
	lockPath, err := projectTxLockPath(projectRoot)
	require.NoError(t, err)
	require.Contains(t, lockPath, filepath.Join(stateHome, "toolhive", projectTxLockDir))

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- (&projectTx{}).run(t.Context(), projectRoot, func() error {
			close(firstEntered)
			select {
			case <-releaseFirst:
				return nil
			case <-time.After(5 * time.Second):
				return fmt.Errorf("timed out waiting to release first transaction")
			}
		})
	}()

	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first transaction did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectTxRunHelper$") //nolint:gosec // test binary and fixed args
	cmd.Env = append(os.Environ(),
		projectTxHelperEnv+"=1",
		projectTxHelperRootEnv+"="+projectRoot,
		projectTxHelperReadyEnv+"="+readyPath,
		projectTxHelperMarkerEnv+"="+markerPath,
	)
	var childOutput bytes.Buffer
	cmd.Stdout = &childOutput
	cmd.Stderr = &childOutput
	require.NoError(t, cmd.Start())
	childDone := make(chan error, 1)
	go func() {
		childDone <- cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(readyPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "child process did not reach the project transaction")

	select {
	case err := <-childDone:
		t.Fatalf("child transaction completed while the parent held the project lock: %v\n%s", err, childOutput.String())
	case <-time.After(100 * time.Millisecond):
		// Expected: the child process is blocked by the OS-backed lock.
	}
	_, err = os.Stat(markerPath)
	require.ErrorIs(t, err, os.ErrNotExist, "child callback must not run while the parent holds the project lock")

	release()

	select {
	case err := <-firstDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first transaction did not finish after release")
	}

	select {
	case err := <-childDone:
		require.NoError(t, err, childOutput.String())
	case <-time.After(5 * time.Second):
		t.Fatal("child transaction did not finish after the parent released the project lock")
	}

	_, err = os.Stat(markerPath)
	require.NoError(t, err, "child callback must run after the parent releases the project lock")
	_, err = os.Stat(lockPath)
	require.NoError(t, err, "stable transaction lock must remain in ToolHive state after release")
	rootEntries, err := os.ReadDir(projectRoot)
	require.NoError(t, err)
	require.Empty(t, rootEntries, "transaction lock must not pollute the worktree root")
}

func TestProjectTxRunHelper(t *testing.T) {
	t.Parallel()

	if os.Getenv(projectTxHelperEnv) != "1" {
		return
	}

	projectRoot := os.Getenv(projectTxHelperRootEnv)
	readyPath := os.Getenv(projectTxHelperReadyEnv)
	markerPath := os.Getenv(projectTxHelperMarkerEnv)
	require.NotEmpty(t, projectRoot)
	require.NotEmpty(t, readyPath)
	require.NotEmpty(t, markerPath)
	require.NoError(t, os.WriteFile(readyPath, []byte("ready"), 0o600))
	require.NoError(t, (&projectTx{}).run(t.Context(), projectRoot, func() error {
		return os.WriteFile(markerPath, []byte("entered"), 0o600)
	}))
}

func TestProjectTxRun_CanceledWaiterDoesNotEnter(t *testing.T) {
	t.Parallel()

	projectRoot := t.TempDir()
	tx := &projectTx{}
	unlock, err := tx.lock(t.Context(), projectRoot)
	require.NoError(t, err)
	var unlockOnce sync.Once
	release := func() { unlockOnce.Do(unlock) }
	t.Cleanup(release)

	waitCtx, cancel := context.WithCancel(t.Context())
	entered := atomic.Bool{}
	done := make(chan error, 1)
	go func() {
		done <- tx.run(waitCtx, projectRoot, func() error {
			entered.Store(true)
			return nil
		})
	}()
	cancel()

	select {
	case runErr := <-done:
		require.ErrorIs(t, runErr, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled transaction did not stop waiting for the in-process lock")
	}
	release()
	assert.False(t, entered.Load(), "a canceled transaction must never run its callback")
}

//nolint:paralleltest // holds a process-global lock beyond the former timeout
func TestProjectTxRun_WaitsBeyondInternalTimeoutWhileCallerIsActive(t *testing.T) {
	const formerInternalTimeout = 5 * time.Second

	projectRoot := t.TempDir()
	tx := &projectTx{}
	unlock, err := tx.lock(t.Context(), projectRoot)
	require.NoError(t, err)
	var unlockOnce sync.Once
	release := func() { unlockOnce.Do(unlock) }
	t.Cleanup(release)

	callerCtx, cancel := context.WithTimeout(t.Context(), 2*formerInternalTimeout)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- tx.run(callerCtx, projectRoot, func() error { return nil })
	}()

	timer := time.NewTimer(formerInternalTimeout + 250*time.Millisecond)
	t.Cleanup(func() { timer.Stop() })
	select {
	case runErr := <-done:
		t.Fatalf("project transaction stopped before the active caller released it: %v", runErr)
	case <-timer.C:
	}

	release()
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("project transaction did not enter after the lock was released")
	}
}

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
		func(_ context.Context, _ *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
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

// TestSkillsInstallAndPluginSyncShareProjectTx proves the shared package is
// wired through both public service call sites. The plugin sync is canceled
// while a skills install is paused inside its transaction; it must return from
// lock acquisition without reaching its intentionally unconfigured store.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSkillsInstallAndPluginSyncShareProjectTx(t *testing.T) {
	ctrl := gomock.NewController(t)
	gr := gitmocks.NewMockResolver(ctrl)
	resolveStarted := make(chan struct{})
	allowResolve := make(chan struct{})
	var releaseOnce sync.Once
	releaseResolve := func() { releaseOnce.Do(func() { close(allowResolve) }) }
	t.Cleanup(releaseResolve)

	ref, url := gitRef("shared-project-skill")
	content := gitSkill("shared-project-skill")
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
			close(resolveStarted)
			select {
			case <-allowResolve:
				return &gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "shared-project-skill"},
					Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: content, Mode: 0644}},
					CommitHash:  fixtureCommitHash(url, content),
				}, nil
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("timed out waiting to release skill resolver")
			}
		},
	)

	skillService, projectRoot := newLockTestService(t, gr)
	skillDone := make(chan error, 1)
	go func() {
		_, err := skillService.Install(t.Context(), skills.InstallOptions{
			Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
		})
		skillDone <- err
	}()
	select {
	case <-resolveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("skills install did not enter its project transaction")
	}

	pluginService := pluginsvc.New().(plugins.PluginLockService) //nolint:forcetypeassert
	pluginCtx, cancelPlugin := context.WithCancel(t.Context())
	t.Cleanup(cancelPlugin)
	pluginStarted := make(chan struct{})
	pluginDone := make(chan error, 1)
	go func() {
		close(pluginStarted)
		_, err := pluginService.Sync(pluginCtx, plugins.SyncOptions{ProjectRoot: projectRoot})
		pluginDone <- err
	}()
	select {
	case <-pluginStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin sync goroutine did not start")
	}
	select {
	case err := <-pluginDone:
		t.Fatalf("plugin sync completed while skills install held the shared project transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancelPlugin()
	select {
	case err := <-pluginDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled plugin sync did not stop waiting for the shared project transaction")
	}

	releaseResolve()
	select {
	case err := <-skillDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("skills install did not finish after its resolver was released")
	}
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
		unlock, lockErr := syncer.projectTx.lock(context.Background(), projectRoot)
		if lockErr != nil {
			panic(lockErr)
		}
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
		unlock, lockErr := upgrader.projectTx.lock(context.Background(), projectRoot)
		if lockErr != nil {
			panic(lockErr)
		}
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
