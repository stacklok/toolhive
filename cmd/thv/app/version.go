package app

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/versions"
)

// newVersionCmd creates a new version command
func newVersionCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version of ToolHive",
		Long:  `Display detailed version information about ToolHive, including version number, git commit, build date, and Go version.`,
		Run: func(_ *cobra.Command, _ []string) {
			info := versions.GetVersionInfo()

			if jsonOutput {
				printJSONVersionInfo(info)
			} else {
				printVersionInfo(info)
			}
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output version information as JSON")

	return cmd
}

// printVersionInfo prints the version information
func printVersionInfo(info versions.VersionInfo) {
	fmt.Printf("ToolHive %s\n", info.Version)
	fmt.Printf("Commit: %s\n", info.Commit)
	fmt.Printf("Built: %s\n", info.BuildDate)
	fmt.Printf("Go version: %s\n", info.GoVersion)
	fmt.Printf("Platform: %s\n", info.Platform)
}

// printJSONVersionInfo prints the version information as JSON
func printJSONVersionInfo(info versions.VersionInfo) {
	// Simple JSON formatting without importing encoding/json
	jsonStr := fmt.Sprintf(`{
  "version": "%s",
  "commit": "%s",
  "build_date": "%s",
  "go_version": "%s",
  "platform": "%s"
}`,
		escapeJSON(info.Version),
		escapeJSON(info.Commit),
		escapeJSON(info.BuildDate),
		escapeJSON(info.GoVersion),
		escapeJSON(info.Platform))

	fmt.Printf("%s", jsonStr)
}

// escapeJSON escapes special characters in JSON strings
func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
