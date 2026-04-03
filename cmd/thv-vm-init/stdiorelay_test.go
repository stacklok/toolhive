// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunStdioRelay(t *testing.T) {
	t.Parallel()

	// Find a free port for the relay.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Use "head -1" as the MCP server — it reads one line, echoes it, and exits.
	cmd := exec.Command("head", "-1") //nolint:gosec // test command

	// Run the relay in a goroutine.
	relayErr := make(chan error, 1)
	go func() {
		relayErr <- runStdioRelay(logger, port, cmd)
	}()

	// Wait for the relay to start listening.
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, err, "failed to connect to stdio relay")
	defer conn.Close()

	// Send a JSON-RPC message and verify we get it back (via head -1).
	msg := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	_, err = conn.Write([]byte(msg))
	require.NoError(t, err)

	buf := make([]byte, 256)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, msg, string(buf[:n]))

	// head -1 exits after reading one line, so the relay should exit too.
	select {
	case rErr := <-relayErr:
		assert.NoError(t, rErr)
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not exit after child process finished")
	}
}

func TestRunStdioRelay_ReadinessProbeDoesNotConsumeSlot(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// head -1 reads one line, echoes, and exits.
	cmd := exec.Command("head", "-1") //nolint:gosec // test command

	relayErr := make(chan error, 1)
	go func() {
		relayErr <- runStdioRelay(logger, port, cmd)
	}()

	// Simulate a readiness probe: connect and immediately disconnect.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probeConn, dialErr := net.DialTimeout("tcp", addr, time.Second)
		if dialErr == nil {
			probeConn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Now connect for real data — this should still work.
	time.Sleep(200 * time.Millisecond) // let the relay process the probe disconnect
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err, "second connection should succeed after readiness probe")
	defer conn.Close()

	msg := `{"jsonrpc":"2.0","id":2}` + "\n"
	_, err = conn.Write([]byte(msg))
	require.NoError(t, err)

	buf := make([]byte, 256)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, msg, string(buf[:n]))

	// head -1 exits after reading one line, so the relay exits too.
	select {
	case rErr := <-relayErr:
		assert.NoError(t, rErr)
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not exit")
	}
}

func TestRunStdioRelay_ChildExitsBeforeConnection(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// "false" exits immediately with code 1.
	cmd := exec.Command("false")

	relayErr := make(chan error, 1)
	go func() {
		relayErr <- runStdioRelay(logger, port, cmd)
	}()

	// The relay should return quickly since the child exits immediately.
	select {
	case rErr := <-relayErr:
		// "false" exits with code 1, so we expect an ExitError.
		require.Error(t, rErr)
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not exit after child process crashed")
	}
}

func TestGetEnvValue(t *testing.T) {
	t.Parallel()

	env := []string{"FOO=bar", "MCP_TRANSPORT=stdio", "EMPTY=", "NOEQUALS"}

	assert.Equal(t, "bar", getEnvValue(env, "FOO"))
	assert.Equal(t, "stdio", getEnvValue(env, "MCP_TRANSPORT"))
	assert.Equal(t, "", getEnvValue(env, "EMPTY"))
	assert.Equal(t, "", getEnvValue(env, "MISSING"))
	assert.Equal(t, "", getEnvValue(env, "NOEQUALS"))
}
