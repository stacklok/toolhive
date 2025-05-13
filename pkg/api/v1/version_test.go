package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetVersion(t *testing.T) {
	t.Parallel()
	resp := httptest.NewRecorder()
	getVersion(resp, nil)
	require.Equal(t, http.StatusOK, resp.Code)
	var version versionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&version))
	require.Contains(t, version.Version, "build-")
}
