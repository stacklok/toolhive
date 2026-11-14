// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginInstallScope         string
	aiPluginInstallClientsRaw    string
	aiPluginInstallForce         bool
	aiPluginInstallProjectRoot   string
	aiPluginInstallGroup         string
	aiPluginInstallAllowUnsigned bool
	aiPluginInstallPublicKey     string
)

var aiPluginInstallCmd = &cobra.Command{
	Use:   "install [plugin-name]",
	Short: "Install an AI-tool plugin",
	Long: `Install a plugin from git, an OCI reference, or an exact registry name.

Project-scoped installs verify signatures and record trust in toolhive.lock.yaml.
Use --public-key for the first project install of a key-pair-signed OCI artifact;
the key is then pinned for sync and upgrade. User-scoped installs do not use
lock-file verification and reject --public-key.`,
	Args: cobra.ExactArgs(1),
	PreRunE: chainPreRunE(
		validateAIPluginScope(&aiPluginInstallScope),
		validateProjectRootForScope(&aiPluginInstallScope, &aiPluginInstallProjectRoot),
		validateGroupFlag(),
	),
	RunE: aiPluginInstallCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginInstallCmd)

	aiPluginInstallCmd.Flags().StringVar(&aiPluginInstallClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,codex), or "all" for every available client`)
	aiPluginInstallCmd.Flags().StringVar(
		&aiPluginInstallScope, "scope", string(plugins.ScopeUser), "Installation scope (user, project)",
	)
	aiPluginInstallCmd.Flags().BoolVar(&aiPluginInstallForce, "force", false, "Overwrite existing plugin directory")
	aiPluginInstallCmd.Flags().StringVar(
		&aiPluginInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs",
	)
	aiPluginInstallCmd.Flags().StringVar(&aiPluginInstallGroup, "group", "", "Group to add the plugin to after installation")
	aiPluginInstallCmd.Flags().BoolVar(&aiPluginInstallAllowUnsigned, "allow-unsigned", false,
		"Allow installing a project-scoped plugin without a verified signature (recorded in the lock file)")
	aiPluginInstallCmd.Flags().StringVar(&aiPluginInstallPublicKey, "public-key", "",
		"Path to the cosign public key (cosign.pub) a key-pair-signed plugin must verify against."+
			" Required the first time such a plugin is installed project-scoped; the key is then pinned"+
			" in the lock file and reused automatically")
}

func aiPluginInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())

	projectRoot, err := absProjectRoot(aiPluginInstallProjectRoot)
	if err != nil {
		return err
	}

	// Shared with `thv skill install`: the flag names a file, but the API
	// carries the key material, because the server may be another process on
	// another host where that path names nothing — or something else.
	publicKey, err := readInstallPublicKey(aiPluginInstallPublicKey)
	if err != nil {
		return err
	}

	result, err := c.Install(cmd.Context(), plugins.InstallOptions{
		Name:          args[0],
		Scope:         plugins.Scope(aiPluginInstallScope),
		Clients:       parseSkillInstallClients(aiPluginInstallClientsRaw),
		Force:         aiPluginInstallForce,
		ProjectRoot:   projectRoot,
		Group:         aiPluginInstallGroup,
		AllowUnsigned: aiPluginInstallAllowUnsigned,
		PublicKey:     publicKey,
	})
	if err != nil {
		return formatAIPluginError("install plugin", err)
	}

	printPluginInstallTrust(result)
	return nil
}

// printPluginInstallTrust shows the trust state the install recorded — RFC
// THV-0080 wants the pinned identity displayed prominently, not discovered
// weeks later inside a signer-mismatch error.
//
// Only a recorded trust decision is printed. An install with neither — a
// user-scope install, which writes no lock entry — returns silently, per the
// CLI's silent-success rule: a bare "Installed <name>" carries no trust
// information and would turn every previously quiet install into output.
func printPluginInstallTrust(result *plugins.InstallResult) {
	if result == nil {
		return
	}
	name := result.Plugin.Metadata.Name
	switch {
	// Before the identity cases: a key-pinned install has no signer identity
	// to name, and "signed by " with nothing after it is worse than silence.
	case result.Provenance != nil && result.Provenance.PublicKey != "":
		fmt.Printf("Installed %s (signed by a cosign key pair; the pinned public key is in the lock file)\n", name)
	case result.Provenance != nil && result.Provenance.Provisional:
		fmt.Printf("Installed %s (signed by %s; verification provisional — see lock file)\n",
			name, result.Provenance.SignerIdentity)
	case result.Provenance != nil:
		fmt.Printf("Installed %s (signed by %s)\n", name, result.Provenance.SignerIdentity)
	case result.Unsigned:
		fmt.Printf("Installed %s (unsigned — recorded as an explicit exception in the lock file)\n", name)
	}
}
