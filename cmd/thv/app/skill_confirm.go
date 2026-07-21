// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// requireConfirmation enforces RFC THV-0080's pre-install confirmation gate
// for sync/upgrade. With yes set, it returns immediately without prompting.
// On an interactive terminal, it prompts and reports whether the user
// confirmed. In a non-interactive context, it refuses outright with a
// policy-rejection exit code rather than silently proceeding: skill content
// is a set of AI-executed instructions, so unattended execution without an
// explicit --yes is not an acceptable default the way it might be for a
// lower-stakes operation.
func requireConfirmation(action string, yes bool) (confirmed bool, err error) {
	if yes {
		return true, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // uintptr fits int on all supported platforms
		return false, withExitCode(
			fmt.Errorf("%s requires confirmation; pass --yes to run non-interactively", action),
			ExitCodePolicyRejection,
		)
	}

	fmt.Printf("%s? [y/N]: ", action)
	reader := bufio.NewReader(os.Stdin)
	response, readErr := reader.ReadString('\n')
	if readErr != nil {
		return false, fmt.Errorf("failed to read user input: %w", readErr)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}
