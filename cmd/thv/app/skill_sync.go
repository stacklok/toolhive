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
	skillSyncPrune       bool
	skillSyncFormat      string
)

var skillSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install skills exactly as pinned in the project's lock file",
	Long: `Sync installs every skill pinned in toolhive.lock.yaml at its exact
recorded digest, restoring skills that are missing or have drifted from the
pinned state. Project-scoped skills installed outside of the lock file are
reported as unmanaged, or removed with --prune.

The project root is auto-detected from the current directory (nearest
enclosing git repository) unless --project-root is given.`,
	PreRunE: chainPreRunE(ValidateFormat(&skillSyncFormat)),
	RunE:    skillSyncCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillSyncCmd)

	skillSyncCmd.Flags().StringVar(&skillSyncClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillSyncCmd.Flags().BoolVar(&skillSyncPrune, "prune", false,
		"Uninstall project-scoped skills that are not present in the lock file")
	skillSyncCmd.Flags().StringVar(&skillSyncProjectRoot, "project-root", "",
		"Project root path (auto-detected from the current directory if omitted)")
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
		Prune:       skillSyncPrune,
	})
	if err != nil {
		return formatSkillError("sync skills", err)
	}

	switch skillSyncFormat {
	case FormatJSON:
		data, jsonErr := json.MarshalIndent(result, "", "  ")
		if jsonErr != nil {
			return fmt.Errorf("failed to marshal JSON: %w", jsonErr)
		}
		fmt.Println(string(data))
	default:
		printSyncResultText(result)
	}
	return nil
}

func printSyncResultText(result *skills.SyncResult) {
	printSkillNameList("Installed", result.Installed)
	printSkillNameList("Up to date", result.UpToDate)
	printSkillNameList("Unmanaged (not in lock file; re-run with --prune to remove)", result.Unmanaged)
	printSkillNameList("Pruned", result.Pruned)
	if len(result.Failed) > 0 {
		fmt.Println("Failed:")
		for _, f := range result.Failed {
			fmt.Printf("  %s: %s\n", f.Name, f.Error)
		}
	}
	if len(result.Installed) == 0 && len(result.UpToDate) == 0 && len(result.Unmanaged) == 0 &&
		len(result.Pruned) == 0 && len(result.Failed) == 0 {
		fmt.Println("Nothing to sync: lock file is empty")
	}
}

func printSkillNameList(label string, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, name := range names {
		fmt.Printf("  %s\n", name)
	}
}
