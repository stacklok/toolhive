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

	svc := New(store, WithPathResolver(pr), WithGitResolver(gr))
	return svc, projectRoot
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
// own commit hash so digests differ.
type fakeGitFixtures struct {
	skills map[string]gitFixture // keyed by URL
}

func newGitResolverMock(t *testing.T) (*gitmocks.MockResolver, *fakeGitFixtures) {
	t.Helper()
	fx := &fakeGitFixtures{skills: make(map[string]gitFixture)}
	gr := gitmocks.NewMockResolver(gomock.NewController(t))
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, ref *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
			fixture, ok := fx.skills[ref.URL]
			if !ok {
				return nil, fmt.Errorf("no fixture registered for %q", ref.URL)
			}
			return &gitresolver.ResolveResult{
				SkillConfig: &skills.ParseResult{Name: fixture.name},
				Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: fixture.content, Mode: 0644}},
				CommitHash:  fmt.Sprintf("%040x", len(fixture.content)+len(ref.URL)), // deterministic, distinct per fixture
			}, nil
		})
	return gr, fx
}

func (f *fakeGitFixtures) register(name string, content []byte) {
	_, url := gitRef(name)
	f.skills[url] = gitFixture{name: name, content: content}
}

func readLockfile(t *testing.T, projectRoot string) *lockfile.Lockfile {
	t.Helper()
	root, err := lockfile.OpenRoot(projectRoot)
	require.NoError(t, err)
	lf, err := lockfile.Load(root)
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

	err := svcImpl.materializeDependencies(t.Context(), skills.InstallOptions{Visited: visited}, sk)
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
