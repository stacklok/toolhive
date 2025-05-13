// Package v1 contains the V1 API for ToolHive.
package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/versions"
)

// VersionRouter sets up the version route.
func VersionRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/", getVersion)
	return r
}

type versionResponse struct {
	Version string `json:"version"`
}

func getVersion(w http.ResponseWriter, _ *http.Request) {
	versionInfo := versions.GetVersionInfo()
	err := json.NewEncoder(w).Encode(versionResponse{Version: versionInfo.Version})
	if err != nil {
		http.Error(w, "Failed to marshal version info", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
}
