package local

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/versions"
)

// Version is the local implementation of the api.VersionAPI interface.
type Version struct {
	// debug indicates whether debug mode is enabled
	debug bool
}

// NewVersion creates a new local VersionAPI with the provided debug flag.
func NewVersion(debug bool) api.VersionAPI {
	return &Version{
		debug: debug,
	}
}

// Get returns version information.
func (v *Version) Get(_ context.Context, opts *api.VersionOptions) (string, error) {
	v.logDebug("Getting version information")

	// Get version information
	versionInfo := versions.GetVersionInfo()

	// Format the output based on the format option
	if opts != nil && opts.Format == "json" {
		// Marshal to JSON
		jsonData, err := json.MarshalIndent(versionInfo, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal version information to JSON: %w", err)
		}
		return string(jsonData), nil
	}

	// Format as text
	platform := strings.Split(versionInfo.Platform, "/")
	osName := ""
	arch := ""
	if len(platform) == 2 {
		osName = platform[0]
		arch = platform[1]
	}

	return fmt.Sprintf("ToolHive %s\nCommit: %s\nBuild Date: %s\nGo Version: %s\nOS/Arch: %s/%s",
		versionInfo.Version,
		versionInfo.Commit,
		versionInfo.BuildDate,
		versionInfo.GoVersion,
		osName,
		arch,
	), nil
}

// logDebug logs a debug message if debug mode is enabled
func (v *Version) logDebug(format string, args ...interface{}) {
	if v.debug {
		logger.Log.Infof(format, args...)
	}
}
