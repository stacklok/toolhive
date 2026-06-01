// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Manage upgrades for MCP server workloads",
	Long: `Inspect and apply upgrades for registry-sourced MCP server workloads.

Upgrade checks compare each workload's current image and configuration against
the metadata reported by its source registry. Checks are an offline metadata
comparison and never pull images.`,
}

var upgradeCheckCmd = &cobra.Command{
	Use:   "check [workload-name]",
	Short: "Check workloads for available upgrades",
	Long: `Check whether registry-sourced workloads have a newer image available.

With no arguments, all workloads are checked (including stopped ones) and a
summary table is printed. When a workload name is given, a detailed report for
that single workload is printed, including any new environment variables the
candidate image declares and any configuration (posture) drift.

Examples:
  # Check all workloads
  thv upgrade check

  # Check a single workload with detailed output
  thv upgrade check my-server

  # Check all workloads in JSON format
  thv upgrade check --format json`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMCPServerNames,
	RunE:              upgradeCheckCmdFunc,
}

var upgradeApplyCmd = &cobra.Command{
	Use:   "apply <workload-name>",
	Short: "Apply an available upgrade to a workload",
	Long: `Apply the upgrade the registry reports for a registry-sourced workload.

The candidate image is resolved, verified, and pulled BEFORE the existing
workload is touched. The existing workload is then stopped and replaced with one
running the candidate image; the rest of the workload's configuration (env vars,
secrets, posture, middleware) is preserved. There is no automatic rollback: if
recreation fails the previous workload is not restored, so recovery is a forward
operation.

New environment variables the candidate declares can be supplied with --env and
--secret. When run interactively, missing required values are prompted for; with
--yes (or in a non-interactive shell) the command runs non-interactively and
fails if a required value is missing.

Examples:
  # Apply the available upgrade, prompting for confirmation
  thv upgrade apply my-server

  # Apply non-interactively, supplying a new env var
  thv upgrade apply my-server --yes --env NEW_FLAG=true

  # Preview what an upgrade would change without applying it
  thv upgrade apply my-server --dry-run`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeMCPServerNames,
	RunE:              upgradeApplyCmdFunc,
}

var (
	upgradeCheckFormat  string
	upgradeApplyYes     bool
	upgradeApplyDryRun  bool
	upgradeApplyEnv     []string
	upgradeApplySecrets []string
	upgradeApplyVerify  string
	upgradeApplyCACert  string
)

func init() {
	upgradeCmd.AddCommand(upgradeCheckCmd)
	upgradeCmd.AddCommand(upgradeApplyCmd)

	AddFormatFlag(upgradeCheckCmd, &upgradeCheckFormat, FormatJSON, FormatText)
	upgradeCheckCmd.PreRunE = chainPreRunE(
		ValidateFormat(&upgradeCheckFormat, FormatJSON, FormatText),
	)

	upgradeApplyCmd.Flags().BoolVarP(&upgradeApplyYes, "yes", "y", false,
		"Skip the confirmation prompt and run non-interactively (fail if required values are missing)")
	upgradeApplyCmd.Flags().BoolVar(&upgradeApplyDryRun, "dry-run", false,
		"Print what the upgrade would change without applying it")
	upgradeApplyCmd.Flags().StringArrayVarP(&upgradeApplyEnv, "env", "e", nil,
		"Environment variables to set on the upgraded workload (format: KEY=VALUE, repeatable)")
	upgradeApplyCmd.Flags().StringArrayVar(&upgradeApplySecrets, "secret", nil,
		"Secrets to set on the upgraded workload (format: NAME,target=TARGET, repeatable)")
	upgradeApplyCmd.Flags().StringVar(&upgradeApplyVerify, "image-verification", retriever.VerifyImageWarn,
		fmt.Sprintf("Set image verification mode (%s, %s, %s)",
			retriever.VerifyImageWarn, retriever.VerifyImageEnabled, retriever.VerifyImageDisabled))
	upgradeApplyCmd.Flags().StringVar(&upgradeApplyCACert, "ca-cert", "",
		"Path to a custom CA certificate file to use when resolving the candidate image")
}

func upgradeCheckCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	checker, err := newUpgradeChecker()
	if err != nil {
		return err
	}

	// Single-workload mode: detailed report.
	if len(args) == 1 {
		cfg, err := runner.LoadState(ctx, args[0])
		if err != nil {
			return fmt.Errorf("failed to load configuration for workload %q: %w", args[0], err)
		}
		result, err := checker.Check(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to check workload %q for upgrade: %w", args[0], err)
		}

		if upgradeCheckFormat == FormatJSON {
			return printUpgradeJSON([]*upgrade.CheckResult{result})
		}
		printUpgradeDetail(result)
		return nil
	}

	// Bulk mode: enumerate all workloads and check each.
	configs, err := loadWorkloadRunConfigs(ctx)
	if err != nil {
		return err
	}
	results := checker.CheckAll(ctx, configs)

	if upgradeCheckFormat == FormatJSON {
		return printUpgradeJSON(results)
	}
	printUpgradeTable(results)
	return nil
}

func upgradeApplyCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	checker, err := newUpgradeChecker()
	if err != nil {
		return err
	}

	// 1. Load the workload's saved config and run the check. This is an offline
	// metadata comparison; the Applier re-checks (and re-resolves) before
	// applying, so this result drives messaging/confirmation only.
	cfg, err := runner.LoadState(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to load configuration for workload %q: %w", name, err)
	}
	result, err := checker.Check(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to check workload %q for upgrade: %w", name, err)
	}

	// 2. Nothing to apply: report the status and exit successfully.
	if result.Status != upgrade.StatusUpgradeAvailable {
		printNoUpgradeMessage(result)
		return nil
	}

	// 3. Dry-run: print the planned changes and stop before building the applier.
	if upgradeApplyDryRun {
		fmt.Printf("Dry run: %s would be upgraded.\n\n", name)
		printUpgradeDetail(result)
		return nil
	}

	// 4. Determine interactivity and pick the matching env-var validator.
	configProvider := config.NewDefaultProvider()
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && !upgradeApplyYes
	envVarValidator := func() runner.EnvVarValidator {
		if interactive {
			return runner.NewCLIEnvVarValidator(configProvider)
		}
		return &runner.DetachedEnvVarValidator{}
	}()

	// 5. Interactive confirmation: show the summary and prompt before applying.
	if interactive {
		printUpgradeDetail(result)
		confirmed, err := confirmUpgrade()
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Upgrade cancelled.")
			return nil
		}
	}

	// 6. Build the applier and parse the new env/secret inputs into ApplyOptions.
	envVars, err := environment.ParseEnvironmentVariables(upgradeApplyEnv)
	if err != nil {
		return fmt.Errorf("failed to parse environment variables: %w", err)
	}

	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}
	applier, err := upgrade.NewApplier(manager, checker, configProvider)
	if err != nil {
		return fmt.Errorf("failed to create upgrade applier: %w", err)
	}

	applied, err := applier.Apply(ctx, name, upgrade.ApplyOptions{
		EnvVars:         envVars,
		Secrets:         upgradeApplySecrets,
		EnvVarValidator: envVarValidator,
		VerifySetting:   upgradeApplyVerify,
		CACertPath:      upgradeApplyCACert,
	})
	if err != nil {
		return fmt.Errorf("failed to upgrade workload %q: %w", name, err)
	}

	// 7. Confirm what was applied.
	fmt.Printf("%s upgraded to %s\n", name, applied.CandidateImage)
	return nil
}

// printNoUpgradeMessage prints a friendly, non-error explanation of why there
// is nothing to apply for the given check result.
func printNoUpgradeMessage(r *upgrade.CheckResult) {
	switch r.Status {
	case upgrade.StatusUpToDate:
		fmt.Printf("%s is already up to date.\n", r.WorkloadName)
	case upgrade.StatusNotRegistrySourced:
		fmt.Printf("%s was not created from a registry entry; no upgrade can be applied.\n", r.WorkloadName)
	case upgrade.StatusServerNotFound:
		fmt.Printf("%s references a registry server that no longer exists; no upgrade can be applied.\n", r.WorkloadName)
	case upgrade.StatusUnknown:
		msg := "the upgrade status could not be determined"
		if r.Reason != "" {
			msg = r.Reason
		}
		fmt.Printf("%s: %s; no upgrade can be applied.\n", r.WorkloadName, msg)
	case upgrade.StatusUpgradeAvailable:
		// Unreachable: callers only invoke this for non-upgrade-available results.
		fmt.Printf("%s has an upgrade available.\n", r.WorkloadName)
	default:
		fmt.Printf("%s: no upgrade can be applied.\n", r.WorkloadName)
	}
}

