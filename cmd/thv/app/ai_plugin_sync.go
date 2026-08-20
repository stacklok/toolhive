// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

var (
	aiPluginSyncProjectRoot string
	aiPluginSyncClientsRaw  string
	aiPluginSyncCheck       bool
	aiPluginSyncAdopt       bool
	aiPluginSyncPrune       bool
	aiPluginSyncYes         bool
	aiPluginSyncFormat      string
)

var aiPluginSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Restore project plugins to match the lock file",
	Long: `Restore a project's installed plugins to match toolhive.lock.yaml.

Missing or drifted plugins are reinstalled at their pinned digest. Use
--check to report drift without installing anything (suitable for CI).
Use --adopt to record lock entries for existing unmanaged installs, and
--prune to remove installs no longer present in the lock file.

Unless --check is set, sync prompts for confirmation before installing —
plugin content is a set of AI-followed instructions. Pass --yes to skip the
prompt (required in non-interactive contexts such as CI).

Requires TOOLHIVE_PLUGINS_LOCK_ENABLED=true.`,
	PreRunE: chainPreRunE(
		ValidateFormat(&aiPluginSyncFormat),
	),
	RunE: aiPluginSyncCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginSyncCmd)

	aiPluginSyncCmd.Flags().StringVar(&aiPluginSyncProjectRoot, "project-root", "",
		"Project root path (default: auto-detected from the current directory)")
	aiPluginSyncCmd.Flags().StringVar(&aiPluginSyncClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	aiPluginSyncCmd.Flags().BoolVar(&aiPluginSyncCheck, "check", false,
		"Report drift without installing, writing, or removing anything")
	aiPluginSyncCmd.Flags().BoolVar(&aiPluginSyncAdopt, "adopt", false,
		"Write lock entries for existing unmanaged project-scope installs")
	aiPluginSyncCmd.Flags().BoolVar(&aiPluginSyncPrune, "prune", false,
		"Remove installs no longer present in the lock file")
	aiPluginSyncCmd.Flags().BoolVar(&aiPluginSyncYes, "yes", false,
		"Skip the confirmation prompt (required when not running interactively)")
	AddFormatFlag(aiPluginSyncCmd, &aiPluginSyncFormat)
}

func aiPluginSyncCmdFunc(cmd *cobra.Command, _ []string) error {
	projectRoot, err := resolveProjectRoot(aiPluginSyncProjectRoot)
	if err != nil {
		return err
	}

	if !aiPluginSyncCheck {
		if !aiPluginSyncYes {
			printPluginLockEntriesSummary(projectRoot)
		}
		confirmed, confirmErr := requireConfirmation("Sync plugins for "+projectRoot, aiPluginSyncYes)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Sync cancelled.")
			return nil
		}
	}

	c := newAIPluginClient(cmd.Context())
	result, err := c.Sync(cmd.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot,
		Clients:     parseSkillInstallClients(aiPluginSyncClientsRaw),
		Check:       aiPluginSyncCheck,
		Adopt:       aiPluginSyncAdopt,
		Prune:       aiPluginSyncPrune,
	})
	if err != nil {
		return formatAIPluginError("sync plugins", err)
	}

	if err := printPluginSyncResult(result, aiPluginSyncFormat); err != nil {
		return err
	}
	return pluginSyncExitError(result, aiPluginSyncCheck)
}

func pluginSyncExitError(result *plugins.SyncResult, check bool) error {
	if len(result.Failed) > 0 {
		return withExitCode(fmt.Errorf("sync failed for %d plugin(s)", len(result.Failed)), ExitCodePartialFailure)
	}
	if outOfSync := len(result.Drifted) + len(result.Missing); check && outOfSync > 0 {
		return withExitCode(
			fmt.Errorf("%d plugin(s) drifted from or are missing against the lock file", outOfSync),
			ExitCodeCheckFailure,
		)
	}
	return nil
}

func printPluginSyncResult(result *plugins.SyncResult, format string) error {
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

func printPluginLockEntriesSummary(projectRoot string) {
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return
	}
	lf, err := lockfile.Load(root)
	if err != nil || len(lf.Plugins) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Lock file plugin entries for %s:\n", sanitizeTerminal(projectRoot))
	for _, e := range lf.Plugins {
		fmt.Fprintf(os.Stderr, "  %s  %s  %s\n",
			sanitizeTerminal(e.Name), sanitizeTerminal(e.Source), sanitizeTerminal(shortDigest(e.Digest)))
	}
}
