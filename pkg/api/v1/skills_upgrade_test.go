// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
)

func TestUpgradeSkillsEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		service      skills.SkillService
		body         string
		wantStatus   int
		wantContains string
	}{
		{
			name: "successful upgrade returns 200 with outcomes",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				upgradeFn: func(_ context.Context, opts skills.UpgradeOptions) (*skills.UpgradeResult, error) {
					assert.Equal(t, "/tmp/proj", opts.ProjectRoot)
					assert.Equal(t, []string{"my-skill"}, opts.Names)
					assert.True(t, opts.AllowRefChange)
					return &skills.UpgradeResult{Outcomes: []skills.UpgradeOutcome{
						{Name: "my-skill", Status: skills.UpgradeStatusUpgraded},
					}}, nil
				},
			},
			body:         `{"project_root":"/tmp/proj","names":["my-skill"],"allow_ref_change":true}`,
			wantStatus:   http.StatusOK,
			wantContains: `"upgraded"`,
		},
		{
			name:       "service without Upgrade support returns 501",
			service:    skillsmocks.NewMockSkillService(gomock.NewController(t)),
			body:       `{"project_root":"/tmp/proj"}`,
			wantStatus: http.StatusNotImplemented,
		},
		{
			name: "invalid JSON body returns 400",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				upgradeFn: func(context.Context, skills.UpgradeOptions) (*skills.UpgradeResult, error) {
					t.Fatal("Upgrade must not be called for an invalid body")
					return nil, nil
				},
			},
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "fail-on-changes conflict propagates as 409",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				upgradeFn: func(context.Context, skills.UpgradeOptions) (*skills.UpgradeResult, error) {
					return nil, httperr.WithCode(assert.AnError, http.StatusConflict)
				},
			},
			body:       `{"project_root":"/tmp/proj","fail_on_changes":true}`,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := SkillsRouter(tt.service)
			req := httptest.NewRequest(http.MethodPost, "/upgrade", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantContains != "" {
				assert.Contains(t, rec.Body.String(), tt.wantContains)
			}
		})
	}
}

func TestUpgradeSkillsResponseIsValidJSON(t *testing.T) {
	t.Parallel()

	svc := &skillServiceWithSync{
		SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
		upgradeFn: func(context.Context, skills.UpgradeOptions) (*skills.UpgradeResult, error) {
			return &skills.UpgradeResult{Outcomes: []skills.UpgradeOutcome{
				{Name: "a", Status: skills.UpgradeStatusUpToDate},
			}}, nil
		},
	}
	router := SkillsRouter(svc)
	req := httptest.NewRequest(http.MethodPost, "/upgrade", bytes.NewBufferString(`{"project_root":"/tmp/proj"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result skills.UpgradeResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, "a", result.Outcomes[0].Name)
}
