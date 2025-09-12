// Package cli provides utility functions specific to the
// CLI that we want to test more thoroughly.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/runner"
)

// ToolsOverrideJSON is a struct that represents the tools override JSON file.
type toolsOverrideJSON struct {
	ToolsOverride map[string]runner.ToolOverride `json:"toolsOverride"`
}

// LoadToolsOverride loads the tools override JSON file from the given path.
func LoadToolsOverride(path string) (*map[string]runner.ToolOverride, error) {
	jsonFile, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open tools override file: %v", err)
	}
	defer jsonFile.Close()

	var toolsOverride toolsOverrideJSON
	decoder := json.NewDecoder(jsonFile)
	err = decoder.Decode(&toolsOverride)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tools override file: %v", err)
	}
	if toolsOverride.ToolsOverride == nil {
		return nil, errors.New("tools override are empty")
	}
	return &toolsOverride.ToolsOverride, nil
}
