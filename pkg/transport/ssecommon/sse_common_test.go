package ssecommon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSSEMessage(t *testing.T) {
	t.Parallel()

	eventType := "test-event"
	data := "test data"

	msg := NewSSEMessage(eventType, data)

	require.NotNil(t, msg)
	assert.Equal(t, eventType, msg.EventType)
	assert.Equal(t, data, msg.Data)
	assert.Empty(t, msg.TargetClientID)
	assert.WithinDuration(t, time.Now(), msg.CreatedAt, time.Second)
}

func TestSSEMessage_WithTargetClientID(t *testing.T) {
	t.Parallel()

	msg := NewSSEMessage("test", "data")
	clientID := "client-123"

	result := msg.WithTargetClientID(clientID)

	// Should return the same instance (fluent interface)
	assert.Same(t, msg, result)
	assert.Equal(t, clientID, msg.TargetClientID)
}

func TestSSEMessage_ToSSEString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		eventType      string
		data           string
		expectedOutput string
	}{
		{
			name:      "simple message",
			eventType: "message",
			data:      "Hello, World!",
			expectedOutput: "event: message\n" +
				"data: Hello, World!\n" +
				"\n",
		},
		{
			name:      "multiline data",
			eventType: "multiline",
			data:      "Line 1\nLine 2\nLine 3",
			expectedOutput: "event: multiline\n" +
				"data: Line 1\n" +
				"data: Line 2\n" +
				"data: Line 3\n" +
				"\n",
		},
		{
			name:      "empty data",
			eventType: "empty",
			data:      "",
			expectedOutput: "event: empty\n" +
				"data: \n" +
				"\n",
		},
		{
			name:      "data with trailing newline",
			eventType: "trailing",
			data:      "Data with newline\n",
			expectedOutput: "event: trailing\n" +
				"data: Data with newline\n" +
				"data: \n" +
				"\n",
		},
		{
			name:      "JSON data",
			eventType: "json",
			data:      `{"key": "value", "number": 42}`,
			expectedOutput: "event: json\n" +
				`data: {"key": "value", "number": 42}` + "\n" +
				"\n",
		},
		{
			name:      "special characters",
			eventType: "special",
			data:      "Data with: colons, newlines\nand other chars!@#$%",
			expectedOutput: "event: special\n" +
				"data: Data with: colons, newlines\n" +
				"data: and other chars!@#$%\n" +
				"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := NewSSEMessage(tt.eventType, tt.data)
			result := msg.ToSSEString()

			assert.Equal(t, tt.expectedOutput, result)

			// Verify the format is correct for SSE
			lines := strings.Split(result, "\n")
			assert.True(t, strings.HasPrefix(lines[0], "event: "), "First line should start with 'event: '")

			// Count data lines
			dataLines := 0
			for _, line := range lines {
				if strings.HasPrefix(line, "data: ") {
					dataLines++
				}
			}
			expectedDataLines := len(strings.Split(tt.data, "\n"))
			assert.Equal(t, expectedDataLines, dataLines, "Should have correct number of data lines")

			// Should end with empty line
			assert.Equal(t, "", lines[len(lines)-1], "Should end with empty line")
			assert.Equal(t, "", lines[len(lines)-2], "Should have blank line before final newline")
		})
	}
}

func TestSSEMessage_ToSSEString_Integration(t *testing.T) {
	t.Parallel()

	// Test a complete message with target client ID
	msg := NewSSEMessage("notification", "User logged in")
	msg.WithTargetClientID("client-456")

	result := msg.ToSSEString()

	expected := "event: notification\n" +
		"data: User logged in\n" +
		"\n"

	assert.Equal(t, expected, result)

	// Note: TargetClientID is not included in the SSE string format
	// It's used for routing but not part of the SSE protocol
	assert.NotContains(t, result, "client-456")
}

func TestNewPendingSSEMessage(t *testing.T) {
	t.Parallel()

	originalMsg := NewSSEMessage("test", "data")

	pendingMsg := NewPendingSSEMessage(originalMsg)

	require.NotNil(t, pendingMsg)
	assert.Same(t, originalMsg, pendingMsg.Message)
	assert.WithinDuration(t, time.Now(), pendingMsg.CreatedAt, time.Second)
}

func TestPendingSSEMessage_CreatedAtIndependence(t *testing.T) {
	t.Parallel()

	// Create original message
	originalMsg := NewSSEMessage("test", "data")
	originalTime := originalMsg.CreatedAt

	// Wait a bit to ensure different timestamps
	time.Sleep(10 * time.Millisecond)

	// Create pending message
	pendingMsg := NewPendingSSEMessage(originalMsg)

	// The pending message should have its own CreatedAt timestamp
	assert.True(t, pendingMsg.CreatedAt.After(originalTime),
		"Pending message should have a later CreatedAt timestamp")
	assert.Equal(t, originalTime, pendingMsg.Message.CreatedAt,
		"Original message CreatedAt should be unchanged")
}

func TestSSEClient_Structure(t *testing.T) {
	t.Parallel()

	// Test that SSEClient can be created and has expected fields
	client := &SSEClient{
		MessageCh: make(chan string, 10),
		CreatedAt: time.Now(),
	}

	require.NotNil(t, client)
	require.NotNil(t, client.MessageCh)
	assert.WithinDuration(t, time.Now(), client.CreatedAt, time.Second)

	// Test that the channel works
	testMessage := "test message"
	client.MessageCh <- testMessage

	select {
	case received := <-client.MessageCh:
		assert.Equal(t, testMessage, received)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Should have received message from channel")
	}
}

func TestSSEMessage_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		data      string
	}{
		{
			name:      "empty event type",
			eventType: "",
			data:      "some data",
		},
		{
			name:      "whitespace event type",
			eventType: "   ",
			data:      "some data",
		},
		{
			name:      "event type with spaces",
			eventType: "my event",
			data:      "some data",
		},
		{
			name:      "very long data",
			eventType: "long",
			data:      strings.Repeat("A", 10000),
		},
		{
			name:      "unicode data",
			eventType: "unicode",
			data:      "Hello 世界 🌍 émojis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := NewSSEMessage(tt.eventType, tt.data)

			assert.Equal(t, tt.eventType, msg.EventType)
			assert.Equal(t, tt.data, msg.Data)

			// Should not panic when converting to SSE string
			result := msg.ToSSEString()
			assert.NotEmpty(t, result)
			assert.Contains(t, result, fmt.Sprintf("event: %s\n", tt.eventType))
		})
	}
}
