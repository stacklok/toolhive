package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container"
	runtime "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage logical groupings of MCP servers",
	Long:  `The group command provides subcommands to manage logical groupings of MCP servers.`,
}

var groupCreateCmd = &cobra.Command{
	Use:   "create [group-name]",
	Short: "Create a new group of MCP servers",
	Long: `Create a new logical group of MCP servers.
		 The group can be used to organize and manage multiple MCP servers together.`,
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupCreateCmdFunc,
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all groups",
	Long:  `List all logical groups of MCP servers.`,
	RunE:  groupListCmdFunc,
}

var groupRmCmd = &cobra.Command{
	Use:   "rm [group-name]",
	Short: "Remove a group and remove workloads from it",
	Long: "Remove a group and remove all MCP servers from it. By default, this only removes the group " +
		"membership from workloads without deleting them. Use --with-workloads to also delete the workloads. ",
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupRmCmdFunc,
}

var groupRunCmd = &cobra.Command{
	Use:   "run [group-name]",
	Short: "Deploy all MCP servers from a registry group",
	Long: `Deploy all MCP servers defined in a registry group.
		 This creates a new runtime group and starts all MCP servers within it.`,
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupRunCmdFunc,
}

func validateGroupArg() func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("group name is required")
		}
		if err := validation.ValidateGroupName(args[0]); err != nil {
			return fmt.Errorf("invalid group name: %w", err)
		}
		return nil
	}
}

var (
	withWorkloadsFlag bool
	groupSecrets      []string
	groupEnvVars      []string
)

func groupCreateCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	if err := manager.Create(ctx, groupName); err != nil {
		return err
	}

	fmt.Printf("Group '%s' created successfully.\n", groupName)
	return nil
}

func groupListCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	allGroups, err := manager.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	if len(allGroups) == 0 {
		fmt.Println("No groups configured.")
		return nil
	}

	// Create a tabwriter for table output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME")

	// Print group names in table format
	for _, group := range allGroups {
		fmt.Fprintf(w, "%s\n", group.Name)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to flush tabwriter: %w", err)
	}

	return nil
}

func groupRmCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	if strings.EqualFold(groupName, groups.DefaultGroup) {
		return fmt.Errorf("cannot delete the %s group", groups.DefaultGroup)
	}
	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Check if group exists
	exists, err := manager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Create workloads manager
	workloadsManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workloads manager: %w", err)
	}

	// Get all workloads and filter for the group
	allWorkloads, err := workloadsManager.ListWorkloads(ctx, true) // listAll=true to include stopped workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	groupWorkloads, err := workloads.FilterByGroup(allWorkloads, groupName)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by group: %w", err)
	}

	// Show warning and get user confirmation
	confirmed, err := showWarningAndGetConfirmation(groupName, groupWorkloads)
	if err != nil {
		return err
	}

	if !confirmed {
		return nil
	}

	// Handle workloads if any exist
	if len(groupWorkloads) > 0 {
		if withWorkloadsFlag {
			err = deleteWorkloadsInGroup(ctx, workloadsManager, groupWorkloads, groupName)
		} else {
			err = moveWorkloadsToGroup(ctx, workloadsManager, groupWorkloads, groupName, groups.DefaultGroup)
		}
	}
	if err != nil {
		return err
	}

	if err = manager.Delete(ctx, groupName); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	fmt.Printf("Group '%s' deleted successfully\n", groupName)
	return nil
}

