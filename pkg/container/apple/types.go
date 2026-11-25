// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const (
	// RuntimeName is the registered name for the Apple container runtime.
	RuntimeName = "apple"

	containerBinary        = "container"
	swVersBinary           = "sw_vers"
	minContainerMajor      = 1
	minMacOSIsolationMajor = 26
)

// appleReferenceEpoch is Apple's CFAbsoluteTime / Foundation.Date JSON epoch (2001-01-01T00:00:00Z).
var appleReferenceEpoch = time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)

// appleTimestamp decodes timestamps from Apple container JSON, which may be either
// RFC3339 strings (container 1.7+) or CFAbsoluteTime float64 seconds (container 1.0).
type appleTimestamp struct {
	time.Time
}

func (ts *appleTimestamp) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		ts.Time = time.Time{}
		return nil
	}

	if trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			ts.Time = time.Time{}
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("invalid apple timestamp %q: %w", raw, err)
		}
		ts.Time = parsed
		return nil
	}

	var secs float64
	if err := json.Unmarshal(trimmed, &secs); err != nil {
		return err
	}
	ts.Time = parseAppleTimestamp(&secs)
	return nil
}

// appleContainerJSON mirrors the JSON emitted by `container inspect <id>` and
// `container list --all --format json` across apple/container 1.0+ releases.
// It supports both the flat 1.0 schema (string status, top-level startedDate/networks)
// and the nested 1.7+ schema (status object containing state, startedDate, and networks).
type appleContainerJSON struct {
	ID            string
	Status        string
	StartedDate   *float64
	StartedAt     time.Time
	CreatedAt     time.Time
	Configuration appleContainerConfigJSON
	Networks      []appleContainerNetworkJSON
}

type appleContainerStatusJSON struct {
	State       string                      `json:"state"`
	StartedDate appleTimestamp              `json:"startedDate,omitempty"`
	Networks    []appleContainerNetworkJSON `json:"networks,omitempty"`
}

func (cont *appleContainerJSON) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID            string                      `json:"id,omitempty"`
		Status        json.RawMessage             `json:"status,omitempty"`
		StartedDate   appleTimestamp              `json:"startedDate,omitempty"`
		Configuration appleContainerConfigJSON    `json:"configuration"`
		Networks      []appleContainerNetworkJSON `json:"networks,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	cont.ID = raw.ID
	cont.Configuration = raw.Configuration
	cont.Networks = raw.Networks
	cont.StartedAt = raw.StartedDate.Time
	cont.CreatedAt = raw.Configuration.CreationDate.Time

	return cont.decodeStatus(raw.Status)
}

func (cont *appleContainerJSON) decodeStatus(rawStatus json.RawMessage) error {
	trimmed := bytes.TrimSpace(rawStatus)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	if trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &cont.Status)
	}

	var statusObj appleContainerStatusJSON
	if err := json.Unmarshal(trimmed, &statusObj); err != nil {
		return err
	}
	cont.Status = statusObj.State
	if !statusObj.StartedDate.IsZero() {
		cont.StartedAt = statusObj.StartedDate.Time
	}
	if len(statusObj.Networks) > 0 {
		cont.Networks = statusObj.Networks
	}
	return nil
}

type appleContainerConfigJSON struct {
	ID             string                   `json:"id"`
	CreationDate   appleTimestamp           `json:"creationDate,omitempty"`
	Labels         map[string]string        `json:"labels,omitempty"`
	Image          appleContainerImageJSON  `json:"image"`
	PublishedPorts []applePublishedPortJSON `json:"publishedPorts,omitempty"`
	ReadOnly       bool                     `json:"readOnly,omitempty"`
	CapAdd         []string                 `json:"capAdd,omitempty"`
	CapDrop        []string                 `json:"capDrop,omitempty"`
	InitProcess    appleInitProcessJSON     `json:"initProcess,omitempty"`
}

type appleContainerImageJSON struct {
	Reference string `json:"reference"`
}

type applePublishedPortJSON struct {
	HostAddress   string `json:"hostAddress,omitempty"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Count         int    `json:"count,omitempty"`
	Proto         string `json:"proto,omitempty"`
}

type appleInitProcessJSON struct {
	Environment []string `json:"environment,omitempty"`
}

