// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestStrictTokenReader(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("forces Strict=true and preserves caller binding fields", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		inner := mocks.NewMockTokenReader(ctrl)
		reader := upstreamtoken.NewStrictTokenReader(inner)

		expected := &storage.ExpectedBinding{
			UserID:          "user-123",
			ClientID:        "client-abc",
			UpstreamSubject: "upstream-sub",
		}
		inner.EXPECT().
			GetAllUpstreamCredentials(ctx, "session-1", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, got *storage.ExpectedBinding,
			) (map[string]upstreamtoken.UpstreamCredential, []string, error) {
				assert.True(t, got.Strict, "Strict must be forced true")
				assert.Equal(t, "user-123", got.UserID, "caller UserID must be preserved")
				assert.Equal(t, "client-abc", got.ClientID, "caller ClientID must be preserved")
				assert.Equal(t, "upstream-sub", got.UpstreamSubject, "caller UpstreamSubject must be preserved")
				return map[string]upstreamtoken.UpstreamCredential{"github": {AccessToken: "tok"}}, nil, nil
			})

		creds, failed, err := reader.GetAllUpstreamCredentials(ctx, "session-1", expected)
		require.NoError(t, err)
		assert.Empty(t, failed)
		assert.Equal(t, "tok", creds["github"].AccessToken)

		assert.False(t, expected.Strict, "caller's expected binding must not be mutated (copy-before-mutate)")
	})

	t.Run("overrides a caller-supplied Strict=false", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		inner := mocks.NewMockTokenReader(ctrl)
		reader := upstreamtoken.NewStrictTokenReader(inner)

		expected := &storage.ExpectedBinding{UserID: "user-123", Strict: false}
		inner.EXPECT().
			GetAllUpstreamCredentials(ctx, "session-1", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, got *storage.ExpectedBinding,
			) (map[string]upstreamtoken.UpstreamCredential, []string, error) {
				assert.True(t, got.Strict, "Strict=false from the caller must be overridden")
				return nil, nil, nil
			})

		_, _, err := reader.GetAllUpstreamCredentials(ctx, "session-1", expected)
		require.NoError(t, err)
		assert.False(t, expected.Strict, "caller's struct must not be mutated")
	})

	t.Run("nil expected is synthesized with only Strict=true", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		inner := mocks.NewMockTokenReader(ctrl)
		reader := upstreamtoken.NewStrictTokenReader(inner)

		inner.EXPECT().
			GetAllUpstreamCredentials(ctx, "session-1", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, got *storage.ExpectedBinding,
			) (map[string]upstreamtoken.UpstreamCredential, []string, error) {
				require.NotNil(t, got)
				assert.Equal(t, storage.ExpectedBinding{Strict: true}, *got)
				return nil, nil, nil
			})

		_, _, err := reader.GetAllUpstreamCredentials(ctx, "session-1", nil)
		require.NoError(t, err)
	})

	t.Run("inner ErrInvalidBinding propagates", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		inner := mocks.NewMockTokenReader(ctrl)
		reader := upstreamtoken.NewStrictTokenReader(inner)

		inner.EXPECT().
			GetAllUpstreamCredentials(ctx, "session-1", gomock.Any()).
			Return(nil, nil, fmt.Errorf("wrapped: %w", storage.ErrInvalidBinding))

		_, _, err := reader.GetAllUpstreamCredentials(ctx, "session-1", &storage.ExpectedBinding{UserID: "u"})
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrInvalidBinding)
	})

	t.Run("session ID and context are forwarded unchanged", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		inner := mocks.NewMockTokenReader(ctrl)
		reader := upstreamtoken.NewStrictTokenReader(inner)

		type ctxKey struct{}
		keyedCtx := context.WithValue(ctx, ctxKey{}, "marker")

		inner.EXPECT().
			GetAllUpstreamCredentials(keyedCtx, "exact-session-id", gomock.Any()).
			Return(nil, []string{"github"}, nil)

		_, failed, err := reader.GetAllUpstreamCredentials(keyedCtx, "exact-session-id", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"github"}, failed, "failed-provider list must pass through untouched")
	})
}
