package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/workloads"
)

var stopCmd = &cobra.Command{
	Use:               "stop [workload-name]",
	Short:             "Stop an MCP server",
	Long:              `Stop a running MCP server managed by ToolHive.`,
	Args:              validateStopArgs,
	RunE:              stopCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

var (
	stopTimeout int
	stopAll     bool
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the workload")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running MCP servers")
}

// validateStopArgs validates the arguments for the stop command
func validateStopArgs(cmd *cobra.Command, args []string) error {
	// Check if --all flag is set
	all, _ := cmd.Flags().GetBool("all")

	if all {
		// If --all is set, no arguments should be provided
		if len(args) > 0 {
			return fmt.Errorf("no arguments should be provided when --all flag is set")
		}
	} else {
		// If --all is not set, exactly one argument should be provided
		if len(args) != 1 {
			return fmt.Errorf("exactly one workload name must be provided")
		}
	}

	return nil
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	statusManager, err := workloads.NewStatusManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %v", err)
	}

	var group *errgroup.Group

	// Check if --all flag is set
	if stopAll {
		// Get list of all running workloads first
		workloadList, err := statusManager.ListWorkloads(ctx, false) // false = only running workloads
		if err != nil {
			return fmt.Errorf("failed to list workloads: %v", err)
		}

		// Extract workload names
		var workloadNames []string
		for _, workload := range workloadList {
			workloadNames = append(workloadNames, workload.Name)
		}

		if len(workloadNames) == 0 {
			fmt.Println("No running workloads to stop")
			return nil
		}

		// Stop all workloads using the bulk method
		group, err = workloadManager.StopWorkloads(ctx, workloadNames)
		if err != nil {
			return fmt.Errorf("failed to stop all workloads: %v", err)
		}

		// Since the stop operation is asynchronous, wait for the group to finish.
		if err := group.Wait(); err != nil {
			return fmt.Errorf("failed to stop all workloads: %v", err)
		}
		fmt.Println("All workloads stopped successfully")
	} else {
		// Get workload name
		workloadName := args[0]

		// Stop a single workload
		group, err = workloadManager.StopWorkloads(ctx, []string{workloadName})
		if err != nil {
			// If the workload is not found or not running, treat as a non-fatal error.
			if errors.Is(err, workloads.ErrWorkloadNotFound) ||
				errors.Is(err, workloads.ErrWorkloadNotRunning) ||
				errors.Is(err, workloads.ErrInvalidWorkloadName) {
				fmt.Printf("workload %s is not running\n", workloadName)
				return nil
			}
			return fmt.Errorf("unexpected error stopping workload: %v", err)
		}

		// Since the stop operation is asynchronous, wait for the group to finish.
		if err := group.Wait(); err != nil {
			return fmt.Errorf("failed to stop workload %s: %v", workloadName, err)
		}
		fmt.Printf("workload %s stopped successfully\n", workloadName)
	}

	return nil
}
