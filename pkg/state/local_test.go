// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalStoreRejectsPathTraversalNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name          string
		setupOutside  bool
		expectOutside bool
		run           func(*testing.T, context.Context, *LocalStore, string) error
	}{
		{
			name:          "GetReader",
			setupOutside:  true,
			expectOutside: true,
			run: func(t *testing.T, ctx context.Context, store *LocalStore, name string) error {
				reader, err := store.GetReader(ctx, name)
				if reader != nil {
					require.NoError(t, reader.Close())
				}
				return err
			},
		},
		{
			name: "GetWriter",
			run: func(t *testing.T, ctx context.Context, store *LocalStore, name string) error {
				writer, err := store.GetWriter(ctx, name)
				if writer != nil {
					require.NoError(t, writer.Close())
				}
				return err
			},
		},
		{
			name: "CreateExclusive",
			run: func(t *testing.T, ctx context.Context, store *LocalStore, name string) error {
				writer, err := store.CreateExclusive(ctx, name)
				if writer != nil {
					require.NoError(t, writer.Close())
				}
				return err
			},
		},
		{
			name:          "Delete",
			setupOutside:  true,
			expectOutside: true,
			run: func(_ *testing.T, ctx context.Context, store *LocalStore, name string) error {
				return store.Delete(ctx, name)
			},
		},
		{
			name:          "Exists",
			setupOutside:  true,
			expectOutside: true,
			run: func(_ *testing.T, ctx context.Context, store *LocalStore, name string) error {
				_, err := store.Exists(ctx, name)
				return err
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			basePath := filepath.Join(tempDir, "state")
			require.NoError(t, os.MkdirAll(basePath, 0750))

			outsidePath := filepath.Join(tempDir, "outside.json")
			if tt.setupOutside {
				require.NoError(t, os.WriteFile(outsidePath, []byte("{}"), 0600))
			}

			store := &LocalStore{basePath: basePath}
			err := tt.run(t, ctx, store, "../outside")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "escapes state directory")
			if tt.expectOutside {
				assert.FileExists(t, outsidePath)
			} else {
				assert.NoFileExists(t, outsidePath)
			}
		})
	}
}
