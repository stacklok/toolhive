//go:build windows

package networking

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetProcessOnPort returns the PID of the process listening on the given TCP port.
// Returns 0 if the port is free or if the holder cannot be determined.
// Uses netstat which is available on Windows.
func GetProcessOnPort(port int) (int, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d", port)
	}

	cmd := exec.Command("netstat", "-a", "-n", "-o")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("netstat failed: %w", err)
	}

	wantSuffix := fmt.Sprintf(":%d", port)

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		// netstat columns: Proto LocalAddr ForeignAddr State PID
		// Example: TCP  0.0.0.0:8080  0.0.0.0:0  LISTENING  1234
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !strings.EqualFold(fields[0], "TCP") {
			continue
		}

		localAddr := fields[1]
		state := fields[len(fields)-2]
		pidStr := fields[len(fields)-1]

		if !strings.HasSuffix(localAddr, wantSuffix) {
			continue
		}
		if !strings.EqualFold(state, "LISTENING") {
			continue
		}

		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			continue
		}
		return pid, nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("failed to parse netstat output: %w", err)
	}

	return 0, nil
}