type appleContainerNetworkJSON struct {
	Network     string `json:"network"`
	IPv4Address string `json:"ipv4Address,omitempty"`
	IPv4Gateway string `json:"ipv4Gateway,omitempty"`
	IPv6Address string `json:"ipv6Address,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
}

func parseAppleTimestamp(secs *float64) time.Time {
	if secs == nil || math.IsNaN(*secs) || math.IsInf(*secs, 0) || *secs <= 0 {
		return time.Time{}
	}
	wholeSec, fracSec := math.Modf(*secs)
	return appleReferenceEpoch.Add(
		time.Duration(wholeSec)*time.Second + time.Duration(fracSec*float64(time.Second)),
	)
}

func stripCIDR(addr string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return ""
	}
	if prefix, err := netip.ParsePrefix(trimmed); err == nil {
		return prefix.Addr().String()
	}
	if parsed, err := netip.ParseAddr(trimmed); err == nil {
		return parsed.String()
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

func appleToDomainStatus(status string) runtime.WorkloadStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return runtime.WorkloadStatusRunning
	case "stopped", "exited":
		return runtime.WorkloadStatusStopped
	case "created", "booting", "starting":
		return runtime.WorkloadStatusStarting
	case "stopping":
		return runtime.WorkloadStatusStopping
	case "error", "failed":
		return runtime.WorkloadStatusError
	default:
		return runtime.WorkloadStatusUnknown
	}
}

func (cont appleContainerJSON) containerID() string {
	if cont.Configuration.ID != "" {
		return cont.Configuration.ID
	}
	return cont.ID
}

func toContainerInfo(cont appleContainerJSON) runtime.ContainerInfo {
	labels := maps.Clone(cont.Configuration.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}

	ports := make([]runtime.PortMapping, 0, len(cont.Configuration.PublishedPorts))
	for _, pub := range cont.Configuration.PublishedPorts {
		count := pub.Count
		if count <= 0 {
			count = 1
		}
		proto := strings.ToLower(strings.TrimSpace(pub.Proto))
		if proto == "" {
			proto = "tcp"
		}
		for offset := range count {
			ports = append(ports, runtime.PortMapping{
				ContainerPort: pub.ContainerPort + offset,
				HostPort:      pub.HostPort + offset,
				Protocol:      proto,
			})
		}
	}

	name := cont.containerID()

	startedAt := cont.StartedAt
	if startedAt.IsZero() {
		startedAt = parseAppleTimestamp(cont.StartedDate)
	}
	createdAt := cont.CreatedAt
	if createdAt.IsZero() {
		createdAt = startedAt
	}

	return runtime.ContainerInfo{
		Name:      name,
		Image:     cont.Configuration.Image.Reference,
		Status:    cont.Status,
		State:     appleToDomainStatus(cont.Status),
		Created:   createdAt,
		StartedAt: startedAt,
		Labels:    labels,
		Ports:     ports,
	}
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	var cmdErr *CommandError
	if errors.As(err, &cmdErr) && cmdErr.Stderr != "" {
		msg = cmdErr.Stderr
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "container not found") ||
		strings.Contains(lower, "image not found") ||
		strings.Contains(lower, "network not found") ||
		strings.Contains(lower, "notfound:") ||
		(strings.Contains(lower, "container with id ") && strings.Contains(lower, " not found")) ||
		strings.Contains(lower, "no such container") ||
		strings.Contains(lower, "no such image") ||
		strings.Contains(lower, "no such network")
}

func parseInspectOutput(data []byte) (appleContainerJSON, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("null")) {
		return appleContainerJSON{}, runtime.ErrContainerNotFound
	}

	if trimmed[0] == '[' {
		var items []appleContainerJSON
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return appleContainerJSON{}, fmt.Errorf("failed to parse container inspect array: %w", err)
		}
		if len(items) == 0 {
			return appleContainerJSON{}, runtime.ErrContainerNotFound
		}
		return items[0], nil
	}

	var item appleContainerJSON
	if err := json.Unmarshal(trimmed, &item); err != nil {
		return appleContainerJSON{}, fmt.Errorf("failed to parse container inspect object: %w", err)
	}
	return item, nil
}
