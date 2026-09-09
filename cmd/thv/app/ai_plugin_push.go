// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/identitytoken"
)

var (
	aiPluginPushKey           string
	aiPluginPushIdentityToken string
	aiPluginPushNoSign        bool
)

var aiPluginPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built AI-tool plugin to an OCI registry",
	Long: `Push a previously built plugin artifact to a remote OCI registry.

Push signs keylessly by default. Use --key to sign with a cosign key pair
instead, or --no-sign to publish unsigned.`,
	Args: cobra.ExactArgs(1),
	RunE: aiPluginPushCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginPushCmd)
	aiPluginPushCmd.Flags().StringVar(&aiPluginPushKey, "key", "",
		"Path to a cosign private key to sign the pushed artifact. The path is resolved by the "+
			"'thv serve' process that performs the signing, NOT by this command: against a remote "+
			"server the key file and COSIGN_PASSWORD must both be present there, so provision or "+
			"mount the key on that host — or use keyless signing, which needs no key at all. "+
			"Consumers installing the result project-scoped must pass --public-key with the "+
			"matching cosign public key the first time; distribute it alongside the artifact. "+
			"Keyless signing needs no such out-of-band step, since the signer identity is "+
			"verifiable from the artifact itself")
	aiPluginPushCmd.Flags().StringVar(&aiPluginPushIdentityToken, "identity-token", "",
		"OIDC identity token (or a path to a file containing one) for keyless signing. "+
			"Mutually exclusive with --key. If omitted, one is acquired automatically: from the "+
			"GitHub Actions OIDC token when running with id-token: write permission, otherwise "+
			"via an interactive browser sign-in")
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
		Key:       aiPluginPushKey,
		NoSign:    aiPluginPushNoSign,
		Confirm:   confirmBrowserSignIn,
		Remediation: "Provide --key or --identity-token, run in CI with id-token: write permission, " +
			"or pass --no-sign to push unsigned",
	})
	if err != nil {
		return formatAIPluginError("push plugin", err)
	}

	c := newAIPluginClient(ctx)
	err = c.Push(ctx, plugins.PushOptions{
		Reference:     args[0],
		Key:           aiPluginPushKey,
		IdentityToken: token,
		NoSign:        aiPluginPushNoSign,
	})
	if err != nil {
		return formatAIPluginError("push plugin", err)
	}

	return nil
}
