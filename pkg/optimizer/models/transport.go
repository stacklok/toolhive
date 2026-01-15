package models

import (
	"database/sql/driver"
	"fmt"
)

// TransportType represents the transport protocol used by an MCP server.
// Maps 1:1 to ToolHive transport modes.
type TransportType string

const (
	// TransportSSE represents Server-Sent Events transport
	TransportSSE TransportType = "sse"
	// TransportStreamable represents Streamable HTTP transport
	TransportStreamable TransportType = "streamable-http"
)

// Valid returns true if the transport type is valid
func (t TransportType) Valid() bool {
	switch t {
	case TransportSSE, TransportStreamable:
		return true
	default:
		return false
	}
}

// String returns the string representation
func (t TransportType) String() string {
	return string(t)
}

// Value implements the driver.Valuer interface for database storage
func (t TransportType) Value() (driver.Value, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("invalid transport type: %s", t)
	}
	return string(t), nil
}

// Scan implements the sql.Scanner interface for database retrieval
func (t *TransportType) Scan(value interface{}) error {
	if value == nil {
		return fmt.Errorf("transport type cannot be nil")
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("transport type must be a string, got %T", value)
	}

	*t = TransportType(str)
	if !t.Valid() {
		return fmt.Errorf("invalid transport type from database: %s", str)
	}

	return nil
}

// MCPStatus represents the status of an MCP server backend.
type MCPStatus string

const (
	// StatusRunning indicates the backend is running
	StatusRunning MCPStatus = "running"
	// StatusStopped indicates the backend is stopped
	StatusStopped MCPStatus = "stopped"
)

// Valid returns true if the status is valid
func (s MCPStatus) Valid() bool {
	switch s {
	case StatusRunning, StatusStopped:
		return true
	default:
		return false
	}
}

// String returns the string representation
func (s MCPStatus) String() string {
	return string(s)
}

// Value implements the driver.Valuer interface for database storage
func (s MCPStatus) Value() (driver.Value, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("invalid MCP status: %s", s)
	}
	return string(s), nil
}

// Scan implements the sql.Scanner interface for database retrieval
func (s *MCPStatus) Scan(value interface{}) error {
	if value == nil {
		return fmt.Errorf("MCP status cannot be nil")
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("MCP status must be a string, got %T", value)
	}

	*s = MCPStatus(str)
	if !s.Valid() {
		return fmt.Errorf("invalid MCP status from database: %s", str)
	}

	return nil
}
