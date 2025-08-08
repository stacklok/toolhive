package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestGroupsRouter(t *testing.T) {
	t.Parallel()

	// Initialize logger to prevent panic
	logger.Initialize()

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		setupMock      func(*groupsmocks.MockManager, *workloadsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:   "list groups success",
			method: "GET",
			path:   "/",
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
					{Name: "group1", RegisteredClients: []string{}},
					{Name: "group2", RegisteredClients: []string{}},
				}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"groups":[{"name":"group1", "registered_clients": []},{"name":"group2", "registered_clients": []}]}`,
		},
		{
			name:   "list groups error",
			method: "GET",
			path:   "/",
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().List(gomock.Any()).Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Failed to list groups",
		},
		{
			name:   "create group success",
			method: "POST",
			path:   "/",
			body:   `{"name":"newgroup"}`,
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().Create(gomock.Any(), "newgroup").Return(nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `{"name":"newgroup"}`,
		},
		{
			name:   "create group empty name",
			method: "POST",
			path:   "/",
			body:   `{"name":""}`,
			setupMock: func(_ *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				// No mock setup needed as validation happens before manager call
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "group name cannot be empty or consist only of whitespace",
		},
		{
			name:   "create group already exists",
			method: "POST",
			path:   "/",
			body:   `{"name":"existinggroup"}`,
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().Create(gomock.Any(), "existinggroup").Return(errors.NewGroupAlreadyExistsError("group 'existinggroup' already exists", nil))
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "group_already_exists: group 'existinggroup' already exists",
		},
		{
			name:   "create group invalid json",
			method: "POST",
			path:   "/",
			body:   `{"name":`,
			setupMock: func(_ *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				// No mock setup needed as JSON parsing fails first
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request body",
		},
		{
			name:   "get group success",
			method: "GET",
			path:   "/testgroup",
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "testgroup").
					Return(&groups.Group{Name: "testgroup", RegisteredClients: []string{}}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"name":"testgroup", "registered_clients": []}`,
		},
		{
			name:   "get group not found",
			method: "GET",
			path:   "/nonexistent",
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "nonexistent").Return(nil, fmt.Errorf("group not found"))
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Group not found",
		},
		{
			name:   "delete group success",
			method: "DELETE",
			path:   "/testgroup",
			setupMock: func(gm *groupsmocks.MockManager, wm *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				wm.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{}, nil)
				gm.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
		{
			name:   "delete group not found",
			method: "DELETE",
			path:   "/nonexistent",
			setupMock: func(gm *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "nonexistent").Return(false, nil)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Group not found",
		},
		{
			name:   "delete default group protected",
			method: "DELETE",
			path:   "/default",
			setupMock: func(_ *groupsmocks.MockManager, _ *workloadsmocks.MockManager) {
				// No mock setup needed as validation happens before manager call
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Cannot delete the default group",
		},
		{
			name:   "delete group with workloads flag true",
			method: "DELETE",
			path:   "/testgroup?with-workloads=true",
			setupMock: func(gm *groupsmocks.MockManager, wm *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				wm.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{}, nil)
				gm.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
		{
			name:   "delete group with workloads flag false",
			method: "DELETE",
			path:   "/testgroup?with-workloads=false",
			setupMock: func(gm *groupsmocks.MockManager, wm *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				wm.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{}, nil)
				gm.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
		{
			name:   "delete group without workloads flag (default behavior)",
			method: "DELETE",
			path:   "/testgroup",
			setupMock: func(gm *groupsmocks.MockManager, wm *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				wm.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{}, nil)
				gm.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
		{
			name:   "delete group with no workloads",
			method: "DELETE",
			path:   "/testgroup",
			setupMock: func(gm *groupsmocks.MockManager, wm *workloadsmocks.MockManager) {
				gm.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				wm.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{}, nil)
				gm.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock controller
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create mock managers
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			if tt.setupMock != nil {
				tt.setupMock(mockGroupManager, mockWorkloadManager)
			}

			// Create router
			router := GroupsRouter(mockGroupManager, mockWorkloadManager)

			// Create request
			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}

			// Set up chi context for path parameters
			rctx := chi.NewRouteContext()
			if strings.Contains(tt.path, "/") && !strings.HasSuffix(tt.path, "/") {
				parts := strings.Split(strings.TrimPrefix(tt.path, "/"), "/")
				if len(parts) > 0 {
					rctx.URLParams.Add("name", parts[0])
				}
			}
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			// Create response recorder
			w := httptest.NewRecorder()

			// Serve request
			router.ServeHTTP(w, req)

			// Assert status code
			assert.Equal(t, tt.expectedStatus, w.Code)

			// Assert response body
			if tt.expectedBody != "" {
				// For error responses, check if it's plain text
				if tt.expectedStatus >= 400 {
					assert.Contains(t, w.Body.String(), tt.expectedBody)
				} else {
					assert.JSONEq(t, tt.expectedBody, w.Body.String())
				}
			} else {
				assert.Empty(t, w.Body.String())
			}
		})
	}
}

func TestGroupsRouter_Integration(t *testing.T) {
	t.Parallel()

	// Test with real managers (integration test)
	groupManager, err := groups.NewManager()
	if err != nil {
		t.Skip("Skipping integration test: failed to create group manager")
	}

	workloadManager, err := workloads.NewManager(context.Background())
	if err != nil {
		t.Skip("Skipping integration test: failed to create workload manager")
	}

	router := GroupsRouter(groupManager, workloadManager)

	// Test creating a group
	t.Run("create and list group", func(t *testing.T) {
		t.Parallel()

		// Create a test group
		createReq := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"testgroup-api"}`))
		createReq.Header.Set("Content-Type", "application/json")
		createW := httptest.NewRecorder()

		router.ServeHTTP(createW, createReq)
		assert.Equal(t, http.StatusCreated, createW.Code)

		// List groups
		listReq := httptest.NewRequest("GET", "/", nil)
		listW := httptest.NewRecorder()

		router.ServeHTTP(listW, listReq)
		assert.Equal(t, http.StatusOK, listW.Code)

		var response groupListResponse
		err := json.NewDecoder(listW.Body).Decode(&response)
		assert.NoError(t, err)

		// Find our test group
		found := false
		for _, group := range response.Groups {
			if group.Name == "testgroup-api" {
				found = true
				break
			}
		}
		assert.True(t, found, "Test group should be in the list")

		// Clean up - delete the group
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", "testgroup-api")
		deleteReq := httptest.NewRequest("DELETE", "/testgroup-api", nil)
		deleteReq = deleteReq.WithContext(context.WithValue(deleteReq.Context(), chi.RouteCtxKey, rctx))
		deleteW := httptest.NewRecorder()

		router.ServeHTTP(deleteW, deleteReq)
		assert.Equal(t, http.StatusNoContent, deleteW.Code)
	})
}
