// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
)

// newLockTestService builds a service backed by a real (temp-file) SQLite
// store and the real default Installer, so extracted files and lock file
// state can be inspected on disk exactly as they would be in production.
// Only the git resolver and path resolver are test doubles.
func newLockTestService(t *testing.T, gr *gitmocks.MockResolver) (skills.SkillService, string) {
	t.Helper()
	t.Setenv(skills.LockFileEnvVar, "true")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := sqlite.NewSkillStore(db)

	projectRoot := makeProjectRoot(t)
	installBase := filepath.Join(projectRoot, ".claude", "skills")

	ctrl := gomock.NewController(t)
	pr := skillsmocks.NewMockPathResolver(ctrl)
	pr.EXPECT().GetSkillPath(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_, skillName string, _ skills.Scope, _ string) (string, error) {
			return filepath.Join(installBase, skillName), nil
		})
	// Only reached when a caller omits --clients with no prior DB record to
	// fall back on (e.g. sync restoring an entry missing from a fresh
	// clone) — the RFC's client-agnostic design expands that to every
	// skill-supporting client detected on the host. Deliberately returns
	// more than one client so tests can distinguish "preserved the skill's
	// existing (narrower) client list" from "fell through to every detected
	// client" (see TestUpgrade_PreservesExistingClients).
	pr.EXPECT().ListSkillSupportingClients().AnyTimes().Return([]string{"claude-code", "cursor"})

	svc := New(store, WithPathResolver(pr), WithGitResolver(gr))
	return svc, projectRoot
}

// gitSkillVersion builds SKILL.md content for a git-resolved test fixture
// with a distinct version ("2.0.0"), for tests simulating newer content
// published at the same source (e.g. an upgrade scenario). It never
// declares requires.
func gitSkillVersion(name string) []byte {
	fm := fmt.Sprintf("---\nname: %s\ndescription: test skill\nversion: 2.0.0\n---\n# %s\n", name, name)
	return []byte(fm)
}

// gitSkill builds SKILL.md content for a git-resolved test fixture,
// optionally declaring toolhive.requires dependencies.
func gitSkill(name string, requires ...string) []byte {
	fm := fmt.Sprintf("---\nname: %s\ndescription: test skill\n", name)
	if len(requires) > 0 {
		fm += "toolhive.requires:\n"
		for _, r := range requires {
			fm += "  - " + r + "\n"
		}
	}
	fm += "---\n# " + name + "\n"
	return []byte(fm)
}

// gitRef builds the git:// reference and matching *gitresolver.GitReference
// for a fake repo named after the skill it resolves to. ParseGitReference
// picks "http://" instead of "https://" only when TOOLHIVE_DEV=true (see
// gitresolver.isDevMode); this mirrors that so the fixture URL matches
// whatever ParseGitReference actually produces in the environment the test
// runs in, rather than hardcoding one scheme.
func gitRef(name string) (string, string) {
	scheme := "https"
	if strings.EqualFold(os.Getenv("TOOLHIVE_DEV"), "true") {
		scheme = "http"
	}
	return "git://github.com/test/" + name, scheme + "://github.com/test/" + name
}

// gitFixture is a single registered skill: its SKILL.md content and the
// parsed name/version installFromGit reads from SkillConfig (it trusts the
// resolver's parse rather than re-parsing Files itself).
type gitFixture struct {
	name    string
	content []byte
}

// fakeGitFixtures dispatches a MockResolver's Resolve calls by matching the
// reference URL against a fixed set of registered skills, each getting its
// own commit hash so digests differ. Like a real repository, it keeps every
// registered revision: resolving a reference pinned to a full commit hash
// returns that revision's content, while an unpinned reference returns the
// latest — so sync's pinned restores behave the way real git checkouts do.
type fakeGitFixtures struct {
	skills  map[string]gitFixture            // latest content, keyed by URL
	history map[string]map[string]gitFixture // URL -> commit hash -> content
}

// fixtureCommitHash derives the deterministic per-revision commit hash the
// fake resolver reports for a registered fixture.
func fixtureCommitHash(url string, content []byte) string {
	return fmt.Sprintf("%040x", len(content)+len(url))
}

