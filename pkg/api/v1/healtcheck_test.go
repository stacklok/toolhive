package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetHealthcheck(t *testing.T) {
	t.Parallel()
	resp := httptest.NewRecorder()
	getHealthcheck(resp, nil)
	require.Equal(t, http.StatusNoContent, resp.Code)
	require.Empty(t, resp.Body)
}
