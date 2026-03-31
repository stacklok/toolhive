// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Client implements the runtime.Runtime interface using go-microvm microVMs.
// This is an EXPERIMENTAL runtime — expect breaking changes.
type Client struct {
	mu   sync.RWMutex
	vms  map[string]*vmEntry
	opts clientOptions
}

// vmEntry tracks a running go-microvm VM mapped to a ToolHive workload.
type vmEntry struct {
	name               string
	image              string
	labels             map[string]string
	state              runtime.WorkloadStatus
	vm                 vmHandle
	createdAt          time.Time
	dataDir            string
	ports              []runtime.PortMapping
	transportType      string
	stdioRelayHostPort int // host port mapped to the guest stdio relay (stdio transport only)
}

// clientOptions holds configuration for the go-microvm runtime.
type clientOptions struct {
	dataDir       string
	imageCacheDir string
	cpus          uint32
	memory        uint32
	logLevel      uint32
	procCheck     *processChecker
}

// NewClient creates a new go-microvm runtime client.
func NewClient(_ context.Context) (*Client, error) {
	slog.Warn("[EXPERIMENTAL] initializing go-microvm microVM runtime — this is experimental and may change without notice")
	c := &Client{
		vms:  make(map[string]*vmEntry),
		opts: defaultClientOptions(),
	}
	c.recoverState()
	return c, nil
}

func defaultClientOptions() clientOptions {
	dir := os.Getenv("GO_MICROVM_DATA_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(home, ".config", "toolhive", "gomicrovm")
		}
	}

	imageCacheDir := os.Getenv("TOOLHIVE_GO_MICROVM_IMAGE_CACHE_DIR")
	if imageCacheDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			imageCacheDir = filepath.Join(home, ".config", "toolhive", "gomicrovm", "cache", "images")
		}
	}

	var logLevel uint32
	if v := os.Getenv("TOOLHIVE_GO_MICROVM_LOG_LEVEL"); v != "" {
		if parsed, err := strconv.ParseUint(v, 10, 32); err == nil {
			logLevel = uint32(parsed)
		}
	}

	return clientOptions{
		dataDir:       dir,
		imageCacheDir: imageCacheDir,
		cpus:          1,
		memory:        512,
		logLevel:      logLevel,
		procCheck:     defaultProcessChecker(),
	}
}

// AttachToWorkload connects to the guest VM's stdio relay over TCP.
// The relay port is set up during DeployWorkload when stdio transport is used.
// stdioRelayHostPort and transportType are immutable after the entry is
// inserted into Client.vms — safe to read after releasing the lock.
// state is mutable (updated by lifecycle methods) but the check here is
// best-effort; AttachToWorkload is called once during StdioTransport.Start().
func (c *Client) AttachToWorkload(_ context.Context, name string) (io.WriteCloser, io.ReadCloser, error) {
	c.mu.RLock()
	entry, ok := c.vms[name]
	c.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("go-microvm runtime: workload %q not found", name)
	}

	if entry.state != runtime.WorkloadStatusRunning {
		return nil, nil, fmt.Errorf("go-microvm runtime: workload %q is not running", name)
	}

	if entry.stdioRelayHostPort == 0 {
		return nil, nil, fmt.Errorf("go-microvm runtime: workload %q has no stdio relay port (transport=%s)", name, entry.transportType)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", entry.stdioRelayHostPort)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("go-microvm runtime: dialing stdio relay at %s: %w", addr, err)
	}

	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("go-microvm runtime: expected TCP connection, got %T", conn)
	}

	writer, reader := splitTCPConn(tcpConn)
	return writer, reader, nil
}

// IsRunning checks the health of the go-microvm runtime.
func (*Client) IsRunning(_ context.Context) error {
	if !IsAvailable() {
		return fmt.Errorf("go-microvm runtime is not available: KVM or go-microvm-runner not found")
	}
	return nil
}
