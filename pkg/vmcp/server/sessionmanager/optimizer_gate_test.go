// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// TestOptimizerFactoryGatedOnAdvertiseFromCore locks in the AC6 double-index
// guarantee: New surfaces the optimizer factory via OptimizerFactory() ONLY when
// AdvertiseFromCore is true. That makes the session-factory decorator (installed iff
// !AdvertiseFromCore) and the Serve-layer getter mutually exclusive store writers, so a
// Serve composition root that enables the optimizer but forgets the flag gets a nil
// factory (no Serve-layer optimizer) instead of a silent second upsert into the store.
func TestOptimizerFactoryGatedOnAdvertiseFromCore(t *testing.T) {
	t.Parallel()

	optFactory := func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error) {
		return nil, nil
	}

	tests := []struct {
		name              string
		advertiseFromCore bool
		wantSurfaced      bool
	}{
		{"surfaced to Serve when advertising from core", true, true},
		{"not surfaced on the legacy path (decorator owns it)", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			factory := newMockFactory(t, ctrl, newMockSession(t, ctrl, "", nil))

			sm, cleanup, err := New(newTestSessionDataStorage(t), &FactoryConfig{
				Base:              factory,
				OptimizerFactory:  optFactory,
				AdvertiseFromCore: tc.advertiseFromCore,
			}, newFakeRegistry())
			require.NoError(t, err)
			t.Cleanup(func() { _ = cleanup(context.Background()) })

			if tc.wantSurfaced {
				assert.NotNil(t, sm.OptimizerFactory(),
					"the factory must be surfaced to the Serve layer when AdvertiseFromCore is set")
			} else {
				assert.Nil(t, sm.OptimizerFactory(),
					"the factory must NOT be surfaced when AdvertiseFromCore is false (decorator owns the store)")
			}
		})
	}
}
