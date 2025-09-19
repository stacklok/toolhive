package git

import (
	"github.com/go-git/go-git/v5"
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

	// Directory is the local directory to clone into
	Directory string
}

// RepositoryInfo contains information about a Git repository
type RepositoryInfo struct {
	// Repository is the go-git repository instance
	Repository *git.Repository

	// Branch is the current branch name
	Branch string

	// RemoteURL is the remote repository URL
	RemoteURL string
}
