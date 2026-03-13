// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

var registryLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear cached registry credentials",
	Long:  `Remove cached OAuth tokens for the configured registry.`,
	RunE:  registryLogoutCmdFunc,
}

func init() {
	registryCmd.AddCommand(registryLogoutCmd)
}

func registryLogoutCmdFunc(cmd *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	secretsProvider, err := newSecretsProvider(configProvider)
	if err != nil {
		return err
	}
	return auth.Logout(cmd.Context(), configProvider, secretsProvider)
}
