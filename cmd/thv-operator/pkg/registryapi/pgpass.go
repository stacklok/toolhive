package registryapi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Skip pgpass secret generation if DatabaseConfig is not specified
	if mcpRegistry.Spec.DatabaseConfig == nil {
		ctxLogger.V(1).Info("No databaseConfig specified, skipping pgpass secret generation")
		return nil
	}

	dbConfig := mcpRegistry.Spec.DatabaseConfig

	// Get default values for database configuration
	host := dbConfig.Host
	if host == "" {
		host = "postgres"
	}

	port := dbConfig.Port
	if port == 0 {
		port = 5432
	}

	database := dbConfig.Database
	if database == "" {
		database = "registry"
	}

	appUser := dbConfig.User
	if appUser == "" {
		appUser = "db_app"
	}

	migrationUser := dbConfig.MigrationUser
	if migrationUser == "" {
		migrationUser = "db_migrator"
	}

	// Read app user password from secret
	appUserPassword, err := m.kubeClient.GetSecretValue(ctx, mcpRegistry.Namespace, dbConfig.DBAppUserPasswordSecretRef)

	if err != nil {
		ctxLogger.Error(err, "Failed to read app user password from secret",
			"secretName", dbConfig.DBAppUserPasswordSecretRef.Name,
			"secretKey", dbConfig.DBAppUserPasswordSecretRef.Key)
		return fmt.Errorf("failed to read app user password: %w", err)
	}

	// Read migration user password from secret
	migrationUserPassword, err := m.kubeClient.GetSecretValue(ctx, mcpRegistry.Namespace, dbConfig.DBMigrationUserPasswordSecretRef)
	if err != nil {
		ctxLogger.Error(err, "Failed to read migration user password from secret",
			"secretName", dbConfig.DBMigrationUserPasswordSecretRef.Name,
			"secretKey", dbConfig.DBMigrationUserPasswordSecretRef.Key)
		return fmt.Errorf("failed to read migration user password: %w", err)
	}

	// Build pgpass file content
	// Format: hostname:port:database:username:password
	pgpassContent := fmt.Sprintf("%s:%d:%s:%s:%s\n%s:%d:%s:%s:%s\n",
		host, port, database, appUser, appUserPassword,
		host, port, database, migrationUser, migrationUserPassword,
	)

	// Create the pgpass secret
	secretName := mcpRegistry.GetPGPassSecretName()
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
	if _, err := m.kubeClient.UpsertSecretWithOwnerReference(ctx, secret, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to upsert pgpass secret")
		return fmt.Errorf("failed to upsert pgpass secret: %w", err)
	}

	return nil
}
