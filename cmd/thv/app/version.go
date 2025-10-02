package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/versions"
)

// newVersionCmd creates a new version command
func newVersionCmd() *cobra.Command {
	var outputFormat string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version of ToolHive",
		Long:  `Display detailed version information about ToolHive, including version number, git commit, build date, and Go version.`,
		Run: func(_ *cobra.Command, _ []string) {
			info := versions.GetVersionInfo()

			if outputFormat == FormatJSON {
				printJSONVersionInfo(info)
			} else {
				printVersionInfo(info)
			}
		},
	}

	// Keep the --json flag for backward compatibility
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output version information as JSON (deprecated, use --format instead)")
	// Add the --format flag for consistency with other commands
	cmd.Flags().StringVar(&outputFormat, "format", FormatText, "Output format (json or text)")

	// If --json is set, override the format
	cmd.PreRun = func(_ *cobra.Command, _ []string) {
		if jsonOutput {
			outputFormat = FormatJSON
		}
	}

	return cmd
}

// printVersionInfo prints the version information
func printVersionInfo(info versions.VersionInfo) {
	if strings.HasPrefix(info.Version, "build-") {
		fmt.Printf("You are running a local build of ToolHive\n\n")
	}
	fmt.Printf("ToolHive %s\n", info.Version)
	fmt.Printf("Commit: %s\n", info.Commit)
	fmt.Printf("Built: %s\n", info.BuildDate)
	fmt.Printf("Go version: %s\n", info.GoVersion)
	fmt.Printf("Platform: %s\n", info.Platform)
}

// printJSONVersionInfo prints the version information as JSON
func printJSONVersionInfo(info versions.VersionInfo) {
	// Use encoding/json for proper JSON formatting
	jsonData, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling JSON: %v\n", err)
		return
	}

	fmt.Printf("%s", jsonData)
}
