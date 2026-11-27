// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestPrepareServerStorage_RefusesUserKeyedStorage(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	wrapped, err := storage.NewUserKeyedUpstreamStorage(base)
	require.NoError(t, err)

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "option on", cfg: Config{UserKeyedUpstreamStorage: true}},
		{name: "option off", cfg: Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _, err := prepareServerStorageAndRegisterClients(context.Background(), tt.cfg, wrapped)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "already contains the user-keyed upstream storage layer")
		})
	}
}
