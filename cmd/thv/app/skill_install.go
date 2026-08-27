// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

var (
	skillInstallScope         string
	skillInstallClientsRaw    string
	skillInstallForce         bool
	skillInstallProjectRoot   string
	skillInstallGroup         string
	skillInstallAllowUnsigned bool
	skillInstallPublicKey     string
)

var skillInstallCmd = &cobra.Command{
	Use:   "install [skill-name]",
	Short: "Install a skill",
	Long: `Install a skill by name or OCI reference.
The skill will be fetched from a remote registry and installed locally.`,
	Args: cobra.ExactArgs(1),
	PreRunE: chainPreRunE(
		validateSkillScope(&skillInstallScope),
		validateProjectRootForScope(&skillInstallScope, &skillInstallProjectRoot),
		validateGroupFlag(),
	),
	RunE: skillInstallCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillInstallCmd)

	skillInstallCmd.Flags().StringVar(&skillInstallClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillInstallCmd.Flags().StringVar(&skillInstallScope, "scope", string(skills.ScopeUser), "Installation scope (user, project)")
	skillInstallCmd.Flags().BoolVar(&skillInstallForce, "force", false, "Overwrite existing skill directory")
	skillInstallCmd.Flags().StringVar(&skillInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs")
	skillInstallCmd.Flags().StringVar(&skillInstallGroup, "group", "", "Group to add the skill to after installation")
	skillInstallCmd.Flags().BoolVar(&skillInstallAllowUnsigned, "allow-unsigned", false,
		"Allow installing a project-scoped skill without a verified signature (recorded in the lock file)")
	skillInstallCmd.Flags().StringVar(&skillInstallPublicKey, "public-key", "",
		"Path to the cosign public key (cosign.pub) a key-pair-signed skill must verify against."+
			" Required the first time such a skill is installed project-scoped; the key is then pinned"+
			" in the lock file and reused automatically")
}

func skillInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient(cmd.Context())

	projectRoot, err := absProjectRoot(skillInstallProjectRoot)
	if err != nil {
		return err
	}

	publicKey, err := readInstallPublicKey(skillInstallPublicKey)
	if err != nil {
		return err
	}

	result, err := c.Install(cmd.Context(), skills.InstallOptions{
		Name:          args[0],
		Scope:         skills.Scope(skillInstallScope),
		Clients:       parseSkillInstallClients(skillInstallClientsRaw),
		Force:         skillInstallForce,
		ProjectRoot:   projectRoot,
		Group:         skillInstallGroup,
		AllowUnsigned: skillInstallAllowUnsigned,
		PublicKey:     publicKey,
	})
	if err != nil {
		return formatSkillError("install skill", err)
	}

	printInstallTrust(result)
	return nil
}

// readInstallPublicKey turns a --public-key file path into the encoded key
// material the API carries. The CLI reads the file rather than forwarding its
// path because the server is a separate process, possibly on another host,
// where that path names nothing — or something else.
func readInstallPublicKey(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	cleanPath := filepath.Clean(path)
	// #nosec G304 - the path is a CLI flag the user chose; reading the file
	// they named is the operation.
	pemBytes, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	encoded, err := verifier.EncodePublicKey(pemBytes)
	if err != nil {
		return "", fmt.Errorf("read public key %s: %w", cleanPath, err)
	}
	return encoded, nil
}

// printInstallTrust shows the trust state the install recorded — RFC
// THV-0080 wants the pinned identity displayed prominently, not discovered
// weeks later inside a signer-mismatch error.
func printInstallTrust(result *skills.InstallResult) {
	if result == nil {
		return
	}
	name := result.Skill.Metadata.Name
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
	default:
		fmt.Printf("Installed %s\n", name)
	}
}

// parseSkillInstallClients splits a comma-separated --clients flag value.
// Empty input yields nil so the server applies its default client.
func parseSkillInstallClients(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
