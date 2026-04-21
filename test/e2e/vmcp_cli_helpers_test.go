// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// allocateVMCPPort returns a free TCP port on 127.0.0.1 for use by thv vmcp serve.
func allocateVMCPPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred(), "should be able to allocate a free port")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// stopVMCPProcess sends SIGINT to a running vmcp serve process and waits for it
// to exit. If the process does not exit within 5 seconds, SIGKILL is sent to
// prevent the test suite from hanging. Safe to call on a nil cmd or on a cmd
// whose process has already exited.
func stopVMCPProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGINT)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// process exited cleanly
	case <-time.After(5 * time.Second):
		GinkgoWriter.Printf("vmcp process did not exit after SIGINT; sending SIGKILL\n")
		_ = cmd.Process.Kill()
		<-done // wait for the goroutine to finish after Kill
	}
}

// launchYardstick starts a yardstick backend on port 8080 in the given group
// but does not wait for it to become ready.
func launchYardstick(config *e2e.TestConfig, groupName, backendName string) {
	launchYardstickOnPort(config, groupName, backendName, 8080)
}

// launchYardstickOnPort starts a yardstick backend on the given port in the
// given group but does not wait for it to become ready. The port is passed both
// as --target-port (so thv's proxy maps to it) and as the -port flag to the
// yardstick binary (so the server actually listens on that port).
func launchYardstickOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	portStr := strconv.Itoa(port)
	e2e.NewTHVCommand(config,
		"run", images.YardstickServerImage,
		"--name", backendName,
		"--group", groupName,
		"--transport", "streamable-http",
		"--target-port", portStr,
		"--env", "TRANSPORT=streamable-http",
		"--", "-port", portStr, "-transport", "streamable-http",
	).ExpectSuccess()
}

// startYardstick runs a yardstick backend on port 8080 in the given group and
// waits for it to be ready.
func startYardstick(config *e2e.TestConfig, groupName, backendName string) {
	launchYardstick(config, groupName, backendName)
	err := e2e.WaitForMCPServer(config, backendName, 120*time.Second)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("yardstick backend %q should become running", backendName))
}

// startYardstickOnPort runs a yardstick backend on the given port in the given
// group and waits for it to be ready.
func startYardstickOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	launchYardstickOnPort(config, groupName, backendName, port)
	err := e2e.WaitForMCPServer(config, backendName, 120*time.Second)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("yardstick backend %q should become running", backendName))
}

// initVMCPConfig generates a starter YAML config for the given group into path
// using `thv vmcp init`.
func initVMCPConfig(config *e2e.TestConfig, groupName, path string) {
	e2e.NewTHVCommand(config,
		"vmcp", "init",
		"--group", groupName,
		"--config", path,
	).ExpectSuccess()
}
