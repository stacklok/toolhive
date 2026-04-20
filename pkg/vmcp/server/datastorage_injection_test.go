// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// countingDataStorage wraps a real LocalSessionDataStorage and counts how
// many times Close has been invoked. Used to assert that Server.Stop does
// not close a caller-supplied DataStorage.
type countingDataStorage struct {
	transportsession.DataStorage
	closeCalls atomic.Int32
}

func (c *countingDataStorage) Close() error {
	c.closeCalls.Add(1)
	return c.DataStorage.Close()
}

func newCountingDataStorage(t *testing.T) *countingDataStorage {
	t.Helper()
	inner, err := transportsession.NewLocalSessionDataStorage(5 * time.Minute)
	require.NoError(t, err)
	return &countingDataStorage{DataStorage: inner}
}

func TestNew_CallerOwnedDataStorageNotClosedOnStop(t *testing.T) {
	t.Parallel()

	spy := newCountingDataStorage(t)
	// The spy is caller-owned; close the inner LocalSessionDataStorage
	// directly at the end of the test so the counter is not ticked by
	// cleanup — the post-Stop assertion below must reflect only the server's
	// behaviour. Err ignored: closing an already-closed local store is a
	// no-op in this implementation.
	t.Cleanup(func() {
		_ = spy.DataStorage.Close()
	})

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().Times(1)

	srv, err := server.New(
		t.Context(),
		&server.Config{
			Host:           "127.0.0.1",
			Port:           0,
			SessionFactory: newNoopMockFactory(t),
			DataStorage:    spy,
		},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		nil,
	)
	require.NoError(t, err)

	err = srv.Stop(t.Context())
	require.NoError(t, err)

	assert.Equal(t, int32(0), spy.closeCalls.Load(),
		"server must not close a caller-supplied DataStorage")
}

func TestNew_BothSessionStorageAndDataStorageErrors(t *testing.T) {
	t.Parallel()

	spy := newCountingDataStorage(t)
	// Err ignored: closing an already-closed local store is a no-op.
	t.Cleanup(func() {
		_ = spy.DataStorage.Close()
	})

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

	_, err := server.New(
		t.Context(),
		&server.Config{
			Host:           "127.0.0.1",
			Port:           0,
			SessionFactory: newNoopMockFactory(t),
			SessionStorage: &vmcpconfig.SessionStorageConfig{
				Provider: "redis",
				Address:  "127.0.0.1:6379",
			},
			DataStorage: spy,
		},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DataStorage")
	assert.Contains(t, err.Error(), "SessionStorage")
	assert.Equal(t, int32(0), spy.closeCalls.Load(),
		"server must not close a caller-supplied DataStorage on misconfiguration")
}

func TestNew_ServerBuiltDataStorageStopSucceeds(t *testing.T) {
	// Guards against accidental regression of the server-owned close path
	// when Close moved from an inline Stop() block onto sessionDataStorageCloser.
	// Stop() must still complete without error when the server built the store.
	// This is a smoke test — it cannot observe Close on the internal
	// LocalSessionDataStorage because that type is constructed inside New().
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().Times(1)

	srv, err := server.New(
		t.Context(),
		&server.Config{
			Host:           "127.0.0.1",
			Port:           0,
			SessionFactory: newNoopMockFactory(t),
			SessionStorage: &vmcpconfig.SessionStorageConfig{Provider: "memory"},
		},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		nil,
	)
	require.NoError(t, err)

	require.NoError(t, srv.Stop(t.Context()))
}
