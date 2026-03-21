// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp/session"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

func TestNewDecoratingFactory_NoDecorators_ReturnBase(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory := session.NewDecoratingFactory(base)

	assert.Equal(t, base, factory, "with no decorators, base factory should be returned as-is")
}

func TestNewDecoratingFactory_DecoratorsAppliedInOrder(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	var order []int
	dec1 := func(_ context.Context, s session.MultiSession) (session.MultiSession, error) {
		order = append(order, 1)
		return s, nil
	}
	dec2 := func(_ context.Context, s session.MultiSession) (session.MultiSession, error) {
		order = append(order, 2)
		return s, nil
	}

	factory := session.NewDecoratingFactory(base, dec1, dec2)
	_, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, order)
}

func TestNewDecoratingFactory_DecoratorError_ClosesSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	sess.EXPECT().Close().Return(nil)

	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	decErr := errors.New("decorator boom")
	factory := session.NewDecoratingFactory(base, func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) {
		return nil, decErr
	})

	_, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	require.ErrorIs(t, err, decErr)
}

func TestNewDecoratingFactory_SecondDecoratorError_ClosesCurrentSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	wrappedSess := sessionmocks.NewMockMultiSession(ctrl)
	// Only wrappedSess (the session current at the time of failure) should be closed.
	wrappedSess.EXPECT().Close().Return(nil)

	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	decErr := errors.New("second decorator boom")
	dec1 := func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) { return wrappedSess, nil }
	dec2 := func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) { return nil, decErr }

	factory := session.NewDecoratingFactory(base, dec1, dec2)
	_, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	require.ErrorIs(t, err, decErr)
}

func TestNewDecoratingFactory_NilReturnWithNoError_ClosesSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	sess.EXPECT().Close().Return(nil)

	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	factory := session.NewDecoratingFactory(base, func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) {
		return nil, nil // buggy decorator
	})

	_, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil session")
}

func TestNewDecoratingFactory_CloseErrorOnDecoratorFailure_DoesNotSuppressOriginalError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	sess.EXPECT().Close().Return(errors.New("close failed"))

	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	decErr := errors.New("decorator error")
	factory := session.NewDecoratingFactory(base, func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) {
		return nil, decErr
	})

	_, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	// The original decorator error, not the close error, is returned.
	require.ErrorIs(t, err, decErr)
}

func TestNewDecoratingFactory_HappyPath_ReturnsFinalSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := sessionmocks.NewMockMultiSession(ctrl)
	finalSess := sessionmocks.NewMockMultiSession(ctrl)

	base := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	base.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil)

	factory := session.NewDecoratingFactory(base,
		func(_ context.Context, _ session.MultiSession) (session.MultiSession, error) { return finalSess, nil },
	)

	got, err := factory.MakeSessionWithID(context.Background(), "id", nil, true, nil)
	require.NoError(t, err)
	assert.Equal(t, finalSess, got)
}
