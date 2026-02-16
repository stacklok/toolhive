// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
)

func TestSkillsRouter(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		setupMock      func(*skillsmocks.MockSkillService)
		expectedStatus int
		expectedBody   string
	}{
		// listSkills
		{
			name:   "list skills success empty",
			method: "GET",
			path:   "/",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().List(gomock.Any(), skills.ListOptions{}).
					Return([]skills.InstalledSkill{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"skills":[]}`,
		},
		{
			name:   "list skills success with results",
			method: "GET",
			path:   "/",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().List(gomock.Any(), skills.ListOptions{}).
					Return([]skills.InstalledSkill{
						{
							Metadata:    skills.SkillMetadata{Name: "my-skill"},
							Scope:       skills.ScopeUser,
							Status:      skills.InstallStatusInstalled,
							InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
						},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"my-skill"`,
		},
		{
			name:   "list skills with scope filter",
			method: "GET",
			path:   "/?scope=project",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().List(gomock.Any(), skills.ListOptions{Scope: skills.ScopeProject}).
					Return([]skills.InstalledSkill{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"skills":[]}`,
		},
		{
			name:   "list skills error",
			method: "GET",
			path:   "/",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().List(gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// installSkill
		{
			name:   "install skill success",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-skill"}`,
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Install(gomock.Any(), skills.InstallOptions{Name: "my-skill"}).
					Return(&skills.InstallResult{
						Skill: skills.InstalledSkill{
							Metadata:    skills.SkillMetadata{Name: "my-skill"},
							Scope:       skills.ScopeUser,
							Status:      skills.InstallStatusPending,
							InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
						},
					}, nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"my-skill"`,
		},
		{
			name:           "install skill empty name",
			method:         "POST",
			path:           "/",
			body:           `{"name":""}`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "name is required",
		},
		{
			name:           "install skill missing name field",
			method:         "POST",
			path:           "/",
			body:           `{}`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "name is required",
		},
		{
			name:           "install skill malformed json",
			method:         "POST",
			path:           "/",
			body:           `{invalid`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:   "install skill already exists",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-skill"}`,
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Install(gomock.Any(), gomock.Any()).
					Return(nil, storage.ErrAlreadyExists)
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "resource already exists",
		},
		{
			name:   "install skill invalid name from service",
			method: "POST",
			path:   "/",
			body:   `{"name":"A"}`,
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Install(gomock.Any(), gomock.Any()).
					Return(nil, httperr.WithCode(fmt.Errorf("invalid skill name"), http.StatusBadRequest))
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid skill name",
		},
		// uninstallSkill
		{
			name:   "uninstall skill success",
			method: "DELETE",
			path:   "/my-skill",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Uninstall(gomock.Any(), skills.UninstallOptions{Name: "my-skill"}).
					Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:           "uninstall skill invalid name",
			method:         "DELETE",
			path:           "/A",
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid skill name",
		},
		{
			name:           "uninstall skill invalid scope",
			method:         "DELETE",
			path:           "/my-skill?scope=invalid",
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid scope",
		},
		{
			name:   "uninstall skill not found",
			method: "DELETE",
			path:   "/my-skill",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Uninstall(gomock.Any(), gomock.Any()).
					Return(storage.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "resource not found",
		},
		// getSkillInfo
		{
			name:   "get skill info found",
			method: "GET",
			path:   "/my-skill",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Info(gomock.Any(), skills.InfoOptions{Name: "my-skill"}).
					Return(&skills.SkillInfo{
						Metadata:  skills.SkillMetadata{Name: "my-skill"},
						Installed: true,
						InstalledSkill: &skills.InstalledSkill{
							Metadata: skills.SkillMetadata{Name: "my-skill"},
							Scope:    skills.ScopeUser,
							Status:   skills.InstallStatusInstalled,
						},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"installed":true`,
		},
		{
			name:   "get skill info not found",
			method: "GET",
			path:   "/my-skill",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Info(gomock.Any(), skills.InfoOptions{Name: "my-skill"}).
					Return(nil, storage.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "resource not found",
		},
		{
			name:           "get skill info invalid name",
			method:         "GET",
			path:           "/A",
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid skill name",
		},
		// getSkillInfo service error
		{
			name:   "get skill info service error",
			method: "GET",
			path:   "/my-skill",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Info(gomock.Any(), skills.InfoOptions{Name: "my-skill"}).
					Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// install with version and scope
		{
			name:   "install skill with version and scope",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-skill","version":"1.2.0","scope":"project"}`,
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Install(gomock.Any(), skills.InstallOptions{
					Name:    "my-skill",
					Version: "1.2.0",
					Scope:   skills.ScopeProject,
				}).Return(&skills.InstallResult{
					Skill: skills.InstalledSkill{
						Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.2.0"},
						Scope:    skills.ScopeProject,
						Status:   skills.InstallStatusPending,
					},
				}, nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"my-skill"`,
		},
		// uninstall with scope
		{
			name:   "uninstall skill with scope",
			method: "DELETE",
			path:   "/my-skill?scope=project",
			setupMock: func(svc *skillsmocks.MockSkillService) {
				svc.EXPECT().Uninstall(gomock.Any(), skills.UninstallOptions{
					Name:  "my-skill",
					Scope: skills.ScopeProject,
				}).Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		// validateSkill stub
		{
			name:           "validate skill returns 501",
			method:         "POST",
			path:           "/validate",
			body:           `{"path":"/tmp"}`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusNotImplemented,
			expectedBody:   "Not Implemented",
		},
		// buildSkill stub
		{
			name:           "build skill returns 501",
			method:         "POST",
			path:           "/build",
			body:           `{"path":"/tmp"}`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusNotImplemented,
			expectedBody:   "Not Implemented",
		},
		// pushSkill stub
		{
			name:           "push skill returns 501",
			method:         "POST",
			path:           "/push",
			body:           `{"reference":"ghcr.io/test/skill:v1"}`,
			setupMock:      func(_ *skillsmocks.MockSkillService) {},
			expectedStatus: http.StatusNotImplemented,
			expectedBody:   "Not Implemented",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockSvc := skillsmocks.NewMockSkillService(ctrl)
			tt.setupMock(mockSvc)

			router := chi.NewRouter()
			router.Mount("/", SkillsRouter(mockSvc))

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.expectedBody != "" {
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestListSkillsResponseFormat(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	ctrl := gomock.NewController(t)
	mockSvc := skillsmocks.NewMockSkillService(ctrl)

	mockSvc.EXPECT().List(gomock.Any(), gomock.Any()).
		Return([]skills.InstalledSkill{
			{
				Metadata:    skills.SkillMetadata{Name: "skill-one", Version: "1.0.0"},
				Scope:       skills.ScopeUser,
				Status:      skills.InstallStatusInstalled,
				InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		}, nil)

	router := chi.NewRouter()
	router.Mount("/", SkillsRouter(mockSvc))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp skillListResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Skills, 1)
	assert.Equal(t, "skill-one", resp.Skills[0].Metadata.Name)
	assert.Equal(t, skills.InstallStatusInstalled, resp.Skills[0].Status)
}
