// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package updates contains logic for checking if an update is available for ToolHive.
package updates

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/google/uuid"
	"golang.org/x/mod/semver"

	"github.com/stacklok/toolhive/pkg/desktop"
	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/versions"
)

// UpdateChecker is an interface for checking if a new version of ToolHive is available.
type UpdateChecker interface {
	// CheckLatestVersion checks if a new version of ToolHive is available
	// and prints the result to the console.
	CheckLatestVersion() error
}

// NewUpdateChecker creates a new instance of UpdateChecker.
func NewUpdateChecker(versionClient VersionClient) (UpdateChecker, error) {
	path, err := xdg.DataFile(updateFilePathSuffix)
	if err != nil {
		return nil, fmt.Errorf("unable to access update file path %w", err)
	}

	// Get component name for component-specific data
	component := getComponentFromVersionClient(versionClient)

	// Check to see if the file already exists. Read the instance ID and component-specific data from the
	// file if it does. If it doesn't exist, create a new instance ID.
	var instanceID, previousVersion string
	// #nosec G304: File path is not configurable at this time.
	rawContents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			instanceID = uuid.NewString()
		} else {
			return nil, fmt.Errorf("failed to read update file: %w", err)
		}
	} else {
		var contents updateFile
		err = json.Unmarshal(rawContents, &contents)
		if err != nil {
			// If the file is corrupted, attempt to recover
			if recoveredFile, recoverErr := recoverCorruptedJSON(rawContents); recoverErr == nil {
				contents = recoveredFile
				// Note: Update file is corrupted, attempting to preserve instance ID
			} else {
				return nil, fmt.Errorf("failed to deserialize update file: %w", err)
			}
		}
		instanceID = contents.InstanceID
		previousVersion = contents.LatestVersion
	}

	return &defaultUpdateChecker{
		currentVersion:      versions.GetVersionInfo().Version,
		instanceID:          instanceID,
		updateFilePath:      path,
		versionClient:       versionClient,
		previousAPIResponse: previousVersion,
		component:           component,
	}, nil
}

const (
	updateFilePathSuffix = "toolhive/updates.json"
	updateInterval       = 30 * time.Minute
)

// TryGetAnonymousID returns the instance ID from the updates file if it exists.
// This is a read-only operation - it never generates a new ID.
// Returns empty string if the file doesn't exist or doesn't contain an instance ID.
// Use this for optional features like metrics that shouldn't trigger ID generation.
// TODO this should probably be extracted into its own package to handle instance ID generation.
func TryGetAnonymousID() (string, error) {
	path, err := xdg.DataFile(updateFilePathSuffix)
	if err != nil {
		return "", fmt.Errorf("unable to access update file path: %w", err)
	}

	// #nosec G304: File path is not configurable at this time.
	rawContents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet - return empty (don't generate)
			return "", nil
		}
		return "", fmt.Errorf("failed to read update file: %w", err)
	}

	var contents updateFile
	if err := json.Unmarshal(rawContents, &contents); err != nil {
		// If corrupted, try to recover the instance ID
		if recoveredFile, recoverErr := recoverCorruptedJSON(rawContents); recoverErr == nil {
			return recoveredFile.InstanceID, nil
		}
		return "", fmt.Errorf("failed to deserialize update file: %w", err)
	}

	// Return whatever is in the file, even if empty
	return contents.InstanceID, nil
}

// componentInfo represents component-specific update timing information.
type componentInfo struct {
	LastCheck time.Time `json:"last_check"`
}

// updateFile represents the structure of the update file.
type updateFile struct {
	InstanceID    string                   `json:"instance_id"`
	LatestVersion string                   `json:"latest_version"`
	Components    map[string]componentInfo `json:"components"`
}

type defaultUpdateChecker struct {
	instanceID          string
	currentVersion      string
	previousAPIResponse string
	updateFilePath      string
	versionClient       VersionClient
	component           string
}

