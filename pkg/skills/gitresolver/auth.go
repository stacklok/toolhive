// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// tokenMapping maps environment variable names to the git hosts they are scoped to.
// Tokens are only sent to their matching host to prevent credential exfiltration.
var tokenMapping = []struct {
	envVar string
	hosts  []string // empty means the token is sent to any host (user opt-in)
}{
	{envVar: "GITHUB_TOKEN", hosts: []string{"github.com"}},
	{envVar: "GITLAB_TOKEN", hosts: []string{"gitlab.com"}},
	{envVar: "GIT_TOKEN", hosts: nil}, // fallback: sent to any host
}

// EnvFunc is a function that looks up an environment variable.
// The default is os.Getenv; tests can inject a custom implementation.
type EnvFunc func(string) string

// ResolveAuth attempts to find authentication credentials from the environment
// scoped to the given clone URL. Returns nil if no credentials match.
//
// Security: tokens are only sent to their designated hosts. GITHUB_TOKEN is
// only sent to github.com, GITLAB_TOKEN only to gitlab.com. GIT_TOKEN is a
// fallback sent to any host.
func ResolveAuth(cloneURL string) transport.AuthMethod {
	return ResolveAuthWith(os.Getenv, cloneURL)
}

// ResolveAuthWith is like ResolveAuth but uses the provided function to look up
// environment variables, making it testable without modifying process state.
func ResolveAuthWith(getenv EnvFunc, cloneURL string) transport.AuthMethod {
	host := extractHost(cloneURL)

	for _, mapping := range tokenMapping {
		token := getenv(mapping.envVar)
		if token == "" {
			continue
		}
		// If hosts are specified, only send the token to matching hosts.
		if len(mapping.hosts) > 0 && !hostMatches(host, mapping.hosts) {
			continue
		}
		// Log when the fallback GIT_TOKEN is used for non-standard hosts so
		// users can audit credential usage.
		if len(mapping.hosts) == 0 {
			slog.Debug("Using fallback GIT_TOKEN for non-standard host — verify this is intended",
				"env_var", mapping.envVar, "host", host)
		} else {
			slog.Debug("Using git authentication from environment", "env_var", mapping.envVar)
		}
		return &githttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	}

	return nil
}

// extractHost returns the lowercase hostname from a URL, or empty string on failure.
func extractHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// hostMatches checks if host matches any of the allowed hosts (case-insensitive).
func hostMatches(host string, allowed []string) bool {
	for _, h := range allowed {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}
