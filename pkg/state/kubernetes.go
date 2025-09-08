package state

import (
	"context"
	"io"
	"strings"
)

// KubernetesStore is a no-op implementation of Store for Kubernetes environments.
// In Kubernetes, workload state is managed by the cluster, not by local files.
type KubernetesStore struct{}

// NewKubernetesStore creates a new no-op store for Kubernetes environments.
func NewKubernetesStore() Store {
	return &KubernetesStore{}
}

// Exists always returns false for Kubernetes stores since state is not persisted locally.
func (*KubernetesStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// List always returns an empty slice for Kubernetes stores.
func (*KubernetesStore) List(_ context.Context) ([]string, error) {
	return []string{}, nil
}

// GetReader returns a no-op reader for Kubernetes stores.
func (*KubernetesStore) GetReader(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// GetWriter returns a no-op writer for Kubernetes stores.
func (*KubernetesStore) GetWriter(_ context.Context, _ string) (io.WriteCloser, error) {
	return &noopWriteCloser{}, nil
}

// Delete is a no-op for Kubernetes stores.
func (*KubernetesStore) Delete(_ context.Context, _ string) error {
	return nil
}

// noopWriteCloser is a writer that discards all writes.
type noopWriteCloser struct{}

// Write discards all data and returns success.
func (*noopWriteCloser) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// Close is a no-op.
func (*noopWriteCloser) Close() error {
	return nil
}
