package app

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// AddFormatFlag adds a --format flag to a command with the given format variable and allowed formats.
// If no allowed formats are specified, defaults to "json" and "text".
func AddFormatFlag(cmd *cobra.Command, formatVar *string, allowedFormats ...string) {
	if len(allowedFormats) == 0 {
		allowedFormats = []string{FormatJSON, FormatText}
	}

	description := fmt.Sprintf("Output format (%s)", strings.Join(allowedFormats, ", "))
	cmd.Flags().StringVar(formatVar, "format", FormatText, description)
}

// ValidateFormat returns a PreRunE function that validates the format flag value.
// If no allowed formats are specified, defaults to "json" and "text".
func ValidateFormat(formatVar *string, allowedFormats ...string) func(*cobra.Command, []string) error {
	if len(allowedFormats) == 0 {
		allowedFormats = []string{FormatJSON, FormatText}
	}

	return func(_ *cobra.Command, _ []string) error {
		for _, allowed := range allowedFormats {
			if *formatVar == allowed {
				return nil
			}
		}
		return fmt.Errorf("invalid format %q, must be one of: %s",
			*formatVar, strings.Join(allowedFormats, ", "))
	}
}

// chainPreRunE combines multiple PreRunE functions into a single function.
// They are executed in order, and the first error encountered is returned.
func chainPreRunE(fns ...func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		for _, fn := range fns {
			if fn != nil {
				if err := fn(cmd, args); err != nil {
					return err
				}
			}
		}
		return nil
	}
}
