// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

const backendEdgeClientID = "client-under-test"

func TestClientAdmissionStorage_BackendEdges(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	tests := []struct {
		name    string
		client  fosite.Client
		err     error
		wantErr error
	}{
		{name: "nil client is not found", wantErr: storage.ErrNotFound},
		{name: "backend error passes through", err: boom, wantErr: boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			backend := mocks.NewMockStorage(ctrl)
			backend.EXPECT().GetClient(gomock.Any(), backendEdgeClientID).Return(tt.client, tt.err)

			s, err := storage.NewClientAdmissionStorage(backend, nil)
			require.NoError(t, err)
			got, err := s.GetClient(t.Context(), backendEdgeClientID)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, tt.wantErr)
			if errors.Is(tt.wantErr, boom) {
				assert.NotErrorIs(t, err, storage.ErrNotFound)
			}
		})
	}
}

func TestClientAdmissionStorage_ConsumeAssertionJWTUnsupportedBackend(t *testing.T) {
	t.Parallel()

	s, err := storage.NewClientAdmissionStorage(mocks.NewMockStorage(gomock.NewController(t)), nil)
	require.NoError(t, err)
	assert.Error(t, s.ConsumeAssertionJWT(t.Context(), "p", "iss", "jti", time.Now()))
}
