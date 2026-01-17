package migration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_migrateTelemetryConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		inputJSON  string
		outputJSON string // expected output JSON (empty if no migration expected)
		wantErr    bool
	}{
		{
			name: "migrates float64 samplingRate to string",
			inputJSON: `{
				"name": "test-workload",
				"telemetry_config": {
					"endpoint": "http://localhost:4318",
					"samplingRate": 0.1,
					"tracingEnabled": true
				},
				"other_field": "preserved"
			}`,
			outputJSON: `{
				"name": "test-workload",
				"telemetry_config": {
					"endpoint": "http://localhost:4318",
					"samplingRate": "0.1",
					"tracingEnabled": true
				},
				"other_field": "preserved"
			}`,
		},
		{
			name: "migrates integer samplingRate to string",
			inputJSON: `{
				"telemetry_config": {
					"samplingRate": 1
				}
			}`,
			outputJSON: `{
				"telemetry_config": {
					"samplingRate": "1"
				}
			}`,
		},
		{
			name: "does not migrate string samplingRate",
			inputJSON: `{
				"telemetry_config": {
					"samplingRate": "0.5"
				}
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when no telemetry_config",
			inputJSON: `{
				"name": "test-workload",
				"other_config": {
					"samplingRate": 0.1
				}
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when no samplingRate",
			inputJSON: `{
				"telemetry_config": {
					"endpoint": "http://localhost:4318",
					"tracingEnabled": true
				}
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "preserves all existing fields",
			inputJSON: `{
				"name": "workload",
				"image": "ghcr.io/test/image:v1",
				"telemetry_config": {
					"endpoint": "http://localhost:4318",
					"serviceName": "my-service",
					"serviceVersion": "1.0.0",
					"tracingEnabled": true,
					"metricsEnabled": false,
					"samplingRate": 0.05,
					"headers": {"x-api-key": "secret"},
					"insecure": true,
					"enablePrometheusMetricsPath": true,
					"environmentVariables": ["VAR1", "VAR2"]
				},
				"port": 8080,
				"env": {"KEY": "value"},
				"permissions": ["network"]
			}`,
			outputJSON: `{
				"name": "workload",
				"image": "ghcr.io/test/image:v1",
				"telemetry_config": {
					"endpoint": "http://localhost:4318",
					"serviceName": "my-service",
					"serviceVersion": "1.0.0",
					"tracingEnabled": true,
					"metricsEnabled": false,
					"samplingRate": "0.05",
					"headers": {"x-api-key": "secret"},
					"insecure": true,
					"enablePrometheusMetricsPath": true,
					"environmentVariables": ["VAR1", "VAR2"]
				},
				"port": 8080,
				"env": {"KEY": "value"},
				"permissions": ["network"]
			}`,
		},
		{
			name:       "returns error for empty input",
			inputJSON:  "",
			outputJSON: "",
			wantErr:    true,
		},
		{
			name:       "returns error for invalid JSON",
			inputJSON:  `{"invalid": json}`,
			outputJSON: "",
			wantErr:    true,
		},
		{
			name: "handles zero sampling rate",
			inputJSON: `{
				"telemetry_config": {
					"samplingRate": 0
				}
			}`,
			outputJSON: `{
				"telemetry_config": {
					"samplingRate": "0"
				}
			}`,
		},
		{
			name: "handles sampling rate with many decimal places",
			inputJSON: `{
				"telemetry_config": {
					"samplingRate": 0.123456789
				}
			}`,
			outputJSON: `{
				"telemetry_config": {
					"samplingRate": "0.123456789"
				}
			}`,
		},
		{
			name: "does not modify telemetry_config that is not an object",
			inputJSON: `{
				"telemetry_config": "invalid"
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "migrates middleware telemetry config with float64 samplingRate",
			inputJSON: `{
				"name": "test-workload",
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"endpoint": "http://localhost:4318",
								"samplingRate": 0.1,
								"tracingEnabled": true
							}
						}
					}
				]
			}`,
			outputJSON: `{
				"name": "test-workload",
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"endpoint": "http://localhost:4318",
								"samplingRate": "0.1",
								"tracingEnabled": true
							}
						}
					}
				]
			}`,
		},
		{
			name: "migrates middleware telemetry config with integer samplingRate",
			inputJSON: `{
				"name": "mermaid",
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": 1,
								"serviceName": "toolhive-mcp-proxy"
							},
							"server_name": "mermaid",
							"transport": "streamable-http"
						}
					}
				]
			}`,
			outputJSON: `{
				"name": "mermaid",
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": "1",
								"serviceName": "toolhive-mcp-proxy"
							},
							"server_name": "mermaid",
							"transport": "streamable-http"
						}
					}
				]
			}`,
		},
		{
			name: "does not migrate middleware telemetry config with string samplingRate",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": "0.5"
							}
						}
					}
				]
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "migrates both top-level telemetry_config and middleware configs",
			inputJSON: `{
				"name": "test-workload",
				"telemetry_config": {
					"samplingRate": 0.2
				},
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": 0.1
							}
						}
					}
				]
			}`,
			outputJSON: `{
				"name": "test-workload",
				"telemetry_config": {
					"samplingRate": "0.2"
				},
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": "0.1"
							}
						}
					}
				]
			}`,
		},
		{
			name: "migrates multiple telemetry middleware configs",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "auth",
						"parameters": {}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": 0.05
							}
						}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": 1
							}
						}
					}
				]
			}`,
			outputJSON: `{
				"middleware_configs": [
					{
						"type": "auth",
						"parameters": {}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": "0.05"
							}
						}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"samplingRate": "1"
							}
						}
					}
				]
			}`,
		},
		{
			name: "does not migrate non-telemetry middleware configs",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "auth",
						"parameters": {
							"config": {
								"samplingRate": 0.1
							}
						}
					}
				]
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when middleware configs is not an array",
			inputJSON: `{
				"middleware_configs": "invalid"
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when middleware has no parameters",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "telemetry"
					}
				]
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when middleware parameters has no config",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {}
					}
				]
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "does not migrate when middleware config has no samplingRate",
			inputJSON: `{
				"middleware_configs": [
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"endpoint": "http://localhost:4318"
							}
						}
					}
				]
			}`,
			outputJSON: "", // no migration
		},
		{
			name: "preserves complex middleware config structure",
			inputJSON: `{
				"name": "mermaid",
				"middleware_configs": [
					{
						"type": "auth",
						"parameters": {}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"endpoint": "",
								"serviceName": "toolhive-mcp-proxy",
								"serviceVersion": "v0.6.13",
								"tracingEnabled": true,
								"metricsEnabled": true,
								"samplingRate": 1,
								"headers": {},
								"insecure": true,
								"enablePrometheusMetricsPath": true,
								"environmentVariables": ["USER", "HOST"]
							},
							"server_name": "mermaid",
							"transport": "streamable-http"
						}
					}
				]
			}`,
			outputJSON: `{
				"name": "mermaid",
				"middleware_configs": [
					{
						"type": "auth",
						"parameters": {}
					},
					{
						"type": "telemetry",
						"parameters": {
							"config": {
								"endpoint": "",
								"serviceName": "toolhive-mcp-proxy",
								"serviceVersion": "v0.6.13",
								"tracingEnabled": true,
								"metricsEnabled": true,
								"samplingRate": "1",
								"headers": {},
								"insecure": true,
								"enablePrometheusMetricsPath": true,
								"environmentVariables": ["USER", "HOST"]
							},
							"server_name": "mermaid",
							"transport": "streamable-http"
						}
					}
				]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			migratedData, err := migrateTelemetryConfigJSON([]byte(tt.inputJSON))

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			wantMigrated := tt.outputJSON != ""

			if wantMigrated {
				require.NotNil(t, migratedData, "expected migration to occur")

				// Parse expected and actual output
				var expectedConfig, actualConfig map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(tt.outputJSON), &expectedConfig))
				require.NoError(t, json.Unmarshal(migratedData, &actualConfig))

				// Compare the full configs
				assert.Equal(t, expectedConfig, actualConfig)
			} else {
				assert.Nil(t, migratedData, "expected no migration")
			}
		})
	}
}

func Test_migrateTelemetryConfigJSON_Idempotent(t *testing.T) {
	t.Parallel()

	// After migration, running again should be a no-op
	inputJSON := `{
		"telemetry_config": {
			"samplingRate": 0.1
		}
	}`

	// First migration
	migratedData, err := migrateTelemetryConfigJSON([]byte(inputJSON))
	require.NoError(t, err)
	require.NotNil(t, migratedData, "expected migration to occur")

	// Second migration on the output should be a no-op (returns nil)
	secondMigration, err := migrateTelemetryConfigJSON(migratedData)
	require.NoError(t, err)
	assert.Nil(t, secondMigration, "second migration should be a no-op")
}
