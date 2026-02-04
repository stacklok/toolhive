// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package desktop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	// toolhiveDir is the directory name for toolhive files in the user's home.
	toolhiveDir = ".toolhive"
	// markerFileName is the name of the CLI source marker file.
	markerFileName = ".cli-source"
)

// errMarkerNotFound is returned when the marker file does not exist.
var errMarkerNotFound = errors.New("marker file not found")

// errInvalidMarker is returned when the marker file exists but is invalid.
var errInvalidMarker = errors.New("invalid marker file")

// getMarkerFilePath returns the path to the CLI source marker file.
// The marker file is located at ~/.toolhive/.cli-source
func getMarkerFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, toolhiveDir, markerFileName), nil
}

// readMarkerFile reads and parses the CLI source marker file.
// Returns errMarkerNotFound if the file doesn't exist.
// Returns errInvalidMarker if the file exists but cannot be parsed or has
// an invalid schema version.
func readMarkerFile() (*cliSourceMarker, error) {
	markerPath, err := getMarkerFilePath()
	if err != nil {
		return nil, err
	}

	return readMarkerFileFromPath(markerPath)
}

// readMarkerFileFromPath reads and parses the CLI source marker file from
// a specific path. This is useful for testing.
func readMarkerFileFromPath(path string) (*cliSourceMarker, error) {
	// #nosec G304 -- path is always the marker file path from getMarkerFilePath or tests
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMarkerNotFound
		}
		return nil, err
	}

	var marker cliSourceMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, errInvalidMarker
	}

	// Validate schema version
	if marker.SchemaVersion != currentSchemaVersion {
		return nil, errInvalidMarker
	}

	// Validate source field
	if marker.Source != "desktop" {
		return nil, errInvalidMarker
	}

	return &marker, nil
}

// markerFileExists checks if the marker file exists without reading it.
func markerFileExists() (bool, error) {
	markerPath, err := getMarkerFilePath()
	if err != nil {
		return false, err
	}

	_, err = os.Stat(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
