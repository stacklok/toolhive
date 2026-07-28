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

// TestOptimizerFactoryGatedOnAdvertiseFromCore locks in the constructor guard
// behind the AC6 single-writer guarantee and the Modern capability gate's
// enablement signal: New accepts an optimizer only together with
// AdvertiseFromCore, so the Serve layer (via OptimizerFactory()) is the sole
// writer of the shared FTS5 store and a non-nil OptimizerFactory() faithfully
// means "optimizer enabled" (pkg/vmcp/server/modern_gate.go depends on that).
// Optimizer-without-flag is rejected at construction instead of silently
// producing an optimizer that indexes the store but serves nobody.
func TestOptimizerFactoryGatedOnAdvertiseFromCore(t *testing.T) {
	t.Parallel()

	optFactory := func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error) {
		return nil, nil
	}

	tests := []struct {
		name              string
		advertiseFromCore bool
	}{
		{"surfaced to Serve when advertising from core", true},
		{"rejected at construction without AdvertiseFromCore", false},
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

			if !tc.advertiseFromCore {
				require.ErrorContains(t, err, "AdvertiseFromCore",
					"New must reject an optimizer without AdvertiseFromCore at construction")
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = cleanup(context.Background()) })
			assert.NotNil(t, sm.OptimizerFactory(),
				"the factory must be surfaced to the Serve layer when AdvertiseFromCore is set")
		})
	}
}
