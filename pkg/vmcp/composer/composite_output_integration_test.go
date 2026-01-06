package composer

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestCompositeToolWithOutputConfig_SimpleTypes tests composite tools with simple output types.
func TestCompositeToolWithOutputConfig_SimpleTypes(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that calls a backend tool and constructs typed output
	workflow := &WorkflowDefinition{
		Name:        "data_processing",
		Description: "Process data with typed outputs",
		Steps: []WorkflowStep{
			toolStep("fetch", "data.fetch", map[string]any{
				"source": "{{.params.source}}",
			}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"message": {
					Type:        "string",
					Description: "Result message",
					Value:       "{{.steps.fetch.output.text}}",
				},
				"count": {
					Type:        "integer",
					Description: "Item count",
					Value:       "{{.steps.fetch.output.count}}",
				},
				"success": {
					Type:        "boolean",
					Description: "Success flag",
					Value:       "{{.steps.fetch.output.success}}",
				},
				"score": {
					Type:        "number",
					Description: "Quality score",
					Value:       "{{.steps.fetch.output.score}}",
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("data.fetch", map[string]any{"source": "api"}, map[string]any{
		"text":    "Data fetched successfully",
		"count":   "42",
		"success": "true",
		"score":   "95.5",
	})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, map[string]any{"source": "api"})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify output has correct types
	assert.Equal(t, "Data fetched successfully", result.Output["message"])
	assert.Equal(t, int64(42), result.Output["count"])
	assert.Equal(t, true, result.Output["success"])
	assert.Equal(t, 95.5, result.Output["score"])
}

// TestCompositeToolWithOutputConfig_NestedObjects tests nested object construction.
func TestCompositeToolWithOutputConfig_NestedObjects(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow with nested object output
	workflow := &WorkflowDefinition{
		Name:        "user_info",
		Description: "Fetch and structure user information",
		Steps: []WorkflowStep{
			toolStep("fetch_user", "user.get", map[string]any{
				"user_id": "{{.params.user_id}}",
			}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"user": {
					Type:        "object",
					Description: "User information",
					Properties: map[string]config.OutputProperty{
						"id": {
							Type:        "string",
							Description: "User ID",
							Value:       "{{.steps.fetch_user.output.id}}",
						},
						"name": {
							Type:        "string",
							Description: "User name",
							Value:       "{{.steps.fetch_user.output.name}}",
						},
						"stats": {
							Type:        "object",
							Description: "User statistics",
							Properties: map[string]config.OutputProperty{
								"posts": {
									Type:        "integer",
									Description: "Number of posts",
									Value:       "{{.steps.fetch_user.output.post_count}}",
								},
								"followers": {
									Type:        "integer",
									Description: "Number of followers",
									Value:       "{{.steps.fetch_user.output.follower_count}}",
								},
							},
						},
					},
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("user.get", map[string]any{"user_id": "123"}, map[string]any{
		"id":             "123",
		"name":           "Alice",
		"post_count":     "45",
		"follower_count": "1200",
	})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, map[string]any{"user_id": "123"})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify nested structure
	user, ok := result.Output["user"].(map[string]any)
	require.True(t, ok, "user should be a map")
	assert.Equal(t, "123", user["id"])
	assert.Equal(t, "Alice", user["name"])

	stats, ok := user["stats"].(map[string]any)
	require.True(t, ok, "stats should be a map")
	assert.Equal(t, int64(45), stats["posts"])
	assert.Equal(t, int64(1200), stats["followers"])
}

// TestCompositeToolWithOutputConfig_MultiStepAggregation tests aggregating data from multiple steps.
func TestCompositeToolWithOutputConfig_MultiStepAggregation(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that calls multiple backend tools and aggregates results
	workflow := &WorkflowDefinition{
		Name:        "issue_workflow",
		Description: "Create issue and add label",
		Steps: []WorkflowStep{
			toolStep("create", "github.create_issue", map[string]any{
				"title": "{{.params.title}}",
				"body":  "{{.params.body}}",
			}),
			toolStepWithDeps("label", "github.add_label", map[string]any{
				"issue": "{{.steps.create.output.number}}",
				"label": "{{.params.label}}",
			}, []string{"create"}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"issue_number": {
					Type:        "integer",
					Description: "Created issue number",
					Value:       "{{.steps.create.output.number}}",
				},
				"issue_url": {
					Type:        "string",
					Description: "Issue URL",
					Value:       "{{.steps.create.output.url}}",
				},
				"label_added": {
					Type:        "boolean",
					Description: "Whether label was added",
					Value:       "{{.steps.label.output.success}}",
				},
				"label_name": {
					Type:        "string",
					Description: "Applied label",
					Value:       "{{.params.label}}",
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("github.create_issue",
		map[string]any{"title": "Bug report", "body": "Something is broken"},
		map[string]any{"number": 456, "url": "https://github.com/org/repo/issues/456"})

	te.expectToolCallWithAnyArgs("github.add_label",
		map[string]any{"success": "true"})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, map[string]any{
		"title": "Bug report",
		"body":  "Something is broken",
		"label": "bug",
	})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify aggregated output
	assert.Equal(t, int64(456), result.Output["issue_number"])
	assert.Equal(t, "https://github.com/org/repo/issues/456", result.Output["issue_url"])
	assert.Equal(t, true, result.Output["label_added"])
	assert.Equal(t, "bug", result.Output["label_name"])
}

// TestCompositeToolWithOutputConfig_DefaultValues tests default value fallback.
func TestCompositeToolWithOutputConfig_DefaultValues(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow with default values for missing fields
	workflow := &WorkflowDefinition{
		Name:        "fetch_with_defaults",
		Description: "Fetch data with fallback defaults",
		Steps: []WorkflowStep{
			toolStep("fetch", "data.get", map[string]any{
				"id": "{{.params.id}}",
			}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"id": {
					Type:        "string",
					Description: "Record ID",
					Value:       "{{.steps.fetch.output.id}}",
				},
				"status": {
					Type:        "string",
					Description: "Status",
					Value:       "{{.steps.fetch.output.status}}",
					Default:     config.RawJSON{Raw: []byte(`"unknown"`)},
				},
				"priority": {
					Type:        "integer",
					Description: "Priority level",
					Value:       "{{.steps.fetch.output.priority}}",
					Default:     config.RawJSON{Raw: []byte(`1`)},
				},
				"enabled": {
					Type:        "boolean",
					Description: "Enabled flag",
					Value:       "{{.steps.fetch.output.enabled}}",
					Default:     config.RawJSON{Raw: []byte(`false`)},
				},
			},
		},
	}

	// Setup expectations - backend returns partial data (missing status, priority, enabled)
	te.expectToolCall("data.get", map[string]any{"id": "rec123"}, map[string]any{
		"id": "rec123",
		// status, priority, enabled are missing
	})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, map[string]any{"id": "rec123"})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify output with defaults applied
	assert.Equal(t, "rec123", result.Output["id"])
	assert.Equal(t, "unknown", result.Output["status"])
	assert.Equal(t, int64(1), result.Output["priority"])
	assert.Equal(t, false, result.Output["enabled"])
}

// TestCompositeToolWithOutputConfig_JSONDeserialization tests JSON object/array deserialization.
func TestCompositeToolWithOutputConfig_JSONDeserialization(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that receives JSON strings and deserializes them
	workflow := &WorkflowDefinition{
		Name:        "json_processing",
		Description: "Process JSON data",
		Steps: []WorkflowStep{
			toolStep("fetch", "api.call", map[string]any{
				"endpoint": "{{.params.endpoint}}",
			}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"metadata": {
					Type:        "object",
					Description: "Metadata object",
					Value:       "{{.steps.fetch.output.metadata_json}}",
				},
				"tags": {
					Type:        "array",
					Description: "Tags array",
					Value:       "{{.steps.fetch.output.tags_json}}",
				},
			},
		},
	}

	// Setup expectations - backend returns JSON strings
	te.expectToolCall("api.call", map[string]any{"endpoint": "/data"}, map[string]any{
		"metadata_json": `{"version": "1.0", "author": "system"}`,
		"tags_json":     `["important", "reviewed", "approved"]`,
	})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, map[string]any{"endpoint": "/data"})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify deserialized object
	metadata, ok := result.Output["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be deserialized to map")
	assert.Equal(t, "1.0", metadata["version"])
	assert.Equal(t, "system", metadata["author"])

	// Verify deserialized array
	tags, ok := result.Output["tags"].([]any)
	require.True(t, ok, "tags should be deserialized to array")
	assert.Len(t, tags, 3)
	assert.Equal(t, "important", tags[0])
	assert.Equal(t, "reviewed", tags[1])
	assert.Equal(t, "approved", tags[2])
}

// TestCompositeToolWithOutputConfig_RequiredFields tests required field validation.
func TestCompositeToolWithOutputConfig_RequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		outputCfg    *config.OutputConfig
		stepOutput   map[string]any
		shouldFail   bool
		missingField string
	}{
		{
			name: "all required fields present",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"id": {
						Type:        "string",
						Description: "ID",
						Value:       "{{.steps.fetch.output.id}}",
					},
					"name": {
						Type:        "string",
						Description: "Name",
						Value:       "{{.steps.fetch.output.name}}",
					},
				},
				Required: []string{"id", "name"},
			},
			stepOutput: map[string]any{
				"id":   "123",
				"name": "Test",
			},
			shouldFail: false,
		},
		{
			name: "missing required field without default",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"id": {
						Type:        "string",
						Description: "ID",
						Value:       "{{.steps.fetch.output.id}}",
					},
					// name property is not in the output config at all
				},
				Required: []string{"id", "name"},
			},
			stepOutput: map[string]any{
				"id": "123",
			},
			shouldFail:   true,
			missingField: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)

			workflow := &WorkflowDefinition{
				Name:        "validation_test",
				Description: "Test required field validation",
				Steps: []WorkflowStep{
					toolStep("fetch", "data.get", nil),
				},
				Output: tt.outputCfg,
			}

			// Setup expectations
			te.expectToolCall("data.get", nil, tt.stepOutput)

			// Execute workflow
			result, err := execute(t, te.Engine, workflow, nil)

			if tt.shouldFail {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.missingField)
				assert.Equal(t, WorkflowStatusFailed, result.Status)
			} else {
				require.NoError(t, err)
				assert.Equal(t, WorkflowStatusCompleted, result.Status)
			}
		})
	}
}

