// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/registry"
)

var (
	convertIn       string
	convertOut      string
	convertInPlace  bool
	convertNoBackup bool
)

var registryConvertCmd = &cobra.Command{
	Use:   "convert",
	Short: "Convert a legacy registry file to the upstream MCP format",
	Long: `Convert a legacy ToolHive registry JSON file to the upstream MCP registry format.

Reads from --in (or stdin) and writes to --out (or stdout). Use --in-place to
overwrite the input file; a backup is written to <path>.bak unless --no-backup
is set.`,
	RunE:    registryConvertCmdFunc,
	PreRunE: registryConvertPreRunE,
}

func init() {
	registryCmd.AddCommand(registryConvertCmd)
	registryConvertCmd.Flags().StringVar(&convertIn, "in", "", "Input file (default: stdin)")
	registryConvertCmd.Flags().StringVar(&convertOut, "out", "", "Output file (default: stdout)")
	registryConvertCmd.Flags().BoolVar(&convertInPlace, "in-place", false,
		"Overwrite the input file (writes a .bak backup unless --no-backup is set)")
	registryConvertCmd.Flags().BoolVar(&convertNoBackup, "no-backup", false,
		"Do not write a .bak backup when using --in-place")
}

func registryConvertPreRunE(_ *cobra.Command, _ []string) error {
	if convertInPlace && convertIn == "" {
		return errors.New("--in-place requires --in")
	}
	if convertInPlace && convertOut != "" {
		return errors.New("--out cannot be combined with --in-place")
	}
	if convertNoBackup && !convertInPlace {
		return errors.New("--no-backup only applies with --in-place")
	}
	return nil
}

func registryConvertCmdFunc(cmd *cobra.Command, _ []string) error {
	input, err := readConvertInput()
	if err != nil {
		return err
	}

	output, err := registry.ConvertJSON(input)
	if errors.Is(err, registry.ErrAlreadyUpstream) {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Input is already in upstream format; nothing to do.")
		return nil
	}
	if err != nil {
		return err
	}

	return writeConvertOutput(input, output)
}

func readConvertInput() ([]byte, error) {
	if convertIn == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read input from stdin: %w", err)
		}
		return data, nil
	}
	// #nosec G304: convertIn is a user-supplied path, intentional read.
	data, err := os.ReadFile(convertIn)
	if err != nil {
		return nil, fmt.Errorf("failed to read input file %s: %w", convertIn, err)
	}
	return data, nil
}

func writeConvertOutput(original, output []byte) error {
	switch {
	case convertInPlace:
		return writeInPlace(convertIn, original, output, !convertNoBackup)
	case convertOut != "":
		if err := os.WriteFile(convertOut, output, 0o600); err != nil {
			return fmt.Errorf("failed to write output file %s: %w", convertOut, err)
		}
		return nil
	default:
		if _, err := os.Stdout.Write(output); err != nil {
			return fmt.Errorf("failed to write output to stdout: %w", err)
		}
		return nil
	}
}

// writeInPlace overwrites path with output atomically (write a sibling temp
// file, fsync it, then rename) so a crash mid-write can't corrupt the input.
// When backup is true, the original bytes are written to <path>.bak first; the
// helper refuses to clobber an existing backup so a previous good copy is
// never silently destroyed.
func writeInPlace(path string, original, output []byte, backup bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat input file %s: %w", path, err)
	}
	mode := info.Mode().Perm()

	if backup {
		backupPath := path + ".bak"
		switch _, err := os.Stat(backupPath); {
		case err == nil:
			return fmt.Errorf("backup file %s already exists; remove it or pass --no-backup to skip the backup", backupPath)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("failed to check backup path %s: %w", backupPath, err)
		}
		if err := os.WriteFile(backupPath, original, mode); err != nil {
			return fmt.Errorf("failed to write backup %s: %w", backupPath, err)
		}
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(output); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to sync temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		cleanup()
		return fmt.Errorf("failed to set permissions on temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to overwrite %s: %w", path, err)
	}
	return nil
}
