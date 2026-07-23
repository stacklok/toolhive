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
	skillSyncProjectRoot string
	skillSyncClientsRaw  string
	skillSyncCheck       bool
	skillSyncAdopt       bool
	skillSyncPrune       bool
	skillSyncFormat      string
)

var skillSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Restore project skills to match the lock file",
	Long: `Restore a project's installed skills to match toolhive.lock.yaml.

Missing or drifted skills are reinstalled at their pinned digest. Use
--check to report drift without installing anything (suitable for CI).
Use --adopt to record lock entries for existing unmanaged installs, and
--prune to remove installs no longer present in the lock file.`,
	PreRunE: chainPreRunE(
		ValidateFormat(&skillSyncFormat),
	),
	RunE: skillSyncCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillSyncCmd)

	skillSyncCmd.Flags().StringVar(&skillSyncProjectRoot, "project-root", "",
		"Project root path (default: auto-detected from the current directory)")
	skillSyncCmd.Flags().StringVar(&skillSyncClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillSyncCmd.Flags().BoolVar(&skillSyncCheck, "check", false,
		"Report drift without installing, writing, or removing anything")
	skillSyncCmd.Flags().BoolVar(&skillSyncAdopt, "adopt", false,
		"Write lock entries for existing unmanaged project-scope installs")
	skillSyncCmd.Flags().BoolVar(&skillSyncPrune, "prune", false,
		"Remove installs no longer present in the lock file")
	AddFormatFlag(skillSyncCmd, &skillSyncFormat)
}

func skillSyncCmdFunc(cmd *cobra.Command, _ []string) error {
	projectRoot, err := resolveProjectRoot(skillSyncProjectRoot)
	if err != nil {
		return err
	}

	c := newSkillClient(cmd.Context())
	result, err := c.Sync(cmd.Context(), skills.SyncOptions{
		ProjectRoot: projectRoot,
		Clients:     parseSkillInstallClients(skillSyncClientsRaw),
		Check:       skillSyncCheck,
		Adopt:       skillSyncAdopt,
		Prune:       skillSyncPrune,
	})
	if err != nil {
		return formatSkillError("sync skills", err)
	}

	return printSyncResult(result, skillSyncFormat)
}

func printSyncResult(result *skills.SyncResult, format string) error {
	if format == FormatJSON {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	printSkillNameGroup("Installed", result.Installed)
	printSkillNameGroup("Drifted", result.Drifted)
	printSkillNameGroup("Missing (not installed)", result.Missing)
	printSkillNameGroup("Up to date", result.AlreadyCurrent)
	printSkillNameGroup("Never managed (use --adopt to record)", result.NeverManaged)
	printSkillNameGroup("Removed from lock (use --prune to remove)", result.RemovedFromLock)
	printSkillNameGroup("Pruned", result.Pruned)
	if len(result.Failed) > 0 {
		fmt.Println("Failed:")
		for _, f := range result.Failed {
			fmt.Printf("  %s [%s]: %s\n", f.Name, f.Reason, f.Error)
		}
	}
	if isSyncResultEmpty(result) {
		fmt.Println("Nothing to sync — the project matches its lock file")
	}
	return nil
}

func printSkillNameGroup(label string, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, name := range names {
		fmt.Printf("  %s\n", name)
	}
}

func isSyncResultEmpty(result *skills.SyncResult) bool {
	return len(result.Installed) == 0 &&
		len(result.Drifted) == 0 &&
		len(result.Missing) == 0 &&
		len(result.AlreadyCurrent) == 0 &&
		len(result.NeverManaged) == 0 &&
		len(result.RemovedFromLock) == 0 &&
		len(result.Pruned) == 0 &&
		len(result.Failed) == 0
}
