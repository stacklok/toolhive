// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// stubReporter allows controlling Start/ReportStatus behavior in tests.
type stubReporter struct {
	startErr       error
	shutdownErr    error
	shutdownCalled chan struct{}
	reported       []*vmcp.Status
}

func (s *stubReporter) ReportStatus(_ context.Context, status *vmcp.Status) error {
	s.reported = append(s.reported, status)
	return nil
}

func (s *stubReporter) Start(_ context.Context) (func(context.Context) error, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	return func(_ context.Context) error {
		if s.shutdownCalled != nil {
			select {
			case s.shutdownCalled <- struct{}{}:
			default:
			}
		}
		return s.shutdownErr
	}, nil
}

func TestServerStartFailsWhenReporterStartFails(t *testing.T) {
	t.Parallel()

	sr := &stubReporter{startErr: errors.New("boom")}

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
	// Status reporting may attempt to call List() before failure is detected
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{}).AnyTimes()

	srv, err := server.New(
		context.Background(),
		&server.Config{Host: "127.0.0.1", Port: 0, StatusReporter: sr},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	err = srv.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start status reporter")
}

func TestServerStopRunsReporterShutdown(t *testing.T) {
	t.Parallel()

	shutdownCalled := make(chan struct{}, 1)
	sr := &stubReporter{shutdownErr: nil, shutdownCalled: shutdownCalled}

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().Times(1)
	// Status reporting runs in background and may call List() 0 or more times before shutdown
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{}).AnyTimes()

	srv, err := server.New(
		context.Background(),
		&server.Config{Host: "127.0.0.1", Port: 0, StatusReporter: sr},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-done:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not become ready")
	}

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatalf("server start/stop did not complete")
	}

	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatalf("shutdown func was not called")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *server.Config
		expectedHost string
		expectedPort int
		expectedPath string
		expectedName string
		expectedVer  string
	}{
		{
			name:         "applies all defaults",
			config:       &server.Config{},
			expectedHost: "127.0.0.1",
			expectedPort: 4483,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
		{
			name: "uses provided configuration",
			config: &server.Config{
				Name:         "custom-vmcp",
				Version:      "1.0.0",
				Host:         "0.0.0.0",
				Port:         8080,
				EndpointPath: "/api/mcp",
			},
			expectedHost: "0.0.0.0",
			expectedPort: 8080,
			expectedPath: "/api/mcp",
			expectedName: "custom-vmcp",
			expectedVer:  "1.0.0",
		},
		{
			name: "applies partial defaults",
			config: &server.Config{
				Host: "192.168.1.1",
				Port: 9000,
			},
			expectedHost: "192.168.1.1",
			expectedPort: 9000,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
			require.NoError(t, err)
			require.NotNil(t, s)

			addr := s.Address()
			require.Contains(t, addr, tt.expectedHost)
		})
	}
}

func TestServer_Address(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *server.Config
		expected string
	}{
		{
			name: "default host with explicit port",
			config: &server.Config{
				Port: 4483,
			},
			expected: "127.0.0.1:4483",
		},
		{
			name: "port 0 for dynamic allocation",
			config: &server.Config{
				Port: 0,
			},
			expected: "127.0.0.1:0",
		},
		{
			name: "custom host and port",
			config: &server.Config{
				Host: "0.0.0.0",
				Port: 8080,
			},
			expected: "0.0.0.0:8080",
		},
		{
			name: "localhost",
			config: &server.Config{
				Host: "localhost",
				Port: 3000,
			},
			expected: "localhost:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
			require.NoError(t, err)
			addr := s.Address()
			assert.Equal(t, tt.expected, addr)
		})
	}
}

func TestServer_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stop without starting is safe", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)
		mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
		mockDiscoveryMgr.EXPECT().Stop().Times(1)

		s, err := server.New(context.Background(), &server.Config{}, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
		require.NoError(t, err)
		err = s.Stop(context.Background())
		require.NoError(t, err)
	})
}

func TestNew_WithAuditConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		auditConfig *audit.Config
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil audit config is valid",
			auditConfig: nil,
			wantErr:     false,
		},
		{
			name: "empty audit config is valid",
			auditConfig: &audit.Config{
				Component: "vmcp-server",
			},
			wantErr: false,
		},
		{
			name: "full audit config is valid",
			auditConfig: &audit.Config{
				Component:           "vmcp-server",
				IncludeRequestData:  true,
				IncludeResponseData: true,
				MaxDataSize:         1024,
			},
			wantErr: false,
		},
		{
			name: "negative MaxDataSize is invalid",
			auditConfig: &audit.Config{
				Component:   "vmcp-server",
				MaxDataSize: -100,
			},
			wantErr:     true,
			errContains: "maxDataSize cannot be negative",
		},
		{
			name: "invalid event type is rejected",
			auditConfig: &audit.Config{
				Component:  "vmcp-server",
				EventTypes: []string{"invalid_event_type"},
			},
			wantErr:     true,
			errContains: "unknown event type: invalid_event_type",
		},
		{
			name: "invalid exclude event type is rejected",
			auditConfig: &audit.Config{
				Component:         "vmcp-server",
				ExcludeEventTypes: []string{"bad_event"},
			},
			wantErr:     true,
			errContains: "unknown exclude event type: bad_event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			config := &server.Config{
				AuditConfig: tt.auditConfig,
			}

			s, err := server.New(context.Background(), config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, s)
		})
	}
}
