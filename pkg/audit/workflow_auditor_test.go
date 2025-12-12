package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

// testLogWriter captures log output for testing.
type testLogWriter struct {
	logs []string
}

func (w *testLogWriter) Write(p []byte) (n int, err error) {
	w.logs = append(w.logs, string(p))
	return len(p), nil
}

func (w *testLogWriter) getLastLog() string {
	if len(w.logs) == 0 {
		return ""
	}
	return w.logs[len(w.logs)-1]
}

func (w *testLogWriter) reset() {
	w.logs = nil
}

// createTestAuditor creates a WorkflowAuditor for testing with captured output.
func createTestAuditor(t *testing.T, config *Config) (*WorkflowAuditor, *testLogWriter) {
	t.Helper()

	if config == nil {
		config = DefaultConfig()
	}

	writer := &testLogWriter{}
	auditor := &WorkflowAuditor{
		auditLogger: NewAuditLogger(writer),
		config:      config,
		component:   "vmcp-composer",
	}

	return auditor, writer
}

// parseLogEntry parses a JSON log entry.
func parseLogEntry(t *testing.T, logLine string) map[string]any {
	t.Helper()

	var entry map[string]any
	err := json.Unmarshal([]byte(logLine), &entry)
	require.NoError(t, err, "failed to parse log entry")

	return entry
}

func TestNewWorkflowAuditor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil_config_uses_default",
			config:  nil,
			wantErr: false,
		},
		{
			name: "valid_config",
			config: &Config{
				Component:  "test-component",
				EventTypes: []string{EventTypeWorkflowStarted},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, err := NewWorkflowAuditor(tt.config)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, auditor)
			} else {
				require.NoError(t, err)
				require.NotNil(t, auditor)
				assert.NotNil(t, auditor.auditLogger)
				assert.NotNil(t, auditor.config)
				assert.Equal(t, "vmcp-composer", auditor.component)
			}
		})
	}
}

func TestWorkflowAuditor_LogWorkflowStarted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		config             *Config
		workflowID         string
		workflowName       string
		parameters         map[string]any
		timeout            time.Duration
		contextIdentity    *auth.Identity
		wantLogged         bool
		wantIncludeData    bool
		wantIncludeSubject bool
	}{
		{
			name: "logs_with_parameters",
			config: &Config{
				EventTypes:         []string{EventTypeWorkflowStarted},
				IncludeRequestData: true,
			},
			workflowID:   "wf-123",
			workflowName: "test-workflow",
			parameters: map[string]any{
				"param1": "value1",
				"param2": float64(42),
			},
			timeout: 30 * time.Second,
			contextIdentity: &auth.Identity{
				Subject: "user-123",
				Email:   "user@example.com",
			},
			wantLogged:         true,
			wantIncludeData:    true,
			wantIncludeSubject: true,
		},
		{
			name: "logs_without_parameters",
			config: &Config{
				EventTypes:         []string{EventTypeWorkflowStarted},
				IncludeRequestData: false,
			},
			workflowID:   "wf-456",
			workflowName: "another-workflow",
			parameters:   nil,
			timeout:      1 * time.Minute,
			contextIdentity: &auth.Identity{
				Subject: "user-456",
			},
			wantLogged:         true,
			wantIncludeData:    false,
			wantIncludeSubject: true,
		},
		{
			name: "filtered_out_by_config",
			config: &Config{
				EventTypes: []string{EventTypeWorkflowCompleted}, // Different event type
			},
			workflowID:      "wf-789",
			workflowName:    "filtered-workflow",
			parameters:      map[string]any{},
			timeout:         1 * time.Minute,
			contextIdentity: nil,
			wantLogged:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, writer := createTestAuditor(t, tt.config)

			ctx := context.Background()
			if tt.contextIdentity != nil {
				ctx = auth.WithIdentity(ctx, tt.contextIdentity)
			}

			auditor.LogWorkflowStarted(ctx, tt.workflowID, tt.workflowName, tt.parameters, tt.timeout)

			if !tt.wantLogged {
				assert.Empty(t, writer.logs, "expected no logs")
				return
			}

			require.NotEmpty(t, writer.logs, "expected log entry")
			entry := parseLogEntry(t, writer.getLastLog())

			// Verify event type
			assert.Equal(t, EventTypeWorkflowStarted, entry["type"])
			assert.Equal(t, "vmcp-composer", entry["component"])
			assert.Equal(t, OutcomeSuccess, entry["outcome"])

			// Verify target
			target, ok := entry["target"].(map[string]any)
			require.True(t, ok, "target should be a map")
			assert.Equal(t, tt.workflowID, target[TargetKeyWorkflowID])
			assert.Equal(t, tt.workflowName, target[TargetKeyWorkflowName])
			assert.Equal(t, TargetTypeWorkflow, target[TargetKeyType])

			// Verify subjects
			if tt.wantIncludeSubject && tt.contextIdentity != nil {
				subjects, ok := entry["subjects"].(map[string]any)
				require.True(t, ok, "subjects should be a map")
				if tt.contextIdentity.Subject != "" {
					assert.Equal(t, tt.contextIdentity.Subject, subjects[SubjectKeyUserID])
				}
			}

			// Verify data inclusion
			if tt.wantIncludeData {
				data, ok := entry["data"].(map[string]any)
				require.True(t, ok, "data should be a map")
				if tt.parameters != nil {
					params, ok := data["parameters"].(map[string]any)
					require.True(t, ok, "parameters should be in data")
					assert.Equal(t, tt.parameters, params)
				}
				assert.Equal(t, float64(tt.timeout.Milliseconds()), data["timeout_ms"])
			} else {
				_, hasData := entry["data"]
				assert.False(t, hasData, "data should not be included")
			}
		})
	}
}

