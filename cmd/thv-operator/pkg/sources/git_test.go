package sources

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/git"
	"github.com/stacklok/toolhive/pkg/registry"
)

const (
	testGitRepoURL = "https://github.com/example/test-repo.git"
	testBranch     = "main"
	testTag        = "v1.0.0"
	testCommit     = "abc123def456"
	testFilePath   = "custom-registry.json"
)

// MockGitClient is a mock implementation of git.Client
type MockGitClient struct {
	mock.Mock
}

func (m *MockGitClient) Clone(ctx context.Context, config *git.CloneConfig) (*git.RepositoryInfo, error) {
	args := m.Called(ctx, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*git.RepositoryInfo), args.Error(1)
}

func (m *MockGitClient) Pull(ctx context.Context, repoInfo *git.RepositoryInfo) error {
	args := m.Called(ctx, repoInfo)
	return args.Error(0)
}

func (m *MockGitClient) GetFileContent(repoInfo *git.RepositoryInfo, path string) ([]byte, error) {
	args := m.Called(repoInfo, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockGitClient) GetCommitHash(repoInfo *git.RepositoryInfo) (string, error) {
	args := m.Called(repoInfo)
	return args.String(0), args.Error(1)
}

func (m *MockGitClient) Cleanup(repoInfo *git.RepositoryInfo) error {
	args := m.Called(repoInfo)
	return args.Error(0)
}

// MockSourceDataValidator is a mock implementation of SourceDataValidator
type MockSourceDataValidator struct {
	mock.Mock
}

func (m *MockSourceDataValidator) ValidateData(data []byte, format string) (*registry.Registry, error) {
	args := m.Called(data, format)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*registry.Registry), args.Error(1)
}

func TestNewGitSourceHandler(t *testing.T) {
	t.Parallel()

	handler := NewGitSourceHandler()

	assert.NotNil(t, handler)
	assert.NotNil(t, handler.gitClient)
	assert.NotNil(t, handler.validator)
}

func TestGitSourceHandler_Validate(t *testing.T) {
	t.Parallel()

	handler := NewGitSourceHandler()

	tests := []struct {
		name        string
		source      *mcpv1alpha1.MCPRegistrySource
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid git source with repository only",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
				},
			},
			expectError: false,
		},
		{
			name: "valid git source with branch",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Branch:     testBranch,
				},
			},
			expectError: false,
		},
		{
			name: "valid git source with tag",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Tag:        testTag,
				},
			},
			expectError: false,
		},
		{
			name: "valid git source with commit",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Commit:     testCommit,
				},
			},
			expectError: false,
		},
		{
			name: "valid git source with custom path",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Path:       testFilePath,
				},
			},
			expectError: false,
		},
		{
			name: "invalid source type",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
				},
			},
			expectError: true,
			errorMsg:    "invalid source type",
		},
		{
			name: "missing git configuration",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git:  nil,
			},
			expectError: true,
			errorMsg:    "git configuration is required",
		},
		{
			name: "empty repository URL",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: "",
				},
			},
			expectError: true,
			errorMsg:    "git repository URL cannot be empty",
		},
		{
			name: "multiple reference types - branch and tag",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Branch:     testBranch,
					Tag:        testTag,
				},
			},
			expectError: true,
			errorMsg:    "only one of branch, tag, or commit may be specified",
		},
		{
			name: "multiple reference types - branch and commit",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Branch:     testBranch,
					Commit:     testCommit,
				},
			},
			expectError: true,
			errorMsg:    "only one of branch, tag, or commit may be specified",
		},
		{
			name: "multiple reference types - tag and commit",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Tag:        testTag,
					Commit:     testCommit,
				},
			},
			expectError: true,
			errorMsg:    "only one of branch, tag, or commit may be specified",
		},
		{
			name: "all reference types specified",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
					Branch:     testBranch,
					Tag:        testTag,
					Commit:     testCommit,
				},
			},
			expectError: true,
			errorMsg:    "only one of branch, tag, or commit may be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := handler.Validate(tt.source)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				// Check that default path is set when not specified
				if tt.source.Git != nil && tt.source.Git.Path == "" {
					assert.Equal(t, DefaultRegistryDataFile, tt.source.Git.Path)
				}
			}
		})
	}
}

