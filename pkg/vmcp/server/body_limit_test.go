// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// TestHandler_RejectsOversizedBody verifies the vMCP server's handler rejects an
// oversized request body with 413, matching the proxy transports. The body-limit
// wrapper runs before the MCP parser, so the oversized body is rejected before
// any handler buffers it.
func TestHandler_RejectsOversizedBody(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	srv, err := server.New(
		t.Context(),
		&server.Config{Host: "127.0.0.1", Port: 0, SessionFactory: newNoopMockFactory(t), Aggregator: newStubAggregator(nil)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)

	// A POST one byte over the default cap must be rejected before reaching the
	// MCP handler. Routed through "/" (the MCP handler mount).
	body := bytes.NewReader(make([]byte, bodylimit.DefaultMaxRequestBodySize+1))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