func TestWorkflowAuditor_LogWorkflowCompleted(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes:          []string{EventTypeWorkflowCompleted},
		IncludeResponseData: true,
	})

	ctx := context.Background()
	output := map[string]any{
		"result": "success",
		"count":  float64(5),
	}

	auditor.LogWorkflowCompleted(ctx, "wf-123", "test-workflow", 2*time.Second, 3, output)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	assert.Equal(t, EventTypeWorkflowCompleted, entry["type"])
	assert.Equal(t, OutcomeSuccess, entry["outcome"])

	// Verify metadata
	metadata, ok := entry["metadata"].(map[string]any)
	require.True(t, ok)
	extra, ok := metadata["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(2000), extra[MetadataExtraKeyDuration])
	assert.Equal(t, float64(3), extra[MetadataExtraKeyStepCount])

	// Verify output data
	data, ok := entry["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, output, data)
}

func TestWorkflowAuditor_LogWorkflowFailed(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{EventTypeWorkflowFailed},
	})

	ctx := context.Background()
	testErr := errors.New("workflow execution failed")

	auditor.LogWorkflowFailed(ctx, "wf-456", "failing-workflow", 5*time.Second, 7, testErr)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	assert.Equal(t, EventTypeWorkflowFailed, entry["type"])
	assert.Equal(t, OutcomeFailure, entry["outcome"])

	// Verify error in metadata
	metadata, ok := entry["metadata"].(map[string]any)
	require.True(t, ok)
	extra, ok := metadata["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, testErr.Error(), extra["error"])
	assert.Equal(t, float64(5000), extra[MetadataExtraKeyDuration])
	assert.Equal(t, float64(7), extra[MetadataExtraKeyStepCount])
}

func TestWorkflowAuditor_LogWorkflowTimedOut(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{EventTypeWorkflowTimedOut},
	})

	ctx := context.Background()

	auditor.LogWorkflowTimedOut(ctx, "wf-789", "timeout-workflow", 30*time.Second, 10)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	assert.Equal(t, EventTypeWorkflowTimedOut, entry["type"])
	assert.Equal(t, OutcomeFailure, entry["outcome"])

	// Verify metadata
	metadata, ok := entry["metadata"].(map[string]any)
	require.True(t, ok)
	extra, ok := metadata["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(30000), extra[MetadataExtraKeyDuration])
	assert.Equal(t, float64(10), extra[MetadataExtraKeyStepCount])
}

