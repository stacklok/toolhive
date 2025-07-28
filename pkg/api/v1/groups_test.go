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
	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
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
		setupMock      func(*mocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:   "list groups success",
			method: "GET",
			path:   "/",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*groups.Group{
					{Name: "group1"},
					{Name: "group2"},
				}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"groups":[{"name":"group1"},{"name":"group2"}]}`,
		},
		{
			name:   "list groups error",
			method: "GET",
			path:   "/",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Failed to list groups",
		},
		{
			name:   "create group success",
			method: "POST",
			path:   "/",
			body:   `{"name":"newgroup"}`,
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Create(gomock.Any(), "newgroup").Return(nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `{"name":"newgroup"}`,
		},
		{
			name:   "create group empty name",
			method: "POST",
			path:   "/",
			body:   `{"name":""}`,
			setupMock: func(_ *mocks.MockManager) {
				// No mock setup needed as validation happens before manager call
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Group name is required",
		},
		{
			name:   "create group already exists",
			method: "POST",
			path:   "/",
			body:   `{"name":"existinggroup"}`,
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Create(gomock.Any(), "existinggroup").Return(errors.NewGroupAlreadyExistsError("group 'existinggroup' already exists", nil))
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "group_already_exists: group 'existinggroup' already exists",
		},
		{
			name:   "create group invalid json",
			method: "POST",
			path:   "/",
			body:   `{"name":`,
			setupMock: func(_ *mocks.MockManager) {
				// No mock setup needed as JSON parsing fails first
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request body",
		},
		{
			name:   "get group success",
			method: "GET",
			path:   "/testgroup",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "testgroup").Return(&groups.Group{Name: "testgroup"}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"name":"testgroup"}`,
		},
		{
			name:   "get group not found",
			method: "GET",
			path:   "/nonexistent",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "nonexistent").Return(nil, fmt.Errorf("group not found"))
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Group not found",
		},
		{
			name:   "delete group success",
			method: "DELETE",
			path:   "/testgroup",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				m.EXPECT().Delete(gomock.Any(), "testgroup").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
			expectedBody:   "",
		},
		{
			name:   "delete group not found",
			method: "DELETE",
			path:   "/nonexistent",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Exists(gomock.Any(), "nonexistent").Return(false, nil)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Group not found",
		},
		{
			name:   "list workloads in group success",
			method: "GET",
			path:   "/testgroup/workloads",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Exists(gomock.Any(), "testgroup").Return(true, nil)
				m.EXPECT().ListWorkloadsInGroup(gomock.Any(), "testgroup").Return([]string{"workload1", "workload2"}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"group_name":"testgroup","workload_names":["workload1","workload2"]}`,
		},
		{
			name:   "list workloads in group not found",
			method: "GET",
			path:   "/nonexistent/workloads",
			setupMock: func(m *mocks.MockManager) {
				m.EXPECT().Exists(gomock.Any(), "nonexistent").Return(false, nil)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Group not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock controller
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create mock manager
			mockManager := mocks.NewMockManager(ctrl)
			if tt.setupMock != nil {
				tt.setupMock(mockManager)
			}

			// Create router
			router := GroupsRouter(mockManager)

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

	// Test with real group manager (integration test)
	groupManager, err := groups.NewManager()
	if err != nil {
		t.Skip("Skipping integration test: failed to create group manager")
	}

	router := GroupsRouter(groupManager)

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
