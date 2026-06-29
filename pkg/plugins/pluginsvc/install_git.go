// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/stacklok/toolhive-core/httperr"
	ociartifact "github.com/stacklok/toolhive-core/oci/artifact"
	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// cloneTimeout is the maximum time allowed for cloning a git repository.
// Mirror of gitresolver.cloneTimeout.
const cloneTimeout = 2 * time.Minute

// semverLike matches refs that look like semantic version tags. Mirror of
// gitresolver.semverLike.
var semverLike = regexp.MustCompile(`^v\d+\.\d+(\.\d+)*(-[a-zA-Z0-9._-]+)?$`)

// installFromGit clones a git repository, reads the plugin manifest, collects
// the plugin file tree, builds an in-memory tar.gz layer, and delegates to
// installWithExtraction. The digest is the git commit hash, enabling same-commit
// no-op and upgrade detection.
//
// Unlike skillsvc.installFromGit, this does NOT call gitResolver.Resolve
// (which is skill-specific: it reads SKILL.md). Instead it replicates the
// clone+collect flow and reads .claude-plugin/plugin.json via
// plugins.ParsePluginManifest.
func (s *service) installFromGit(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
) (*plugins.InstallResult, error) {
	if len(s.materializers) == 0 {
		return nil, httperr.WithCode(
			fmt.Errorf("no materializers configured for plugin installs"),
			http.StatusInternalServerError,
		)
	}

	gitRef, err := gitresolver.ParseGitReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid git reference: %w", err),
			http.StatusBadRequest,
		)
	}

	gitURL := opts.Name

	files, manifest, commitHash, err := s.cloneAndCollectPlugin(ctx, gitRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("resolving git plugin: %w", err),
			http.StatusBadGateway,
		)
	}

	if err := plugins.ValidatePluginName(manifest.Name); err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin contains invalid name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Build the in-memory tar.gz layer from the collected file tree. This
	// matches the OCI artifact layer shape so the MaterializationAdapter can
	// extract it identically to an OCI-pulled plugin.
	layerData, err := ociartifact.CompressTar(files, ociartifact.DefaultTarOptions(), ociartifact.DefaultGzipOptions())
	if err != nil {
		return nil, fmt.Errorf("compressing git plugin tree: %w", err)
	}

	// Hydrate install options from the git result.
	opts.Name = manifest.Name
	opts.LayerData = layerData
	opts.Reference = gitURL
	opts.Digest = commitHash
	opts.Components = manifestComponentInventory(manifest)
	opts.Description = manifest.Description
	if opts.Version == "" && manifest.Version != "" {
		opts.Version = manifest.Version
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	return s.installWithExtraction(ctx, opts, scope)
}

// cloneAndCollectPlugin clones the repo referenced by gitRef, reads the plugin
// manifest, and collects every file under the plugin subdirectory. Returns the
// files as ociartifact.FileEntry values (ready for CompressTar), the parsed
// manifest, and the commit hash.
func (s *service) cloneAndCollectPlugin(
	ctx context.Context, gitRef *gitresolver.GitReference,
) ([]ociartifact.FileEntry, *plugins.PluginManifest, string, error) {
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	cloneConfig := &git.CloneConfig{URL: gitRef.URL}
	if gitRef.Ref != "" {
		switch {
		case len(gitRef.Ref) == 40 && isHex(gitRef.Ref):
			cloneConfig.Commit = gitRef.Ref
		case semverLike.MatchString(gitRef.Ref):
			cloneConfig.Tag = gitRef.Ref
		default:
			cloneConfig.Branch = gitRef.Ref
		}
	}

	client := s.gitClientForURL(gitRef.URL)
	repoInfo, err := client.Clone(ctx, cloneConfig)
	if err != nil {
		return nil, nil, "", fmt.Errorf("cloning repository: %w", err)
	}
	defer func() { _ = client.Cleanup(ctx, repoInfo) }()

	commitHash, err := client.HeadCommitHash(repoInfo)
	if err != nil {
		return nil, nil, "", fmt.Errorf("getting commit hash: %w", err)
	}

	// Read the plugin manifest. It lives at <path>/.claude-plugin/plugin.json.
	// We collect the whole file tree first, then locate the manifest among the
	// collected entries so we don't need a second pass over the repo.
	fileEntries, err := collectPluginFiles(repoInfo, gitRef.Path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("collecting plugin files: %w", err)
	}

	manifestBytes, err := findManifestBytes(fileEntries)
	if err != nil {
		return nil, nil, "", fmt.Errorf("reading plugin manifest: %w", err)
	}
	manifest, err := plugins.ParsePluginManifestFromBytes(manifestBytes)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parsing plugin manifest: %w", err)
	}
	return fileEntries, manifest, commitHash, nil
}

