package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSyncResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []byte
		serverCount int32
		expectedLen int
	}{
		{
			name:        "empty data",
			data:        []byte{},
			serverCount: 0,
			expectedLen: 64, // SHA256 hex string length
		},
		{
			name:        "simple json data",
			data:        []byte(`{"servers": {"test": {}}}`),
			serverCount: 1,
			expectedLen: 64,
		},
		{
			name:        "complex data",
			data:        []byte(`{"servers": {"server1": {"name": "test1"}, "server2": {"name": "test2"}}}`),
			serverCount: 2,
			expectedLen: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NewSyncResult(tt.data, tt.serverCount)

			assert.Equal(t, tt.data, result.Data)
			assert.Equal(t, tt.serverCount, result.ServerCount)
			assert.Len(t, result.Hash, tt.expectedLen)
			assert.NotEmpty(t, result.Hash)
		})
	}
}

func TestSyncResultHashConsistency(t *testing.T) {
	t.Parallel()

	data := []byte(`{"test": "data"}`)
	serverCount := int32(5)

	result1 := NewSyncResult(data, serverCount)
	result2 := NewSyncResult(data, serverCount)

	// Same data should produce same hash
	assert.Equal(t, result1.Hash, result2.Hash)
	assert.Equal(t, result1.Data, result2.Data)
	assert.Equal(t, result1.ServerCount, result2.ServerCount)
}

func TestSyncResultHashDifference(t *testing.T) {
	t.Parallel()

	data1 := []byte(`{"test": "data1"}`)
	data2 := []byte(`{"test": "data2"}`)
	serverCount := int32(1)

	result1 := NewSyncResult(data1, serverCount)
	result2 := NewSyncResult(data2, serverCount)

	// Different data should produce different hashes
	assert.NotEqual(t, result1.Hash, result2.Hash)
	assert.NotEqual(t, result1.Data, result2.Data)
	assert.Equal(t, result1.ServerCount, result2.ServerCount)
}