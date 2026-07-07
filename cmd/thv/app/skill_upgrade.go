// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

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
	Short: "Check for and install newer content for locked skills",
	Long: `Upgrade re-resolves each lock file entry's original source (registry
name, OCI reference, or git reference) and installs any newer content it
finds, updating toolhive.lock.yaml. Entries pinned to an immutable reference
(an OCI digest or a full git commit hash) are reported as not upgradable.

Use --preview to see what would change. Preview still fetches artifacts into
the local cache but does not install or rewrite the lock file.

With no arguments, every entry in the lock file is checked.

The project root is auto-detected from the current directory (nearest
enclosing git repository) unless --project-root is given.`,
	PreRunE: chainPreRunE(ValidateFormat(&skillUpgradeFormat)),
	RunE:    skillUpgradeCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillUpgradeCmd)

	skillUpgradeCmd.Flags().StringVar(&skillUpgradeClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradePreview, "preview", false,
		"Report what would change without installing (still fetches artifacts)")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeFailOnChanges, "fail-on-changes", false,
		"Exit non-zero when preview finds any upgradable skill")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeAllowRefChange, "allow-ref-change", false,
		"Allow resolvedReference changes during upgrade")
	skillUpgradeCmd.Flags().BoolVar(&skillUpgradeYes, "yes", false,
		"Skip the pre-upgrade confirmation prompt")
	skillUpgradeCmd.Flags().StringVar(&skillUpgradeProjectRoot, "project-root", "",
		"Project root path (auto-detected from the current directory if omitted)")
	AddFormatFlag(skillUpgradeCmd, &skillUpgradeFormat)
}

func skillUpgradeCmdFunc(cmd *cobra.Command, args []string) error {
	projectRoot, err := resolveProjectRoot(skillUpgradeProjectRoot)
	if err != nil {
		return err
	}

	c := newSkillClient(cmd.Context())
	opts := skills.UpgradeOptions{
		ProjectRoot:    projectRoot,
		Names:          args,
		Preview:        skillUpgradePreview,
		FailOnChanges:  skillUpgradeFailOnChanges,
		AllowRefChange: skillUpgradeAllowRefChange,
		Clients:        parseSkillInstallClients(skillUpgradeClientsRaw),
	}

	if skillUpgradePreview {
		result, err := c.Upgrade(cmd.Context(), opts)
		if err != nil {
			return formatSkillError("upgrade skills", err)
		}
		return finishUpgradeResult(result, skillUpgradeFormat)
	}

	previewOpts := opts
	previewOpts.Preview = true
	preview, err := c.Upgrade(cmd.Context(), previewOpts)
	if err != nil {
		return formatSkillError("upgrade skills", err)
	}

	if err := requireInteractiveConfirmation(skillUpgradeYes, func() {
		printUpgradePreflight(preview)
	}); err != nil {
		return err
	}

	result, err := c.Upgrade(cmd.Context(), opts)
	if err != nil {
		return formatSkillError("upgrade skills", err)
	}
	return finishUpgradeResult(result, skillUpgradeFormat)
}

func finishUpgradeResult(result *skills.UpgradeResult, format string) error {
	switch format {
	case FormatJSON:
		data, jsonErr := json.MarshalIndent(result, "", "  ")
		if jsonErr != nil {
			return fmt.Errorf("failed to marshal JSON: %w", jsonErr)
		}
		fmt.Println(string(data))
	default:
		printUpgradeResultText(result)
	}

	if code := upgradeExitCode(result); code != 0 {
		return newExitCodeError(code, nil)
	}
	return nil
}

func upgradeExitCode(result *skills.UpgradeResult) int {
	for _, o := range result.Outcomes {
		switch o.Status {
		case skills.UpgradeStatusFailed:
			return ExitCodePartialFailure
		case skills.UpgradeStatusRefChangeBlocked:
			return ExitCodePolicyRejection
		case skills.UpgradeStatusUpgraded, skills.UpgradeStatusUpToDate, skills.UpgradeStatusNotUpgradable:
			// continue
		}
	}
	if skillUpgradeFailOnChanges {
		for _, o := range result.Outcomes {
			if o.Status == skills.UpgradeStatusUpgraded {
				return ExitCodeCheckFailure
			}
		}
	}
	return 0
}

func printUpgradePreflight(result *skills.UpgradeResult) {
	fmt.Println("Pre-flight upgrade summary:")
	printUpgradeResultText(result)
}

func printUpgradeResultText(result *skills.UpgradeResult) {
	if len(result.Outcomes) == 0 {
		fmt.Println("Nothing to upgrade: lock file is empty")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tOLD DIGEST\tNEW DIGEST")
	for _, o := range result.Outcomes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.Name, o.Status, shortDigest(o.OldDigest), shortDigest(o.NewDigest))
	}
	_ = w.Flush()

	for _, o := range result.Outcomes {
		if o.Error != "" {
			fmt.Printf("  %s: %s\n", o.Name, o.Error)
		}
	}
}

// shortDigest truncates a digest to "sha256:" plus 12 hex characters for
// compact table display, matching the convention used by container tooling.
func shortDigest(d string) string {
	const shortLen = 19 // len("sha256:") + 12
	if len(d) > shortLen {
		return d[:shortLen]
	}
	return d
}
