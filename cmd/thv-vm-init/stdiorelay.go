// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// runStdioRelay starts the MCP server as a child process and bridges its
// stdin/stdout over a TCP listener on the given port. The host-side proxy
// connects to this relay through a port forward to exchange newline-delimited
// JSON-RPC messages.
//
// The relay accepts connections in a loop. Probe connections (those that close
// before sending data, e.g. readiness checks) are discarded without affecting
// the child. The first connection that sends data becomes the active bridge.
// When the child process exits, all connections and the listener are closed.
func runStdioRelay(logger *slog.Logger, relayPort int, cmd *exec.Cmd) error {
	childStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	childStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	// Stderr still goes to the console log for debugging.
	cmd.Stderr = os.Stderr

	// Start the TCP listener before launching the child so the readiness
	// probe on the host side can connect as soon as the port is forwarded.
	// Bind to 0.0.0.0 — the host-side port forward connects to the guest's
	// network interface (e.g. 192.168.127.2), not loopback.
	addr := fmt.Sprintf("0.0.0.0:%d", relayPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	defer func() { _ = ln.Close() }()
	logger.Info("stdio relay listening", "addr", addr)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting MCP server: %w", err)
	}
	logger.Info("MCP server started", "pid", cmd.Process.Pid, "cmd", cmd.Args)

	// Forward SIGTERM/SIGINT to the child for graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for received := range sig {
			logger.Info("received signal, forwarding to child", "signal", received)
			_ = cmd.Process.Signal(received)
		}
	}()

	// childDone is closed when cmd.Wait() returns, signaling the accept
	// loop and any active bridge to tear down.
	childDone := make(chan struct{})

	// Accept loop — runs in a goroutine so we can also wait for the child.
	// Connections that close before sending data (readiness probes) are
	// discarded. The first connection that sends data gets the full bridge.
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			select {
			case <-childDone:
				_ = conn.Close()
				return
			default:
			}

			logger.Info("stdio relay: client connected", "remote", conn.RemoteAddr())

			// Read the first byte to distinguish probes from data connections.
			// Probes (readiness checks) connect and immediately disconnect.
			firstByte := make([]byte, 1)
			n, readErr := conn.Read(firstByte)
			if readErr != nil || n == 0 {
				logger.Debug("stdio relay: connection closed before data (probe)")
				_ = conn.Close()
				continue
			}

			// Real data connection — bridge to child stdin/stdout.
			// Prepend the first byte we already read.
			reader := io.MultiReader(bytes.NewReader(firstByte[:n]), conn)

			// Once this data connection drops, childStdin is closed and the
			// child sees EOF. The child is expected to exit. Reconnection is
			// not supported — recovery requires restarting the VM workload.
			bridgeDataConn(logger, conn, reader, childStdin, childStdout, childDone)
		}
	}()

	waitErr := cmd.Wait()
	close(childDone)
	signal.Stop(sig)

	_ = ln.Close()
	<-bridgeDone

	return waitErr
}

// bridgeDataConn copies data bidirectionally between a TCP connection and the
// child's stdin/stdout. It blocks until both directions are done.
// The connReader includes any data already buffered from the initial probe read.
func bridgeDataConn(
	logger *slog.Logger,
	conn net.Conn,
	connReader io.Reader,
	childStdin io.WriteCloser,
	childStdout io.Reader,
	childDone <-chan struct{},
) {
	// Force-close the connection when the child exits so io.Copy unblocks.
	go func() {
		<-childDone
		_ = conn.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// connReader → child stdin
	go func() {
		defer wg.Done()
		if _, cpErr := io.Copy(childStdin, connReader); cpErr != nil {
			logger.Debug("stdio relay: conn→stdin copy ended", "error", cpErr)
		}
		// Client disconnected. Close childStdin so the child sees EOF.
		_ = childStdin.Close()
	}()

	// child stdout → conn
	go func() {
		defer wg.Done()
		if _, cpErr := io.Copy(conn, childStdout); cpErr != nil {
			logger.Debug("stdio relay: stdout→conn copy ended", "error", cpErr)
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	wg.Wait()
	_ = conn.Close()
}
