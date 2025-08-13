// Package ignore provides functionality for processing .thvignore files
// to filter bind mount contents using tmpfs overlays.
package ignore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"go.uber.org/zap"
)

// Processor handles loading and processing ignore patterns
type Processor struct {
	GlobalPatterns  []string
	LocalPatterns   []string
	Config          *Config
	sharedEmptyFile string // Cached path to a single shared empty file
	logger          *zap.SugaredLogger
}

// Config holds configuration for ignore processing
type Config struct {
	LoadGlobal    bool // Whether to load global ignore patterns
	PrintOverlays bool // Whether to print resolved overlay paths for debugging
}

const ignoreFileName = ".thvignore"

// NewProcessor creates a new Processor instance with the given configuration
func NewProcessor(config *Config, logger *zap.SugaredLogger) *Processor {
	if config == nil {
		config = &Config{
			LoadGlobal:    true,
			PrintOverlays: false,
		}
	}

	return &Processor{
		GlobalPatterns: make([]string, 0),
		LocalPatterns:  make([]string, 0),
		Config:         config,
		logger:         logger,
	}
}

// LoadGlobal loads global ignore patterns from ~/.config/toolhive/thvignore
func (p *Processor) LoadGlobal() error {
	// Skip loading global patterns if disabled in config
	if !p.Config.LoadGlobal {
		p.logger.Debug("Global ignore patterns disabled by configuration")
		return nil
	}

	globalIgnoreFile, err := xdg.ConfigFile("toolhive/thvignore")
	if err != nil {
		p.logger.Debugf("Failed to get XDG config file path: %v", err)
		return nil // Not a fatal error, continue without global patterns
	}

	patterns, err := p.loadIgnoreFile(globalIgnoreFile)
	if err != nil {
		if os.IsNotExist(err) {
			p.logger.Debugf("Global ignore file not found: %s", globalIgnoreFile)
			return nil // Not a fatal error
		}
		return fmt.Errorf("failed to load global ignore file: %w", err)
	}

	p.GlobalPatterns = patterns
	p.logger.Debugf("Loaded %d global ignore patterns from %s", len(patterns), globalIgnoreFile)
	return nil
}

// LoadLocal loads local ignore patterns from the configured ignore file in the specified directory
func (p *Processor) LoadLocal(sourceDir string) error {
	localIgnoreFile := filepath.Join(sourceDir, ignoreFileName)
	patterns, err := p.loadIgnoreFile(localIgnoreFile)
	if err != nil {
		if os.IsNotExist(err) {
			p.logger.Debugf("Local ignore file not found: %s", localIgnoreFile)
			return nil // Not a fatal error
		}
		return fmt.Errorf("failed to load local ignore file: %w", err)
	}

	p.LocalPatterns = append(p.LocalPatterns, patterns...)
	p.logger.Debugf("Loaded %d local ignore patterns from %s", len(patterns), localIgnoreFile)
	return nil
}

// loadIgnoreFile loads patterns from a .gitignore-style file
func (*Processor) loadIgnoreFile(filePath string) ([]string, error) {
	// #nosec G304 - This is intentional as we're reading user-specified ignore files
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		patterns = append(patterns, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading ignore file: %w", err)
	}

	return patterns, nil
}

// OverlayMount represents a mount that should overlay an ignored path
type OverlayMount struct {
	ContainerPath string // Path in the container to overlay
	HostPath      string // Host path (for bind mounts) or empty (for tmpfs)
	Type          string // "tmpfs" for directories, "bind" for files
}

// GetOverlayMounts returns mounts that should overlay ignored paths
// based on the loaded ignore patterns
func (p *Processor) GetOverlayMounts(bindMount, containerPath string) []OverlayMount {
	var overlayMounts []OverlayMount
	overlaySet := make(map[string]bool) // To avoid duplicates

	// Combine global and local patterns
	allPatterns := append(p.GlobalPatterns, p.LocalPatterns...)

	for _, pattern := range allPatterns {
		overlayMounts = append(overlayMounts, p.processPattern(bindMount, containerPath, pattern, overlaySet)...)
	}

	p.printOverlays(overlayMounts, bindMount, containerPath)
	return overlayMounts
}

// processPattern processes a single ignore pattern and returns overlay mounts
func (p *Processor) processPattern(bindMount, containerPath, pattern string, overlaySet map[string]bool) []OverlayMount {
	var overlayMounts []OverlayMount
	matchingPaths := p.getMatchingPaths(bindMount, pattern)

	for _, matchPath := range matchingPaths {
		if overlay := p.createOverlayMount(matchPath, bindMount, containerPath, pattern, overlaySet); overlay != nil {
			overlayMounts = append(overlayMounts, *overlay)
		}
	}

	return overlayMounts
}