func (d *defaultUpdateChecker) CheckLatestVersion() error {
	// Read the current update file to get component-specific data
	var currentFile updateFile
	// #nosec G304: File path is not configurable at this time.
	rawContents, err := os.ReadFile(d.updateFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read update file: %w", err)
	}

	// Initialize file structure if it doesn't exist or is empty
	if os.IsNotExist(err) || len(rawContents) == 0 {
		currentFile = updateFile{
			InstanceID: d.instanceID,
			Components: make(map[string]componentInfo),
		}
	} else {
		if err := json.Unmarshal(rawContents, &currentFile); err != nil {
			// If the file is corrupted, attempt to recover
			if recoveredFile, recoverErr := recoverCorruptedJSON(rawContents); recoverErr == nil {
				currentFile = recoveredFile
				// Note: Recovered corrupted update file, preserving instance ID
			} else {
				return fmt.Errorf("failed to deserialize update file: %w", err)
			}
		}

		// Initialize components map if it doesn't exist (for backward compatibility)
		if currentFile.Components == nil {
			currentFile.Components = make(map[string]componentInfo)
		}

		// Use the instance ID from file, but fallback to the one we generated
		if currentFile.InstanceID == "" {
			currentFile.InstanceID = d.instanceID
		}
	}

	// Check component-specific timing
	if componentData, exists := currentFile.Components[d.component]; exists {
		if time.Since(componentData.LastCheck) < updateInterval {
			// If it is too soon - notify the user if we already know there is
			// an update, then exit.
			notifyIfUpdateAvailable(d.currentVersion, currentFile.LatestVersion)
			return nil
		}
	}

	// If the component data is stale or does not exist - get the latest version
	// from the API.
	latestVersion, err := d.versionClient.GetLatestVersion(currentFile.InstanceID, d.currentVersion)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	notifyIfUpdateAvailable(d.currentVersion, latestVersion)

	// Update shared latest version and component-specific timing
	currentFile.LatestVersion = latestVersion
	currentFile.Components[d.component] = componentInfo{
		LastCheck: time.Now().UTC(),
	}

	// Write the updated file
	updatedData, err := json.Marshal(currentFile)
	if err != nil {
		return fmt.Errorf("failed to marshal updated data: %w", err)
	}

	// Acquire lock just before writing to minimize lock time
	lockPath := d.updateFilePath + ".lock"
	lockFile := lockfile.NewTrackedLock(lockPath)
	if err := lockFile.Lock(); err != nil {
		return fmt.Errorf("failed to acquire lock on update file: %w", err)
	}
	defer lockfile.ReleaseTrackedLock(lockPath, lockFile)

	if err := os.WriteFile(d.updateFilePath, updatedData, 0600); err != nil {
		return fmt.Errorf("failed to write updated file: %w", err)
	}

	return nil
}

// getComponentFromVersionClient extracts the component name from a VersionClient.
func getComponentFromVersionClient(versionClient VersionClient) string {
	return versionClient.GetComponent()
}

func notifyIfUpdateAvailable(current, latest string) {
	// Desktop app manages its own updates, suppress CLI update message
	if desktop.IsDesktopManagedCLI() {
		return
	}
	// Print a meaningful message for people running local builds.
	if strings.HasPrefix(current, "build-") {
		// No need to compare versions, user is already aware they are not on the latest release.
		return
	}
	// Ensure both versions have the 'v' prefix for proper semantic version comparison
	if !semver.IsValid(current) {
		current = fmt.Sprintf("v%s", current)
	}
	if !semver.IsValid(latest) {
		latest = fmt.Sprintf("v%s", latest)
	}
	// Compare the versions ensuring their canonical forms
	if semver.Compare(semver.Canonical(current), semver.Canonical(latest)) < 0 {
		fmt.Fprintf(os.Stderr, "A new version of ToolHive is available: %s\nCurrently running: %s\n", latest, current)
	}
}

// recoverCorruptedJSON attempts to recover from common JSON corruption issues
// while preserving the instance_id to avoid regenerating it.
func recoverCorruptedJSON(rawContents []byte) (updateFile, error) {
	content := string(rawContents)

	// Extract the instance_id from the corrupted JSON and regenerate the file
	instanceIDRegex := regexp.MustCompile(`"instance_id":"([^"]+)"`)
	if matches := instanceIDRegex.FindStringSubmatch(content); len(matches) > 1 {
		return updateFile{
			InstanceID: matches[1],
			Components: make(map[string]componentInfo),
		}, nil
	}

	return updateFile{}, fmt.Errorf("unable to recover corrupted JSON")
}
