package api

import (
	"encoding/json"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
)

var openapiSpec *openapi3.T

func init() {
	openapiSpec = &openapi3.T{
		OpenAPI: "3.1.1",
		Info: &openapi3.Info{
			Title:          "ToolHive API",
			Description:    "A REST API for managing ToolHive servers and containers. This API allows you to create, manage, and monitor containerized servers in your ToolHive environment.",
			Version:        "1.0.0",
			TermsOfService: "https://github.com/stacklok/toolhive/blob/main/LICENSE",
			Contact: &openapi3.Contact{
				Name:  "ToolHive Support",
				URL:   "https://github.com/stacklok/toolhive",
				Email: "support@stacklok.com",
			},
			License: &openapi3.License{
				Name: "Apache 2.0",
				URL:  "http://www.apache.org/licenses/LICENSE-2.0.html",
			},
		},
		Servers: openapi3.Servers{
			&openapi3.Server{
				URL:         "http://localhost:8080",
				Description: "Local development server",
			},
		},
		Paths: openapi3.NewPaths(),
		Tags: []*openapi3.Tag{
			{
				Name:        "system",
				Description: "System management endpoints",
			},
			{
				Name:        "servers",
				Description: "Server management endpoints",
			},
		},
	}

	addServerPaths()
}

func addServerPaths() {
	openapiSpec.Paths.Set("/health", &openapi3.PathItem{
		Get: &openapi3.Operation{
			OperationID: "getHealth",
			Summary:     "Health check",
			Description: "Check if the API is healthy",
			Tags:        []string{"system"},
			Responses:   openapi3.NewResponses(),
		},
	})

	openapiSpec.Paths.Set("/api/v1beta/version", &openapi3.PathItem{
		Get: &openapi3.Operation{
			OperationID: "getVersion",
			Summary:     "Get version",
			Description: "Get the API version information",
			Tags:        []string{"system"},
			Responses:   openapi3.NewResponses(),
		},
	})

	openapiSpec.Paths.Set("/api/v1beta/servers", &openapi3.PathItem{
		Get: &openapi3.Operation{
			OperationID: "listServers",
			Summary:     "List all servers",
			Description: "Get a list of all running servers",
			Tags:        []string{"servers"},
			Parameters: []*openapi3.ParameterRef{
				{
					Value: &openapi3.Parameter{
						Name:        "all",
						In:          "query",
						Description: "Include stopped servers",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"boolean"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(),
		},
		Post: &openapi3.Operation{
			OperationID: "createServer",
			Summary:     "Create a new server",
			Description: "Create and start a new server",
			Tags:        []string{"servers"},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required: true,
					Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
						Type: &openapi3.Types{"object"},
						Properties: map[string]*openapi3.SchemaRef{
							"name": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"string"},
									Example: "puppeteer",
								},
							},
							"image": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"string"},
									Example: "mcp/puppeteer:latest",
								},
							},
							"cmd_arguments": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"array"},
									Items: &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type: &openapi3.Types{"string"},
										},
									},
									Example: []string{},
								},
							},
							"target_port": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"integer"},
									Example: 3000,
								},
							},
							"env_vars": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"array"},
									Items: &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type: &openapi3.Types{"string"},
										},
									},
									Example: []string{"DOCKER_CONTAINER=true"},
								},
							},
							"secrets": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"array"},
									Items: &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type: &openapi3.Types{"object"},
											Properties: map[string]*openapi3.SchemaRef{
												"name": {
													Value: &openapi3.Schema{
														Type: &openapi3.Types{"string"},
													},
												},
												"value": {
													Value: &openapi3.Schema{
														Type: &openapi3.Types{"string"},
													},
												},
											},
										},
									},
								},
							},
							"volumes": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"array"},
									Items: &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type: &openapi3.Types{"string"},
										},
									},
								},
							},
							"transport": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"string"},
									Example: "stdio",
								},
							},
							"authz_config": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"string"},
								},
							},
							"oidc": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"object"},
									Properties: map[string]*openapi3.SchemaRef{
										"issuer": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"audience": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"jwks_url": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"client_id": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
									},
								},
							},
							"permission_profile": {
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"string"},
								},
							},
						},
						Required: []string{"name", "image", "target_port"},
					}),
				},
			},
			Responses: openapi3.NewResponses(),
		},
	})

	openapiSpec.Paths.Set("/api/v1beta/servers/{name}", &openapi3.PathItem{
		Get: &openapi3.Operation{
			OperationID: "getServer",
			Summary:     "Get server details",
			Description: "Get details of a specific server",
			Tags:        []string{"servers"},
			Parameters: []*openapi3.ParameterRef{
				{
					Value: &openapi3.Parameter{
						Name:        "name",
						In:          "path",
						Required:    true,
						Description: "Server name",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"string"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(),
		},
		Delete: &openapi3.Operation{
			OperationID: "deleteServer",
			Summary:     "Delete a server",
			Description: "Delete a server",
			Tags:        []string{"servers"},
			Parameters: []*openapi3.ParameterRef{
				{
					Value: &openapi3.Parameter{
						Name:        "name",
						In:          "path",
						Required:    true,
						Description: "Server name",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"string"},
							},
						},
					},
				},
				{
					Value: &openapi3.Parameter{
						Name:        "force",
						In:          "query",
						Description: "Force deletion",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"boolean"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(),
		},
	})

	openapiSpec.Paths.Set("/api/v1beta/servers/{name}/stop", &openapi3.PathItem{
		Post: &openapi3.Operation{
			OperationID: "stopServer",
			Summary:     "Stop a server",
			Description: "Stop a running server",
			Tags:        []string{"servers"},
			Parameters: []*openapi3.ParameterRef{
				{
					Value: &openapi3.Parameter{
						Name:        "name",
						In:          "path",
						Required:    true,
						Description: "Server name",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"string"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(),
		},
	})

	openapiSpec.Paths.Set("/api/v1beta/servers/{name}/restart", &openapi3.PathItem{
		Post: &openapi3.Operation{
			OperationID: "restartServer",
			Summary:     "Restart a server",
			Description: "Restart a running server",
			Tags:        []string{"servers"},
			Parameters: []*openapi3.ParameterRef{
				{
					Value: &openapi3.Parameter{
						Name:        "name",
						In:          "path",
						Required:    true,
						Description: "Server name",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"string"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(),
		},
	})

	addResponses()
}

func addResponses() {
	healthCheck := openapiSpec.Paths.Find("/health").Get
	healthCheck.Responses.Set("204", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("No Content"),
		},
	})
	healthCheck.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	version := openapiSpec.Paths.Find("/api/v1beta/version").Get
	version.Responses.Set("200", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("OK"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"version": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	version.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	version.Responses.Set("500", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Internal Server Error"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	listServers := openapiSpec.Paths.Find("/api/v1beta/servers").Get
	listServers.Responses.Set("200", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("OK"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"servers": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"array"},
							Items: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"object"},
									Properties: map[string]*openapi3.SchemaRef{
										"id": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"name": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"image": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"status": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"state": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
										"created": {
											Value: &openapi3.Schema{
												Type:   &openapi3.Types{"string"},
												Format: "date-time",
											},
										},
										"labels": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"object"},
												AdditionalProperties: openapi3.AdditionalProperties{
													Has: boolPtr(true),
													Schema: &openapi3.SchemaRef{
														Value: &openapi3.Schema{
															Type: &openapi3.Types{"string"},
														},
													},
												},
											},
										},
										"ports": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"array"},
												Items: &openapi3.SchemaRef{
													Value: &openapi3.Schema{
														Type: &openapi3.Types{"object"},
														Properties: map[string]*openapi3.SchemaRef{
															"container_port": {
																Value: &openapi3.Schema{
																	Type: &openapi3.Types{"integer"},
																},
															},
															"host_port": {
																Value: &openapi3.Schema{
																	Type: &openapi3.Types{"integer"},
																},
															},
															"protocol": {
																Value: &openapi3.Schema{
																	Type: &openapi3.Types{"string"},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}),
		},
	})
	listServers.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	createServer := openapiSpec.Paths.Find("/api/v1beta/servers").Post
	createServer.Responses.Set("201", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Created"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"name": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"port": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"integer"},
						},
					},
				},
			}),
		},
	})
	createServer.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	createServer.Responses.Set("400", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Bad Request"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	createServer.Responses.Set("409", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Conflict"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	getServer := openapiSpec.Paths.Find("/api/v1beta/servers/{name}").Get
	getServer.Responses.Set("200", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("OK"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"id": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"name": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"image": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"status": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"state": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
					"created": {
						Value: &openapi3.Schema{
							Type:   &openapi3.Types{"string"},
							Format: "date-time",
						},
					},
					"labels": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"object"},
							AdditionalProperties: openapi3.AdditionalProperties{
								Has: boolPtr(true),
								Schema: &openapi3.SchemaRef{
									Value: &openapi3.Schema{
										Type: &openapi3.Types{"string"},
									},
								},
							},
						},
					},
					"ports": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"array"},
							Items: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"object"},
									Properties: map[string]*openapi3.SchemaRef{
										"container_port": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"integer"},
											},
										},
										"host_port": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"integer"},
											},
										},
										"protocol": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
									},
								},
							},
						},
					},
				},
			}),
		},
	})
	getServer.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	getServer.Responses.Set("404", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Not Found"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	deleteServer := openapiSpec.Paths.Find("/api/v1beta/servers/{name}").Delete
	deleteServer.Responses.Set("204", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("No Content"),
		},
	})
	deleteServer.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	deleteServer.Responses.Set("404", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Not Found"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	stopServer := openapiSpec.Paths.Find("/api/v1beta/servers/{name}/stop").Post
	stopServer.Responses.Set("204", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("No Content"),
		},
	})
	stopServer.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	stopServer.Responses.Set("404", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Not Found"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})

	restartServer := openapiSpec.Paths.Find("/api/v1beta/servers/{name}/restart").Post
	restartServer.Responses.Set("204", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("No Content"),
		},
	})
	restartServer.Responses.Set("405", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Method Not Allowed"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
	restartServer.Responses.Set("404", &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr("Not Found"),
			Content: openapi3.NewContentWithJSONSchema(&openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: map[string]*openapi3.SchemaRef{
					"error": {
						Value: &openapi3.Schema{
							Type: &openapi3.Types{"string"},
						},
					},
				},
			}),
		},
	})
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func ServeOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(openapiSpec)
}