func TestWorkflowAuditor_LogStepStarted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stepID     string
		stepType   string
		toolName   string
		wantTarget map[string]string
	}{
		{
			name:     "tool_step",
			stepID:   "step-1",
			stepType: "tool",
			toolName: "my-tool",
			wantTarget: map[string]string{
				TargetKeyStepID:   "step-1",
				TargetKeyStepType: "tool",
				TargetKeyToolName: "my-tool",
				TargetKeyType:     TargetTypeWorkflowStep,
			},
		},
		{
			name:     "elicitation_step_no_tool",
			stepID:   "step-2",
			stepType: "elicitation",
			toolName: "",
			wantTarget: map[string]string{
				TargetKeyStepID:   "step-2",
				TargetKeyStepType: "elicitation",
				TargetKeyType:     TargetTypeWorkflowStep,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, writer := createTestAuditor(t, &Config{
				EventTypes: []string{EventTypeWorkflowStepStarted},
			})

			ctx := context.Background()
			auditor.LogStepStarted(ctx, "wf-123", tt.stepID, tt.stepType, tt.toolName)

			require.NotEmpty(t, writer.logs)
			entry := parseLogEntry(t, writer.getLastLog())

			assert.Equal(t, EventTypeWorkflowStepStarted, entry["type"])
			assert.Equal(t, OutcomeSuccess, entry["outcome"])

			// Verify target
			target, ok := entry["target"].(map[string]any)
			require.True(t, ok)
			for key, expectedValue := range tt.wantTarget {
				assert.Equal(t, expectedValue, target[key], "target key %s mismatch", key)
			}
		})
	}
}

func TestWorkflowAuditor_LogStepCompleted(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{EventTypeWorkflowStepCompleted},
	})

	ctx := context.Background()
	auditor.LogStepCompleted(ctx, "wf-123", "step-1", 500*time.Millisecond, 2)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	assert.Equal(t, EventTypeWorkflowStepCompleted, entry["type"])
	assert.Equal(t, OutcomeSuccess, entry["outcome"])

	// Verify metadata
	metadata, ok := entry["metadata"].(map[string]any)
	require.True(t, ok)
	extra, ok := metadata["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(500), extra[MetadataExtraKeyDuration])
	assert.Equal(t, float64(2), extra[MetadataExtraKeyRetryCount])
}

func TestWorkflowAuditor_LogStepFailed(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{EventTypeWorkflowStepFailed},
	})

	ctx := context.Background()
	testErr := errors.New("step execution failed")

	auditor.LogStepFailed(ctx, "wf-123", "step-2", 1*time.Second, 3, testErr)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	assert.Equal(t, EventTypeWorkflowStepFailed, entry["type"])
	assert.Equal(t, OutcomeFailure, entry["outcome"])

	// Verify error in metadata
	metadata, ok := entry["metadata"].(map[string]any)
	require.True(t, ok)
	extra, ok := metadata["extra"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, testErr.Error(), extra["error"])
	assert.Equal(t, float64(1000), extra[MetadataExtraKeyDuration])
	assert.Equal(t, float64(3), extra[MetadataExtraKeyRetryCount])
}

func TestWorkflowAuditor_LogStepSkipped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		condition     string
		wantCondition bool
	}{
		{
			name:          "with_condition",
			condition:     "{{.params.skip}} == true",
			wantCondition: true,
		},
		{
			name:          "without_condition",
			condition:     "",
			wantCondition: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, writer := createTestAuditor(t, &Config{
				EventTypes: []string{EventTypeWorkflowStepSkipped},
			})

			ctx := context.Background()
			auditor.LogStepSkipped(ctx, "wf-123", "step-3", tt.condition)

			require.NotEmpty(t, writer.logs)
			entry := parseLogEntry(t, writer.getLastLog())

			assert.Equal(t, EventTypeWorkflowStepSkipped, entry["type"])
			assert.Equal(t, OutcomeSuccess, entry["outcome"])

			// Verify condition in metadata
			if tt.wantCondition {
				metadata, ok := entry["metadata"].(map[string]any)
				require.True(t, ok)
				extra, ok := metadata["extra"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, tt.condition, extra["condition"])
			} else {
				// Should have no extra metadata if no condition
				if metadata, ok := entry["metadata"].(map[string]any); ok {
					if extra, ok := metadata["extra"].(map[string]any); ok {
						_, hasCondition := extra["condition"]
						assert.False(t, hasCondition, "should not have condition in metadata")
					}
				}
			}
		})
	}
}