// gitClientForURL returns a git client for the given clone URL. When a fixed
// client is configured (testing) it is returned as-is; otherwise a new client
// is created with host-scoped auth from the environment. Mirror of
// gitresolver.defaultResolver.clientForURL.
func (s *service) gitClientForURL(cloneURL string) git.Client {
	if s.gitClient != nil {
		return s.gitClient
	}
	auth := gitresolver.ResolveAuth(cloneURL)
	var opts []git.ClientOption
	if auth != nil {
		opts = append(opts, git.WithAuth(auth))
	}
	return git.NewDefaultGitClient(opts...)
}

// collectPluginFiles reads all files from the given path in the repository,
// walking nested subtrees recursively. Returned paths are forward-slash
// relative to basePath. Mirror of gitresolver.defaultResolver.collectFiles,
// adapted to produce ociartifact.FileEntry values.
func collectPluginFiles(repoInfo *git.RepositoryInfo, basePath string) ([]ociartifact.FileEntry, error) {
	ref, err := repoInfo.Repository.Head()
	if err != nil {
		return nil, fmt.Errorf("getting HEAD: %w", err)
	}
	commit, err := repoInfo.Repository.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("getting commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("getting tree: %w", err)
	}
	if basePath != "" {
		tree, err = tree.Tree(basePath)
		if err != nil {
			return nil, fmt.Errorf("navigating to path %q: %w", basePath, err)
		}
	}

	var files []ociartifact.FileEntry
	err = tree.Files().ForEach(func(f *object.File) error {
		content, contentErr := f.Contents()
		if contentErr != nil {
			return fmt.Errorf("reading content of %q: %w", f.Name, contentErr)
		}
		files = append(files, ociartifact.FileEntry{
			Path:    f.Name,
			Content: []byte(content),
			Mode:    int64(fs.FileMode(0644)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterating tree: %w", err)
	}
	return files, nil
}

// findManifestBytes locates the .claude-plugin/plugin.json entry among the
// collected files and returns its content. Match is case-insensitive on the
// path for robustness against path-separator differences.
func findManifestBytes(files []ociartifact.FileEntry) ([]byte, error) {
	target := path.Clean(plugins.ManifestPath)
	for _, f := range files {
		if path.Clean(f.Path) == target {
			return f.Content, nil
		}
	}
	return nil, fmt.Errorf("%s not found in plugin directory", plugins.ManifestPath)
}

// manifestComponentInventory derives a ComponentInventory from a parsed plugin
// manifest by counting entries in each component directory array.
func manifestComponentInventory(m *plugins.PluginManifest) plugins.ComponentInventory {
	if m == nil {
		return nil
	}
	inv := plugins.ComponentInventory{}
	if len(m.Commands) > 0 {
		inv[string(plugins.ComponentCommands)] = len(m.Commands)
	}
	if len(m.Agents) > 0 {
		inv[string(plugins.ComponentAgents)] = len(m.Agents)
	}
	if len(m.Skills) > 0 {
		inv[string(plugins.ComponentSkills)] = len(m.Skills)
	}
	if len(m.Hooks) > 0 {
		inv[string(plugins.ComponentHooks)] = len(m.Hooks)
	}
	if m.McpServers != nil {
		inv[string(plugins.ComponentMCP)] = 1
	}
	if m.LspServers != nil {
		inv[string(plugins.ComponentLSP)] = 1
	}
	if len(inv) == 0 {
		return nil
	}
	return inv
}

// isHex checks if a string is a valid non-empty hexadecimal string. Mirror of
// gitresolver.isHex.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
			continue
		default:
			return false
		}
	}
	return true
}
