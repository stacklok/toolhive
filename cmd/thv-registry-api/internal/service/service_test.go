package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service"
	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service/mocks"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestService_GetRegistry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		setupMocks     func(*mocks.MockRegistryDataProvider)
		expectedError  string
		expectedSource string
		validateResult func(*testing.T, *registry.Registry)
	}{
		{
			name: "successful registry fetch",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers: map[string]*registry.ImageMetadata{
						"test-server": {
							BaseServerMetadata: registry.BaseServerMetadata{
								Name:        "test-server",
								Description: "A test server",
							},
							Image: "test:latest",
						},
					},
				}, nil)
				m.EXPECT().GetSource().Return("configmap:test-namespace/test-configmap").AnyTimes()
			},
			expectedSource: "configmap:test-namespace/test-configmap",
			validateResult: func(t *testing.T, r *registry.Registry) {
				t.Helper()
				assert.Equal(t, "1.0.0", r.Version)
				assert.Len(t, r.Servers, 1)
				assert.Contains(t, r.Servers, "test-server")
			},
		},
		{
			name: "provider returns error",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(nil, errors.New("configmap not found")).Times(2) // Once during NewService, once during GetRegistry
				m.EXPECT().GetSource().Return("configmap:test-namespace/test-configmap").AnyTimes()
			},
			expectedSource: "configmap:test-namespace/test-configmap",
			validateResult: func(t *testing.T, r *registry.Registry) {
				t.Helper()
				// Should return empty registry on error
				assert.NotNil(t, r)
				assert.Equal(t, "1.0.0", r.Version)
				assert.Empty(t, r.Servers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockRegistryDataProvider(ctrl)
			tt.setupMocks(mockProvider)

			svc, err := service.NewService(context.Background(), mockProvider, nil)
			require.NoError(t, err)

			registry, source, err := svc.GetRegistry(context.Background())

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedSource, source)
			if tt.validateResult != nil {
				tt.validateResult(t, registry)
			}
		})
	}
}

func TestService_CheckReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setupMocks    func(*mocks.MockRegistryDataProvider)
		expectedError string
	}{
		{
			name: "ready with successful data load",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers:     make(map[string]*registry.ImageMetadata),
				}, nil).Times(1) // Only during NewService, CheckReadiness uses cached data
			},
			expectedError: "",
		},
		{
			name: "not ready when provider fails",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(nil, errors.New("connection failed")).Times(2)
			},
			expectedError: "registry data not available: failed to get registry data: connection failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockRegistryDataProvider(ctrl)
			tt.setupMocks(mockProvider)

			svc, err := service.NewService(context.Background(), mockProvider, nil)
			require.NoError(t, err)

			err = svc.CheckReadiness(context.Background())

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestService_ListServers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		setupMocks      func(*mocks.MockRegistryDataProvider)
		expectedCount   int
		validateServers func(*testing.T, []registry.ServerMetadata)
	}{
		{
			name: "list servers from registry",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers: map[string]*registry.ImageMetadata{
						"server1": {
							BaseServerMetadata: registry.BaseServerMetadata{
								Name:        "server1",
								Description: "Server 1",
							},
							Image: "server1:latest",
						},
						"server2": {
							BaseServerMetadata: registry.BaseServerMetadata{
								Name:        "server2",
								Description: "Server 2",
							},
							Image: "server2:latest",
						},
					},
					RemoteServers: map[string]*registry.RemoteServerMetadata{
						"remote1": {
							BaseServerMetadata: registry.BaseServerMetadata{
								Name:        "remote1",
								Description: "Remote server 1",
							},
							URL: "https://example.com/remote1",
						},
					},
				}, nil).AnyTimes()
			},
			expectedCount: 3,
			validateServers: func(t *testing.T, servers []registry.ServerMetadata) {
				t.Helper()
				names := make([]string, len(servers))
				for i, s := range servers {
					names[i] = s.GetName()
				}
				assert.Contains(t, names, "server1")
				assert.Contains(t, names, "server2")
				assert.Contains(t, names, "remote1")
			},
		},
		{
			name: "empty registry returns empty list",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers:     make(map[string]*registry.ImageMetadata),
				}, nil).AnyTimes()
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockRegistryDataProvider(ctrl)
			tt.setupMocks(mockProvider)

			svc, err := service.NewService(context.Background(), mockProvider, nil)
			require.NoError(t, err)

			servers, err := svc.ListServers(context.Background())
			assert.NoError(t, err)
			assert.Len(t, servers, tt.expectedCount)

			if tt.validateServers != nil {
				tt.validateServers(t, servers)
			}
		})
	}
}

