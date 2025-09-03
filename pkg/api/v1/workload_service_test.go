package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apitypes "github.com/stacklok/toolhive/pkg/api/v1/types"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestWorkloadService_resolveClientSecret(t *testing.T) {
	t.Parallel()

	t.Run("with secret parameter", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
		mockSecretsProvider.EXPECT().
			GetSecret(gomock.Any(), "secret-name").
			Return("secret-value", nil)

		service := &WorkloadService{
			secretsProvider: mockSecretsProvider,
		}

		secretParam := &secrets.SecretParameter{Name: "secret-name"}

		result, err := service.resolveClientSecret(context.Background(), secretParam)

		require.NoError(t, err)
		assert.Equal(t, "secret-value", result)
	})

	t.Run("without secret parameter", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		result, err := service.resolveClientSecret(context.Background(), nil)

		require.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("secret provider error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
		mockSecretsProvider.EXPECT().
			GetSecret(gomock.Any(), "non-existent-secret").
			Return("", errors.New("secret not found"))

		service := &WorkloadService{
			secretsProvider: mockSecretsProvider,
		}

		secretParam := &secrets.SecretParameter{Name: "non-existent-secret"}

		result, err := service.resolveClientSecret(context.Background(), secretParam)

		assert.Error(t, err)
		assert.Equal(t, "", result)
		assert.Contains(t, err.Error(), "failed to resolve OAuth client secret")
	})
}

func TestWorkloadService_createRequestToRemoteAuthConfig(t *testing.T) {
	t.Parallel()

	t.Run("with OAuth config", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
		mockSecretsProvider.EXPECT().
			GetSecret(gomock.Any(), "secret-name").
			Return("secret-value", nil)

		service := &WorkloadService{
			secretsProvider: mockSecretsProvider,
		}

		req := &apitypes.CreateRequest{
			UpdateRequest: apitypes.UpdateRequest{
				OAuthConfig: &apitypes.RemoteOAuthConfig{
					ClientID:     "client-id",
					ClientSecret: &secrets.SecretParameter{Name: "secret-name"},
					Scopes:       []string{"read", "write"},
					Issuer:       "https://oauth.example.com",
					AuthorizeURL: "https://oauth.example.com/auth",
					TokenURL:     "https://oauth.example.com/token",
					OAuthParams:  map[string]string{"custom": "param"},
					CallbackPort: 8081,
				},
			},
		}

		result, err := service.createRequestToRemoteAuthConfig(context.Background(), req)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "client-id", result.ClientID)
		assert.Equal(t, "secret-value", result.ClientSecret)
		assert.Equal(t, []string{"read", "write"}, result.Scopes)
		assert.Equal(t, "https://oauth.example.com", result.Issuer)
		assert.Equal(t, "https://oauth.example.com/auth", result.AuthorizeURL)
		assert.Equal(t, "https://oauth.example.com/token", result.TokenURL)
		assert.Equal(t, map[string]string{"custom": "param"}, result.OAuthParams)
		assert.Equal(t, 8081, result.CallbackPort)
	})

	t.Run("without OAuth config", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		req := &apitypes.CreateRequest{}

		result, err := service.createRequestToRemoteAuthConfig(context.Background(), req)

		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("secret resolution fails", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
		mockSecretsProvider.EXPECT().
			GetSecret(gomock.Any(), "secret-name").
			Return("", errors.New("secret not found"))

		service := &WorkloadService{
			secretsProvider: mockSecretsProvider,
		}

		req := &apitypes.CreateRequest{
			UpdateRequest: apitypes.UpdateRequest{
				OAuthConfig: &apitypes.RemoteOAuthConfig{
					ClientSecret: &secrets.SecretParameter{Name: "secret-name"},
				},
			},
		}

		result, err := service.createRequestToRemoteAuthConfig(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to resolve OAuth client secret")
	})
}

func TestWorkloadService_GetWorkloadNamesFromRequest(t *testing.T) {
	t.Parallel()

	t.Run("with names", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		req := apitypes.BulkOperationRequest{
			Names: []string{"workload1", "workload2"},
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("with group", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return([]string{"workload1", "workload2"}, nil)

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
		}

		req := apitypes.BulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("invalid group name", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		req := apitypes.BulkOperationRequest{
			Group: "invalid-group-name-with-special-chars!@#",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "invalid group name")
	})

	t.Run("group does not exist", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "non-existent-group").
			Return(false, nil)

		service := &WorkloadService{
			groupManager: mockGroupManager,
		}

		req := apitypes.BulkOperationRequest{
			Group: "non-existent-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "group 'non-existent-group' does not exist")
	})

	t.Run("list workloads error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return(nil, errors.New("database error"))

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
		}

		req := apitypes.BulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})
}

func TestNewWorkloadService(t *testing.T) {
	t.Parallel()

	service := NewWorkloadService(nil, nil, nil, nil, false)
	require.NotNil(t, service)
}
