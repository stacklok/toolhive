package state

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKubernetesStore(t *testing.T) {
	t.Parallel()
	store := NewKubernetesStore()
	assert.NotNil(t, store)
	assert.IsType(t, &KubernetesStore{}, store)
}

func TestKubernetesStore_Exists(t *testing.T) {
	t.Parallel()
	store := &KubernetesStore{}
	ctx := context.Background()

	// Test with various names
	testCases := []string{
		"test-workload",
		"another-workload",
		"",
		"workload-with-special-chars-123",
	}

	for _, name := range testCases {
		name := name
		t.Run("name_"+name, func(t *testing.T) {
			t.Parallel()
			exists, err := store.Exists(ctx, name)
			assert.NoError(t, err)
			assert.False(t, exists, "Exists should always return false for KubernetesStore")
		})
	}
}

func TestKubernetesStore_List(t *testing.T) {
	t.Parallel()
	store := &KubernetesStore{}
	ctx := context.Background()

	list, err := store.List(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list, "List should always return an empty slice for KubernetesStore")
}

func TestKubernetesStore_GetReader(t *testing.T) {
	t.Parallel()
	store := &KubernetesStore{}
	ctx := context.Background()

	testCases := []string{
		"test-workload",
		"another-workload",
		"",
	}

	for _, name := range testCases {
		name := name
		t.Run("name_"+name, func(t *testing.T) {
			t.Parallel()
			reader, err := store.GetReader(ctx, name)
			assert.NoError(t, err)
			assert.NotNil(t, reader)

			// Verify it's a no-op reader that returns empty content
			data, err := io.ReadAll(reader)
			assert.NoError(t, err)
			assert.Empty(t, data, "Reader should return empty content")

			// Verify we can close it without error
			err = reader.Close()
			assert.NoError(t, err)
		})
	}
}

func TestKubernetesStore_GetWriter(t *testing.T) {
	t.Parallel()
	store := &KubernetesStore{}
	ctx := context.Background()

	testCases := []string{
		"test-workload",
		"another-workload",
		"",
	}

	for _, name := range testCases {
		name := name
		t.Run("name_"+name, func(t *testing.T) {
			t.Parallel()
			writer, err := store.GetWriter(ctx, name)
			assert.NoError(t, err)
			assert.NotNil(t, writer)
			assert.IsType(t, &noopWriteCloser{}, writer)
		})
	}
}

func TestKubernetesStore_Delete(t *testing.T) {
	t.Parallel()
	store := &KubernetesStore{}
	ctx := context.Background()

	testCases := []string{
		"test-workload",
		"another-workload",
		"",
		"non-existent-workload",
	}

	for _, name := range testCases {
		name := name
		t.Run("name_"+name, func(t *testing.T) {
			t.Parallel()
			err := store.Delete(ctx, name)
			assert.NoError(t, err, "Delete should always succeed for KubernetesStore")
		})
	}
}

func TestNoopWriteCloser_Write(t *testing.T) {
	t.Parallel()
	writer := &noopWriteCloser{}

	testCases := [][]byte{
		[]byte("hello world"),
		[]byte(""),
		[]byte("test data with special chars: 你好世界"),
		make([]byte, 1024), // Large buffer
		nil,
	}

	for i, data := range testCases {
		data := data
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			t.Parallel()
			n, err := writer.Write(data)
			assert.NoError(t, err)
			assert.Equal(t, len(data), n, "Write should return the length of input data")
		})
	}
}

func TestNoopWriteCloser_Close(t *testing.T) {
	t.Parallel()
	writer := &noopWriteCloser{}

	// Test multiple closes
	for i := 0; i < 3; i++ {
		i := i
		t.Run(fmt.Sprintf("close_%d", i), func(t *testing.T) {
			t.Parallel()
			err := writer.Close()
			assert.NoError(t, err, "Close should always succeed")
		})
	}
}

func TestNoopWriteCloser_WriteAndClose(t *testing.T) {
	t.Parallel()
	writer := &noopWriteCloser{}

	// Write some data
	data := []byte("test data")
	n, err := writer.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	// Close the writer
	err = writer.Close()
	assert.NoError(t, err)

	// Write after close should still work (it's a no-op)
	n, err = writer.Write([]byte("more data"))
	assert.NoError(t, err)
	assert.Equal(t, 9, n) // len("more data")
}

// TestKubernetesStore_InterfaceCompliance verifies that KubernetesStore implements the Store interface
func TestKubernetesStore_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ Store = &KubernetesStore{}
	var _ = NewKubernetesStore()
}

// TestNoopWriteCloser_InterfaceCompliance verifies that noopWriteCloser implements io.WriteCloser
func TestNoopWriteCloser_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ io.WriteCloser = &noopWriteCloser{}
	var _ io.Writer = &noopWriteCloser{}
	var _ io.Closer = &noopWriteCloser{}
}