func newGitResolverMock(t *testing.T) (*gitmocks.MockResolver, *fakeGitFixtures) {
	t.Helper()
	fx := &fakeGitFixtures{
		skills:  make(map[string]gitFixture),
		history: make(map[string]map[string]gitFixture),
	}
	gr := gitmocks.NewMockResolver(gomock.NewController(t))
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, ref *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
			fixture, ok := fx.skills[ref.URL]
			if !ok {
				return nil, fmt.Errorf("no fixture registered for %q", ref.URL)
			}
			if pinned, isPinned := fx.history[ref.URL][ref.Ref]; isPinned {
				fixture = pinned
			}
			return &gitresolver.ResolveResult{
				SkillConfig: &skills.ParseResult{Name: fixture.name},
				Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: fixture.content, Mode: 0644}},
				CommitHash:  fixtureCommitHash(ref.URL, fixture.content),
			}, nil
		})
	return gr, fx
}

func (f *fakeGitFixtures) register(name string, content []byte) {
	_, url := gitRef(name)
	fixture := gitFixture{name: name, content: content}
	f.skills[url] = fixture
	if f.history[url] == nil {
		f.history[url] = make(map[string]gitFixture)
	}
	f.history[url][fixtureCommitHash(url, content)] = fixture
}

func mustOpenRoot(t *testing.T, projectRoot string) lockfile.Root {
	t.Helper()
	root, err := lockfile.OpenRoot(projectRoot)
	require.NoError(t, err)
	return root
}

func readLockfile(t *testing.T, projectRoot string) *lockfile.Lockfile {
	t.Helper()
	lf, err := lockfile.Load(mustOpenRoot(t, projectRoot))
	require.NoError(t, err)
	return lf
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_LockFileDisabled_NoLockFileWritten(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)
	t.Setenv(skills.LockFileEnvVar, "false") // override newLockTestService's default

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(projectRoot, lockfile.FileName))
	assert.True(t, os.IsNotExist(err), "lock file must not be written when the feature is disabled")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_RecordsExplicitEntry(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	result, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	assert.True(t, result.Skill.Managed, "project-scope install must be marked lock-managed")

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("my-skill")
	require.True(t, ok, "expected a lock entry for my-skill")
	assert.Equal(t, ref, entry.Source, "source must be exactly what the caller requested")
	// For a direct git install, ResolvedReference is the same git:// URL: the
	// package preserves it verbatim as the artifact's provenance reference,
	// distinct from a registry-resolved install where Source (registry name)
	// and ResolvedReference (the concrete git:// URL it resolved to) differ.
	assert.Equal(t, ref, entry.ResolvedReference)
	assert.NotEmpty(t, entry.Digest)
	assert.NotEmpty(t, entry.ContentDigest, "contentDigest must be computed for a project-scope install")
	assert.True(t, entry.Explicit)
	assert.Empty(t, entry.RequiredBy)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_MaterializesDependency(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("shared-dep")
	fx.register("parent-skill", gitSkill("parent-skill", depRef))
	fx.register("shared-dep", gitSkill("shared-dep"))
	svc, projectRoot := newLockTestService(t, gr)

	parentRef, _ := gitRef("parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: parentRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	lf := readLockfile(t, projectRoot)
	parent, ok := lf.Get("parent-skill")
	require.True(t, ok)
	assert.True(t, parent.Explicit)

	dep, ok := lf.Get("shared-dep")
	require.True(t, ok, "dependency must be materialized and recorded")
	assert.False(t, dep.Explicit)
	assert.Equal(t, []string{"parent-skill"}, dep.RequiredBy)
	assert.NotEmpty(t, dep.ContentDigest)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_SharedDependencyMergesRequiredBy(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("shared-dep")
	fx.register("parent-a", gitSkill("parent-a", depRef))
	fx.register("parent-b", gitSkill("parent-b", depRef))
	fx.register("shared-dep", gitSkill("shared-dep"))
	svc, projectRoot := newLockTestService(t, gr)

	refA, _ := gitRef("parent-a")
	refB, _ := gitRef("parent-b")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: refA, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: refB, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	lf := readLockfile(t, projectRoot)
	dep, ok := lf.Get("shared-dep")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"parent-a", "parent-b"}, dep.RequiredBy,
		"a dependency shared by two parents must keep both, not overwrite on the second install")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestMaterializeDependencies_RejectsTreeExceedingMaxDependencies(t *testing.T) {
	gr, _ := newGitResolverMock(t)
	svc, projectRoot := newLockTestService(t, gr)
	svcImpl := svc.(*service) //nolint:forcetypeassert // white-box test in the same package

	depRef, _ := gitRef("one-more-dep")
	sk := skills.InstalledSkill{
		Metadata:    skills.SkillMetadata{Name: "root-skill"},
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	}
	skillDir := filepath.Join(projectRoot, ".claude", "skills", sk.Metadata.Name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), gitSkill("root-skill", depRef), 0o644))

	// Pre-fill Visited to the cap, as if this were deep in an already-large
	// dependency tree; the next never-seen dependency must trip the limit
	// rather than silently keep expanding.
	visited := make(map[string]struct{}, skills.MaxDependencies)
	for i := range skills.MaxDependencies {
		visited[fmt.Sprintf("seen-%d", i)] = struct{}{}
	}

	err := svcImpl.materializeDependencies(t.Context(), skills.InstallOptions{Visited: visited}, "root-skill", sk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_DependencyCycleTerminates(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	refA, _ := gitRef("skill-a")
	refB, _ := gitRef("skill-b")
	fx.register("skill-a", gitSkill("skill-a", refB))
	fx.register("skill-b", gitSkill("skill-b", refA)) // cycle: a -> b -> a
	svc, projectRoot := newLockTestService(t, gr)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name: refA, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
		})
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: dependency cycle did not terminate")
	}

	lf := readLockfile(t, projectRoot)
	a, ok := lf.Get("skill-a")
	require.True(t, ok)
	assert.True(t, a.Explicit)
	// The cycle check must trip on skill-b's requires entry for refA — the
	// same git:// reference skill-a was installed with, not skill-a's
	// resolved bare name — so skill-a is never re-materialized as its own
	// dependency. Without that, skill-a would gain itself a spurious
	// RequiredBy via skill-b.
	assert.Empty(t, a.RequiredBy, "skill-a must not become required-by skill-b through the a<->b cycle")
	b, ok := lf.Get("skill-b")
	require.True(t, ok)
	assert.Equal(t, []string{"skill-a"}, b.RequiredBy)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_LockWriteFailureRollsBackInstall(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	// Replace the lock file location with a directory so writes to it fail.
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, lockfile.FileName), 0o755))

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.Error(t, err, "install must fail when the lock file cannot be written")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "the DB record must be rolled back so a retry starts fresh")
}

