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

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantMaj   int
		wantMin   int
		wantPatch int
		wantOK    bool
	}{
		{
			name:      "container CLI banner",
			input:     "container CLI version 1.0.2 (build 42)\n",
			wantMaj:   1,
			wantMin:   0,
			wantPatch: 2,
			wantOK:    true,
		},
		{
			name:      "v-prefixed semver",
			input:     "v1.2.3",
			wantMaj:   1,
			wantMin:   2,
			wantPatch: 3,
			wantOK:    true,
		},
		{
			name:      "two-component macOS version",
			input:     "26.1\n",
			wantMaj:   26,
			wantMin:   1,
			wantPatch: 0,
			wantOK:    true,
		},
		{
			name:      "pre-1.0 version",
			input:     "container version 0.9.5",
			wantMaj:   0,
			wantMin:   9,
			wantPatch: 5,
			wantOK:    true,
		},
		{
			name:   "no numeric version",
			input:  "container dev build",
			wantOK: false,
		},
		{
			name:   "negative version component rejected",
			input:  "-1.0.0",
			wantOK: false,
		},
		{
			name:   "four components rejected",
			input:  "1.2.3.4",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			maj, minVal, pat, ok := apple.ParseVersion(tc.input)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantMaj, maj)
			assert.Equal(t, tc.wantMin, minVal)
			assert.Equal(t, tc.wantPatch, pat)
		})
	}
}

func TestCheckAvailability(t *testing.T) {
	t.Parallel()

	t.Run("rejects non-darwin OS", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		err := apple.CheckAvailability(context.Background(), runner, "linux", "arm64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported on macOS")
	})

	t.Run("rejects non-arm64 architecture", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "amd64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Apple Silicon")
	})

	t.Run("rejects missing container binary", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().LookPath(apple.ContainerBinary).Return("", errors.New("not in PATH"))

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "arm64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in PATH")
	})

	t.Run("rejects pre-1.0 CLI version", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().LookPath(apple.ContainerBinary).Return("/usr/local/bin/container", nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "--version").Return([]byte("container CLI 0.9.1"), nil)

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "arm64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported")
	})

	t.Run("rejects unparsable CLI version", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().LookPath(apple.ContainerBinary).Return("/usr/local/bin/container", nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "--version").Return([]byte("unknown"), nil)

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "arm64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not parse")
	})

	t.Run("rejects stopped daemon with actionable hint", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().LookPath(apple.ContainerBinary).Return("/usr/local/bin/container", nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "--version").Return([]byte("container CLI 1.0.0"), nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "system", "status").Return(nil, errors.New("not running"))

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "arm64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "container system start")
	})

	t.Run("succeeds when darwin arm64 v1+ and daemon running", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().LookPath(apple.ContainerBinary).Return("/usr/local/bin/container", nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "--version").Return([]byte("container CLI version 1.1.0"), nil)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "system", "status").Return([]byte("apiserver is running\n"), nil)

		err := apple.CheckAvailability(context.Background(), runner, "darwin", "arm64")
		require.NoError(t, err)
	})
}

func TestCheckMacOSVersionForIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		output    string
		runErr    error
		wantError string
	}{
		{
			name:   "macOS 26.0 accepted",
			output: "26.0\n",
		},
		{
			name:   "macOS 26.2.1 accepted",
			output: "26.2.1\n",
		},
		{
			name:      "macOS 15.5 rejected",
			output:    "15.5\n",
			wantError: "requires macOS 26",
		},
		{
			name:      "unparsable sw_vers output rejected",
			output:    "unknown\n",
			wantError: "could not parse macOS product version",
		},
		{
			name:      "sw_vers error propagated",
			runErr:    errors.New("exec failed"),
			wantError: "failed to determine macOS version",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockCommandRunner(ctrl)
			runner.EXPECT().Run(gomock.Any(), apple.SwVersBinary, "-productVersion").Return([]byte(tc.output), tc.runErr)

			err := apple.CheckMacOSVersionForIsolation(context.Background(), runner)
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}
