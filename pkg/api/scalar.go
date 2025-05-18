package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const scalarHTML = `<!doctype html>
<html>
  <head>
    <title>ToolHive API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" type="application/json">
    %s
    </script>
    <script>
      var configuration = {
        theme: "saturn",
        metaData: {
          title: "ToolHive API",
          description: "API Reference for ToolHive",
        },
        servers: [
          {
            name: "Development",
            url: "http://localhost:8080",
            description: "Local development server"
          }
        ],
        showServers: true,
        allowCustomServers: true
      }

      document.getElementById('api-reference').dataset.configuration =
        JSON.stringify(configuration)
    </script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// ServeScalar serves the Scalar API reference page
func ServeScalar(w http.ResponseWriter, _ *http.Request) {
	// Get the OpenAPI specification
	spec, err := json.Marshal(openapiSpec)
	if err != nil {
		http.Error(w, "Failed to marshal OpenAPI specification", http.StatusInternalServerError)
		return
	}

	// Insert the OpenAPI specification into the HTML template
	html := fmt.Sprintf(scalarHTML, spec)

	w.Header().Set("Content-Type", "text/html")
	if _, err := w.Write([]byte(html)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
