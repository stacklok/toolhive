package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	unknownStr = "unknown"
)

// Version information set by build using -ldflags
var (
	// Version is the current version of ToolHive
	Version = "dev"
	// Commit is the git commit hash of the build
	//nolint:goconst // This is a placeholder for the commit hash
	Commit = unknownStr
	// BuildDate is the date when the binary was built
	// nolint:goconst // This is a placeholder for the build date
	BuildDate = unknownStr
)

// versionInfo represents the version information
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// newVersionCmd creates a new version command
func newVersionCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version of ToolHive",
		Long:  `Display detailed version information about ToolHive, including version number, git commit, build date, and Go version.`,
		Run: func(_ *cobra.Command, _ []string) {
			info := getVersionInfo()

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

// getVersionInfo returns the version information
func getVersionInfo() versionInfo {
	// If version is still "dev", try to get it from build info
	ver := Version
	commit := Commit
	buildDate := BuildDate

	if ver == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			// Try to get version from build info
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					if commit == unknownStr {
						commit = setting.Value
					}
				case "vcs.time":
					if buildDate == unknownStr {
						buildDate = setting.Value
					}
				}
			}
		}
	}

	// Format the build date if it's a timestamp
	if buildDate != unknownStr {
		if t, err := time.Parse(time.RFC3339, buildDate); err == nil {
			buildDate = t.Format("2006-01-02 15:04:05 MST")
		}
	}

	return versionInfo{
		Version:   ver,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// printVersionInfo prints the version information
func printVersionInfo(info versionInfo) {
	fmt.Printf("ToolHive %s", info.Version)
	fmt.Printf("Commit: %s", info.Commit)
	fmt.Printf("Built: %s", info.BuildDate)
	fmt.Printf("Go version: %s", info.GoVersion)
	fmt.Printf("Platform: %s", info.Platform)
}

// printJSONVersionInfo prints the version information as JSON
func printJSONVersionInfo(info versionInfo) {
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
