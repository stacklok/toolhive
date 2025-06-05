package secrets_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/1password/onepassword-sdk-go"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	cm "github.com/stacklok/toolhive/pkg/secrets/clients/mocks"
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
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		setupMock   func(mockClient *cm.MockOnePasswordClient)
		wantSecret  string
		wantErr     bool
		errContains string
	}{
		{
			name:        "invalid path format",
			path:        "invalid-path",
			setupMock:   func(*cm.MockOnePasswordClient) {},
			wantSecret:  "",
			wantErr:     true,
			errContains: "invalid secret path",
		},
		{
			name: "valid path format with success",
			path: "op://vault/item/field",
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
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
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
					Resolve(gomock.Any(), "op://vault/item/field").
					Return("", fmt.Errorf("secret not found"))
			},
			wantSecret:  "",
			wantErr:     true,
			errContains: "error resolving secret",
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel execution
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Enable parallel execution
			ctx := t.Context()

			// Create a new mock controller for each test case
			ctrl := gomock.NewController(t)
			t.Cleanup(func() { ctrl.Finish() })

			// Create a new mock client for each test case
			mockClient := cm.NewMockOnePasswordClient(ctrl)

			// Create a new manager with the mock client
			manager := secrets.NewOnePasswordManagerWithClient(mockClient, "")

			// Setup expectations
			tt.setupMock(mockClient)

			// Execute test
			secret, err := manager.GetSecret(ctx, tt.path)

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

func TestOnePasswordManager_ListSecrets(t *testing.T) {
	t.Parallel()

	// Define test vault name
	const testVaultName = "test-vault"

	tests := []struct {
		name        string
		setupMock   func(mockClient *cm.MockOnePasswordClient)
		wantSecrets []secrets.SecretDescription
		wantErr     bool
		errContains string
	}{
		{
			name: "successful listing",
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
					List(gomock.Any(), testVaultName, gomock.Any()).
					Return([]onepassword.ItemOverview{
						{ID: "item1", Title: "Secret 1", VaultID: "vault1"},
						{ID: "item2", Title: "Secret 2", VaultID: "vault1"},
						{ID: "item3", Title: "Secret 3", VaultID: "vault2"},
					}, nil)
			},
			wantSecrets: []secrets.SecretDescription{
				{Key: "op://vault1/item1", Description: "Secret 1"},
				{Key: "op://vault1/item2", Description: "Secret 2"},
				{Key: "op://vault2/item3", Description: "Secret 3"},
			},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "empty list",
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
					List(gomock.Any(), testVaultName, gomock.Any()).
					Return([]onepassword.ItemOverview{}, nil)
			},
			wantSecrets: []secrets.SecretDescription{},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "list with some invalid items",
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
					List(gomock.Any(), testVaultName, gomock.Any()).
					Return([]onepassword.ItemOverview{
						{ID: "item1", Title: "Secret 1", VaultID: "vault1"},
						{ID: "", Title: "Invalid Secret", VaultID: "vault1"}, // Missing ID
						{ID: "item3", Title: "", VaultID: "vault2"},          // Missing Title
						{ID: "item4", Title: "Secret 4", VaultID: "vault2"},
					}, nil)
			},
			wantSecrets: []secrets.SecretDescription{
				{Key: "op://vault1/item1", Description: "Secret 1"},
				{Key: "op://vault2/item4", Description: "Secret 4"},
			},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "error from client",
			setupMock: func(mockClient *cm.MockOnePasswordClient) {
				mockClient.EXPECT().
					List(gomock.Any(), testVaultName, gomock.Any()).
					Return(nil, fmt.Errorf("connection error"))
			},
			wantSecrets: nil,
			wantErr:     true,
			errContains: "error retrieving secrets from 1password API",
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel execution
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Enable parallel execution
			ctx := t.Context()

			// Create a new mock controller for each test case
			ctrl := gomock.NewController(t)
			t.Cleanup(func() { ctrl.Finish() })

			// Create a new mock client for each test case
			mockClient := cm.NewMockOnePasswordClient(ctrl)

			// Create a new manager with the mock client and test vault name
			manager := secrets.NewOnePasswordManagerWithClient(mockClient, testVaultName)

			// Setup expectations
			tt.setupMock(mockClient)

			// Execute test
			secrets, err := manager.ListSecrets(ctx)

			// Assert results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.wantSecrets, secrets)
			}
		})
	}
}

func TestOnePasswordManager_UnsupportedOperations(t *testing.T) {
	t.Parallel()
	// Create a mock controller
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	// Create mock client
	mockClient := cm.NewMockOnePasswordClient(ctrl)
	manager := secrets.NewOnePasswordManagerWithClient(mockClient, "")

	t.Run("SetSecret", func(t *testing.T) {
		t.Parallel()
		err := manager.SetSecret(t.Context(), "test", "value")
		assert.Error(t, err, secrets.Err1PasswordReadOnly)
	})

	t.Run("DeleteSecret", func(t *testing.T) {
		t.Parallel()
		err := manager.DeleteSecret(t.Context(), "test")
		assert.Error(t, err, secrets.Err1PasswordReadOnly)
	})

	t.Run("Cleanup", func(t *testing.T) {
		t.Parallel()
		err := manager.Cleanup()
		assert.NoError(t, err, "Cleanup should return nil as it's not supported")
	})
}