func TestService_GetServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		serverName     string
		setupMocks     func(*mocks.MockRegistryDataProvider)
		expectedError  string
		validateServer func(*testing.T, registry.ServerMetadata)
	}{
		{
			name:       "get existing server",
			serverName: "test-server",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers: map[string]*registry.ImageMetadata{
						"test-server": {
							BaseServerMetadata: registry.BaseServerMetadata{
								Name:        "test-server",
								Description: "A test server",
							},
							Image: "test:latest",
						},
					},
				}, nil).AnyTimes()
			},
			validateServer: func(t *testing.T, s registry.ServerMetadata) {
				t.Helper()
				assert.Equal(t, "test-server", s.GetName())
				assert.Equal(t, "A test server", s.GetDescription())
			},
		},
		{
			name:       "server not found",
			serverName: "nonexistent",
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers:     make(map[string]*registry.ImageMetadata),
				}, nil).AnyTimes()
			},
			expectedError: "server not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockRegistryDataProvider(ctrl)
			tt.setupMocks(mockProvider)

			svc, err := service.NewService(context.Background(), mockProvider, nil)
			require.NoError(t, err)

			server, err := svc.GetServer(context.Background(), tt.serverName)

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
				assert.Nil(t, server)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, server)
				if tt.validateServer != nil {
					tt.validateServer(t, server)
				}
			}
		})
	}
}

func TestService_ListDeployedServers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setupMocks    func(*mocks.MockRegistryDataProvider, *mocks.MockDeploymentProvider)
		useDeployment bool
		expectedCount int
		expectedError string
	}{
		{
			name: "list deployed servers",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, dep *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
				dep.EXPECT().ListDeployedServers(gomock.Any()).Return([]*service.DeployedServer{
					{
						Name:      "deployed1",
						Namespace: "default",
						Status:    "Running",
						Image:     "server:latest",
						Ready:     true,
					},
					{
						Name:      "deployed2",
						Namespace: "test",
						Status:    "Running",
						Image:     "server2:latest",
						Ready:     true,
					},
				}, nil)
			},
			useDeployment: true,
			expectedCount: 2,
		},
		{
			name: "no deployment provider returns empty list",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, _ *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
			},
			useDeployment: false,
			expectedCount: 0,
		},
		{
			name: "deployment provider error",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, dep *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
				dep.EXPECT().ListDeployedServers(gomock.Any()).Return(nil, errors.New("k8s api error"))
			},
			useDeployment: true,
			expectedError: "k8s api error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRegProvider := mocks.NewMockRegistryDataProvider(ctrl)
			var mockDepProvider *mocks.MockDeploymentProvider
			if tt.useDeployment {
				mockDepProvider = mocks.NewMockDeploymentProvider(ctrl)
			}

			tt.setupMocks(mockRegProvider, mockDepProvider)

			var depProvider service.DeploymentProvider
			if tt.useDeployment {
				depProvider = mockDepProvider
			}

			svc, err := service.NewService(context.Background(), mockRegProvider, depProvider)
			require.NoError(t, err)

			servers, err := svc.ListDeployedServers(context.Background())

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Len(t, servers, tt.expectedCount)
			}
		})
	}
}

