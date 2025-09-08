package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSerialization tests the serialization and deserialization functions
func TestSerialization(t *testing.T) {
	t.Run("Serialize and Deserialize ProxySession", func(t *testing.T) {
		// Create a session with metadata
		original := NewProxySession("test-proxy-1")
		original.SetMetadata("key1", "value1")
		original.SetMetadata("key2", "value2")
		original.SetData(map[string]interface{}{"custom": "data"})

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Verify JSON structure
		var jsonData map[string]interface{}
		err = json.Unmarshal(data, &jsonData)
		require.NoError(t, err)
		assert.Equal(t, "test-proxy-1", jsonData["id"])
		assert.Equal(t, string(SessionTypeMCP), jsonData["type"])

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)
		assert.NotNil(t, restored)

		// Verify restored session
		assert.Equal(t, original.ID(), restored.ID())
		assert.Equal(t, original.Type(), restored.Type())
		
		// Check metadata
		metadata := restored.GetMetadata()
		assert.Equal(t, "value1", metadata["key1"])
		assert.Equal(t, "value2", metadata["key2"])
	})

	t.Run("Serialize and Deserialize SSESession", func(t *testing.T) {
		// Create an SSE session
		original := NewSSESession("test-sse-1")
		original.SetMetadata("client", "browser")
		original.SetMetadata("version", "1.0")

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)
		assert.NotNil(t, restored)

		// Verify it's an SSE session
		assert.Equal(t, SessionTypeSSE, restored.Type())
		assert.Equal(t, "test-sse-1", restored.ID())

		// Check it's the right type
		sseSession, ok := restored.(*SSESession)
		assert.True(t, ok)
		assert.NotNil(t, sseSession.MessageCh)

		// Check metadata
		metadata := restored.GetMetadata()
		assert.Equal(t, "browser", metadata["client"])
		assert.Equal(t, "1.0", metadata["version"])
	})

	t.Run("Serialize and Deserialize StreamableSession", func(t *testing.T) {
		// Create a streamable session
		original := NewStreamableSession("test-stream-1")
		original.SetMetadata("protocol", "http")

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)
		assert.NotNil(t, restored)

		// Verify it's a streamable session
		assert.Equal(t, SessionTypeStreamable, restored.Type())
		assert.Equal(t, "test-stream-1", restored.ID())

		// Check metadata
		metadata := restored.GetMetadata()
		assert.Equal(t, "http", metadata["protocol"])
	})

	t.Run("Serialize nil session", func(t *testing.T) {
		data, err := serializeSession(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "nil session")
		assert.Nil(t, data)
	})

	t.Run("Deserialize empty data", func(t *testing.T) {
		session, err := deserializeSession([]byte{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty data")
		assert.Nil(t, session)
	})

	t.Run("Deserialize invalid JSON", func(t *testing.T) {
		session, err := deserializeSession([]byte("not json"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal")
		assert.Nil(t, session)
	})

	t.Run("Preserve timestamps", func(t *testing.T) {
		// Create a session with specific timestamps
		original := NewProxySession("test-time-1")
		createdAt := original.CreatedAt()
		
		// Wait a bit and touch to update the timestamp
		time.Sleep(10 * time.Millisecond)
		original.Touch()
		updatedAt := original.UpdatedAt()

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)

		// Timestamps should be preserved
		assert.Equal(t, createdAt.Unix(), restored.CreatedAt().Unix())
		assert.Equal(t, updatedAt.Unix(), restored.UpdatedAt().Unix())
	})

	t.Run("Handle session with no metadata", func(t *testing.T) {
		// Create a session without metadata
		original := NewProxySession("test-no-meta")

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)

		// Metadata should be empty but not nil
		metadata := restored.GetMetadata()
		assert.NotNil(t, metadata)
		assert.Len(t, metadata, 0)
	})

	t.Run("Handle complex data in session", func(t *testing.T) {
		// Create a session with complex data
		original := NewProxySession("test-complex")
		complexData := map[string]interface{}{
			"string": "value",
			"number": 42,
			"bool":   true,
			"nested": map[string]interface{}{
				"key": "value",
			},
		}
		original.SetData(complexData)

		// Serialize
		data, err := serializeSession(original)
		require.NoError(t, err)

		// Deserialize
		restored, err := deserializeSession(data)
		require.NoError(t, err)

		// Data should be preserved as JSON
		restoredData := restored.GetData()
		assert.NotNil(t, restoredData)
		
		// The data will be stored as json.RawMessage
		if rawData, ok := restoredData.(json.RawMessage); ok {
			var parsed map[string]interface{}
			err = json.Unmarshal(rawData, &parsed)
			require.NoError(t, err)
			assert.Equal(t, "value", parsed["string"])
			assert.Equal(t, float64(42), parsed["number"]) // JSON numbers are float64
			assert.Equal(t, true, parsed["bool"])
		}
	})

	t.Run("Unknown session type defaults to ProxySession", func(t *testing.T) {
		// Create JSON with unknown session type
		jsonData := `{
			"id": "unknown-1",
			"type": "unknown",
			"created_at": "2024-01-01T00:00:00Z",
			"updated_at": "2024-01-01T00:00:00Z"
		}`

		// Deserialize
		restored, err := deserializeSession([]byte(jsonData))
		require.NoError(t, err)
		assert.NotNil(t, restored)

		// Should be a ProxySession with the unknown type
		assert.Equal(t, SessionType("unknown"), restored.Type())
		proxySession, ok := restored.(*ProxySession)
		assert.True(t, ok)
		assert.NotNil(t, proxySession)
	})
}