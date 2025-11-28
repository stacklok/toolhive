package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryexport"
	"github.com/stacklok/toolhive/pkg/env/mocks"
)

func TestIsRegistryExportEnabledWithEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		envValue string
		want     bool
	}{
		{"enabled with true", "true", true},
		{"enabled with 1", "1", true},
		{"enabled with yes", "yes", true},
		{"disabled with false", "false", false},
		{"disabled with 0", "0", false},
		{"disabled with empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReader := mocks.NewMockReader(ctrl)
			mockReader.EXPECT().Getenv(registryexport.EnvEnableRegistryExport).Return(tt.envValue)

			got := IsRegistryExportEnabledWithEnv(mockReader)
			assert.Equal(t, tt.want, got)
		})
	}
}
