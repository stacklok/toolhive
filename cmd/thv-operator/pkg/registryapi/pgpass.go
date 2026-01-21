// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// pgpassSecretKey is the key name for the pgpass file content in the generated secret
	pgpassSecretKey = ".pgpass"
)

// GetPGPassSecretKey returns the key name used for the pgpass file content in secrets
func GetPGPassSecretKey() string {
	return pgpassSecretKey
}

// ensurePGPassSecret creates or updates the pgpass secret for the given MCPRegistry.
// It reads passwords from the referenced secrets and generates a pgpass file with
// entries for both the application user and migration user.
func (m *manager) ensurePGPassSecret(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) error {
	dbConfig := mcpRegistry.GetDatabaseConfig()

	// Read app user password from secret
	appUserPassword, err := m.kubeHelper.Secrets.GetValue(ctx, mcpRegistry.Namespace, dbConfig.DBAppUserPasswordSecretRef)
	if err != nil {
		return fmt.Errorf("failed to read app user password from secret %s: %w",
			dbConfig.DBAppUserPasswordSecretRef.Name, err)
	}

	// Read migration user password from secret
	migrationUserPassword, err := m.kubeHelper.Secrets.GetValue(
		ctx, mcpRegistry.Namespace, dbConfig.DBMigrationUserPasswordSecretRef)
	if err != nil {
		return fmt.Errorf("failed to read migration user password from secret %s: %w",
			dbConfig.DBMigrationUserPasswordSecretRef.Name, err)
	}

	// Build pgpass file content
	// Format: hostname:port:database:username:password
	pgpassContent := fmt.Sprintf("%s:%d:%s:%s:%s\n%s:%d:%s:%s:%s\n",
		dbConfig.Host, mcpRegistry.GetDatabasePort(), dbConfig.Database, dbConfig.User, appUserPassword,
		dbConfig.Host, mcpRegistry.GetDatabasePort(), dbConfig.Database, dbConfig.MigrationUser, migrationUserPassword,
	)

	// Create the pgpass secret
	secretName := mcpRegistry.BuildPGPassSecretName()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mcpRegistry.Namespace,
			Labels:    labelsForRegistryAPI(mcpRegistry, secretName),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			pgpassSecretKey: []byte(pgpassContent),
		},
	}

	// Upsert the secret with owner reference for garbage collection
	if _, err := m.kubeHelper.Secrets.UpsertWithOwnerReference(ctx, secret, mcpRegistry); err != nil {
		return fmt.Errorf("failed to upsert pgpass secret %s: %w", secretName, err)
	}

	return nil
}
