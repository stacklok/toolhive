package api

import (
	"encoding/json"
	"net/http"

	"github.com/stacklok/toolhive/docs/server"
)

// ServeOpenAPI writes the OpenAPI specification as JSON to the response.
// @Summary      Get OpenAPI specification
// @Description  Returns the OpenAPI specification for the API
// @Tags         system
// @Produce      json
// @Success      200  {object}  object  "OpenAPI specification"
// @Router       /api/openapi.json [get]
func ServeOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse the OpenAPI spec into a proper JSON object
	var openAPISpec map[string]interface{}
	if err := json.Unmarshal([]byte(server.SwaggerInfo.ReadDoc()), &openAPISpec); err != nil {
		http.Error(w, "Failed to parse OpenAPI specification", http.StatusInternalServerError)
		return
	}

	// Encode the JSON object
	if err := json.NewEncoder(w).Encode(openAPISpec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
