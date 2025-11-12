package session

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewVMCPSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
		wantNilRT bool
		wantType  transportsession.SessionType
	}{
		{
			name:      "creates session with valid ID",
			sessionID: "test-session-123",
			wantNilRT: true,
			wantType:  transportsession.SessionTypeStreamable,
		},
		{
			name:      "creates session with empty ID",
			sessionID: "",
			wantNilRT: true,
			wantType:  transportsession.SessionTypeStreamable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sess := NewVMCPSession(tt.sessionID)

			require.NotNil(t, sess)
			assert.Equal(t, tt.sessionID, sess.ID())
			assert.Equal(t, tt.wantType, sess.Type())

			if tt.wantNilRT {
				assert.Nil(t, sess.GetRoutingTable())
			}

			// Verify embedded StreamableSession is initialized
			assert.NotNil(t, sess.StreamableSession)
		})
	}
}

func TestVMCPSession_GetSetRoutingTable(t *testing.T) {
	t.Parallel()

	sess := NewVMCPSession("test-session")
	require.NotNil(t, sess)

	// Initially nil
	assert.Nil(t, sess.GetRoutingTable())

	// Create a routing table
	rt := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"tool1": {
				WorkloadID:   "backend1",
				WorkloadName: "Backend 1",
				BaseURL:      "http://localhost:8080",
			},
		},
		Resources: map[string]*vmcp.BackendTarget{
			"resource://test": {
				WorkloadID:   "backend2",
				WorkloadName: "Backend 2",
				BaseURL:      "http://localhost:8081",
			},
		},
		Prompts: map[string]*vmcp.BackendTarget{
			"prompt1": {
				WorkloadID:   "backend3",
				WorkloadName: "Backend 3",
				BaseURL:      "http://localhost:8082",
			},
		},
	}

	// Set routing table
	sess.SetRoutingTable(rt)

	// Verify retrieval
	retrieved := sess.GetRoutingTable()
	require.NotNil(t, retrieved)
	assert.Equal(t, rt, retrieved)
	assert.Len(t, retrieved.Tools, 1)
	assert.Len(t, retrieved.Resources, 1)
	assert.Len(t, retrieved.Prompts, 1)
}

func TestVMCPSession_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	sess := NewVMCPSession("test-session")
	require.NotNil(t, sess)

	// Create routing tables for concurrent writes
	rt1 := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"tool1": {WorkloadID: "backend1"},
		},
	}
	rt2 := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"tool2": {WorkloadID: "backend2"},
		},
	}

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent writes
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			sess.SetRoutingTable(rt1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			sess.SetRoutingTable(rt2)
		}
	}()

	// Concurrent reads
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = sess.GetRoutingTable()
		}
	}()

	// Wait for all goroutines
	wg.Wait()

	// Verify session is still valid
	rt := sess.GetRoutingTable()
	require.NotNil(t, rt)
	assert.NotEmpty(t, rt.Tools)
}

func TestVMCPSessionFactory(t *testing.T) {
	t.Parallel()

	factory := VMCPSessionFactory()
	require.NotNil(t, factory)

	// Create session using factory
	sess := factory("test-session-123")
	require.NotNil(t, sess)

	// Verify it's a VMCPSession
	vmcpSess, ok := sess.(*VMCPSession)
	require.True(t, ok, "Factory should return *VMCPSession")
	assert.Equal(t, "test-session-123", vmcpSess.ID())
	assert.Equal(t, transportsession.SessionTypeStreamable, vmcpSess.Type())
	assert.Nil(t, vmcpSess.GetRoutingTable())
}
