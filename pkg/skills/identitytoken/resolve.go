// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package identitytoken resolves and acquires the OIDC identity token used
// for keyless skill push signing (RFC THV-0080 / #6307). It is CLI-side
// only: the server never handles credentials, only the already-acquired
// token forwarded in the push request (skills.PushOptions.IdentityToken).
package identitytoken

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve interprets the --identity-token flag value: a path to a file
// containing the token, or the raw token itself (cosign parity with --key,
// which already accepts a path). An existing regular file wins; otherwise a
// JWT-shaped value (two dots) is used as a literal token; otherwise this is
// an error rather than a guess, so a mistyped path is never silently
// forwarded to Fulcio as if it were a token.
func Resolve(value string) (string, error) {
	if path, ok := existingFile(value); ok {
		data, err := os.ReadFile(path) //nolint:gosec // path is canonicalized and vetted by existingFile
		if err != nil {
			return "", fmt.Errorf("reading identity token file %q: %w", value, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if looksLikeJWT(value) {
		return value, nil
	}
	return "", fmt.Errorf("--identity-token %q is neither a readable file nor a JWT", value)
}

// existingFile reports whether value names a readable regular file,
// following the same canonicalization core's resolveKeyPath uses for --key:
// absolute, cleaned, symlinks resolved.
func existingFile(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// looksLikeJWT applies the same shape heuristic already used elsewhere in
// this repo (pkg/authz/authorizers/cedar/core.go) to distinguish a
// compact-serialized JWT from other input: exactly two dots. This is a
// disambiguation heuristic, not validation — Fulcio is the real authority
// on whether the token is valid.
func looksLikeJWT(s string) bool {
	return strings.Count(s, ".") == 2
}
