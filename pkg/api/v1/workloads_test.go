package v1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
)

func TestGetWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		workloadName   string
		setupMock      func(*workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Workload not found",
		},
		{
			name:         "invalid workload name",
			workloadName: "invalid-name",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "invalid-name").
					Return(core.Workload{}, wt.ErrInvalidWorkloadName)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid workload name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(mockWorkloadManager, mockRuntime, mockGroupManager)
			mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
			workloadService := NewWorkloadService(mockWorkloadManager, mockGroupManager, mockSecretsProvider, mockRuntime, false)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				workloadService:  workloadService,
				debugMode:        false,
			}

			req := httptest.NewRequest("GET", "/"+tt.workloadName, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			routes.getWorkload(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestCreateWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		requestBody    string
		setupMock      func(*workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "invalid JSON",
			requestBody:    `{"name":`,
			setupMock:      func(_ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Failed to decode request",
		},
		{
			name:        "workload already exists",
			requestBody: `{"name": "existing-workload", "image": "test-image"}`,
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "existing-workload").Return(true, nil)
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "Workload with name existing-workload already exists",
		},
		{
			name:        "invalid proxy mode",
			requestBody: `{"name": "test-workload", "image": "test-image", "proxy_mode": "invalid"}`,
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid proxy_mode: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(mockWorkloadManager, mockRuntime, mockGroupManager)
			mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
			workloadService := NewWorkloadService(mockWorkloadManager, mockGroupManager, mockSecretsProvider, mockRuntime, false)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				workloadService:  workloadService,
				debugMode:        false,
			}

			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			routes.createWorkload(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		workloadName   string
		requestBody    string
		setupMock      func(*workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "invalid JSON",
			workloadName:   "test-workload",
			requestBody:    `{"image":`,
			setupMock:      func(_ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid JSON",
		},
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, fmt.Errorf("workload not found"))
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Workload not found",
		},
		{
			name:         "update workload fails - config build error",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				// No UpdateWorkload call expected since config build fails first
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Failed to update workload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(mockWorkloadManager, mockRuntime, mockGroupManager)
			mockSecretsProvider := secretsmocks.NewMockProvider(ctrl)
			workloadService := NewWorkloadService(mockWorkloadManager, mockGroupManager, mockSecretsProvider, mockRuntime, false)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				workloadService:  workloadService,
				debugMode:        false,
			}

			req := httptest.NewRequest("POST", "/"+tt.workloadName+"/edit", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			routes.updateWorkload(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}
