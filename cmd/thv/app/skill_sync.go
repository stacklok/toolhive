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
	skillSyncCheck       bool
	skillSyncAdopt       bool
	skillSyncYes         bool
	skillSyncFormat      string
)

var skillSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install skills exactly as pinned in the project's lock file",
	Long: `Sync installs every skill pinned in toolhive.lock.yaml at its exact
recorded digest and verifies contentDigest integrity. Project-scoped skills
installed outside of the lock file are reported as never-managed or
removed-from-lock; use --prune to uninstall the latter.

Use --check to verify on-disk content without installing. Use --adopt to write
lock entries for existing unmanaged installs.

The project root is auto-detected from the current directory (nearest
enclosing git repository) unless --project-root is given.

Pin history: git log -p -- toolhive.lock.yaml`,
	PreRunE: chainPreRunE(ValidateFormat(&skillSyncFormat)),
	RunE:    skillSyncCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillSyncCmd)

	skillSyncCmd.Flags().StringVar(&skillSyncClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillSyncCmd.Flags().BoolVar(&skillSyncPrune, "prune", false,
		"Uninstall previously lock-managed skills that are no longer in the lock file")
	skillSyncCmd.Flags().BoolVar(&skillSyncCheck, "check", false,
		"Verify on-disk content matches the lock file without installing")
	skillSyncCmd.Flags().BoolVar(&skillSyncAdopt, "adopt", false,
		"Write lock entries for existing unmanaged project-scope installs")
	skillSyncCmd.Flags().BoolVar(&skillSyncYes, "yes", false,
		"Skip the pre-install confirmation prompt")
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
	opts := skills.SyncOptions{
		ProjectRoot: projectRoot,
		Clients:     parseSkillInstallClients(skillSyncClientsRaw),
		Prune:       skillSyncPrune,
		Check:       skillSyncCheck,
		Adopt:       skillSyncAdopt,
	}

	if skillSyncCheck || skillSyncAdopt {
		result, err := c.Sync(cmd.Context(), opts)
		if err != nil {
			return formatSkillError("sync skills", err)
		}
		return finishSyncResult(result, skillSyncFormat)
	}

	// Two-phase gate: preview with check, then apply.
	previewOpts := opts
	previewOpts.Check = true
	preview, err := c.Sync(cmd.Context(), previewOpts)
	if err != nil {
		return formatSkillError("sync skills", err)
	}

	if err := requireInteractiveConfirmation(skillSyncYes, func() {
		printSyncPreflight(preview, skillSyncPrune)
	}); err != nil {
		return err
	}

	result, err := c.Sync(cmd.Context(), opts)
	if err != nil {
		return formatSkillError("sync skills", err)
	}
	return finishSyncResult(result, skillSyncFormat)
}

func finishSyncResult(result *skills.SyncResult, format string) error {
	switch format {
	case FormatJSON:
		data, jsonErr := json.MarshalIndent(result, "", "  ")
		if jsonErr != nil {
			return fmt.Errorf("failed to marshal JSON: %w", jsonErr)
		}
		fmt.Println(string(data))
	default:
		printSyncResultText(result)
	}

	exitCode := syncExitCode(result)
	if exitCode != 0 {
		return newExitCodeError(exitCode, nil)
	}
	return nil
}

func syncExitCode(result *skills.SyncResult) int {
	if len(result.Failed) > 0 {
		return ExitCodePartialFailure
	}
	if skillSyncCheck && (len(result.Failed) > 0 || len(result.Drifted) > 0) {
		return ExitCodeCheckFailure
	}
	return 0
}

func printSyncPreflight(result *skills.SyncResult, prune bool) {
	fmt.Println("Pre-flight summary:")
	printSkillNameList("  To install/reinstall", append(append([]string{}, result.Drifted...), diffInstalled(result)...))
	printSkillNameList("  Up to date", result.UpToDate)
	printSkillNameList("  Never managed", result.NeverManaged)
	printSkillNameList("  Removed from lock", result.RemovedFromLock)
	if prune {
		printSkillNameList("  Would prune", result.RemovedFromLock)
	}
}

func diffInstalled(result *skills.SyncResult) []string {
	// During check phase, skills needing install appear as failed with content-mismatch
	// or we rely on non-up-to-date entries. Keep simple: show failed names as pending install.
	names := make([]string, 0, len(result.Failed))
	for _, f := range result.Failed {
		if f.Reason == skills.FailureReasonContentMismatch {
			names = append(names, f.Name)
		}
	}
	return names
}

func printSyncResultText(result *skills.SyncResult) {
	printSkillNameList("Installed", result.Installed)
	printSkillNameList("Drifted (reinstalled)", result.Drifted)
	printSkillNameList("Up to date", result.UpToDate)
	printSkillNameList("Never managed", result.NeverManaged)
	printSkillNameList("Removed from lock", result.RemovedFromLock)
	printSkillNameList("Unmanaged (deprecated)", result.Unmanaged)
	printSkillNameList("Pruned", result.Pruned)
	if len(result.Failed) > 0 {
		fmt.Println("Failed:")
		for _, f := range result.Failed {
			if f.Reason != "" {
				fmt.Printf("  %s (%s): %s\n", f.Name, f.Reason, f.Error)
			} else {
				fmt.Printf("  %s: %s\n", f.Name, f.Error)
			}
		}
	}
	if len(result.Installed) == 0 && len(result.UpToDate) == 0 && len(result.NeverManaged) == 0 &&
		len(result.RemovedFromLock) == 0 && len(result.Pruned) == 0 && len(result.Failed) == 0 {
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
