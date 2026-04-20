// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileConfig is the top-level structure for a webhook configuration file.
// It supports both YAML and JSON formats.
//
// Example YAML:
//
//	validating:
//	  - name: policy-check
//	    url: https://policy.example.com/validate
//	    timeout: 5s
//	    failure_policy: fail
//
//	mutating:
//	  - name: hr-enrichment
//	    url: https://hr-api.example.com/enrich
//	    timeout: 3s
//	    failure_policy: ignore
type FileConfig struct {
	// Validating is the list of validating webhook configurations.
	Validating []Config `yaml:"validating" json:"validating"`
	// Mutating is the list of mutating webhook configurations.
	Mutating []Config `yaml:"mutating" json:"mutating"`
}

// LoadConfig reads and parses a webhook configuration file.
// The format is auto-detected by file extension: ".json" uses JSON decoding;
// all other extensions (including ".yaml" and ".yml") use YAML decoding.
func LoadConfig(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is caller-supplied
	if err != nil {
		return nil, fmt.Errorf("webhook config file not found: %s", path)
	}

	var cfg FileConfig
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse webhook config %s as JSON: %w", path, err)
		}
	} else {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse webhook config %s as YAML: %w", path, err)
		}
	}

	normalizeConfig(cfg)

	return &cfg, nil
}

// normalizeConfig applies effective defaults after parsing so validation sees
// the same values the runtime will use.
func normalizeConfig(cfg FileConfig) {
	for i := range cfg.Validating {
		if cfg.Validating[i].Timeout == 0 {
			cfg.Validating[i].Timeout = DefaultTimeout
		}
	}
	for i := range cfg.Mutating {
		if cfg.Mutating[i].Timeout == 0 {
			cfg.Mutating[i].Timeout = DefaultTimeout
		}
	}
}

// MergeConfigs merges multiple FileConfigs into one.
// Webhooks with the same name are de-duplicated: entries from later configs
// override entries from earlier ones (last-writer-wins per webhook name).
// The resulting Validating and Mutating slices preserve the order in which
// unique names were first seen and apply overrides in place.
func MergeConfigs(configs ...*FileConfig) *FileConfig {
	merged := &FileConfig{}

	validatingIndex := make(map[string]int) // name -> index in merged.Validating
	mutatingIndex := make(map[string]int)   // name -> index in merged.Mutating

	for _, cfg := range configs {
		if cfg == nil {
			continue
		}
		for _, wh := range cfg.Validating {
			if idx, exists := validatingIndex[wh.Name]; exists {
				merged.Validating[idx] = wh
			} else {
				validatingIndex[wh.Name] = len(merged.Validating)
				merged.Validating = append(merged.Validating, wh)
			}
		}
		for _, wh := range cfg.Mutating {
			if idx, exists := mutatingIndex[wh.Name]; exists {
				merged.Mutating[idx] = wh
			} else {
				mutatingIndex[wh.Name] = len(merged.Mutating)
				merged.Mutating = append(merged.Mutating, wh)
			}
		}
	}

	return merged
}

// ValidateConfig validates all webhook configurations in a FileConfig,
// collecting all validation errors before returning.
func ValidateConfig(cfg *FileConfig) error {
	if cfg == nil {
		return nil
	}

	var errs []error
	for i, wh := range cfg.Validating {
		if err := wh.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("validating webhook[%d] %q: %w", i, wh.Name, err))
		}
	}
	for i, wh := range cfg.Mutating {
		if err := wh.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("mutating webhook[%d] %q: %w", i, wh.Name, err))
		}
	}

	return errors.Join(errs...)
}