// createOverlayMount creates an overlay mount for a matched path
func (p *Processor) createOverlayMount(
	matchPath, bindMount, containerPath, pattern string, overlaySet map[string]bool,
) *OverlayMount {
	// Calculate relative path from bind mount to matched path
	relPath, err := filepath.Rel(bindMount, matchPath)
	if err != nil {
		p.logger.Debugf("Failed to calculate relative path for %s: %v", matchPath, err)
		return nil
	}

	// Convert to container path
	containerOverlayPath := filepath.Join(containerPath, relPath)

	// Skip if we already have this overlay
	if overlaySet[containerOverlayPath] {
		return nil
	}
	overlaySet[containerOverlayPath] = true

	// Check if the matched path is a directory or file
	info, err := os.Stat(matchPath)
	if err != nil {
		p.logger.Debugf("Failed to stat path %s: %v", matchPath, err)
		return nil
	}

	if info.IsDir() {
		// For directories, use tmpfs mount
		p.logger.Debugf("Adding tmpfs overlay for directory pattern '%s' at container path: %s", pattern, containerOverlayPath)
		return &OverlayMount{
			ContainerPath: containerOverlayPath,
			HostPath:      "", // tmpfs doesn't need host path
			Type:          "tmpfs",
		}
	}

	// For files, create empty file and bind mount it
	emptyFilePath, err := p.createEmptyFile()
	if err != nil {
		p.logger.Debugf("Failed to create empty file for pattern '%s': %v", pattern, err)
		return nil
	}

	p.logger.Debugf("Adding bind overlay for file pattern '%s' at container path: %s (host: %s)",
		pattern, containerOverlayPath, emptyFilePath)
	return &OverlayMount{
		ContainerPath: containerOverlayPath,
		HostPath:      emptyFilePath,
		Type:          "bind",
	}
}

// printOverlays prints resolved overlays if requested
func (p *Processor) printOverlays(overlayMounts []OverlayMount, bindMount, containerPath string) {
	if p.Config.PrintOverlays && len(overlayMounts) > 0 {
		p.logger.Infof("Resolved overlays for mount %s -> %s:", bindMount, containerPath)
		for _, overlay := range overlayMounts {
			if overlay.Type == "tmpfs" {
				p.logger.Infof("  - %s (tmpfs)", overlay.ContainerPath)
			} else {
				p.logger.Infof("  - %s (bind: %s)", overlay.ContainerPath, overlay.HostPath)
			}
		}
	}
}

// createEmptyFile returns a path to a shared empty file for bind mounting
func (p *Processor) createEmptyFile() (string, error) {
	// Return cached shared empty file if it exists
	if p.sharedEmptyFile != "" {
		// Verify the file still exists
		if _, err := os.Stat(p.sharedEmptyFile); err == nil {
			return p.sharedEmptyFile, nil
		}
		// File was deleted, clear the cache
		p.sharedEmptyFile = ""
	}

	// Create a new shared empty file
	tmpFile, err := os.CreateTemp("", "thvignore-shared-empty-*")
	if err != nil {
		return "", fmt.Errorf("failed to create shared empty file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close shared empty file: %w", err)
	}

	// Cache the path for reuse
	p.sharedEmptyFile = tmpFile.Name()
	p.logger.Debugf("Created shared empty file for bind mounting: %s", p.sharedEmptyFile)

	return p.sharedEmptyFile, nil
}

// Cleanup removes the shared empty file if it exists
func (p *Processor) Cleanup() error {
	if p.sharedEmptyFile != "" {
		if err := os.Remove(p.sharedEmptyFile); err != nil && !os.IsNotExist(err) {
			p.logger.Debugf("Failed to remove shared empty file %s: %v", p.sharedEmptyFile, err)
			return fmt.Errorf("failed to remove shared empty file: %w", err)
		}
		p.logger.Debugf("Cleaned up shared empty file: %s", p.sharedEmptyFile)
		p.sharedEmptyFile = ""
	}
	return nil
}

// GetOverlayPaths returns container paths that should be overlaid
// based on the loaded ignore patterns (kept for backward compatibility)
func (p *Processor) GetOverlayPaths(bindMount, containerPath string) []string {
	overlayMounts := p.GetOverlayMounts(bindMount, containerPath)
	var overlayPaths []string

	for _, mount := range overlayMounts {
		overlayPaths = append(overlayPaths, mount.ContainerPath)
	}

	return overlayPaths
}

// getMatchingPaths returns all paths that match the given pattern in the directory
func (p *Processor) getMatchingPaths(dir, pattern string) []string {
	var matchingPaths []string

	// Handle directory patterns (ending with /)
	if strings.HasSuffix(pattern, "/") {
		dirPattern := strings.TrimSuffix(pattern, "/")
		targetPath := filepath.Join(dir, dirPattern)
		if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
			matchingPaths = append(matchingPaths, targetPath)
		}
		return matchingPaths
	}

	// Handle direct file/directory matches
	targetPath := filepath.Join(dir, pattern)
	if _, err := os.Stat(targetPath); err == nil {
		matchingPaths = append(matchingPaths, targetPath)
		return matchingPaths
	}

	// Handle glob patterns
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		p.logger.Debugf("Error matching pattern '%s': %v", pattern, err)
		return matchingPaths
	}

	return matches
}

// patternMatchesInDirectory checks if a pattern matches any files/directories in the given directory
func (p *Processor) patternMatchesInDirectory(dir, pattern string) bool {
	return len(p.getMatchingPaths(dir, pattern)) > 0
}

// ShouldIgnore checks if a given path should be ignored based on loaded patterns
func (p *Processor) ShouldIgnore(path string) bool {
	// Combine global and local patterns
	allPatterns := append(p.GlobalPatterns, p.LocalPatterns...)

	for _, pattern := range allPatterns {
		// Simple pattern matching - can be enhanced with more sophisticated gitignore-style matching
		if matched, err := filepath.Match(pattern, filepath.Base(path)); err == nil && matched {
			return true
		}

		// Check if path contains the pattern
		if strings.Contains(path, pattern) {
			return true
		}
	}

	return false
}