// TestInstallProjectScope_DependencyFailureRollsBackParentLockEntry covers a
// failure that happens after the parent's own lock entry was already
// written: recordLockEntry succeeds for the parent, then
// materializeDependencies fails on an unresolvable toolhive.requires
// dependency. The parent's lock entry (and DB record) must be rolled back
// too, not just the DB record — otherwise toolhive.lock.yaml claims the
// parent is pinned while Info() reports it doesn't exist.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_DependencyFailureRollsBackParentLockEntry(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	missingDepRef, _ := gitRef("missing-dep") // never registered — Resolve fails for it
	fx.register("parent-skill", gitSkill("parent-skill", missingDepRef))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.Error(t, err, "install must fail when a declared dependency cannot be resolved")

	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("parent-skill")
	assert.False(t, ok, "parent's lock entry must be rolled back when a dependency fails to materialize")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "parent-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "the DB record must be rolled back so a retry starts fresh")

	// Retrying (with Force, since the first attempt's extracted files are
	// left on disk per this rollback's documented contract) must succeed
	// once the dependency is fixed, not hit a stale, permanently-broken state.
	fx.register("missing-dep", gitSkill("missing-dep"))
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"}, Force: true,
	})
	require.NoError(t, err, "retry after fixing the dependency must succeed")
}

// TestInstallProjectScope_SiblingDependencyRolledBackWithParent covers the
// partial-success case: the first declared dependency installs cleanly, then
// a later sibling fails. Rolling back only the parent would leak the
// succeeded sibling as an orphan — installed, managed, and pinned with a
// RequiredBy pointing at a parent that no longer exists.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_SiblingDependencyRolledBackWithParent(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	okDepRef, _ := gitRef("ok-dep")
	missingDepRef, _ := gitRef("missing-dep") // never registered — Resolve fails
	fx.register("ok-dep", gitSkill("ok-dep"))
	fx.register("parent-skill", gitSkill("parent-skill", okDepRef, missingDepRef))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.Error(t, err, "install must fail when a later dependency cannot be resolved")

	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("parent-skill")
	assert.False(t, ok, "parent's lock entry must be rolled back")
	_, ok = lf.Get("ok-dep")
	assert.False(t, ok, "the succeeded sibling's lock entry must be rolled back too, not leaked as an orphan")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "ok-dep", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "the succeeded sibling's DB record must be rolled back too")
}

