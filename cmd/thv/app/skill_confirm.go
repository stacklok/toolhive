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

func confirmAction(prompt string) (bool, error) {
	fmt.Printf("\n%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read user input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

func requireInteractiveConfirmation(yes bool, summaryFn func()) error {
	if yes {
		return nil
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if !interactive {
		return newExitCodeError(ExitCodePolicyRejection,
			fmt.Errorf("non-interactive terminal: pass --yes to proceed without confirmation"))
	}
	summaryFn()
	confirmed, err := confirmAction("Install?")
	if err != nil {
		return err
	}
	if !confirmed {
		return newExitCodeError(ExitCodePolicyRejection, fmt.Errorf("operation cancelled"))
	}
	return nil
}
