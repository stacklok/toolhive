package registryapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes"
)

func TestEnsurePGPassSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mcpRegistry   *mcpv1alpha1.MCPRegistry
		existingObjs  []client.Object
		setupClient   func(*testing.T, []client.Object) client.Client
		expectedError string
		validate      func(*testing.T, client.Client, *mcpv1alpha1.MCPRegistry)
	}{
		{
			name:         "successfully creates pgpass secret with correct content format",
			mcpRegistry:  baseMCPRegistry(t),
			existingObjs: standardPasswordSecrets(),
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			//nolint:thelper // We want to see these lines in the test output
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				secret := getPGPassSecret(t, c, mcpRegistry)
				secretName := mcpRegistry.BuildPGPassSecretName()

				// Verify secret metadata
				assert.Equal(t, secretName, secret.Name)
				assert.Equal(t, "test-namespace", secret.Namespace)
				assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)

				// Verify labels
				assert.Equal(t, secretName, secret.Labels["app.kubernetes.io/name"])
				assert.Equal(t, "registry-api", secret.Labels["app.kubernetes.io/component"])
				assert.Equal(t, "toolhive-operator", secret.Labels["app.kubernetes.io/managed-by"])
				assert.Equal(t, "test-registry", secret.Labels["toolhive.stacklok.io/registry-name"])

				// Verify owner reference
				require.Len(t, secret.OwnerReferences, 1)
				assert.Equal(t, mcpRegistry.Name, secret.OwnerReferences[0].Name)
				assert.Equal(t, "MCPRegistry", secret.OwnerReferences[0].Kind)

				// Verify pgpass content format
				pgpassContent := string(secret.Data[".pgpass"])
				expectedContent := "postgres.example.com:5432:test_db:app_user:app_password_123\n" +
					"postgres.example.com:5432:test_db:migration_user:migration_password_456\n"
				assert.Equal(t, expectedContent, pgpassContent, "pgpass content should have correct format")
			},
		},
		{
			name:         "uses default port 5432 when port is 0",
			mcpRegistry:  baseMCPRegistry(t, withPort(0)),
			existingObjs: standardPasswordSecrets(),
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			//nolint:thelper // We want to see these lines in the test output
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				secret := getPGPassSecret(t, c, mcpRegistry)
				pgpassContent := string(secret.Data[".pgpass"])
				assert.Contains(t, pgpassContent, ":5432:", "Should use default port 5432 when port is 0")
			},
		},
		{
			name:         "uses custom port when specified",
			mcpRegistry:  baseMCPRegistry(t, withPort(9999)),
			existingObjs: standardPasswordSecrets(),
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			//nolint:thelper // We want to see these lines in the test output
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				secret := getPGPassSecret(t, c, mcpRegistry)
				pgpassContent := string(secret.Data[".pgpass"])
				assert.Contains(t, pgpassContent, ":9999:", "Should use custom port 9999")
			},
		},
		{
			name:        "returns error when app user password secret read fails",
			mcpRegistry: baseMCPRegistry(t),
			existingObjs: []client.Object{
				// Only migration secret exists, app secret is missing
				createPasswordSecret("migration-secret", "password", "migration_password_456"),
			},
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			expectedError: "failed to read app user password from secret app-secret",
		},
		{
			name:        "returns error when app user password secret key does not exist",
			mcpRegistry: baseMCPRegistry(t),
			existingObjs: []client.Object{
				// App secret exists but with wrong key
				createPasswordSecret("app-secret", "wrong-key", "app_password_123"),
				createPasswordSecret("migration-secret", "password", "migration_password_456"),
			},
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			expectedError: "failed to read app user password from secret app-secret",
		},
		{
			name:        "returns error when migration user password secret read fails",
			mcpRegistry: baseMCPRegistry(t),
			existingObjs: []client.Object{
				// Only app secret exists, migration secret is missing
				createPasswordSecret("app-secret", "password", "app_password_123"),
			},
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			expectedError: "failed to read migration user password from secret migration-secret",
		},
		{
			name:         "returns error when upsert fails",
			mcpRegistry:  baseMCPRegistry(t),
			existingObjs: standardPasswordSecrets(),
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				// Create a client that fails when creating the pgpass secret
				return fake.NewClientBuilder().
					WithScheme(createTestScheme()).
					WithObjects(objs...).
					WithInterceptorFuncs(interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if secret, ok := obj.(*corev1.Secret); ok {
								// Fail only for the pgpass secret
								if secret.Name == "test-registry-db-pgpass" {
									return errors.New("simulated create failure: permission denied")
								}
							}
							return c.Create(ctx, obj, opts...)
						},
					}).Build()
			},
			expectedError: "failed to upsert pgpass secret test-registry-db-pgpass",
		},
		{
			name: "verifies pgpass content format with special characters in password",
			mcpRegistry: baseMCPRegistry(t,
				withHost("db.prod.example.com"),
				withDatabase("prod_registry"),
				withUser("app_prod"),
				withMigrationUser("migrator_prod"),
			),
			existingObjs: []client.Object{
				// Passwords with special characters
				createPasswordSecret("app-secret", "password", "p@ssw0rd!#$%"),
				createPasswordSecret("migration-secret", "password", "migr@t0r&*()_+"),
			},
			setupClient: func(t *testing.T, objs []client.Object) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).WithObjects(objs...).Build()
			},
			//nolint:thelper // We want to see these lines in the test output
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				secret := getPGPassSecret(t, c, mcpRegistry)

				// Verify the exact pgpass format: hostname:port:database:username:password
				pgpassContent := string(secret.Data[".pgpass"])
				expectedLine1 := "db.prod.example.com:5432:prod_registry:app_prod:p@ssw0rd!#$%\n"
				expectedLine2 := "db.prod.example.com:5432:prod_registry:migrator_prod:migr@t0r&*()_+\n"
				expectedContent := expectedLine1 + expectedLine2

				assert.Equal(t, expectedContent, pgpassContent,
					"pgpass content should have correct format with both user entries and special characters")

				// Verify it contains exactly two lines
				lines := []byte(pgpassContent)
				lineCount := 0
				for _, b := range lines {
					if b == '\n' {
						lineCount++
					}
				}
				assert.Equal(t, 2, lineCount, "pgpass content should have exactly 2 lines")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with existing objects
			fakeClient := tt.setupClient(t, tt.existingObjs)

			// Create scheme
			scheme := createTestScheme()

			// Create manager
			m := &manager{
				client:     fakeClient,
				scheme:     scheme,
				kubeHelper: kubernetes.NewClient(fakeClient, scheme),
			}

			// Execute
			err := m.ensurePGPassSecret(context.Background(), tt.mcpRegistry)

			// Verify
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, fakeClient, tt.mcpRegistry)
				}
			}
		})
	}
}

