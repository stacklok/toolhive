// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

var registryLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the configured registry",
	Long:  `Perform an interactive OAuth login against the configured registry.`,
	RunE:  registryLoginCmdFunc,
}

func init() {
	registryCmd.AddCommand(registryLoginCmd)
}

func registryLoginCmdFunc(cmd *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	secretsProvider, err := auth.NewSecretsProvider(configProvider)
	if err != nil {
		return err
	}
	return auth.Login(cmd.Context(), configProvider, secretsProvider)
}