func TestGitSourceHandler_FetchRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registry      *mcpv1alpha1.MCPRegistry
		setupMocks    func(*MockGitClient, *MockSourceDataValidator)
		expectError   bool
		errorContains string
	}{
		{
			name: "successful fetch with default path",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeGit,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
							Branch:     testBranch,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}
				testData := []byte(`{"version": "1.0.0"}`)
				testRegistry := &registry.Registry{
					Version:       "1.0.0",
					Servers:       make(map[string]*registry.ImageMetadata),
					RemoteServers: make(map[string]*registry.RemoteServerMetadata),
				}

				gitClient.On("Clone", mock.Anything, mock.MatchedBy(func(config *git.CloneConfig) bool {
					return config.URL == testGitRepoURL && config.Branch == testBranch
				})).Return(repoInfo, nil)

				gitClient.On("GetFileContent", repoInfo, DefaultRegistryDataFile).Return(testData, nil)
				gitClient.On("Cleanup", repoInfo).Return(nil)

				validator.On("ValidateData", testData, mcpv1alpha1.RegistryFormatToolHive).Return(testRegistry, nil)
			},
			expectError: false,
		},
		{
			name: "successful fetch with custom path",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeGit,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
							Tag:        testTag,
							Path:       testFilePath,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}
				testData := []byte(`{"version": "1.0.0"}`)
				testRegistry := &registry.Registry{
					Version:       "1.0.0",
					Servers:       make(map[string]*registry.ImageMetadata),
					RemoteServers: make(map[string]*registry.RemoteServerMetadata),
				}

				gitClient.On("Clone", mock.Anything, mock.MatchedBy(func(config *git.CloneConfig) bool {
					return config.URL == testGitRepoURL && config.Tag == testTag
				})).Return(repoInfo, nil)

				gitClient.On("GetFileContent", repoInfo, testFilePath).Return(testData, nil)
				gitClient.On("Cleanup", repoInfo).Return(nil)

				validator.On("ValidateData", testData, mcpv1alpha1.RegistryFormatToolHive).Return(testRegistry, nil)
			},
			expectError: false,
		},
		{
			name: "validation failure",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeGit,
						Git: &mcpv1alpha1.GitSource{
							Repository: "", // Invalid
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				// No mocks needed as validation should fail before git operations
			},
			expectError:   true,
			errorContains: "source validation failed",
		},
		{
			name: "clone failure",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeGit,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
							Commit:     testCommit,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				gitClient.On("Clone", mock.Anything, mock.MatchedBy(func(config *git.CloneConfig) bool {
					return config.URL == testGitRepoURL && config.Commit == testCommit
				})).Return(nil, errors.New("clone failed"))
			},
			expectError:   true,
			errorContains: "failed to clone repository",
		},
		{
			name: "file not found",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeGit,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}

				gitClient.On("Clone", mock.Anything, mock.Anything).Return(repoInfo, nil)
				gitClient.On("GetFileContent", repoInfo, DefaultRegistryDataFile).Return(nil, errors.New("file not found"))
				gitClient.On("Cleanup", repoInfo).Return(nil)
			},
			expectError:   true,
			errorContains: "failed to get file",
		},
		{
			name: "validation data failure",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeGit,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient, validator *MockSourceDataValidator) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}
				testData := []byte(`invalid json`)

				gitClient.On("Clone", mock.Anything, mock.Anything).Return(repoInfo, nil)
				gitClient.On("GetFileContent", repoInfo, DefaultRegistryDataFile).Return(testData, nil)
				gitClient.On("Cleanup", repoInfo).Return(nil)

				validator.On("ValidateData", testData, mcpv1alpha1.RegistryFormatToolHive).Return(nil, errors.New("invalid data"))
			},
			expectError:   true,
			errorContains: "registry data validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mocks
			mockGitClient := new(MockGitClient)
			mockValidator := new(MockSourceDataValidator)

			// Setup mocks
			tt.setupMocks(mockGitClient, mockValidator)

			// Create handler with mocks
			handler := &GitSourceHandler{
				gitClient: mockGitClient,
				validator: mockValidator,
			}

			// Execute test
			result, err := handler.FetchRegistry(context.Background(), tt.registry)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.NotEmpty(t, result.Hash)
				assert.Equal(t, tt.registry.Spec.Source.Format, result.Format)
			}

			// Verify all mock expectations
			mockGitClient.AssertExpectations(t)
			mockValidator.AssertExpectations(t)
		})
	}
}

