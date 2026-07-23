// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// requireConfirmation enforces RFC THV-0080's pre-install confirmation gate
// for sync/upgrade. With yes set, it returns immediately without prompting.
// On an interactive terminal, it prompts and reports whether the user
// confirmed. In a non-interactive context, it refuses outright with a
// policy-rejection exit code rather than silently proceeding: skill content
// is a set of AI-executed instructions, so unattended execution without an
// explicit --yes is not an acceptable default the way it might be for a
// lower-stakes operation.
//
// The prompt goes to stderr — stdout is reserved for the command's result
// (e.g. --format json output must stay machine-parseable) — and everything
// echoed into it is sanitized: a directory name carrying ANSI/OSC escapes
// must not be able to repaint the very prompt acting as the human gate.
func requireConfirmation(action string, yes bool) (confirmed bool, err error) {
	if yes {
		return true, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // uintptr fits int on all supported platforms
		return false, withExitCode(
			fmt.Errorf("%s requires confirmation; pass --yes to run non-interactively", sanitizeTerminal(action)),
			ExitCodePolicyRejection,
		)
	}

	fmt.Fprintf(os.Stderr, "%s? [y/N]: ", sanitizeTerminal(action))
	reader := bufio.NewReader(os.Stdin)
	response, readErr := reader.ReadString('\n')
	if readErr != nil {
		return false, fmt.Errorf("failed to read user input: %w", readErr)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

// printLockEntriesSummary shows what the confirmation is actually about:
// the lock entries sync/upgrade will act on (name, source, pinned digest).
// A bare "proceed? [y/N]" tells the user nothing — a lock-file diff that
// swapped a digest or resolvedReference would sail past it unseen. Printed
// to stderr alongside the prompt; best-effort (an unreadable lock file is
// reported by the command itself, not here).
func printLockEntriesSummary(projectRoot string) {
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return
	}
	lf, err := lockfile.Load(root)
	if err != nil || len(lf.Skills) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Lock file entries for %s:\n", sanitizeTerminal(projectRoot))
	for _, e := range lf.Skills {
		fmt.Fprintf(os.Stderr, "  %s  %s  %s\n",
			sanitizeTerminal(e.Name), sanitizeTerminal(e.Source), sanitizeTerminal(shortDigest(e.Digest)))
	}
}

// shortDigest truncates a pin for display: enough hex to eyeball-compare,
// not enough to dominate the line.
func shortDigest(d string) string {
	const keep = 19 // "sha256:" + 12 hex, or 19 chars of a commit hash
	if len(d) <= keep {
		return d
	}
	return d[:keep] + "…"
}

// sanitizeTerminal strips non-graphic runes (control characters, ANSI/OSC
// escape introducers) from a string echoed to the terminal, keeping spaces.
func sanitizeTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || unicode.IsGraphic(r) {
			return r
		}
		return -1
	}, s)
}