// confirmUpgrade prompts the user to confirm an upgrade and returns whether they
// accepted. It reads a single line from stdin and treats only "y"/"yes" as
// confirmation.
func confirmUpgrade() (bool, error) {
	fmt.Printf("\nApply? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read user input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

// newUpgradeChecker builds an upgrade.Checker backed by the default registry
// provider used throughout the CLI.
func newUpgradeChecker() (*upgrade.Checker, error) {
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %w", err)
	}
	checker, err := upgrade.NewChecker(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create upgrade checker: %w", err)
	}
	return checker, nil
}

// loadWorkloadRunConfigs enumerates all workloads (mirroring the list command's
// path) and loads each workload's saved RunConfig. Configs that fail to load are
// skipped so a single corrupt entry does not abort the whole check.
func loadWorkloadRunConfigs(ctx context.Context) ([]*runner.RunConfig, error) {
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}

	// Sort by name for deterministic output, matching the `thv list` ordering.
	core.SortWorkloadsByName(workloadList)

	configs := make([]*runner.RunConfig, 0, len(workloadList))
	for _, wl := range workloadList {
		cfg, err := runner.LoadState(ctx, wl.Name)
		if err != nil {
			slog.Debug("skipping workload with unloadable config", "workload", wl.Name, "error", err)
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// printUpgradeJSON prints upgrade check results as indented JSON.
func printUpgradeJSON(results []*upgrade.CheckResult) error {
	if results == nil {
		results = []*upgrade.CheckResult{}
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal upgrade check results: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// printUpgradeTable prints a one-line-per-workload summary of upgrade results.
func printUpgradeTable(results []*upgrade.CheckResult) {
	if len(results) == 0 {
		fmt.Println("No MCP servers found")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSTATUS\tCURRENT\tCANDIDATE\tNEW-ENV\tPOSTURE"); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output header: %v", err))
		return
	}

	for _, r := range results {
		if r == nil {
			// CheckAll does not return nil entries today; guard so this stays
			// robust if that ever changes.
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			r.WorkloadName,
			r.Status,
			dashIfEmpty(r.CurrentImage),
			dashIfEmpty(r.CandidateImage),
			newEnvCount(r),
			postureMarker(r),
		); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write upgrade result: %v", err))
		}
	}

	if err := w.Flush(); err != nil {
		slog.Error(fmt.Sprintf("Failed to flush tabwriter: %v", err))
	}
}

// printUpgradeDetail prints a verbose, single-workload upgrade report.
func printUpgradeDetail(r *upgrade.CheckResult) {
	fmt.Printf("Workload:  %s\n", r.WorkloadName)
	fmt.Printf("Status:    %s\n", r.Status)
	if r.RegistryServer != "" {
		fmt.Printf("Registry:  %s\n", r.RegistryServer)
	}
	if r.CurrentImage != "" {
		fmt.Printf("Current:   %s\n", r.CurrentImage)
	}
	if r.CandidateImage != "" {
		fmt.Printf("Candidate: %s\n", r.CandidateImage)
	}
	if r.Reason != "" {
		fmt.Printf("Reason:    %s\n", r.Reason)
	}

	if r.EnvVarDrift != nil && len(r.EnvVarDrift.Added) > 0 {
		fmt.Println("\nNew environment variables declared by the candidate:")
		for _, ev := range r.EnvVarDrift.Added {
			required := ""
			if ev.Required {
				required = " (required)"
			}
			desc := ""
			if ev.Description != "" {
				desc = ": " + ev.Description
			}
			fmt.Printf("  - %s%s%s\n", ev.Name, required, desc)
		}
	}

	if r.ConfigDrift != nil {
		fmt.Println("\nConfiguration (posture) drift:")
		if c := r.ConfigDrift.Transport; c != nil {
			fmt.Printf("  ⚠ transport: %s -> %s\n", c.From, c.To)
		}
		if c := r.ConfigDrift.PermissionProfile; c != nil {
			fmt.Printf("  ⚠ permission profile: %s -> %s\n", c.From, c.To)
		}
	}
}

// newEnvCount returns the number of new environment variables the candidate
// declares that the workload does not currently satisfy.
func newEnvCount(r *upgrade.CheckResult) int {
	if r.EnvVarDrift == nil {
		return 0
	}
	return len(r.EnvVarDrift.Added)
}

// postureMarker returns a warning marker when the candidate's posture differs
// from the workload's current configuration, or "-" otherwise.
func postureMarker(r *upgrade.CheckResult) string {
	if r.ConfigDrift != nil {
		return "⚠ drift"
	}
	return "-"
}

// dashIfEmpty returns "-" for an empty string, so columns stay aligned.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
