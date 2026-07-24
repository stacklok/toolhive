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
	skillUpgradeYes            bool
	skillUpgradeFormat         string
)

var skillUpgradeCmd = &cobra.Command{
	Use:   "upgrade [skill-name...]",
	Short: "Upgrade project skills to newer pinned content (experimental)",
	Long: `Re-resolve a project's lock entries and install newer content where available.

Experimental: requires TOOLHIVE_SKILLS_LOCK_ENABLED=true on the ToolHive
server while the lock file feature rolls out.

Skills pinned to an immutable reference (an OCI digest or a full git commit
hash) are reported not-upgradable — there is nothing newer to resolve to.
Use --preview to see what would change without persisting anything (OCI
sources are still fetched into the local artifact store to compare digests),
and --allow-ref-change to permit the resolved reference itself changing
(e.g. a registry entry repointed at a different repository).
--fail-on-changes evaluates the same plan and never installs: it is a CI
freshness gate.

Unless --preview is set, upgrade prompts for confirmation before installing —
skill content is a set of AI-followed instructions. Pass --yes to skip the
prompt (required in non-interactive contexts such as CI).`,
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
		"Report what would change without persisting anything (OCI sources are still fetched to compare digests)")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeFailOnChanges, "fail-on-changes", false,
		"Report what would change without installing anything; a CI freshness gate")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeAllowRefChange, "allow-ref-change", false,
		"Permit the resolved reference itself to change during upgrade")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeYes, "yes", false,
		"Skip the confirmation prompt (required when not running interactively)")
	AddFormatFlag(skillUpgradeCmd, &skillUpgradeFormat)
}

func skillUpgradeCmdFunc(cmd *cobra.Command, args []string) error {
	projectRoot, err := resolveProjectRoot(skillUpgradeProjectRoot)
	if err != nil {
		return err
	}

	if !skillUpgradePreview && !skillUpgradeFailOnChanges {
		if !skillUpgradeYes {
			printLockEntriesSummary(projectRoot)
		}
		confirmed, confirmErr := requireConfirmation("Upgrade skills for "+projectRoot, skillUpgradeYes)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Upgrade cancelled.")
			return nil
		}
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

	if err := printUpgradeResult(result, skillUpgradeFormat, skillUpgradePreview || skillUpgradeFailOnChanges); err != nil {
		return err
	}
	return upgradeExitError(result, skillUpgradePreview, skillUpgradeFailOnChanges)
}

// upgradeExitError maps an UpgradeResult to RFC THV-0080's exit-code
// contract, entirely from the reported outcomes. Precedence: a failed
// outcome (exit 3) beats everything — a genuine failure must never be
// masked as "lock is stale" (exit 2) or a guard doing its job (exit 4).
// With failOnChanges, any would-change outcome is the CI freshness signal
// (exit 2). A ref-change block maps to a policy rejection (exit 4) only
// when the run wasn't a preview/gate evaluation — during those nothing was
// actually blocked, only reported.
func upgradeExitError(result *skills.UpgradeResult, preview, failOnChanges bool) error {
	var failed, refBlocked, wouldChange int
	for _, o := range result.Outcomes {
		switch o.Status {
		case skills.UpgradeStatusFailed:
			failed++
		case skills.UpgradeStatusRefChangeBlocked:
			refBlocked++
			wouldChange++
		case skills.UpgradeStatusUpgraded:
			wouldChange++
		case skills.UpgradeStatusUpToDate, skills.UpgradeStatusNotUpgradable:
			// No exit-code impact.
		}
	}
	if failed > 0 {
		return withExitCode(fmt.Errorf("upgrade failed for %d skill(s)", failed), ExitCodePartialFailure)
	}
	if failOnChanges && wouldChange > 0 {
		return withExitCode(
			fmt.Errorf("%d skill(s) would change; the lock file is stale", wouldChange),
			ExitCodeCheckFailure,
		)
	}
	if !preview && !failOnChanges && refBlocked > 0 {
		return withExitCode(
			fmt.Errorf("%d skill(s) blocked by a reference change; use --allow-ref-change", refBlocked),
			ExitCodePolicyRejection,
		)
	}
	return nil
}

// printUpgradeResult renders the outcomes. planOnly (a --preview or
// --fail-on-changes run) switches "upgraded" to "would upgrade": nothing
// was installed in those modes, and saying otherwise misreads the gate.
func printUpgradeResult(result *skills.UpgradeResult, format string, planOnly bool) error {
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
	upgradedVerb := "upgraded"
	if planOnly {
		upgradedVerb = "would upgrade"
	}
	for _, o := range result.Outcomes {
		switch o.Status {
		case skills.UpgradeStatusUpgraded:
			fmt.Printf("%s: %s %s -> %s\n", o.Name, upgradedVerb, o.OldDigest, o.NewDigest)
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
