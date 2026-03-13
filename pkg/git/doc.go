// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package git provides Git repository operations for ToolHive.
//
// This package implements a thin wrapper around the go-git library to enable
// cloning repositories, checking out specific branches/tags/commits, and
// retrieving file contents. It is used by both the Kubernetes operator
// (for MCPRegistry Git sources) and the CLI (for git-based skill installation).
//
// Key Components:
//
// # Client Interface
//
// The Client interface defines the core Git operations:
//   - Clone: Clone repositories (public or authenticated)
//   - GetFileContent: Retrieve specific files from repositories
//   - Cleanup: Release in-memory repository resources
//
// # LimitedFs
//
// LimitedFs wraps a billy.Filesystem to enforce file count and total size limits,
// preventing resource exhaustion when cloning untrusted repositories.
//
// # Security Considerations
//
// This package is designed for use in environments where Git repositories may
// be untrusted. Resource limits are enforced via LimitedFs (10k files, 100MB).
// Callers are responsible for URL validation (SSRF prevention) and credential
// management.
package git
