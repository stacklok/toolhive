// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

func TestInProcessService_WithProviders(t *testing.T) {
	t.Parallel()

	expired := func(access string) *storage.UpstreamTokens {
		return &storage.UpstreamTokens{
			AccessToken:  access,
			RefreshToken: "refresh-" + access,
			ExpiresAt:    time.Now().Add(-time.Hour),
		}
	}
	fresh := func(access string) *storage.UpstreamTokens {
		return &storage.UpstreamTokens{AccessToken: access, ExpiresAt: time.Now().Add(time.Hour)}
	}

	t.Run("filtered-out expired row is never refreshed", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		x, y := expired("x-old"), expired("y-old")
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": x, "Y": y}, nil)
		refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
		refresher.EXPECT().RefreshAndStore(gomock.Any(), "s1", x).
			Return(&storage.UpstreamTokens{AccessToken: "x-new", ExpiresAt: time.Now().Add(time.Hour)}, nil).Times(1)

		creds, failed, err := NewInProcessService(stor, refresher, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.Equal(t, map[string]UpstreamCredential{"X": {AccessToken: "x-new"}}, creds)
		assert.NotContains(t, creds, "Y")
		assert.NotContains(t, failed, "Y")
	})

	t.Run("filtered-out row whose refresh would fail is not in failed", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": fresh("x"), "Y": {AccessToken: "y", ExpiresAt: time.Now().Add(-time.Hour)}}, nil)

		creds, failed, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.Equal(t, map[string]UpstreamCredential{"X": {AccessToken: "x"}}, creds)
		assert.Nil(t, failed)
	})

	t.Run("selected provider fails while filtered-out healthy provider is absent", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": expired("x"), "Y": fresh("y")}, nil)

		creds, failed, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.NotNil(t, creds)
		assert.Empty(t, creds)
		assert.Contains(t, failed, "X")
		assert.NotContains(t, creds, "Y")
		assert.NotContains(t, failed, "Y")
	})

	t.Run("padded name matches the stored row", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": fresh("x"), "Y": fresh("y")}, nil)

		creds, _, err := NewInProcessService(stor, nil, WithProviders(" X ")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.Equal(t, map[string]UpstreamCredential{"X": {AccessToken: "x"}}, creds)
	})

	t.Run("no option loads every provider", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": fresh("x"), "Y": fresh("y")}, nil)

		creds, _, err := NewInProcessService(stor, nil).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.Len(t, creds, 2)
	})

	for name, names := range map[string][]string{
		"empty set":       {},
		"empty string":    {""},
		"whitespace only": {" ", "\t"},
	} {
		t.Run("no filter for "+name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
				Return(map[string]*storage.UpstreamTokens{"X": fresh("x"), "Y": fresh("y")}, nil)

			creds, _, err := NewInProcessService(stor, nil, WithProviders(names...)).
				GetAllUpstreamCredentials(context.Background(), "s1")

			require.NoError(t, err)
			assert.Len(t, creds, 2)
		})
	}

	t.Run("filter removing every row returns nil creds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"Y": fresh("y")}, nil)

		creds, failed, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.Nil(t, creds)
		assert.Nil(t, failed)
	})

	t.Run("selected row with nil value is not a filtered-out session", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").
			Return(map[string]*storage.UpstreamTokens{"X": nil, "Y": fresh("y")}, nil)

		creds, failed, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.NotNil(t, creds)
		assert.Empty(t, creds)
		assert.Nil(t, failed)
	})

	t.Run("session with no rows still returns empty non-nil map", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), "s1").Return(nil, nil)

		creds, _, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetAllUpstreamCredentials(context.Background(), "s1")

		require.NoError(t, err)
		assert.NotNil(t, creds)
		assert.Empty(t, creds)
	})

	t.Run("single-provider read is not filtered", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), "s1", "Y").Return(fresh("y"), nil)

		cred, err := NewInProcessService(stor, nil, WithProviders("X")).
			GetValidTokens(context.Background(), "s1", "Y")

		require.NoError(t, err)
		assert.Equal(t, "y", cred.AccessToken)
	})
}
