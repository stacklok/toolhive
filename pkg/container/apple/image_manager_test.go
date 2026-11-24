// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/apple"
	"github.com/stacklok/toolhive/pkg/container/apple/mocks"
)

func TestImageManager_ImageExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stdout    string
		runErr    error
		wantExist bool
		wantErr   bool
	}{
		{
			name:      "present image",
			stdout:    `[{"reference":"alpine:latest"}]`,
			wantExist: true,
		},
		{
			name:      "empty output treated as absent",
			stdout:    "  ",
			wantExist: false,
		},
		{
			name:      "empty array treated as absent",
			stdout:    "[]\n",
			wantExist: false,
		},
		{
			name: "not found error returns false without error",
			runErr: &apple.CommandError{
				Command: apple.ContainerBinary,
				Args:    []string{"image", "inspect", "--", "missing:latest"},
				Stderr:  "Error: image not found",
				Err:     errors.New("exit status 1"),
			},
			wantExist: false,
		},
		{
			name:    "unexpected error is returned",
			runErr:  errors.New("daemon crashed"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockCommandRunner(ctrl)
			runner.EXPECT().
				Run(gomock.Any(), apple.ContainerBinary, "image", "inspect", "--", "test-image:1").
				Return([]byte(tc.stdout), tc.runErr)

			mgr := apple.NewImageManagerWithRunner(runner)
			exists, err := mgr.ImageExists(context.Background(), "test-image:1")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantExist, exists)
		})
	}
}

func TestImageManager_PullAndBuildImage(t *testing.T) {
	t.Parallel()

	t.Run("PullImage success and failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "image", "pull", "--", "alpine:latest").Return(nil, nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "image", "pull", "--", "bad:image").Return(nil, errors.New("404"))

		mgr := apple.NewImageManagerWithRunner(runner)
		require.NoError(t, mgr.PullImage(context.Background(), "alpine:latest"))
		require.Error(t, mgr.PullImage(context.Background(), "bad:image"))
	})

	t.Run("BuildImage success and failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "build", "-t", "my-img:dev", "--", "/tmp/ctx").Return(nil, nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "build", "-t", "bad:dev", "--", "/tmp/ctx").Return(nil, errors.New("build failed"))

		mgr := apple.NewImageManagerWithRunner(runner)
		require.NoError(t, mgr.BuildImage(context.Background(), "/tmp/ctx", "my-img:dev"))
		require.Error(t, mgr.BuildImage(context.Background(), "/tmp/ctx", "bad:dev"))
	})
}
