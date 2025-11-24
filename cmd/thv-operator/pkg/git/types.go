package git

import (
	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
)

// CloneConfig contains configuration for cloning a repository
type CloneConfig struct {
	// URL is the repository URL to clone
	URL string

	// Branch is the specific branch to clone (optional)
	Branch string

	// Tag is the specific tag to clone (optional)
	Tag string

	// Commit is the specific commit to clone (optional)
	Commit string
}

// RepositoryInfo contains information about a Git repository
type RepositoryInfo struct {
	// Repository is the go-git repository instance
	Repository *git.Repository

	// Branch is the current branch name
	Branch string

	// RemoteURL is the remote repository URL
	RemoteURL string

	// storerFilesystem holds the in-memory filesystem containing the Git object database (.git/objects).
	// This reference is stored during Clone() and must be explicitly cleared in Cleanup() to release
	// memory, as go-git does not provide automatic cleanup of internal storage structures.
	storerFilesystem billy.Filesystem

	// objectCache holds the LRU cache for decompressed Git objects (commits, trees, blobs).
	// When a repository is cloned, git objects are decompressed and cached here. This cache must
	// be explicitly cleared via Clear() during Cleanup() to release memory, as the garbage collector
	// cannot reclaim cached objects while this reference exists.
	objectCache cache.Object
}
