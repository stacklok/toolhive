package audit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAuditEvent(t *testing.T) {
	source := EventSource{
		Type:  SourceTypeNetwork,
		Value: "192.168.1.100",
		Extra: map[string]any{"user_agent": "test-agent"},
	}
	subjects := map[string]string{
		SubjectKeyUser:   "testuser",
		SubjectKeyUserID: "user123",
	}

	event := NewAuditEvent("test_event", source, OutcomeSuccess, subjects, "test-component")

	assert.NotEmpty(t, event.Metadata.AuditID)
	assert.Equal(t, "test_event", event.Type)
	assert.Equal(t, OutcomeSuccess, event.Outcome)
	assert.Equal(t, source, event.Source)
	assert.Equal(t, subjects, event.Subjects)
	assert.Equal(t, "test-component", event.Component)
	assert.WithinDuration(t, time.Now().UTC(), event.LoggedAt, time.Second)
}

func TestNewAuditEventWithID(t *testing.T) {
	auditID := "custom-audit-id"
	source := EventSource{Type: SourceTypeLocal, Value: "localhost"}
	subjects := map[string]string{SubjectKeyUser: "admin"}

	event := NewAuditEventWithID(auditID, "admin_action", source, OutcomeSuccess, subjects, "admin-panel")

	assert.Equal(t, auditID, event.Metadata.AuditID)
	assert.Equal(t, "admin_action", event.Type)
	assert.Equal(t, OutcomeSuccess, event.Outcome)
	assert.Equal(t, source, event.Source)
	assert.Equal(t, subjects, event.Subjects)
	assert.Equal(t, "admin-panel", event.Component)
}

func TestAuditEventWithTarget(t *testing.T) {
	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	target := map[string]string{
		TargetKeyType:     TargetTypeTool,
		TargetKeyName:     "test-tool",
		TargetKeyEndpoint: "/api/tools/test",
	}

	result := event.WithTarget(target)

	assert.Equal(t, event, result) // Should return same instance
	assert.Equal(t, target, event.Target)
}

func TestAuditEventWithData(t *testing.T) {
	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	testData := map[string]any{"key": "value", "number": 42}
	dataBytes, err := json.Marshal(testData)
	require.NoError(t, err)
	rawMsg := json.RawMessage(dataBytes)

	result := event.WithData(&rawMsg)

	assert.Equal(t, event, result) // Should return same instance
	assert.Equal(t, &rawMsg, event.Data)
}

func TestAuditEventWithDataFromString(t *testing.T) {
	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	jsonString := `{"message": "test data", "count": 5}`

	result := event.WithDataFromString(jsonString)

	assert.Equal(t, event, result) // Should return same instance
	require.NotNil(t, event.Data)

	// Verify the data can be unmarshaled back
	var data map[string]any
	err := json.Unmarshal(*event.Data, &data)
	require.NoError(t, err)
	assert.Equal(t, "test data", data["message"])
	assert.Equal(t, float64(5), data["count"]) // JSON numbers are float64
}

func TestAuditEventJSONSerialization(t *testing.T) {
	source := EventSource{
		Type:  SourceTypeNetwork,
		Value: "10.0.0.1",
		Extra: map[string]any{
			SourceExtraKeyUserAgent: "Mozilla/5.0",
			SourceExtraKeyRequestID: "req-123",
		},
	}
	subjects := map[string]string{
		SubjectKeyUser:          "john.doe",
		SubjectKeyUserID:        "user-456",
		SubjectKeyClientName:    "test-client",
		SubjectKeyClientVersion: "1.0.0",
	}
	target := map[string]string{
		TargetKeyType:     TargetTypeTool,
		TargetKeyName:     "calculator",
		TargetKeyMethod:   "POST",
		TargetKeyEndpoint: "/api/tools/calculator",
	}

	event := NewAuditEvent(EventTypeMCPToolCall, source, OutcomeSuccess, subjects, "calculator-service")
	event.WithTarget(target)
	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:     150,
		MetadataExtraKeyTransport:    "sse",
		MetadataExtraKeyMCPVersion:   "2025-03-26",
		MetadataExtraKeyResponseSize: 1024,
	}

	// Serialize to JSON
	jsonData, err := json.Marshal(event)
	require.NoError(t, err)

	// Deserialize back
	var deserializedEvent AuditEvent
	err = json.Unmarshal(jsonData, &deserializedEvent)
	require.NoError(t, err)

	// Verify all fields are preserved
	assert.Equal(t, event.Metadata.AuditID, deserializedEvent.Metadata.AuditID)
	assert.Equal(t, event.Type, deserializedEvent.Type)
	assert.Equal(t, event.Outcome, deserializedEvent.Outcome)
	assert.Equal(t, event.Source.Type, deserializedEvent.Source.Type)
	assert.Equal(t, event.Source.Value, deserializedEvent.Source.Value)
	assert.Equal(t, event.Subjects, deserializedEvent.Subjects)
	assert.Equal(t, event.Component, deserializedEvent.Component)
	assert.Equal(t, event.Target, deserializedEvent.Target)
	// Note: JSON unmarshaling converts numbers to float64, so we check individual fields
	assert.Equal(t, float64(150), deserializedEvent.Metadata.Extra[MetadataExtraKeyDuration])
	assert.Equal(t, "sse", deserializedEvent.Metadata.Extra[MetadataExtraKeyTransport])
	assert.Equal(t, "2025-03-26", deserializedEvent.Metadata.Extra[MetadataExtraKeyMCPVersion])
	assert.Equal(t, float64(1024), deserializedEvent.Metadata.Extra[MetadataExtraKeyResponseSize])
}

func TestEventSourceConstants(t *testing.T) {
	// Test that constants are defined
	assert.Equal(t, "network", SourceTypeNetwork)
	assert.Equal(t, "local", SourceTypeLocal)
}

func TestOutcomeConstants(t *testing.T) {
	// Test that outcome constants are defined
	assert.Equal(t, "success", OutcomeSuccess)
	assert.Equal(t, "failure", OutcomeFailure)
	assert.Equal(t, "error", OutcomeError)
	assert.Equal(t, "denied", OutcomeDenied)
}

func TestComponentConstants(t *testing.T) {
	// Test that component constants are defined
	assert.Equal(t, "toolhive-api", ComponentToolHive)
}

func TestEventMetadataExtra(t *testing.T) {
	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")

	// Initially should be nil
	assert.Nil(t, event.Metadata.Extra)

	// Add some extra metadata
	event.Metadata.Extra = map[string]any{
		"custom_field": "custom_value",
		"number_field": 42,
	}

	assert.Equal(t, "custom_value", event.Metadata.Extra["custom_field"])
	assert.Equal(t, 42, event.Metadata.Extra["number_field"])
}

func TestEventSourceExtra(t *testing.T) {
	source := EventSource{
		Type:  SourceTypeNetwork,
		Value: "192.168.1.1",
		Extra: map[string]any{
			"port":     8080,
			"protocol": "https",
		},
	}

	event := NewAuditEvent("test", source, OutcomeSuccess, map[string]string{}, "test")

	assert.Equal(t, 8080, event.Source.Extra["port"])
	assert.Equal(t, "https", event.Source.Extra["protocol"])
}
