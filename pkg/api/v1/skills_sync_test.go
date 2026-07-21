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

// skillServiceWithSync wraps a mocked SkillService and adds a Sync method, so
// SkillsRouter's opportunistic skillSyncer type assertion succeeds — the same
// shape skillsvc.New's concrete service has once it implements both.
type skillServiceWithSync struct {
	skills.SkillService
	syncFn func(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error)
}

func (s *skillServiceWithSync) Sync(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {
	return s.syncFn(ctx, opts)
}

func TestSyncSkillsEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		service      skills.SkillService
		body         string
		wantStatus   int
		wantContains string
	}{
		{
			name: "successful sync returns 200 with result",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				syncFn: func(_ context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {
					assert.Equal(t, "/tmp/proj", opts.ProjectRoot)
					assert.True(t, opts.Check)
					return &skills.SyncResult{AlreadyCurrent: []string{"my-skill"}}, nil
				},
			},
			body:         `{"project_root":"/tmp/proj","check":true}`,
			wantStatus:   http.StatusOK,
			wantContains: `"my-skill"`,
		},
		{
			name:       "service without Sync support returns 501",
			service:    skillsmocks.NewMockSkillService(gomock.NewController(t)),
			body:       `{"project_root":"/tmp/proj"}`,
			wantStatus: http.StatusNotImplemented,
		},
		{
			name: "invalid JSON body returns 400",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				syncFn: func(context.Context, skills.SyncOptions) (*skills.SyncResult, error) {
					t.Fatal("Sync must not be called for an invalid body")
					return nil, nil
				},
			},
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "sync error propagates with its status code",
			service: &skillServiceWithSync{
				SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
				syncFn: func(context.Context, skills.SyncOptions) (*skills.SyncResult, error) {
					return nil, httperr.WithCode(assert.AnError, http.StatusForbidden)
				},
			},
			body:       `{"project_root":"/tmp/proj"}`,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := SkillsRouter(tt.service)
			req := httptest.NewRequest(http.MethodPost, "/sync", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantContains != "" {
				assert.Contains(t, rec.Body.String(), tt.wantContains)
			}
		})
	}
}

func TestSyncSkillsResponseIsValidJSON(t *testing.T) {
	t.Parallel()

	svc := &skillServiceWithSync{
		SkillService: skillsmocks.NewMockSkillService(gomock.NewController(t)),
		syncFn: func(context.Context, skills.SyncOptions) (*skills.SyncResult, error) {
			return &skills.SyncResult{Installed: []string{"a"}, Drifted: []string{"b"}}, nil
		},
	}
	router := SkillsRouter(svc)
	req := httptest.NewRequest(http.MethodPost, "/sync", bytes.NewBufferString(`{"project_root":"/tmp/proj"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result skills.SyncResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, []string{"a"}, result.Installed)
	assert.Equal(t, []string{"b"}, result.Drifted)
}