func TestWorkflowAuditor_ExtractSubjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		identity     *auth.Identity
		wantSubjects map[string]string
	}{
		{
			name: "complete_identity",
			identity: &auth.Identity{
				Subject: "auth0|user-123",
				Name:    "John Doe",
				Email:   "john@example.com",
				Claims: map[string]any{
					"client_name":    "my-app",
					"client_version": "1.2.3",
				},
			},
			wantSubjects: map[string]string{
				SubjectKeyUserID:        "auth0|user-123",
				SubjectKeyUser:          "John Doe",
				SubjectKeyClientName:    "my-app",
				SubjectKeyClientVersion: "1.2.3",
			},
		},
		{
			name: "email_fallback",
			identity: &auth.Identity{
				Subject: "user-456",
				Email:   "user@example.com",
			},
			wantSubjects: map[string]string{
				SubjectKeyUserID: "user-456",
				SubjectKeyUser:   "user@example.com",
			},
		},
		{
			name: "preferred_username_fallback",
			identity: &auth.Identity{
				Subject: "user-789",
				Claims: map[string]any{
					"preferred_username": "johndoe",
				},
			},
			wantSubjects: map[string]string{
				SubjectKeyUserID: "user-789",
				SubjectKeyUser:   "johndoe",
			},
		},
		{
			name:     "anonymous_user",
			identity: nil,
			wantSubjects: map[string]string{
				SubjectKeyUser: "anonymous",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, _ := createTestAuditor(t, DefaultConfig())

			ctx := context.Background()
			if tt.identity != nil {
				ctx = auth.WithIdentity(ctx, tt.identity)
			}

			subjects := auditor.extractSubjects(ctx)

			for key, expectedValue := range tt.wantSubjects {
				assert.Equal(t, expectedValue, subjects[key], "subject key %s mismatch", key)
			}
		})
	}
}

func TestWorkflowAuditor_ExtractSource(t *testing.T) {
	t.Parallel()

	auditor, _ := createTestAuditor(t, DefaultConfig())

	source := auditor.extractSource(context.Background())

	assert.Equal(t, SourceTypeLocal, source.Type)
	assert.Equal(t, "vmcp-composer", source.Value)
	assert.NotNil(t, source.Extra)
}

func TestWorkflowAuditor_EventFiltering(t *testing.T) {
	t.Parallel()

	// Create auditor that only logs workflow-level events, not step-level
	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{
			EventTypeWorkflowStarted,
			EventTypeWorkflowCompleted,
		},
	})

	ctx := context.Background()

	// These should be logged
	auditor.LogWorkflowStarted(ctx, "wf-1", "test", nil, time.Minute)
	assert.Len(t, writer.logs, 1, "workflow started should be logged")

	writer.reset()
	auditor.LogWorkflowCompleted(ctx, "wf-1", "test", time.Second, 5, nil)
	assert.Len(t, writer.logs, 1, "workflow completed should be logged")

	// These should NOT be logged (filtered out)
	writer.reset()
	auditor.LogStepStarted(ctx, "wf-1", "step-1", "tool", "my-tool")
	assert.Empty(t, writer.logs, "step started should be filtered out")

	auditor.LogStepCompleted(ctx, "wf-1", "step-1", time.Second, 0)
	assert.Empty(t, writer.logs, "step completed should be filtered out")
}