// TestCompositeToolWithOutputConfig_TypeCoercionErrors tests error handling for invalid type coercion.
func TestCompositeToolWithOutputConfig_TypeCoercionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		propDef    config.OutputProperty
		stepOutput map[string]any
		shouldFail bool
	}{
		{
			name: "invalid integer coercion without default",
			propDef: config.OutputProperty{
				Type:        "integer",
				Description: "Count",
				Value:       "{{.steps.fetch.output.count}}",
			},
			stepOutput: map[string]any{
				"count": "not_a_number",
			},
			shouldFail: true,
		},
		{
			name: "invalid integer coercion with default",
			propDef: config.OutputProperty{
				Type:        "integer",
				Description: "Count",
				Value:       "{{.steps.fetch.output.count}}",
				Default:     config.RawJSON{Raw: []byte(`99`)},
			},
			stepOutput: map[string]any{
				"count": "not_a_number",
			},
			shouldFail: false, // Should use default value
		},
		{
			name: "invalid boolean coercion without default",
			propDef: config.OutputProperty{
				Type:        "boolean",
				Description: "Flag",
				Value:       "{{.steps.fetch.output.flag}}",
			},
			stepOutput: map[string]any{
				"flag": "maybe",
			},
			shouldFail: true,
		},
		{
			name: "invalid JSON for object without default",
			propDef: config.OutputProperty{
				Type:        "object",
				Description: "Data",
				Value:       "{{.steps.fetch.output.data}}",
			},
			stepOutput: map[string]any{
				"data": "not valid json",
			},
			shouldFail: true,
		},
		{
			name: "invalid JSON for object with default",
			propDef: config.OutputProperty{
				Type:        "object",
				Description: "Data",
				Value:       "{{.steps.fetch.output.data}}",
				Default:     config.RawJSON{Raw: []byte(`{"fallback": true}`)},
			},
			stepOutput: map[string]any{
				"data": "not valid json",
			},
			shouldFail: false, // Should use default value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)

			workflow := &WorkflowDefinition{
				Name:        "coercion_test",
				Description: "Test type coercion error handling",
				Steps: []WorkflowStep{
					toolStep("fetch", "data.get", nil),
				},
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"value": tt.propDef,
					},
				},
			}

			// Setup expectations
			te.expectToolCall("data.get", nil, tt.stepOutput)

			// Execute workflow
			result, err := execute(t, te.Engine, workflow, nil)

			if tt.shouldFail {
				require.Error(t, err)
				assert.Equal(t, WorkflowStatusFailed, result.Status)
			} else {
				require.NoError(t, err)
				assert.Equal(t, WorkflowStatusCompleted, result.Status)
				// Verify default value was used
				if !tt.propDef.Default.IsEmpty() {
					assert.NotNil(t, result.Output["value"])
				}
			}
		})
	}
}

