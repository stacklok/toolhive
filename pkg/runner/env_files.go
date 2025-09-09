package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/logger"
)

// processEnvFilesDirectory detects and processes environment files from a directory
// Returns a map of environment variables to be merged with RunConfig.EnvVars
func processEnvFilesDirectory(dirPath string) (map[string]string, error) {
	// Check if directory exists
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debugf("Env files directory %s does not exist", dirPath)
			return make(map[string]string), nil // Return empty map, not an error
		}
		return nil, fmt.Errorf("failed to read env files directory %s: %w", dirPath, err)
	}

	logger.Infof("Env files directory %s detected, processing environment files", dirPath)

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
			logger.Warnf("Failed to process env file %s: %v", entry.Name(), err)
			continue
		}

		// Merge env vars, with later files potentially overriding earlier ones
		for key, value := range fileEnvVars {
			allEnvVars[key] = value
		}
		processedCount++
	}

	logger.Infof("Processed %d env files, %d environment variables extracted", processedCount, len(allEnvVars))
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
		logger.Debugf("No environment variables found in %s", filepath.Base(path))
		return make(map[string]string), nil
	}

	// Use existing ToolHive utility to parse KEY=VALUE format
	envVars, err := environment.ParseEnvironmentVariables(envLines)
	if err != nil {
		return nil, fmt.Errorf("failed to parse environment variables in %s: %w", filepath.Base(path), err)
	}

	logger.Debugf("Extracted %d environment variables from %s", len(envVars), filepath.Base(path))
	return envVars, nil
}