func TestWorkflowAuditor_LogFormat(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{EventTypeWorkflowStarted},
	})

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "test-user",
		Email:   "test@example.com",
	})

	auditor.LogWorkflowStarted(ctx, "wf-test", "test-workflow", map[string]any{"key": "value"}, time.Minute)

	require.NotEmpty(t, writer.logs)
	logLine := writer.getLastLog()

	// Verify it's valid JSON
	var entry map[string]any
	err := json.Unmarshal([]byte(logLine), &entry)
	require.NoError(t, err, "log entry should be valid JSON")

	// Verify required fields exist
	assert.Contains(t, entry, "type")
	assert.Contains(t, entry, "time")
	assert.Contains(t, entry, "component")
	assert.Contains(t, entry, "outcome")
	assert.Contains(t, entry, "source")
	assert.Contains(t, entry, "target")
	assert.Contains(t, entry, "subjects")
}

func TestWorkflowAuditor_ConcurrentLogging(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes: []string{
			EventTypeWorkflowStarted,
			EventTypeWorkflowCompleted,
		},
	})

	ctx := context.Background()
	numGoroutines := 10

	// Log concurrently from multiple goroutines
	done := make(chan bool)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			auditor.LogWorkflowStarted(ctx, "wf-"+string(rune('0'+id)), "workflow", nil, time.Minute)
			auditor.LogWorkflowCompleted(ctx, "wf-"+string(rune('0'+id)), "workflow", time.Second, 5, nil)
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Should have 2 logs per goroutine (started + completed)
	assert.Len(t, writer.logs, numGoroutines*2)

	// Verify all logs are valid JSON
	for _, log := range writer.logs {
		var entry map[string]any
		err := json.Unmarshal([]byte(log), &entry)
		assert.NoError(t, err, "all log entries should be valid JSON")
	}
}

func TestWorkflowAuditor_LargePayload(t *testing.T) {
	t.Parallel()

	auditor, writer := createTestAuditor(t, &Config{
		EventTypes:         []string{EventTypeWorkflowStarted},
		IncludeRequestData: true,
	})

	// Create large parameters payload
	largeParams := make(map[string]any)
	for i := 0; i < 1000; i++ {
		largeParams["key_"+string(rune(i))] = strings.Repeat("value", 100)
	}

	ctx := context.Background()
	auditor.LogWorkflowStarted(ctx, "wf-large", "test", largeParams, time.Minute)

	require.NotEmpty(t, writer.logs)
	entry := parseLogEntry(t, writer.getLastLog())

	// Verify large payload was logged
	data, ok := entry["data"].(map[string]any)
	require.True(t, ok)
	params, ok := data["parameters"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, params, 1000, "all parameters should be logged")
}

func TestWorkflowAuditor_NilConfig(t *testing.T) {
	t.Parallel()

	// NewWorkflowAuditor with nil config should use defaults
	auditor, err := NewWorkflowAuditor(nil)
	require.NoError(t, err)
	require.NotNil(t, auditor)
	require.NotNil(t, auditor.config)

	// Default config should allow all event types
	assert.True(t, auditor.config.ShouldAuditEvent(EventTypeWorkflowStarted))
	assert.True(t, auditor.config.ShouldAuditEvent(EventTypeWorkflowCompleted))
	assert.True(t, auditor.config.ShouldAuditEvent(EventTypeWorkflowStepStarted))
}

// Benchmark tests
func BenchmarkWorkflowAuditor_LogWorkflowStarted(b *testing.B) {
	auditor, _ := createTestAuditor(&testing.T{}, &Config{
		EventTypes: []string{EventTypeWorkflowStarted},
	})

	ctx := context.Background()
	params := map[string]any{"key": "value"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auditor.LogWorkflowStarted(ctx, "wf-bench", "test", params, time.Minute)
	}
}

func BenchmarkWorkflowAuditor_LogStepCompleted(b *testing.B) {
	auditor, _ := createTestAuditor(&testing.T{}, &Config{
		EventTypes: []string{EventTypeWorkflowStepCompleted},
	})

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auditor.LogStepCompleted(ctx, "wf-bench", "step-1", time.Second, 2)
	}
}
