package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRunConfigStoreWithDetector(t *testing.T) {
	t.Parallel()

	store, err := NewRunConfigStoreWithDetector("toolhive", nil)

	require.NoError(t, err)
	assert.IsType(t, &LocalStore{}, store)
}

func TestNewGroupConfigStoreWithDetector(t *testing.T) {
	t.Parallel()

	store, err := NewGroupConfigStoreWithDetector("toolhive", nil)

	require.NoError(t, err)
	assert.IsType(t, &LocalStore{}, store)
}

func TestNewRunConfigStore(t *testing.T) {
	t.Parallel()

	store, err := NewRunConfigStore("toolhive")

	require.NoError(t, err)
	assert.IsType(t, &LocalStore{}, store)
}

func TestNewGroupConfigStore(t *testing.T) {
	t.Parallel()

	store, err := NewGroupConfigStore("toolhive")

	require.NoError(t, err)
	assert.IsType(t, &LocalStore{}, store)
}
