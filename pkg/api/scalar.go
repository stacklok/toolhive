package api

import (
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
    <script id="api-reference" data-url="/api/openapi.json"></script>
    <script>
      const servers = [
        {
          name: "ToolHive",
          url: url,
          description: "ToolHive server",
        },
        {
          name: "Localhost",
          url: "http://localhost:8080",
          description: "Local development server",
        },
        {
          name: "Custom",
          url: "{custom-server-url}",
          description: "Custom server",
          variables: {
            "custom-server-url": {
              name: "Custom Server URL",
              type: "string",
              default: "http://localhost:8080",
            },
          },
        },
      ];

      // if dev and current url is localhost, remove localhost from servers
      if (window.location.hostname === "localhost") {
        servers = servers.filter(server => server.name !== "Localhost");
      }

      var configuration = {
        theme: "saturn",
        metaData: {
          title: "ToolHive API",
          description: "API Reference for ToolHive",
        },
        servers
      };

      document.getElementById('api-reference').dataset.configuration =
        JSON.stringify(configuration)
    </script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// ServeScalar serves the Scalar API reference page
func ServeScalar(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	if _, err := w.Write([]byte(scalarHTML)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
