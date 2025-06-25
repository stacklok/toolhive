package v1

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// LogsRoutes defines the routes for the workload logs API.
type LogsRoutes struct {
	manager workloads.Manager
}

// LogsRouter creates a new router for the workload logs API.
func LogsRouter(
	manager workloads.Manager,
) http.Handler {
	routes := LogsRoutes{
		manager: manager,
	}

	r := chi.NewRouter()
	r.Get("/logs", routes.getLogsForWorkload)
	return r
}

// getLogsForWorkload
//
// @Summary      Get logs for a specific workload
// @Description  Retrieve at most 100 lines of logs for a specific workload by name.
// @Tags         logs
// @Produce      text/plain
// @Param        name  path      string  true  "Workload name"
// @Success      200   {string}  string  "Logs for the specified workload"
// @Failure      404   {string}  string  "Not Found"
// @Router       /api/v1beta/workloads/{name}/logs [get]
func (l *LogsRoutes) getLogsForWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	logs, err := l.manager.GetLogs(ctx, name, false)
	if err != nil {
		if errors.Is(err, workloads.ErrContainerNotFound) {
			http.Error(w, "Workload not found", http.StatusNotFound)
			return
		}
		logger.Errorf("Failed to get logs: %v", err)
		http.Error(w, "Failed to get logs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, err = w.Write([]byte(logs))
	if err != nil {
		logger.Errorf("Failed to write logs response: %v", err)
		http.Error(w, "Failed to write logs response", http.StatusInternalServerError)
		return
	}
}
