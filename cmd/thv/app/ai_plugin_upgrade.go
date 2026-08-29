// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginUpgradeProjectRoot       string
	aiPluginUpgradeClientsRaw        string
	aiPluginUpgradePreview           bool
	aiPluginUpgradeFailOnChanges     bool
	aiPluginUpgradeAllowRefChange    bool
	aiPluginUpgradeAllowSignerChange bool
	aiPluginUpgradeYes               bool
	aiPluginUpgradeFormat            string
)

var aiPluginUpgradeCmd = &cobra.Command{
	Use:   "upgrade [plugin-name...]",
	Short: "Upgrade project plugins to newer pinned content",
	Long: `Re-resolve a project's lock entries and install newer content where available.

Plugins pinned to an immutable reference (an OCI digest or a full git commit
hash) are reported not-upgradable — there is nothing newer to resolve to.
Use --preview to see what would change without persisting anything (OCI
sources are still fetched into the local artifact store to compare digests),
and --allow-ref-change to permit the artifact moving to a different
repository (a version bump within the same repository is not a change
this guard blocks).
--fail-on-changes evaluates the same plan and never installs: it is a CI
freshness gate.

Unless --preview is set, upgrade prompts for confirmation before installing —
plugin content is a set of AI-followed instructions. Pass --yes to skip the
prompt (required in non-interactive contexts such as CI).

Requires TOOLHIVE_PLUGINS_LOCK_ENABLED=true.`,
	PreRunE: chainPreRunE(
		ValidateFormat(&aiPluginUpgradeFormat),
	),
	ValidArgsFunction: completePluginLockNames,
	RunE:              aiPluginUpgradeCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginUpgradeCmd)

	aiPluginUpgradeCmd.Flags().StringVar(&aiPluginUpgradeProjectRoot, "project-root", "",
		"Project root path (default: auto-detected from the current directory)")
	aiPluginUpgradeCmd.Flags().StringVar(&aiPluginUpgradeClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	aiPluginUpgradeCmd.Flags().BoolVar(&aiPluginUpgradePreview, "preview", false,
		"Report what would change without persisting anything (OCI sources are still fetched to compare digests)")
	aiPluginUpgradeCmd.Flags().BoolVar(&aiPluginUpgradeFailOnChanges, "fail-on-changes", false,
		"Report what would change without installing anything; a CI freshness gate")
	aiPluginUpgradeCmd.Flags().BoolVar(&aiPluginUpgradeAllowSignerChange, "allow-signer-change", false,
		"Permit upgrading to an artifact signed by a different identity; the new identity replaces the recorded one")
	aiPluginUpgradeCmd.Flags().BoolVar(&aiPluginUpgradeAllowRefChange, "allow-ref-change", false,
		"Permit the artifact to move to a different repository during upgrade")
	aiPluginUpgradeCmd.Flags().BoolVar(&aiPluginUpgradeYes, "yes", false,
		"Skip the confirmation prompt (required when not running interactively)")
	AddFormatFlag(aiPluginUpgradeCmd, &aiPluginUpgradeFormat)
}

func aiPluginUpgradeCmdFunc(cmd *cobra.Command, args []string) error {
	projectRoot, err := resolveProjectRoot(aiPluginUpgradeProjectRoot)
	if err != nil {
		return err
	}

	if !aiPluginUpgradePreview && !aiPluginUpgradeFailOnChanges {
		if !aiPluginUpgradeYes {
			printPluginLockEntriesSummary(projectRoot)
		}
		confirmed, confirmErr := requireConfirmation("Upgrade plugins for "+projectRoot, aiPluginUpgradeYes)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Upgrade cancelled.")
			return nil
		}
	}

	c := newAIPluginClient(cmd.Context())
	result, err := c.Upgrade(cmd.Context(), plugins.UpgradeOptions{
		ProjectRoot:       projectRoot,
		Names:             args,
		Clients:           parseSkillInstallClients(aiPluginUpgradeClientsRaw),
		Preview:           aiPluginUpgradePreview,
		FailOnChanges:     aiPluginUpgradeFailOnChanges,
		AllowRefChange:    aiPluginUpgradeAllowRefChange,
		AllowSignerChange: aiPluginUpgradeAllowSignerChange,
	})
	if err != nil {
		return formatAIPluginError("upgrade plugins", err)
	}

	planOnly := aiPluginUpgradePreview || aiPluginUpgradeFailOnChanges
	if err := printPluginUpgradeResult(result, aiPluginUpgradeFormat, planOnly); err != nil {
		return err
	}
	return pluginUpgradeExitError(result, aiPluginUpgradePreview, aiPluginUpgradeFailOnChanges)
}

func pluginUpgradeExitError(result *plugins.UpgradeResult, preview, failOnChanges bool) error {
	tally := tallyUpgradeOutcomes(result)
	failed, refBlocked, signerBlocked, wouldChange :=
		tally.failed, tally.refBlocked, tally.signerBlocked, tally.wouldChange
	if failed > 0 {
		return withExitCode(fmt.Errorf("upgrade failed for %d plugin(s)", failed), ExitCodePartialFailure)
	}
	if failOnChanges && wouldChange > 0 {
		return withExitCode(
			fmt.Errorf("%d plugin(s) would change; the lock file is stale", wouldChange),
			ExitCodeCheckFailure,
		)
	}
	if !preview && !failOnChanges && signerBlocked > 0 {
		return withExitCode(
			fmt.Errorf("%d plugin(s) blocked by a signer change; use --allow-signer-change", signerBlocked),
			ExitCodePolicyRejection,
		)
	}
	if !preview && !failOnChanges && refBlocked > 0 {
		return withExitCode(
			fmt.Errorf("%d plugin(s) blocked by a repository change; use --allow-ref-change", refBlocked),
			ExitCodePolicyRejection,
		)
	}
	return nil
}

func printPluginUpgradeResult(result *plugins.UpgradeResult, format string, planOnly bool) error {
	if format == FormatJSON {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(result.Outcomes) == 0 {
		fmt.Println("No plugins in the project's lock file")
		return nil
	}
	upgradedVerb := "upgraded"
	if planOnly {
		upgradedVerb = "would upgrade"
	}
	for _, o := range result.Outcomes {
		switch o.Status {
		case plugins.UpgradeStatusUpgraded:
			fmt.Printf("%s: %s %s -> %s\n", o.Name, upgradedVerb, o.OldDigest, o.NewDigest)
		case plugins.UpgradeStatusUpToDate:
			fmt.Printf("%s: up to date\n", o.Name)
		case plugins.UpgradeStatusNotUpgradable:
			fmt.Printf("%s: not upgradable (pinned to an immutable reference)\n", o.Name)
		case plugins.UpgradeStatusRefChangeBlocked:
			fmt.Printf("%s: repository change blocked (would move to %s; use --allow-ref-change)\n",
				o.Name, o.NewResolvedReference)
		case plugins.UpgradeStatusSignerChangeBlocked:
			newSigner := o.NewSignerIdentity
			if newSigner == "" {
				newSigner = "unsigned"
			}
			fmt.Printf("%s: signer change blocked (candidate is %s; use --allow-signer-change)\n", o.Name, newSigner)
		case plugins.UpgradeStatusFailed:
			fmt.Printf("%s: failed [%s]: %s\n", o.Name, o.Reason, o.Error)
		}
	}
	return nil
}