// TestCompositeToolWithOutputConfig_ConditionalStepsWithOutput tests output from conditionally skipped steps.
func TestCompositeToolWithOutputConfig_ConditionalStepsWithOutput(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow with a conditional step and output referencing it
	workflow := &WorkflowDefinition{
		Name:        "conditional_workflow",
		Description: "Workflow with conditional step",
		Steps: []WorkflowStep{
			toolStep("always", "data.fetch", nil),
			{
				ID:        "conditional",
				Type:      StepTypeTool,
				Tool:      "data.process",
				Condition: "{{if eq .params.process true}}true{{else}}false{{end}}",
			},
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"fetched": {
					Type:        "string",
					Description: "Fetched data",
					Value:       "{{.steps.always.output.data}}",
				},
				"processed": {
					Type:        "string",
					Description: "Processed data",
					Value:       "{{.steps.conditional.output.result}}",
					Default:     config.RawJSON{Raw: []byte(`"not_processed"`)},
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("data.fetch", nil, map[string]any{"data": "raw_data"})
	// data.process should NOT be called (condition is false)

	// Execute workflow with process=false
	result, err := execute(t, te.Engine, workflow, map[string]any{"process": false})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["always"].Status)
	assert.Equal(t, StepStatusSkipped, result.Steps["conditional"].Status)

	// Verify output uses default for skipped step
	assert.Equal(t, "raw_data", result.Output["fetched"])
	assert.Equal(t, "not_processed", result.Output["processed"])
}

// TestCompositeToolWithOutputConfig_ParallelStepsAggregation tests aggregating output from parallel steps.
func TestCompositeToolWithOutputConfig_ParallelStepsAggregation(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow with parallel independent steps
	workflow := &WorkflowDefinition{
		Name:        "parallel_aggregation",
		Description: "Aggregate results from parallel steps",
		Steps: []WorkflowStep{
			toolStep("fetch_users", "api.users", nil),
			toolStep("fetch_posts", "api.posts", nil),
			toolStep("fetch_comments", "api.comments", nil),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"stats": {
					Type:        "object",
					Description: "Aggregated statistics",
					Properties: map[string]config.OutputProperty{
						"user_count": {
							Type:        "integer",
							Description: "Total users",
							Value:       "{{.steps.fetch_users.output.count}}",
						},
						"post_count": {
							Type:        "integer",
							Description: "Total posts",
							Value:       "{{.steps.fetch_posts.output.count}}",
						},
						"comment_count": {
							Type:        "integer",
							Description: "Total comments",
							Value:       "{{.steps.fetch_comments.output.count}}",
						},
					},
				},
			},
		},
	}

	// Setup expectations for parallel calls
	te.expectToolCall("api.users", nil, map[string]any{"count": "150"})
	te.expectToolCall("api.posts", nil, map[string]any{"count": "450"})
	te.expectToolCall("api.comments", nil, map[string]any{"count": "1200"})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, nil)

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify all steps completed (potentially in parallel)
	assert.Equal(t, StepStatusCompleted, result.Steps["fetch_users"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["fetch_posts"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["fetch_comments"].Status)

	// Verify aggregated output
	stats, ok := result.Output["stats"].(map[string]any)
	require.True(t, ok, "stats should be a map")
	assert.Equal(t, int64(150), stats["user_count"])
	assert.Equal(t, int64(450), stats["post_count"])
	assert.Equal(t, int64(1200), stats["comment_count"])
}

// TestCompositeToolWithOutputConfig_ErrorHandlingWithRetry tests output when a step succeeds after retry.
func TestCompositeToolWithOutputConfig_ErrorHandlingWithRetry(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow with retry logic
	workflow := &WorkflowDefinition{
		Name:        "retry_workflow",
		Description: "Workflow with retry logic",
		Steps: []WorkflowStep{
			{
				ID:   "flaky",
				Type: StepTypeTool,
				Tool: "api.flaky_call",
				OnError: &ErrorHandler{
					Action:     "retry",
					RetryCount: 2,
					RetryDelay: 10 * time.Millisecond,
				},
			},
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"result": {
					Type:        "string",
					Description: "Result after retry",
					Value:       "{{.steps.flaky.output.data}}",
				},
			},
		},
	}

	// Setup expectations manually for retry scenario
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "api.flaky_call").Return(target, nil)

	// Fail once, then succeed
	gomock.InOrder(
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "api.flaky_call", gomock.Any()).
			Return(nil, errors.New("temporary failure")),
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "api.flaky_call", gomock.Any()).
			Return(map[string]any{"data": "success_after_retry"}, nil),
	)

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, nil)

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify output includes result
	assert.Equal(t, "success_after_retry", result.Output["result"])
	// Verify step was retried once
	assert.Equal(t, 1, result.Steps["flaky"].RetryCount)
}

// TestCompositeToolWithOutputConfig_ArrayProperty tests array type properties.
func TestCompositeToolWithOutputConfig_ArrayProperty(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that constructs array output
	workflow := &WorkflowDefinition{
		Name:        "list_workflow",
		Description: "Workflow with array output",
		Steps: []WorkflowStep{
			toolStep("fetch", "data.list", nil),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"items": {
					Type:        "array",
					Description: "List of items",
					Value:       "{{.steps.fetch.output.items_json}}",
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("data.list", nil, map[string]any{
		"items_json": `[{"id": 1, "name": "item1"}, {"id": 2, "name": "item2"}]`,
	})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, nil)

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify array output
	items, ok := result.Output["items"].([]any)
	require.True(t, ok, "items should be an array")
	assert.Len(t, items, 2)

	// Verify array elements
	item1, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), item1["id"]) // JSON numbers are float64
	assert.Equal(t, "item1", item1["name"])
}
