// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/environment"
)

// processEnvFilesDirectory detects and processes environment files from a directory
// Returns a map of environment variables to be merged with RunConfig.EnvVars
func processEnvFilesDirectory(dirPath string) (map[string]string, error) {
	// Check if directory exists
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("Env files directory does not exist", "dirpath", dirPath)
			return make(map[string]string), nil // Return empty map, not an error
		}
		return nil, fmt.Errorf("failed to read env files directory %s: %w", dirPath, err)
	}

	slog.Debug("Env files directory detected, processing environment files", "dirpath", dirPath)

	allEnvVars := make(map[string]string)
	processedCount := 0

	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		// Skip hidden files
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		filePath := filepath.Join(dirPath, entry.Name())
		fileEnvVars, err := processEnvFile(filePath)
		if err != nil {
			slog.Warn("Failed to process env file", "name", entry.Name(), "s", err)
			continue
		}

		// Merge env vars, with later files potentially overriding earlier ones
		for key, value := range fileEnvVars {
			allEnvVars[key] = value
		}
		processedCount++
	}

	slog.Debug("Processed env files, environment variables extracted", "processedcount", processedCount, "value2", len(allEnvVars))
	return allEnvVars, nil
}

// processEnvFile reads and processes a single environment file
// Uses existing ToolHive environment parsing utilities
func processEnvFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path) // #nosec G304 - path is controlled internally, validated by caller
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Convert content to slice of KEY=VALUE lines for existing parser
	lines := strings.Split(string(content), "\n")
	var envLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle export statements (common in shell env files)
		line = strings.TrimPrefix(line, "export ")

		// Only process lines that contain '=' (KEY=VALUE format)
		if strings.Contains(line, "=") {
			envLines = append(envLines, line)
		}
	}

	if len(envLines) == 0 {
		slog.Debug("No environment variables found in", "value", filepath.Base(path))
		return make(map[string]string), nil
	}

	// Use existing ToolHive utility to parse KEY=VALUE format
	envVars, err := environment.ParseEnvironmentVariables(envLines)
	if err != nil {
		return nil, fmt.Errorf("failed to parse environment variables in %s: %w", filepath.Base(path), err)
	}

	slog.Debug("Extracted environment variables from", "value1", len(envVars), "value2", filepath.Base(path))
	return envVars, nil
}