func showWarningAndGetConfirmation(groupName string, groupWorkloads []core.Workload) (bool, error) {
	if len(groupWorkloads) == 0 {
		return true, nil
	}

	// Show warning and get user confirmation
	if withWorkloadsFlag {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and DELETE all workloads belonging to it.\n", groupName)
	} else {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and move all workloads to the 'default' group\n", groupName)
	}

	fmt.Printf("   The following %d workload(s) will be affected:\n", len(groupWorkloads))
	for _, workload := range groupWorkloads {
		if withWorkloadsFlag {
			fmt.Printf("   - %s (will be DELETED)\n", workload.Name)
		} else {
			fmt.Printf("   - %s (will be moved to the 'default' group)\n", workload.Name)
		}
	}

	if withWorkloadsFlag {
		fmt.Printf("\nThis action cannot be undone. Are you sure you want to continue? [y/N]: ")
	} else {
		fmt.Printf("\nAre you sure you want to continue? [y/N]: ")
	}

	// Read user input
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read user input: %w", err)
	}

	// Check if user confirmed
	response = strings.TrimSpace(strings.ToLower(response))
	if response != "y" && response != "yes" {
		fmt.Println("Group deletion cancelled.")
		return false, nil
	}

	return true, nil
}

func deleteWorkloadsInGroup(
	ctx context.Context,
	workloadManager workloads.Manager,
	groupWorkloads []core.Workload,
	groupName string,
) error {
	// Extract workload names for deletion
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Delete all workloads in the group
	group, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	// Wait for the deletion to complete
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	fmt.Printf("Deleted %d workload(s) from group '%s'\n", len(groupWorkloads), groupName)
	return nil
}

// moveWorkloadsToGroup moves all workloads in the specified group to a new group.
func moveWorkloadsToGroup(
	ctx context.Context,
	workloadManager workloads.Manager,
	groupWorkloads []core.Workload,
	groupFrom string,
	groupTo string,
) error {

	// Extract workload names for the move operation
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Update workload runconfigs to point to the new group
	if err := workloadManager.MoveToGroup(ctx, workloadNames, groupFrom, groupTo); err != nil {
		return fmt.Errorf("failed to move workloads to default group: %w", err)
	}

	// Update client configurations for the moved workloads
	err := updateClientConfigurations(ctx, groupWorkloads, groupFrom, groupTo)
	if err != nil {
		return fmt.Errorf("failed to update client configurations with new group: %w", err)
	}

	fmt.Printf("Moved %d workload(s) from group '%s' to group '%s'\n", len(groupWorkloads), groupFrom, groupTo)
	return nil
}

func updateClientConfigurations(ctx context.Context, groupWorkloads []core.Workload, groupFrom string, groupTo string) error {
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	for _, w := range groupWorkloads {
		// Only update client configurations for running workloads
		if w.Status != runtime.WorkloadStatusRunning {
			continue
		}

		if err := clientManager.RemoveServerFromClients(ctx, w.Name, groupFrom); err != nil {
			return fmt.Errorf("failed to remove server %s from client configurations: %w", w.Name, err)
		}
		if err := clientManager.AddServerToClients(ctx, w.Name, w.URL, string(w.TransportType), groupTo); err != nil {
			return fmt.Errorf("failed to add server %s to client configurations: %w", w.Name, err)
		}
	}

	return nil
}

func groupRunCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	// Get registry provider
	registryProvider, err := registry.GetDefaultProvider()
	if err != nil {
		return fmt.Errorf("failed to get registry provider: %w", err)
	}

	// Get registry data
	reg, err := registryProvider.GetRegistry()
	if err != nil {
		return fmt.Errorf("failed to get registry: %w", err)
	}

	// Find the group in the registry
	registryGroup, found := reg.GetGroupByName(groupName)
	if !found {
		return fmt.Errorf("group '%s' not found in registry", groupName)
	}

	totalServers := len(registryGroup.Servers) + len(registryGroup.RemoteServers)
	fmt.Printf("Found registry group '%s' with %d servers (%d container, %d remote)\n",
		registryGroup.Name, totalServers, len(registryGroup.Servers), len(registryGroup.RemoteServers))

	// Validate all preconditions before making any changes
	if err := validateGroupRunPreconditions(ctx, groupName, registryGroup, groupSecrets, groupEnvVars); err != nil {
		return err
	}

	// Create managers
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Create the runtime group
	if err := groupManager.Create(ctx, groupName); err != nil {
		return fmt.Errorf("failed to create group '%s': %w", groupName, err)
	}
	fmt.Printf("Created runtime group '%s'\n", groupName)

	// Deploy servers - continue on failure but warn user
	var successfulServers []string
	var failedServers []string

	// Deploy container servers
	for serverName, serverMetadata := range registryGroup.Servers {
		fmt.Printf("Deploying server '%s' from image '%s'\n", serverName, serverMetadata.Image)

		if err := deployServer(ctx, serverName, serverMetadata, groupName, groupSecrets, groupEnvVars, cmd); err != nil {
			fmt.Printf("Warning: failed to deploy server '%s': %v\n", serverName, err)
			failedServers = append(failedServers, serverName)
			continue
		}

		successfulServers = append(successfulServers, serverName)
		fmt.Printf("Started server '%s'\n", serverName)
	}

	// Deploy remote servers
	for serverName, serverMetadata := range registryGroup.RemoteServers {
		fmt.Printf("Deploying remote server '%s' at URL '%s'\n", serverName, serverMetadata.URL)

		if err := deployRemoteServer(ctx, serverName, groupName, groupSecrets, groupEnvVars, cmd); err != nil {
			fmt.Printf("Warning: failed to deploy remote server '%s': %v\n", serverName, err)
			failedServers = append(failedServers, serverName)
			continue
		}

		successfulServers = append(successfulServers, serverName)
		fmt.Printf("Started remote server '%s'\n", serverName)
	}

	// Report deployment results
	if len(successfulServers) > 0 {
		fmt.Printf("Successfully deployed %d servers in group '%s': %v\n", len(successfulServers), groupName, successfulServers)
	}
	if len(failedServers) > 0 {
		fmt.Printf("Warning: %d servers failed to deploy: %v\n", len(failedServers), failedServers)
	}

	return nil
}

// validateGroupRunPreconditions validates all conditions before making any changes
func validateGroupRunPreconditions(ctx context.Context, groupName string,
	registryGroup *registry.Group, secrets []string, envVars []string) error {
	if err := validateRuntimeGroupDoesNotExist(ctx, groupName); err != nil {
		return err
	}
	if err := validateServersDoNotExist(ctx, registryGroup); err != nil {
		return err
	}
	if err := validateSecretsFormat(secrets, registryGroup); err != nil {
		return err
	}
	return validateEnvVarsFormat(envVars, registryGroup)
}

// validateRuntimeGroupDoesNotExist checks that the runtime group doesn't already exist
func validateRuntimeGroupDoesNotExist(ctx context.Context, groupName string) error {
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return fmt.Errorf("runtime group '%s' already exists", groupName)
	}
	return nil
}

// validateServersDoNotExist checks that no servers in the group already exist as workloads
func validateServersDoNotExist(ctx context.Context, registryGroup *registry.Group) error {
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %w", err)
	}
	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Check container servers
	for serverName := range registryGroup.Servers {
		exists, err := workloadManager.DoesWorkloadExist(ctx, serverName)
		if err != nil {
			return fmt.Errorf("failed to check if server '%s' exists: %w", serverName, err)
		}
		if exists {
			return fmt.Errorf("MCP server '%s' already exists", serverName)
		}
	}

	// Check remote servers
	for serverName := range registryGroup.RemoteServers {
		exists, err := workloadManager.DoesWorkloadExist(ctx, serverName)
		if err != nil {
			return fmt.Errorf("failed to check if server '%s' exists: %w", serverName, err)
		}
		if exists {
			return fmt.Errorf("MCP server '%s' already exists", serverName)
		}
	}

	return nil
}

// validateSecretsFormat validates all secrets have correct format and target existing servers
func validateSecretsFormat(secrets []string, registryGroup *registry.Group) error {
	for _, secret := range secrets {
		if err := validateSecretFormat(secret, registryGroup); err != nil {
			return err
		}
	}
	return nil
}

// validateEnvVarsFormat validates all environment variables have correct format and target existing servers
func validateEnvVarsFormat(envVars []string, registryGroup *registry.Group) error {
	for _, envVar := range envVars {
		if err := validateEnvVarFormat(envVar, registryGroup); err != nil {
			return err
		}
	}
	return nil
}

