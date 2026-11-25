// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// ConfigureEnvFile writes the client's LLM env-file entries to the nominated
// dotenv file (e.g. ~/.gemini/.env). Existing entries for other keys are
// preserved; entries managed by thv are added or updated.
//
// Returns the absolute path of the written file, or ("", nil) when the client
// has no env-file entries configured.
func (cm *ClientManager) ConfigureEnvFile(clientType ClientApp, cfg llmgateway.ApplyConfig) (string, error) {
	appCfg := cm.lookupClientAppConfig(clientType)
	if appCfg == nil || len(appCfg.LLMEnvFileKeys) == 0 {
		return "", nil
	}

	path := cm.buildEnvFilePath(appCfg)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating directory for %s: %w", path, err)
	}

	err := fileutils.WithFileLock(path, func() error {
		content, err := readOrInitFile(path, nil)
		if err != nil {
			return err
		}

		entries := parseEnvFile(content)

		for _, spec := range appCfg.LLMEnvFileKeys {
			val, err := envFileValueForSpec(spec, cfg)
			if err != nil {
				return err
			}
			entries = setEnvEntry(entries, spec.Name, val)
		}

		return fileutils.AtomicWriteFile(path, marshalEnvFile(entries), 0o600)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// RevertEnvFile removes the entries that ConfigureEnvFile wrote for clientType
// from the nominated dotenv file. Other entries in the file are preserved.
// If the file does not exist the call is a no-op.
func (cm *ClientManager) RevertEnvFile(clientType ClientApp, envFilePath string) error {
	appCfg := cm.lookupClientAppConfig(clientType)
	if appCfg == nil || len(appCfg.LLMEnvFileKeys) == 0 {
		return nil
	}
	if envFilePath == "" {
		return nil
	}

	if _, err := os.Stat(envFilePath); os.IsNotExist(err) {
		return nil
	}

	return fileutils.WithFileLock(envFilePath, func() error {
		content, err := os.ReadFile(envFilePath) // #nosec G304 -- path is a known tool config file
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", envFilePath, err)
		}

		entries := parseEnvFile(content)
		for _, spec := range appCfg.LLMEnvFileKeys {
			entries = removeEnvEntry(entries, spec.Name)
		}

		return fileutils.AtomicWriteFile(envFilePath, marshalEnvFile(entries), 0o600)
	})
}

// buildEnvFilePath constructs the absolute path to the .env file for the
// given client, using the same home-dir convention as LLM settings paths.
func (cm *ClientManager) buildEnvFilePath(cfg *clientAppConfig) string {
	return buildConfigFilePath(
		cfg.LLMEnvFileName,
		cfg.LLMEnvFileRelPath,
		nil, // no platform prefix for env files
		[]string{cm.homeDir},
	)
}

// HasEnvFileSupport reports whether clientType has .env file entries to manage.
func (cm *ClientManager) HasEnvFileSupport(clientType ClientApp) bool {
	cfg := cm.lookupClientAppConfig(clientType)
	return cfg != nil && len(cfg.LLMEnvFileKeys) > 0
}

// envFileValueForSpec resolves the value to write for a single LLMEnvFileKeySpec.
// Exactly one of spec.Literal or spec.ValueField must be set.
func envFileValueForSpec(spec LLMEnvFileKeySpec, cfg llmgateway.ApplyConfig) (string, error) {
	if spec.Literal != "" && spec.ValueField != "" {
		return "", fmt.Errorf("LLMEnvFileKeySpec for %q has both Literal and ValueField set; exactly one must be set", spec.Name)
	}
	if spec.Literal == "" && spec.ValueField == "" {
		return "", fmt.Errorf("LLMEnvFileKeySpec for %q has neither Literal nor ValueField set; exactly one must be set", spec.Name)
	}
	if spec.Literal != "" {
		return spec.Literal, nil
	}
	if v, ok := resolveApplyConfigField(spec.ValueField, cfg); ok {
		return v, nil
	}
	return "", fmt.Errorf("unknown ValueField %q in LLMEnvFileKeySpec for %q; use Literal for constant values",
		spec.ValueField, spec.Name)
}

// envEntry is an ordered KEY=value entry in a dotenv file. Comments and blank
// lines are stored verbatim so that round-tripping preserves formatting.
type envEntry struct {
	raw   string // raw line for comments / blank lines (when key == "")
	key   string // non-empty for KEY=value lines
	value string
}

// parseEnvFile parses the content of a dotenv file into an ordered list of
// entries. Comments, blank lines, and malformed lines (no "=") are stored as
// raw entries so they survive a round-trip through marshalEnvFile.
func parseEnvFile(content []byte) []envEntry {
	var entries []envEntry
	// bytes.SplitSeq handles arbitrarily long lines since content is already in
	// memory; bufio.Scanner would error on lines longer than its 64 KiB limit.
	// Trim trailing newlines first so an empty or newline-only file produces no
	// entries, and to avoid a spurious empty token after the final newline.
	trimmed := bytes.TrimRight(content, "\r\n")
	if len(trimmed) == 0 {
		return entries
	}
	for rawLine := range bytes.SplitSeq(trimmed, []byte("\n")) {
		// Strip \r to handle CRLF files.
		line := string(bytes.TrimRight(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			entries = append(entries, envEntry{raw: line})
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			// Malformed line — preserve as raw.
			entries = append(entries, envEntry{raw: line})
			continue
		}
		entries = append(entries, envEntry{key: key, value: value})
	}
	return entries
}

// marshalEnvFile serialises entries back to a dotenv-formatted byte slice.
// A trailing newline is always appended.
func marshalEnvFile(entries []envEntry) []byte {
	var b bytes.Buffer
	for _, e := range entries {
		if e.key == "" {
			b.WriteString(e.raw)
		} else {
			b.WriteString(e.key)
			b.WriteByte('=')
			b.WriteString(e.value)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// setEnvEntry updates the value for key in entries, or appends a new entry if
// key is not present. Returns the (possibly grown) slice.
func setEnvEntry(entries []envEntry, key, value string) []envEntry {
	for i, e := range entries {
		if e.key == key {
			entries[i].value = value
			return entries
		}
	}
	return append(entries, envEntry{key: key, value: value})
}

// removeEnvEntry removes the entry with the given key from entries (if any).
func removeEnvEntry(entries []envEntry, key string) []envEntry {
	result := entries[:0:len(entries)]
	for _, e := range entries {
		if e.key != key {
			result = append(result, e)
		}
	}
	return result
}
