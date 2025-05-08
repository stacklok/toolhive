package secrets_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestNewOnePasswordManager(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		// Make sure token is not set
		os.Unsetenv("OP_SERVICE_ACCOUNT_TOKEN")

		manager, err := secrets.NewOnePasswordManager()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "OP_SERVICE_ACCOUNT_TOKEN is not set")
		assert.Nil(t, manager)
	})
}

func TestOnePasswordManager_GetSecret(t *testing.T) {
	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock service
	mockSecretsService := mocks.NewMockOPSecretsService(ctrl)

	// Create manager with our mock service
	manager := secrets.NewOnePasswordManagerWithService(mockSecretsService)

	tests := []struct {
		name        string
		path        string
		setupMock   func()
		wantSecret  string
		wantErr     bool
		errContains string
	}{
		{
			name:        "invalid path format",
			path:        "invalid-path",
			setupMock:   func() {},
			wantSecret:  "",
			wantErr:     true,
			errContains: "invalid secret path",
		},
		{
			name: "valid path format with success",
			path: "op://vault/item/field",
			setupMock: func() {
				mockSecretsService.EXPECT().
					Resolve(gomock.Any(), "op://vault/item/field").
					Return("test-secret-value", nil)
			},
			wantSecret:  "test-secret-value",
			wantErr:     false,
			errContains: "",
		},
		{
			name: "valid path format with error",
			path: "op://vault/item/field",
			setupMock: func() {
				mockSecretsService.EXPECT().
					Resolve(gomock.Any(), "op://vault/item/field").
					Return("", fmt.Errorf("secret not found"))
			},
			wantSecret:  "",
			wantErr:     true,
			errContains: "error resolving secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup expectations
			tt.setupMock()

			// Execute test
			secret, err := manager.GetSecret(tt.path)

			// Assert results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantSecret, secret)
			}
		})
	}
}

func TestOnePasswordManager_UnsupportedOperations(t *testing.T) {
	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock service
	mockSecretsService := mocks.NewMockOPSecretsService(ctrl)
	manager := secrets.NewOnePasswordManagerWithService(mockSecretsService)

	t.Run("SetSecret", func(t *testing.T) {
		err := manager.SetSecret("test", "value")
		assert.NoError(t, err, "SetSecret should return nil as it's not supported")
	})

	t.Run("DeleteSecret", func(t *testing.T) {
		err := manager.DeleteSecret("test")
		assert.NoError(t, err, "DeleteSecret should return nil as it's not supported")
	})

	t.Run("ListSecrets", func(t *testing.T) {
		secrets, err := manager.ListSecrets()
		assert.NoError(t, err, "ListSecrets should return nil error as it's not supported")
		assert.Nil(t, secrets, "ListSecrets should return nil slice as it's not supported")
	})

	t.Run("Cleanup", func(t *testing.T) {
		err := manager.Cleanup()
		assert.NoError(t, err, "Cleanup should return nil as it's not supported")
	})
}