func TestService_GetDeployedServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		serverName      string
		setupMocks      func(*mocks.MockRegistryDataProvider, *mocks.MockDeploymentProvider)
		useDeployment   bool
		expectedError   string
		validateServers func(*testing.T, []*service.DeployedServer)
	}{
		{
			name:       "get deployed server",
			serverName: "deployed1",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, dep *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
				dep.EXPECT().GetDeployedServer(gomock.Any(), "deployed1").Return([]*service.DeployedServer{
					{
						Name:      "deployed1",
						Namespace: "default",
						Status:    "Running",
						Image:     "server:latest",
						Ready:     true,
					},
				}, nil)
			},
			useDeployment: true,
			validateServers: func(t *testing.T, servers []*service.DeployedServer) {
				t.Helper()
				require.Len(t, servers, 1)
				s := servers[0]
				assert.Equal(t, "deployed1", s.Name)
				assert.Equal(t, "default", s.Namespace)
				assert.True(t, s.Ready)
			},
		},
		{
			name:       "no deployment provider",
			serverName: "any",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, _ *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
			},
			useDeployment: false,
			validateServers: func(t *testing.T, servers []*service.DeployedServer) {
				t.Helper()
				assert.Len(t, servers, 0)
			},
		},
		{
			name:       "server not found",
			serverName: "nonexistent",
			setupMocks: func(reg *mocks.MockRegistryDataProvider, dep *mocks.MockDeploymentProvider) {
				reg.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version: "1.0.0",
				}, nil)
				dep.EXPECT().GetDeployedServer(gomock.Any(), "nonexistent").Return([]*service.DeployedServer{}, nil)
			},
			useDeployment: true,
			validateServers: func(t *testing.T, servers []*service.DeployedServer) {
				t.Helper()
				assert.Len(t, servers, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRegProvider := mocks.NewMockRegistryDataProvider(ctrl)
			var mockDepProvider *mocks.MockDeploymentProvider
			if tt.useDeployment {
				mockDepProvider = mocks.NewMockDeploymentProvider(ctrl)
			}

			tt.setupMocks(mockRegProvider, mockDepProvider)

			var depProvider service.DeploymentProvider
			if tt.useDeployment {
				depProvider = mockDepProvider
			}

			svc, err := service.NewService(context.Background(), mockRegProvider, depProvider)
			require.NoError(t, err)

			servers, err := svc.GetDeployedServer(context.Background(), tt.serverName)

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, servers)
				if tt.validateServers != nil {
					tt.validateServers(t, servers)
				}
			}
		})
	}
}

func TestService_WithCacheDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		cacheDuration time.Duration
		setupMocks    func(*mocks.MockRegistryDataProvider)
		callCount     int
	}{
		{
			name:          "custom cache duration",
			cacheDuration: 100 * time.Millisecond,
			setupMocks: func(m *mocks.MockRegistryDataProvider) {
				// Should be called twice: once during NewService, once after cache expires
				m.EXPECT().GetRegistryData(gomock.Any()).Return(&registry.Registry{
					Version:     "1.0.0",
					LastUpdated: time.Now().Format(time.RFC3339),
					Servers:     make(map[string]*registry.ImageMetadata),
				}, nil).Times(2)
				m.EXPECT().GetSource().Return("test-source").AnyTimes()
			},
			callCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockRegistryDataProvider(ctrl)
			tt.setupMocks(mockProvider)

			svc, err := service.NewService(
				context.Background(),
				mockProvider,
				nil,
				service.WithCacheDuration(tt.cacheDuration),
			)
			require.NoError(t, err)

			// First call - should use cached data from NewService
			_, _, err = svc.GetRegistry(context.Background())
			assert.NoError(t, err)

			// Wait for cache to expire
			time.Sleep(tt.cacheDuration + 10*time.Millisecond)

			// Second call - should fetch new data
			_, _, err = svc.GetRegistry(context.Background())
			assert.NoError(t, err)
		})
	}
}