// TestInstallProjectScope_RollbackRestoresPreExistingState covers the
// destructive-rollback hazard: a --force reinstall of an already-installed,
// lock-managed skill that fails partway (here: on a newly-declared
// dependency) must restore the previously-valid DB record and lock entry —
// including RequiredBy links other parents merged onto it — not delete them.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_RollbackRestoresPreExistingState(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	sharedRef, _ := gitRef("shared-skill")
	fx.register("shared-skill", gitSkill("shared-skill"))
	fx.register("other-parent", gitSkill("other-parent", sharedRef))
	svc, projectRoot := newLockTestService(t, gr)

	// shared-skill is installed both explicitly and as other-parent's
	// dependency, so its entry carries Explicit=true and RequiredBy.
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: sharedRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	otherRef, _ := gitRef("other-parent")
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: otherRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before, ok := readLockfile(t, projectRoot).Get("shared-skill")
	require.True(t, ok)
	require.Equal(t, []string{"other-parent"}, before.RequiredBy)

	// Republish shared-skill with a new, unresolvable dependency and force-
	// reinstall it: the install fails after the DB record and lock entry
	// were already rewritten.
	missingDepRef, _ := gitRef("missing-dep")
	fx.register("shared-skill", gitSkill("shared-skill", missingDepRef))
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: sharedRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"}, Force: true,
	})
	require.Error(t, err, "reinstall must fail on the unresolvable dependency")

	after, ok := readLockfile(t, projectRoot).Get("shared-skill")
	require.True(t, ok, "a transient failure must not destroy the pre-existing lock entry")
	assert.Equal(t, before.Digest, after.Digest, "the previous pin must be restored")
	assert.Equal(t, before.RequiredBy, after.RequiredBy, "RequiredBy links from other parents must survive")
	assert.True(t, after.Explicit)

	info, err := svc.Info(t.Context(), skills.InfoOptions{Name: "shared-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err, "the pre-existing DB record must survive")
	assert.Equal(t, before.Digest, info.InstalledSkill.Digest, "the DB record must be restored to its previous state")
}

// TestInstallProjectScope_DependencyJoinsParentGroup asserts a transitively
// materialized dependency is registered in the same group the parent was
// installed into, rather than silently falling back to the default group.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallProjectScope_DependencyJoinsParentGroup(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("grouped-dep")
	fx.register("grouped-dep", gitSkill("grouped-dep"))
	fx.register("grouped-parent", gitSkill("grouped-parent", depRef))
	svc, projectRoot := newLockTestService(t, gr)

	// Track which group each skill lands in via a stub manager.
	skillGroups := make(map[string]string)
	gm := groupmocks.NewMockManager(gomock.NewController(t))
	gm.EXPECT().Get(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, name string) (*groups.Group, error) {
			return &groups.Group{Name: name}, nil
		})
	gm.EXPECT().Update(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, g *groups.Group) error {
			for _, sk := range g.Skills {
				skillGroups[sk] = g.Name
			}
			return nil
		})
	svcImpl := svc.(*service) //nolint:forcetypeassert // white-box test in the same package
	svcImpl.groupManager = gm

	ref, _ := gitRef("grouped-parent")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		Clients: []string{"claude-code"}, Group: "custom-group",
	})
	require.NoError(t, err)

	assert.Equal(t, "custom-group", skillGroups["grouped-parent"])
	assert.Equal(t, "custom-group", skillGroups["grouped-dep"],
		"a materialized dependency must join the parent's group, not the default one")
}

// TestUninstall_LockWriteFailureAbortsBeforeDestruction guards the uninstall
// ordering: the lock entry is removed first, and if that fails the
// uninstall aborts with everything intact. The reverse order left a stale
// lock entry for a gone skill, which the next sync silently reinstalled.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUninstall_LockWriteFailureAbortsBeforeDestruction(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Make the lock file unwritable: read-only project root blocks the
	// temp-file write inside lockfile.Update.
	require.NoError(t, os.Chmod(projectRoot, 0o555))
	t.Cleanup(func() { _ = os.Chmod(projectRoot, 0o755) })

	err = svc.Uninstall(t.Context(), skills.UninstallOptions{
		Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "uninstall must fail when the lock entry cannot be removed")

	require.NoError(t, os.Chmod(projectRoot, 0o755))
	info, err := svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err, "the skill must remain fully installed — nothing may be destroyed before the lock update")
	assert.NotNil(t, info.InstalledSkill)
	_, ok := readLockfile(t, projectRoot).Get("my-skill")
	assert.True(t, ok, "the lock entry must be untouched")

	skillMD := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "SKILL.md")
	_, statErr := os.Stat(skillMD)
	require.NoError(t, statErr, "on-disk files must be untouched")
}
