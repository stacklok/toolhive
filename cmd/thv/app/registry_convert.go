// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"io"
	"os"

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
		info, err := os.Stat(convertIn)
		if err != nil {
			return fmt.Errorf("failed to stat input file %s: %w", convertIn, err)
		}
		mode := info.Mode().Perm()
		if !convertNoBackup {
			backup := convertIn + ".bak"
			if err := os.WriteFile(backup, original, mode); err != nil {
				return fmt.Errorf("failed to write backup %s: %w", backup, err)
			}
		}
		if err := os.WriteFile(convertIn, output, mode); err != nil {
			return fmt.Errorf("failed to overwrite %s: %w", convertIn, err)
		}
		return nil
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
