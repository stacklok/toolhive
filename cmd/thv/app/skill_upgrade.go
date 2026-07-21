// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillUpgradeProjectRoot    string
	skillUpgradeClientsRaw     string
	skillUpgradePreview        bool
	skillUpgradeFailOnChanges  bool
	skillUpgradeAllowRefChange bool
	skillUpgradeFormat         string
)

var skillUpgradeCmd = &cobra.Command{
	Use:   "upgrade [skill-name...]",
	Short: "Upgrade project skills to newer pinned content",
	Long: `Re-resolve a project's lock entries and install newer content where available.

Skills pinned to an immutable reference (an OCI digest or a full git commit
hash) are reported not-upgradable — there is nothing newer to resolve to.
Use --preview to see what would change without installing, and
--allow-ref-change to permit the resolved reference itself changing (e.g. a
registry entry repointed at a different repository).`,
	PreRunE: chainPreRunE(
		ValidateFormat(&skillUpgradeFormat),
	),
	RunE: skillUpgradeCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillUpgradeCmd)

	skillUpgradeCmd.Flags().StringVar(&skillUpgradeProjectRoot, "project-root", "",
		"Project root path (default: auto-detected from the current directory)")
	skillUpgradeCmd.Flags().StringVar(&skillUpgradeClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradePreview, "preview", false,
		"Report what would change without installing anything")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeFailOnChanges, "fail-on-changes", false,
		"Exit with an error if any skill would change (a CI freshness gate)")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeAllowRefChange, "allow-ref-change", false,
		"Permit the resolved reference itself to change during upgrade")
	AddFormatFlag(skillUpgradeCmd, &skillUpgradeFormat)
}

func skillUpgradeCmdFunc(cmd *cobra.Command, args []string) error {
	projectRoot, err := resolveProjectRoot(skillUpgradeProjectRoot)
	if err != nil {
		return err
	}

	c := newSkillClient(cmd.Context())
	result, err := c.Upgrade(cmd.Context(), skills.UpgradeOptions{
		ProjectRoot:    projectRoot,
		Names:          args,
		Clients:        parseSkillInstallClients(skillUpgradeClientsRaw),
		Preview:        skillUpgradePreview,
		FailOnChanges:  skillUpgradeFailOnChanges,
		AllowRefChange: skillUpgradeAllowRefChange,
	})
	if err != nil {
		return formatSkillError("upgrade skills", err)
	}

	return printUpgradeResult(result, skillUpgradeFormat)
}

func printUpgradeResult(result *skills.UpgradeResult, format string) error {
	if format == FormatJSON {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(result.Outcomes) == 0 {
		fmt.Println("No skills in the project's lock file")
		return nil
	}
	for _, o := range result.Outcomes {
		switch o.Status {
		case skills.UpgradeStatusUpgraded:
			fmt.Printf("%s: upgraded %s -> %s\n", o.Name, o.OldDigest, o.NewDigest)
		case skills.UpgradeStatusUpToDate:
			fmt.Printf("%s: up to date\n", o.Name)
		case skills.UpgradeStatusNotUpgradable:
			fmt.Printf("%s: not upgradable (pinned to an immutable reference)\n", o.Name)
		case skills.UpgradeStatusRefChangeBlocked:
			fmt.Printf("%s: reference change blocked (would move to %s; use --allow-ref-change)\n", o.Name, o.NewResolvedReference)
		case skills.UpgradeStatusFailed:
			fmt.Printf("%s: failed [%s]: %s\n", o.Name, o.Reason, o.Error)
		}
	}
	return nil
}