// deployServer deploys a single server using the same path as the normal run command
func deployServer(ctx context.Context, serverName string,
	serverMetadata *registry.ImageMetadata, groupName string, secrets []string, envVars []string, cmd *cobra.Command) error {

	// Filter secrets and env vars for this specific server
	serverSecrets := filterSecretsForServer(secrets, serverName)
	serverEnvVars := filterEnvVarsForServer(envVars, serverName)

	// Create RunFlags similar to normal run command
	runFlags := &RunFlags{
		Name:        serverName,
		Group:       groupName,
		Transport:   "", // Let normal defaulting logic handle it (matches cobra default)
		Host:        transport.LocalhostIPv4,
		ProxyPort:   0,
		TargetPort:  serverMetadata.TargetPort,
		Foreground:  false,
		VerifyImage: retriever.VerifyImageWarn, // Matches cobra default
		Env:         serverEnvVars,
		Volumes:     []string{},
		Secrets:     serverSecrets,
	}

	logger.Errorf("Server name is %s", serverName)
	// Use the shared runSingleServer function
	return runSingleServer(ctx, runFlags, serverName, []string{}, false, cmd, groupName)
}

// deployRemoteServer deploys a single remote server using the same path as the normal run command
func deployRemoteServer(ctx context.Context, serverName string,
	groupName string, secrets []string, envVars []string, cmd *cobra.Command) error {

	// Filter secrets and env vars for this specific server
	serverSecrets := filterSecretsForServer(secrets, serverName)
	serverEnvVars := filterEnvVarsForServer(envVars, serverName)

	// Create RunFlags for remote server
	runFlags := &RunFlags{
		Name:        serverName,
		Group:       groupName,
		Transport:   "", // Let normal defaulting logic handle it (matches cobra default)
		Host:        transport.LocalhostIPv4,
		ProxyPort:   0,
		TargetPort:  0,
		Foreground:  false,
		VerifyImage: retriever.VerifyImageWarn, // Matches cobra default
		Env:         serverEnvVars,
		Volumes:     []string{},
		Secrets:     serverSecrets,
		// Don't pre-set RemoteURL - let GetMCPServer discover it through group lookup
	}

	logger.Errorf("Server name is %s", serverName)
	// Use the shared runSingleServer function, passing the serverName
	return runSingleServer(ctx, runFlags, serverName, []string{}, false, cmd, groupName)
}

// validateSecretFormat validates secret format and checks if target server exists in the group
func validateSecretFormat(secret string, registryGroup *registry.Group) error {
	// Expected format: NAME,target=SERVER_NAME.TARGET
	parts := strings.Split(secret, ",target=")
	if len(parts) != 2 {
		return fmt.Errorf("invalid secret format '%s': expected 'NAME,target=SERVER_NAME.TARGET'", secret)
	}

	secretName := parts[0]
	if secretName == "" {
		return fmt.Errorf("secret name cannot be empty in '%s'", secret)
	}

	targetPart := parts[1]
	if targetPart == "" {
		return fmt.Errorf("target cannot be empty in secret '%s'", secret)
	}

	// Extract server name from target (SERVER_NAME.TARGET)
	targetParts := strings.Split(targetPart, ".")
	if len(targetParts) < 2 {
		return fmt.Errorf("invalid target format in secret '%s': expected 'SERVER_NAME.TARGET'", secret)
	}

	serverName := targetParts[0]
	if serverName == "" {
		return fmt.Errorf("server name cannot be empty in secret target '%s'", secret)
	}

	// Check if server exists in the registry group (check both container and remote servers)
	_, existsInServers := registryGroup.Servers[serverName]
	_, existsInRemoteServers := registryGroup.RemoteServers[serverName]
	if !existsInServers && !existsInRemoteServers {
		return fmt.Errorf("secret cannot be set because the MCP server named '%s' does not exist in group", serverName)
	}

	return nil
}

