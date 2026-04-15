// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

var secretScopeMigrationOnce sync.Once

// CheckAndPerformSecretScopeMigration checks if secret scope migration is needed and performs it.
// It discovers bare system keys and renames them into their __thv_<scope>_ namespace.
// This ensures system-owned secrets are hidden from user-facing secret commands.
func CheckAndPerformSecretScopeMigration() {
	secretScopeMigrationOnce.Do(func() {
		appConfig := config.NewDefaultProvider().GetConfig()
		if appConfig.SecretScopeMigration {
			slog.Debug("secret scope migration already completed, skipping")
			return
		}

		if !appConfig.Secrets.SetupCompleted {
			slog.Debug("secrets not set up, skipping secret scope migration")
			return
		}

		providerType, err := appConfig.Secrets.GetProviderType()
		if err != nil {
			slog.Error("failed to get secrets provider type for migration", "error", err)
			return
		}

		provider, err := secrets.CreateSecretProvider(providerType)
		if err != nil {
			slog.Error("failed to create secrets provider for migration", "error", err)
			return
		}

		migrations, err := secrets.DiscoverMigrations(context.Background(), provider)
		if err != nil {
			slog.Error("failed to discover secret migrations", "error", err)
			return
		}

		if len(migrations) == 0 {
			slog.Debug("no secret scope migrations needed")
		} else {
			slog.Debug("migrating system secrets to scoped namespace", "count", len(migrations))
			if err := secrets.MigrateSystemKeys(context.Background(), provider, migrations); err != nil {
				slog.Error("failed to migrate system secrets", "error", err)
				return
			}
		}

		if err := config.UpdateConfig(func(c *config.Config) error {
			c.SecretScopeMigration = true
			return nil
		}); err != nil {
			slog.Error("failed to update config after secret scope migration", "error", err)
		}
	})
}
