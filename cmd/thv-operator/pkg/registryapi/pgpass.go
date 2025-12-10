package registryapi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// pgpassSecretKey is the key name for the pgpass file content in the generated secret
	pgpassSecretKey = ".pgpass"
)

// PGPassSecretManager manages the creation and updates of pgpass file secrets
// for PostgreSQL authentication in the registry API
type PGPassSecretManager interface {
	// EnsurePGPassSecret creates or updates the pgpass secret for the given MCPRegistry
	EnsurePGPassSecret(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) error
}

// pgpassSecretManager implements the PGPassSecretManager interface
type pgpassSecretManager struct {
	client client.Client
}

// NewPGPassSecretManager creates a new pgpass secret manager
func NewPGPassSecretManager(k8sClient client.Client) PGPassSecretManager {
	return &pgpassSecretManager{
		client: k8sClient,
	}
}

// GetPGPassSecretKey returns the key name used for the pgpass file content in secrets
func GetPGPassSecretKey() string {
	return pgpassSecretKey
}

// EnsurePGPassSecret creates or updates the pgpass secret for the given MCPRegistry.
// It reads passwords from the referenced secrets and generates a pgpass file with
// entries for both the application user and migration user.
func (m *pgpassSecretManager) EnsurePGPassSecret(
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
	appUserPassword, err := m.readSecretValue(ctx, mcpRegistry.Namespace, dbConfig.DBAppUserPasswordSecretRef)
	if err != nil {
		ctxLogger.Error(err, "Failed to read app user password from secret",
			"secretName", dbConfig.DBAppUserPasswordSecretRef.Name,
			"secretKey", dbConfig.DBAppUserPasswordSecretRef.Key)
		return fmt.Errorf("failed to read app user password: %w", err)
	}

	// Read migration user password from secret
	migrationUserPassword, err := m.readSecretValue(ctx, mcpRegistry.Namespace, dbConfig.DBMigrationUserPasswordSecretRef)
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

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, secret, m.client.Scheme()); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for pgpass secret")
		return fmt.Errorf("failed to set controller reference for pgpass secret: %w", err)
	}

	// Check if secret already exists
	existing := &corev1.Secret{}
	err = m.client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			// Secret doesn't exist, create it
			ctxLogger.Info("Creating pgpass secret", "secretName", secretName)
			if err := m.client.Create(ctx, secret); err != nil {
				ctxLogger.Error(err, "Failed to create pgpass secret")
				return fmt.Errorf("failed to create pgpass secret %s: %w", secretName, err)
			}
			ctxLogger.Info("Successfully created pgpass secret", "secretName", secretName)
			return nil
		}
		// Unexpected error
		ctxLogger.Error(err, "Failed to get pgpass secret")
		return fmt.Errorf("failed to get pgpass secret %s: %w", secretName, err)
	}

	// Secret exists, update it if the content has changed
	if string(existing.Data[pgpassSecretKey]) != pgpassContent {
		ctxLogger.Info("Updating pgpass secret", "secretName", secretName)
		existing.Data = secret.Data
		existing.Labels = secret.Labels
		if err := m.client.Update(ctx, existing); err != nil {
			ctxLogger.Error(err, "Failed to update pgpass secret")
			return fmt.Errorf("failed to update pgpass secret %s: %w", secretName, err)
		}
		ctxLogger.Info("Successfully updated pgpass secret", "secretName", secretName)
	} else {
		ctxLogger.V(1).Info("Pgpass secret is up to date", "secretName", secretName)
	}

	return nil
}

// readSecretValue reads a value from a Kubernetes secret using a SecretKeySelector
func (m *pgpassSecretManager) readSecretValue(
	ctx context.Context,
	namespace string,
	secretRef corev1.SecretKeySelector,
) (string, error) {
	secret := &corev1.Secret{}
	err := m.client.Get(ctx, client.ObjectKey{
		Name:      secretRef.Name,
		Namespace: namespace,
	}, secret)

	if err != nil {
		return "", fmt.Errorf("failed to get secret %s: %w", secretRef.Name, err)
	}

	value, exists := secret.Data[secretRef.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s", secretRef.Key, secretRef.Name)
	}

	return string(value), nil
}
