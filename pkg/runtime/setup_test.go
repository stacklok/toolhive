// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/permissions"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestSetup_ForwardsContainerFlags verifies that Setup copies its boolean
// container settings into the DeployWorkloadOptions handed to the deployer.
func TestSetup_ForwardsContainerFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		allowDockerGateway bool
		readOnlyRootfs     bool
	}{
		{name: "defaults off"},
		{name: "allow docker gateway", allowDockerGateway: true},
		{name: "read-only rootfs", readOnlyRootfs: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			deployer := mocks.NewMockDeployer(ctrl)

			var gotOpts *rt.DeployWorkloadOptions
			deployer.EXPECT().
				DeployWorkload(gomock.Any(), "img", "name", gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any(), "stdio", gomock.Any(), false).
				DoAndReturn(func(_, _, _, _, _, _, _, _ any, opts *rt.DeployWorkloadOptions, _ bool) (int, error) {
					gotOpts = opts
					return 0, nil
				})

			_, err := Setup(
				t.Context(),
				types.TransportTypeStdio,
				deployer,
				"name",
				"img",
				nil,
				map[string]string{},
				map[string]string{},
				&permissions.Profile{},
				"",
				false,
				tt.allowDockerGateway,
				tt.readOnlyRootfs,
				nil,
				"127.0.0.1",
				0,
				"127.0.0.1",
				nil,
				nil,
				0,
			)
			require.NoError(t, err)

			require.NotNil(t, gotOpts)
			assert.Equal(t, tt.allowDockerGateway, gotOpts.AllowDockerGateway)
			assert.Equal(t, tt.readOnlyRootfs, gotOpts.ReadOnlyRootfs)
		})
	}
}