// baseMCPRegistry creates a base MCPRegistry for testing with sensible defaults.
// Use functional options to customize specific fields.
func baseMCPRegistry(t *testing.T, opts ...func(*mcpv1alpha1.MCPRegistry)) *mcpv1alpha1.MCPRegistry {
	t.Helper()
	reg := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
			UID:       types.UID("test-uid"),
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			DatabaseConfig: &mcpv1alpha1.MCPRegistryDatabaseConfig{
				Host:          "postgres.example.com",
				Port:          5432,
				Database:      "test_db",
				User:          "app_user",
				MigrationUser: "migration_user",
				DBAppUserPasswordSecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"},
					Key:                  "password",
				},
				DBMigrationUserPasswordSecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "migration-secret"},
					Key:                  "password",
				},
			},
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{Name: "default", Format: mcpv1alpha1.RegistryFormatToolHive},
			},
		},
	}
	for _, opt := range opts {
		opt(reg)
	}
	return reg
}

func withPort(port int) func(*mcpv1alpha1.MCPRegistry) {
	return func(r *mcpv1alpha1.MCPRegistry) { r.Spec.DatabaseConfig.Port = port }
}

func withHost(host string) func(*mcpv1alpha1.MCPRegistry) {
	return func(r *mcpv1alpha1.MCPRegistry) { r.Spec.DatabaseConfig.Host = host }
}

func withDatabase(db string) func(*mcpv1alpha1.MCPRegistry) {
	return func(r *mcpv1alpha1.MCPRegistry) { r.Spec.DatabaseConfig.Database = db }
}

func withUser(user string) func(*mcpv1alpha1.MCPRegistry) {
	return func(r *mcpv1alpha1.MCPRegistry) { r.Spec.DatabaseConfig.User = user }
}

func withMigrationUser(user string) func(*mcpv1alpha1.MCPRegistry) {
	return func(r *mcpv1alpha1.MCPRegistry) { r.Spec.DatabaseConfig.MigrationUser = user }
}

// standardPasswordSecrets creates the standard app and migration password secrets for testing.
func standardPasswordSecrets() []client.Object {
	return []client.Object{
		createPasswordSecret("app-secret", "password", "app_password_123"),
		createPasswordSecret("migration-secret", "password", "migration_password_456"),
	}
}

// createPasswordSecret creates a secret with a single key-value pair.
func createPasswordSecret(name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			key: []byte(value),
		},
	}
}

// getPGPassSecret retrieves and returns the pgpass secret for the given MCPRegistry.
func getPGPassSecret(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      mcpRegistry.BuildPGPassSecretName(),
		Namespace: mcpRegistry.Namespace,
	}, secret)
	require.NoError(t, err, "pgpass secret should have been created")
	return secret
}
