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

// ErrMarkerNotFound is returned when the marker file does not exist.
var ErrMarkerNotFound = errors.New("marker file not found")

// ErrInvalidMarker is returned when the marker file exists but is invalid.
var ErrInvalidMarker = errors.New("invalid marker file")

// GetMarkerFilePath returns the path to the CLI source marker file.
// The marker file is located at ~/.toolhive/.cli-source
func GetMarkerFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, toolhiveDir, markerFileName), nil
}

// ReadMarkerFile reads and parses the CLI source marker file.
// Returns ErrMarkerNotFound if the file doesn't exist.
// Returns ErrInvalidMarker if the file exists but cannot be parsed or has
// an invalid schema version.
func ReadMarkerFile() (*CliSourceMarker, error) {
	markerPath, err := GetMarkerFilePath()
	if err != nil {
		return nil, err
	}

	return ReadMarkerFileFromPath(markerPath)
}

// ReadMarkerFileFromPath reads and parses the CLI source marker file from
// a specific path. This is useful for testing.
func ReadMarkerFileFromPath(path string) (*CliSourceMarker, error) {
	// #nosec G304 -- path is always the marker file path from GetMarkerFilePath or tests
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMarkerNotFound
		}
		return nil, err
	}

	var marker CliSourceMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, ErrInvalidMarker
	}

	// Validate schema version
	if marker.SchemaVersion != CurrentSchemaVersion {
		return nil, ErrInvalidMarker
	}

	// Validate source field
	if marker.Source != "desktop" {
		return nil, ErrInvalidMarker
	}

	return &marker, nil
}

// MarkerFileExists checks if the marker file exists without reading it.
func MarkerFileExists() (bool, error) {
	markerPath, err := GetMarkerFilePath()
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
