// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"

	"github.com/stacklok/toolhive/cmd/thv-memory/lifecycle"
	"github.com/stacklok/toolhive/pkg/memory"
	"github.com/stacklok/toolhive/pkg/memory/mocks"
)

func TestJob_RunOnce_ExpiresEntries(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)

	expired := memory.Entry{
		ID:        "mem_expired",
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	store.EXPECT().ListExpired(gomock.Any()).Return([]memory.Entry{expired}, nil)
	store.EXPECT().Archive(gomock.Any(), "mem_expired", memory.ArchiveReasonExpired, "").Return(nil)
	store.EXPECT().ListActive(gomock.Any()).Return(nil, nil)

	job := lifecycle.New(store, zaptest.NewLogger(t))
	err := job.RunOnce(context.Background())
	require.NoError(t, err)
}

func TestJob_RunOnce_UpdatesScores(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)

	entry := memory.Entry{
		ID:        "mem_active",
		Author:    memory.AuthorHuman,
		CreatedAt: time.Now(),
	}
	store.EXPECT().ListExpired(gomock.Any()).Return(nil, nil)
	store.EXPECT().ListActive(gomock.Any()).Return([]memory.Entry{entry}, nil)
	store.EXPECT().UpdateScores(gomock.Any(), "mem_active", gomock.Any(), gomock.Any()).Return(nil)

	job := lifecycle.New(store, zaptest.NewLogger(t))
	err := job.RunOnce(context.Background())
	require.NoError(t, err)
}
