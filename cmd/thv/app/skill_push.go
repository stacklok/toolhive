// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/identitytoken"
)

var (
	skillPushKey           string
	skillPushIdentityToken string
	skillPushNoSign        bool
)

var skillPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built skill",
	Long:  `Push a previously built skill artifact to a remote OCI registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  skillPushCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillPushCmd)
	skillPushCmd.Flags().StringVar(&skillPushKey, "key", "",
		"Path to a cosign private key to sign the pushed artifact. "+
			"Encrypted keys are decrypted with COSIGN_PASSWORD read from the 'thv serve' process, "+
			"which performs the signing")
	skillPushCmd.Flags().StringVar(&skillPushIdentityToken, "identity-token", "",
		"OIDC identity token (or a path to a file containing one) for keyless signing. "+
			"Mutually exclusive with --key. If omitted, one is acquired automatically: from the "+
			"ambient CI OIDC token when running with id-token: write permission, otherwise via an "+
			"interactive browser sign-in")
	skillPushCmd.Flags().BoolVar(&skillPushNoSign, "no-sign", false,
		"Push without signing (consumers will need an explicit unsigned exception to install project-scoped)")
}

func skillPushCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	token, err := identitytoken.Acquire(ctx, identitytoken.Options{
		FlagValue: skillPushIdentityToken,
		Key:       skillPushKey,
		NoSign:    skillPushNoSign,
		Confirm:   confirmBrowserSignIn,
	})
	if err != nil {
		return formatSkillError("push skill", err)
	}

	c := newSkillClient(ctx)
	err = c.Push(ctx, skills.PushOptions{
		Reference:     args[0],
		Key:           skillPushKey,
		IdentityToken: token,
		NoSign:        skillPushNoSign,
	})
	if err != nil {
		return formatSkillError("push skill", err)
	}

	return nil
}

// confirmBrowserSignIn is the identitytoken.Options.Confirm callback for
// `skill push`: on a non-interactive terminal it declines silently (no
// error — Acquire falls through to its own actionable failure text), since
// this command has no --yes escape hatch the way sync/upgrade do. On a TTY
// it prompts to stderr (stdout is reserved for command output) and reads a
// y/N response. Deliberately not requireConfirmation (skill_confirm.go):
// that helper's non-interactive refusal hardcodes "pass --yes to run
// non-interactively", which doesn't apply to a command with no --yes flag.
func confirmBrowserSignIn() (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // uintptr fits int on all supported platforms
		return false, nil
	}

	fmt.Fprint(os.Stderr, "Sign in with a browser to sign this push keylessly? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read user input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}