// validateEnvVarFormat validates environment variable format and checks if target server exists in the group
func validateEnvVarFormat(envVar string, registryGroup *registry.Group) error {
	// Expected format: SERVER_NAME.KEY=VALUE
	parts := strings.Split(envVar, "=")
	if len(parts) < 2 {
		return fmt.Errorf("invalid env var format '%s': expected 'SERVER_NAME.KEY=VALUE'", envVar)
	}

	keyPart := parts[0]
	keyParts := strings.Split(keyPart, ".")
	if len(keyParts) < 2 {
		return fmt.Errorf("invalid env var format '%s': expected 'SERVER_NAME.KEY=VALUE'", envVar)
	}

	serverName := keyParts[0]
	keyName := strings.Join(keyParts[1:], ".")

	if serverName == "" {
		return fmt.Errorf("server name cannot be empty in env var '%s'", envVar)
	}

	if keyName == "" {
		return fmt.Errorf("key name cannot be empty in env var '%s'", envVar)
	}

	// Check if server exists in the registry group (check both container and remote servers)
	_, existsInServers := registryGroup.Servers[serverName]
	_, existsInRemoteServers := registryGroup.RemoteServers[serverName]
	if !existsInServers && !existsInRemoteServers {
		return fmt.Errorf("env var cannot be set because the MCP server named '%s' does not exist in group", serverName)
	}

	return nil
}

// filterSecretsForServer filters secrets that are targeted for a specific server
func filterSecretsForServer(secrets []string, serverName string) []string {
	var serverSecrets []string
	for _, secret := range secrets {
		// Expected format: NAME,target=SERVER_NAME.TARGET
		parts := strings.Split(secret, ",target=")
		if len(parts) != 2 {
			continue // Skip invalid formats (should have been caught in validation)
		}

		targetPart := parts[1]
		targetParts := strings.Split(targetPart, ".")
		if len(targetParts) < 2 {
			continue // Skip invalid formats (should have been caught in validation)
		}

		targetServerName := targetParts[0]
		if targetServerName == serverName {
			// Convert from group format to normal run format
			// From: GITHUB_TOKEN,target=github.GITHUB_PERSONAL_ACCESS_TOKEN
			// To: GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN
			secretName := parts[0]
			targetEnvVar := strings.Join(targetParts[1:], ".") // Join in case env var has dots
			normalRunFormat := fmt.Sprintf("%s,target=%s", secretName, targetEnvVar)
			serverSecrets = append(serverSecrets, normalRunFormat)
		}
	}
	return serverSecrets
}

// filterEnvVarsForServer filters environment variables that are targeted for a specific server
func filterEnvVarsForServer(envVars []string, serverName string) []string {
	var serverEnvVars []string
	for _, envVar := range envVars {
		// Expected format: SERVER_NAME.KEY=VALUE
		parts := strings.Split(envVar, "=")
		if len(parts) < 2 {
			continue // Skip invalid formats (should have been caught in validation)
		}

		keyPart := parts[0]
		keyParts := strings.Split(keyPart, ".")
		if len(keyParts) < 2 {
			continue // Skip invalid formats (should have been caught in validation)
		}

		targetServerName := keyParts[0]
		if targetServerName == serverName {
			// Convert to standard env var format by removing server prefix
			keyName := strings.Join(keyParts[1:], ".")
			value := strings.Join(parts[1:], "=")
			serverEnvVars = append(serverEnvVars, fmt.Sprintf("%s=%s", keyName, value))
		}
	}
	return serverEnvVars
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupRmCmd)
	groupCmd.AddCommand(groupRunCmd)

	// Add --with-workloads flag to group rm command
	groupRmCmd.Flags().BoolVar(&withWorkloadsFlag, "with-workloads", false,
		"Delete all workloads in the group along with the group")

	// Add flags to group run command
	groupRunCmd.Flags().StringArrayVar(&groupSecrets, "secret", []string{},
		"Secrets to be fetched from the secrets manager and set as environment variables (format: NAME,target=SERVER_NAME.TARGET)")
	groupRunCmd.Flags().StringArrayVar(&groupEnvVars, "env", []string{},
		"Environment variables to pass to an MCP server in the group (format: SERVER_NAME.KEY=VALUE)")
}
