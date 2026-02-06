// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

// TODO: Remove Hidden flag when skills feature is ready for release.
var skillCmd = &cobra.Command{
	Use:    "skill",
	Short:  "Manage skills",
	Long:   `The skill command provides subcommands to manage skills.`,
	Hidden: true,
}
