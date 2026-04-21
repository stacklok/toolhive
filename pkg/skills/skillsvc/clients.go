// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/skills"
)

// clientsAllSentinel is the reserved value that expands to all skill-supporting clients.
const clientsAllSentinel = "all"

// resolveAndValidateClients returns the deduplicated client list and a map of
// client identifier to install directory. Empty opts.Clients (or the sentinel
// value "all") expands to every skill-supporting client returned by the path resolver.
func (s *service) resolveAndValidateClients(
	opts skills.InstallOptions,
	skillName string,
	scope skills.Scope,
	projectRoot string,
) ([]string, map[string]string, error) {
	if s.pathResolver == nil {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("path resolver is required for skill installs"),
			http.StatusInternalServerError,
		)
	}

	var requested []string
	switch {
	case len(opts.Clients) == 0 || (len(opts.Clients) == 1 && strings.EqualFold(opts.Clients[0], clientsAllSentinel)):
		clients := s.pathResolver.ListSkillSupportingClients()
		if len(clients) == 0 {
			return nil, nil, httperr.WithCode(
				errors.New("no supported clients detected on this system; "+
					"use --clients to target a specific client explicitly"),
				http.StatusBadRequest,
			)
		}
		requested = clients
	default:
		for _, c := range opts.Clients {
			if c == "" {
				return nil, nil, httperr.WithCode(
					errors.New("clients entries must be non-empty strings"),
					http.StatusBadRequest,
				)
			}
			if strings.EqualFold(c, clientsAllSentinel) {
				return nil, nil, httperr.WithCode(
					fmt.Errorf("%q cannot be combined with other client names", clientsAllSentinel),
					http.StatusBadRequest,
				)
			}
		}
		requested = dedupeStringsPreserveOrder(opts.Clients)
	}

	paths := make(map[string]string, len(requested))
	for _, ct := range requested {
		dir, err := s.pathResolver.GetSkillPath(ct, skillName, scope, projectRoot)
		if err != nil {
			if errors.Is(err, client.ErrUnsupportedClientType) || errors.Is(err, client.ErrSkillsNotSupported) {
				return nil, nil, httperr.WithCode(
					fmt.Errorf("invalid client %q: %w", ct, err),
					http.StatusBadRequest,
				)
			}
			return nil, nil, fmt.Errorf("resolving skill path for client %q: %w", ct, err)
		}
		dir = filepath.Clean(dir)
		if err := validateResolvedDir(dir); err != nil {
			return nil, nil, fmt.Errorf("resolved path for client %q is unsafe: %w", ct, err)
		}
		paths[ct] = dir
	}
	return requested, paths, nil
}

// expandToExistingClients merges existingClients into requestedClients and
// resolves paths for any existing client not already in clientDirs. This
// ensures upgrades write new files to all clients, not just the requested set.
func (s *service) expandToExistingClients(
	existingClients, requestedClients []string,
	clientDirs map[string]string,
	skillName string, scope skills.Scope, projectRoot string,
) ([]string, map[string]string, error) {
	allClients := mergeClientLists(requestedClients, existingClients)
	allDirs := make(map[string]string, len(allClients))
	for k, v := range clientDirs {
		allDirs[k] = v
	}
	for _, ct := range allClients {
		if _, ok := allDirs[ct]; ok {
			continue
		}
		dir, err := s.pathResolver.GetSkillPath(ct, skillName, scope, projectRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving skill path for existing client %q: %w", ct, err)
		}
		dir = filepath.Clean(dir)
		if err := validateResolvedDir(dir); err != nil {
			return nil, nil, fmt.Errorf("resolved path for client %q is unsafe: %w", ct, err)
		}
		allDirs[ct] = dir
	}
	return allClients, allDirs, nil
}

// validateResolvedDir ensures a directory path is absolute and free of
// path-traversal segments. Callers must pass a filepath.Clean'd value.
func validateResolvedDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("path must be absolute, got %q", dir)
	}
	for _, seg := range strings.Split(filepath.ToSlash(dir), "/") {
		if seg == ".." {
			return fmt.Errorf("path contains traversal segment: %q", dir)
		}
	}
	return nil
}

func dedupeStringsPreserveOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// clientsContainAll reports whether every value in requested appears in existing.
func clientsContainAll(existing, requested []string) bool {
	for _, r := range requested {
		if !slices.Contains(existing, r) {
			return false
		}
	}
	return true
}

// mergeClientLists returns existing followed by any requested entries not already present.
func mergeClientLists(existing, requested []string) []string {
	out := make([]string, len(existing))
	copy(out, existing)
	seen := make(map[string]struct{}, len(existing)+len(requested))
	for _, c := range existing {
		seen[c] = struct{}{}
	}
	for _, c := range requested {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func missingClients(existing, requested []string) []string {
	var out []string
	for _, ct := range requested {
		if !slices.Contains(existing, ct) {
			out = append(out, ct)
		}
	}
	return out
}

// uniqueDirClients returns the subset of clients whose resolved directory is
// unique. When multiple clients share the same path (e.g. vscode and
// vscode-insider both using ~/.copilot/skills), only the first is returned.
// This prevents double-extraction while still recording all clients in the DB.
//
// occupiedDirs is pre-seeded into the seen set so that new clients whose
// directory is already owned by an existing installed client are also skipped.
// Pass nil when there are no pre-existing directories to exclude.
func uniqueDirClients(clients []string, clientDirs map[string]string, occupiedDirs map[string]struct{}) []string {
	seen := make(map[string]struct{}, len(clients)+len(occupiedDirs))
	for dir := range occupiedDirs {
		seen[dir] = struct{}{}
	}
	out := make([]string, 0, len(clients))
	for _, ct := range clients {
		dir := filepath.Clean(clientDirs[ct])
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, ct)
	}
	return out
}

// existingClientDirs builds the set of directories already occupied by the
// given installed clients. Used to seed uniqueDirClients so that new clients
// sharing a directory with an existing client are skipped rather than
// triggering a false "directory exists" conflict.
func existingClientDirs(existing []string, clientDirs map[string]string) map[string]struct{} {
	dirs := make(map[string]struct{}, len(existing))
	for _, ct := range existing {
		if dir, ok := clientDirs[ct]; ok {
			dirs[filepath.Clean(dir)] = struct{}{}
		}
	}
	return dirs
}
