// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

func TestRegisterClientHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		requestBody     any
		storageErr      error
		expectedStatus  int
		expectedError   string // DCR error code; empty means expect success
		expectedErrDesc string // substring match on error_description
	}{
		{
			name: "success",
			requestBody: registration.DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
				ClientName:   "Test Client",
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:           "invalid JSON body",
			requestBody:    "not-valid-json",
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidClientMetadata,
		},
		{
			name: "validation error propagated",
			requestBody: registration.DCRRequest{
				RedirectURIs: []string{"http://example.com/callback"},
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidRedirectURI,
		},
		{
			name: "storage failure returns 500",
			requestBody: registration.DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
			},
			storageErr:      errors.New("disk full"),
			expectedStatus:  http.StatusInternalServerError,
			expectedError:   "server_error",
			expectedErrDesc: "failed to register client",
		},
		{
			name:           "oversized body rejected",
			requestBody:    strings.Repeat("x", 65*1024), // 65KB exceeds 64KB limit
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidClientMetadata,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			stor := mocks.NewMockStorage(ctrl)
			stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).Return(tc.storageErr).AnyTimes()
			handler := &Handler{storage: stor, config: &server.AuthorizationServerConfig{}}

			var body []byte
			if s, ok := tc.requestBody.(string); ok {
				body = []byte(s)
			} else {
				body, _ = json.Marshal(tc.requestBody)
			}

			req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.RegisterClientHandler(w, req)

			assert.Equal(t, tc.expectedStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			if tc.expectedError != "" {
				var errResp registration.DCRError
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
				assert.Equal(t, tc.expectedError, errResp.Error)
				if tc.expectedErrDesc != "" {
					assert.Contains(t, errResp.ErrorDescription, tc.expectedErrDesc)
				}
			} else {
				var resp registration.DCRResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.ClientID)
				assert.NotZero(t, resp.ClientIDIssuedAt)
				assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
			}
		})
	}
}

func TestRegisterClientHandler_ClientIsStored(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	stor := mocks.NewMockStorage(ctrl)
	var storedClient fosite.Client
	stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, client fosite.Client) error {
			storedClient = client
			return nil
		})

	handler := &Handler{storage: stor, config: &server.AuthorizationServerConfig{}}

	reqBody, err := json.Marshal(registration.DCRRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		ClientName:   "Stored Client",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.RegisterClientHandler(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp registration.DCRResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.NotNil(t, storedClient)
	loopbackClient, ok := storedClient.(*registration.LoopbackClient)
	require.True(t, ok, "expected *registration.LoopbackClient, got %T", storedClient)

	assert.Equal(t, resp.ClientID, loopbackClient.GetID())
	assert.True(t, loopbackClient.IsPublic())
	assert.Equal(t, []string{"http://127.0.0.1:8080/callback"}, loopbackClient.GetRedirectURIs())
}
