// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/identitytoken"
)

var (
	aiPluginPushIdentityToken string
	aiPluginPushNoSign        bool
)

var aiPluginPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built AI-tool plugin to an OCI registry",
	Long: `Push a previously built plugin artifact to a remote OCI registry.

Push signs keylessly by default. Use --no-sign to publish unsigned; plugin push
does not support key-pair signing and has no --key flag.`,
	Args: cobra.ExactArgs(1),
	RunE: aiPluginPushCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginPushCmd)
	// No --key flag: plugin push supports keyless signing or explicit
	// --no-sign. Project installs can verify externally key-pair-signed
	// artifacts with ai-plugin install --public-key.
	aiPluginPushCmd.Flags().StringVar(&aiPluginPushIdentityToken, "identity-token", "",
		"OIDC identity token (or a path to a file containing one) for keyless signing. "+
			"If omitted, one is acquired automatically: from the GitHub Actions OIDC token when "+
			"running with id-token: write permission, otherwise via an interactive browser sign-in")
	aiPluginPushCmd.Flags().BoolVar(&aiPluginPushNoSign, "no-sign", false,
		"Push without signing (consumers will need an explicit unsigned exception to install project-scoped)")
}

func aiPluginPushCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Shared with `thv skill push`: the acquisition ladder (explicit flag →
	// ambient CI token → interactive browser sign-in) is a property of
	// Sigstore keyless signing, not of the artifact kind being pushed.
	token, err := identitytoken.Acquire(ctx, identitytoken.Options{
		FlagValue: aiPluginPushIdentityToken,
		NoSign:    aiPluginPushNoSign,
		Confirm:   confirmBrowserSignIn,
		// Plugin push has no --key flag, so remediation must not offer
		// a signing choice this command does not define.
		Remediation: "Provide --identity-token, run in CI with id-token: write permission, " +
			"or pass --no-sign to push unsigned",
	})
	if err != nil {
		return formatAIPluginError("push plugin", err)
	}

	c := newAIPluginClient(ctx)
	err = c.Push(ctx, plugins.PushOptions{
		Reference:     args[0],
		IdentityToken: token,
		NoSign:        aiPluginPushNoSign,
	})
	if err != nil {
		return formatAIPluginError("push plugin", err)
	}

	return nil
}