func TestGitSourceHandler_CurrentHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registry      *mcpv1alpha1.MCPRegistry
		setupMocks    func(*MockGitClient)
		expectError   bool
		errorContains string
		expectedHash  string
	}{
		{
			name: "successful hash calculation",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeGit,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
							Branch:     testBranch,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}
				testData := []byte(`{"version": "1.0.0"}`)

				gitClient.On("Clone", mock.Anything, mock.MatchedBy(func(config *git.CloneConfig) bool {
					return config.URL == testGitRepoURL && config.Branch == testBranch
				})).Return(repoInfo, nil)

				gitClient.On("GetFileContent", repoInfo, DefaultRegistryDataFile).Return(testData, nil)
				gitClient.On("Cleanup", repoInfo).Return(nil)
			},
			expectError:  false,
			expectedHash: fmt.Sprintf("%x", sha256.Sum256([]byte(`{"version": "1.0.0"}`))),
		},
		{
			name: "validation failure",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeGit,
						Git: &mcpv1alpha1.GitSource{
							Repository: "", // Invalid
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient) {
				// No mocks needed as validation should fail
			},
			expectError:   true,
			errorContains: "source validation failed",
		},
		{
			name: "clone failure",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeGit,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient) {
				gitClient.On("Clone", mock.Anything, mock.Anything).Return(nil, errors.New("clone failed"))
			},
			expectError:   true,
			errorContains: "failed to clone repository",
		},
		{
			name: "file not found",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeGit,
						Git: &mcpv1alpha1.GitSource{
							Repository: testGitRepoURL,
							Path:       testFilePath,
						},
					},
				},
			},
			setupMocks: func(gitClient *MockGitClient) {
				repoInfo := &git.RepositoryInfo{
					RemoteURL: testGitRepoURL,
				}

				gitClient.On("Clone", mock.Anything, mock.Anything).Return(repoInfo, nil)
				gitClient.On("GetFileContent", repoInfo, testFilePath).Return(nil, errors.New("file not found"))
				gitClient.On("Cleanup", repoInfo).Return(nil)
			},
			expectError:   true,
			errorContains: "failed to get file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mocks
			mockGitClient := new(MockGitClient)

			// Setup mocks
			tt.setupMocks(mockGitClient)

			// Create handler with mocks
			handler := &GitSourceHandler{
				gitClient: mockGitClient,
				validator: NewSourceDataValidator(), // Use real validator for hash tests
			}

			// Execute test
			hash, err := handler.CurrentHash(context.Background(), tt.registry)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, hash)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, hash)
				if tt.expectedHash != "" {
					assert.Equal(t, tt.expectedHash, hash)
				}
			}

			// Verify all mock expectations
			mockGitClient.AssertExpectations(t)
		})
	}
}

func TestGitSourceHandler_DefaultPath(t *testing.T) {
	t.Parallel()

	handler := NewGitSourceHandler()

	// Test that default path is set when Path is empty
	source := &mcpv1alpha1.MCPRegistrySource{
		Type: mcpv1alpha1.RegistrySourceTypeGit,
		Git: &mcpv1alpha1.GitSource{
			Repository: testGitRepoURL,
			// Path is intentionally empty
		},
	}

	err := handler.Validate(source)
	require.NoError(t, err)
	assert.Equal(t, DefaultRegistryDataFile, source.Git.Path)
}

func TestGitSourceHandler_CleanupFailure(t *testing.T) {
	t.Parallel()

	// This test verifies that cleanup failures don't cause the operation to fail
	mockGitClient := new(MockGitClient)
	mockValidator := new(MockSourceDataValidator)

	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test",
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   mcpv1alpha1.RegistrySourceTypeGit,
				Format: mcpv1alpha1.RegistryFormatToolHive,
				Git: &mcpv1alpha1.GitSource{
					Repository: testGitRepoURL,
				},
			},
		},
	}

	repoInfo := &git.RepositoryInfo{
		RemoteURL: testGitRepoURL,
	}
	testData := []byte(`{"version": "1.0.0"}`)
	testRegistry := &registry.Registry{
		Version:       "1.0.0",
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	mockGitClient.On("Clone", mock.Anything, mock.Anything).Return(repoInfo, nil)
	mockGitClient.On("GetFileContent", repoInfo, DefaultRegistryDataFile).Return(testData, nil)
	mockGitClient.On("Cleanup", repoInfo).Return(errors.New("cleanup failed")) // Cleanup fails

	mockValidator.On("ValidateData", testData, mcpv1alpha1.RegistryFormatToolHive).Return(testRegistry, nil)

	handler := &GitSourceHandler{
		gitClient: mockGitClient,
		validator: mockValidator,
	}

	// Despite cleanup failure, the operation should succeed
	result, err := handler.FetchRegistry(context.Background(), mcpRegistry)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	mockGitClient.AssertExpectations(t)
	mockValidator.AssertExpectations(t)
}
